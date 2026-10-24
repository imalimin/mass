package einoadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/imalimin/mass/internal/domain/run"
)

type traceRecorder struct {
	request, response []byte
	fail              bool
}

func (r *traceRecorder) Request(_ context.Context, b []byte) error {
	r.request = append(r.request, b...)
	return nil
}
func (r *traceRecorder) Response(_ context.Context, _ int, b []byte) error {
	if r.fail {
		return errors.New("storage unavailable")
	}
	r.response = append(r.response, b...)
	return nil
}
func TestActualProtocolMatrix(t *testing.T) {
	cases := []struct {
		name         string
		status       int
		body         string
		valid, retry bool
	}{
		{"unicode split", 200, "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"你好\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":2,\"prompt_tokens_details\":{\"cached_tokens\":0}}}\n\ndata: [DONE]\n\n", true, false},
		{"missing sentinel", 200, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"},\"finish_reason\":null}]}\n\n", false, false},
		{"invalid json", 200, "data: {invalid}\n\ndata: [DONE]\n\n", false, false},
		{"unauthorized", 401, `{"error":{"message":"denied"}}`, false, false},
		{"throttle", 429, `{"error":{"message":"busy"}}`, false, true},
		{"empty unavailable", 503, "", false, true},
		{"usage then broken", 200, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":3}}\n\n", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var sent []byte
			count := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count++
				sent, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(tc.status)
				for _, b := range []byte(tc.body) {
					w.Write([]byte{b})
					if f, ok := w.(http.Flusher); ok {
						f.Flush()
					}
				}
			}))
			defer server.Close()
			recorder := &traceRecorder{}
			transport := &ModelTransport{}
			s := run.Snapshot{Definition: run.Definition{OutputTokens: 256, Model: run.ModelConfig{Endpoint: server.URL, ModelID: "fixture"}}}
			p := run.Prepared{Spec: run.ProjectionSpec{Purpose: "task"}, Messages: []run.Message{{Role: "user", Content: "hello"}}}
			text := ""
			reply, err := transport.Stream(context.Background(), s, p, "secret-only-in-header", recorder, func(part string) error { text += part; return nil })
			if (err == nil) != tc.valid {
				t.Fatal("outcome", err)
			}
			if count != 1 {
				t.Fatal("hidden SDK retry", count)
			}
			if !bytes.Equal(sent, recorder.request) || (tc.name != "invalid json" && !bytes.Equal([]byte(tc.body), recorder.response)) || (tc.name == "invalid json" && (len(recorder.response) == 0 || !bytes.HasPrefix([]byte(tc.body), recorder.response))) {
				t.Fatal("wire mismatch")
			}
			if strings.Contains(string(recorder.request), "secret-only-in-header") {
				t.Fatal("authorization in trace")
			}
			var failure *run.ModelFailure
			if errors.As(err, &failure) != tc.retry {
				t.Fatal("retry classification", err)
			}
			if reply.Observation == nil || reply.Observation.HTTPStatus != tc.status {
				t.Fatal("missing observation")
			}
			if tc.valid && (text != "你好" || reply.Message.Content != text || reply.Observation.FirstTokenMS == nil || reply.Usage.CacheRead == nil || *reply.Usage.CacheRead != 0) {
				t.Fatal("stream/usage mismatch")
			}
			if tc.name == "usage then broken" && (reply.Usage.Input == nil || *reply.Usage.Input != 12) {
				t.Fatal("lost failed attempt usage")
			}
		})
	}
}
func TestRecorderFailureNeverRetries(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503); w.Write([]byte(`{"error":"busy"}`)) }))
	defer server.Close()
	_, err := (&ModelTransport{}).Stream(context.Background(), run.Snapshot{Definition: run.Definition{OutputTokens: 20, Model: run.ModelConfig{Endpoint: server.URL, ModelID: "fixture"}}}, run.Prepared{Messages: []run.Message{{Role: "user", Content: "x"}}}, "fixture", &traceRecorder{fail: true}, func(string) error { return nil })
	var failure *run.ModelFailure
	if err == nil || errors.As(err, &failure) {
		t.Fatal("storage failure retryable", err)
	}
}
func TestUsageUnknownAndRawProtocol(t *testing.T) {
	raw := []byte("data: {\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":null,\"prompt_tokens_details\":{\"cached_tokens\":4},\"cache_creation_input_tokens\":0}}\n\n")
	u := wireUsage(raw)
	b, _ := json.Marshal(u)
	if u.Output != nil || u.CacheRead == nil || *u.CacheRead != 4 || u.CacheWrite == nil || !bytes.Contains(b, []byte("null")) {
		t.Fatal("usage presence", string(b))
	}
}

func TestParallelToolArgumentFrames(t *testing.T) {
	body := "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"a\",\"type\":\"function\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{\\\"path\\\":\"}},{\"index\":1,\"id\":\"b\",\"type\":\"function\",\"function\":{\"name\":\"list_files\",\"arguments\":\"{\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"a.txt\\\"}\"}},{\"index\":1,\"function\":{\"arguments\":\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(body))
	}))
	defer server.Close()
	recorder := &traceRecorder{}
	reply, err := (&ModelTransport{}).Stream(context.Background(), run.Snapshot{Definition: run.Definition{OutputTokens: 256, Model: run.ModelConfig{Endpoint: server.URL, ModelID: "fixture"}}}, run.Prepared{Messages: []run.Message{{Role: "user", Content: "two tools"}}}, "fixture", recorder, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	calls := reply.Message.ToolCalls
	if len(calls) != 2 || calls[0].ID != "a" || calls[1].ID != "b" || calls[0].Arguments != `{"path":"a.txt"}` || calls[1].Arguments != `{}` || !json.Valid(reply.Message.Native) {
		t.Fatal("tool frames reordered or damaged", calls)
	}
}
