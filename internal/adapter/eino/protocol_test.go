package einoadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/imalimin/mass/internal/diagnostic"
	"github.com/imalimin/mass/internal/domain/run"
)

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
				w.Header().Set("X-Request-Id", "fixture-request-123")
				w.WriteHeader(tc.status)
				for _, b := range []byte(tc.body) {
					w.Write([]byte{b})
					if f, ok := w.(http.Flusher); ok {
						f.Flush()
					}
				}
			}))
			defer server.Close()
			transport := &ModelTransport{}
			s, ctx, logPath := modelLog(t)
			s.Definition = run.Definition{OutputTokens: 256, Model: run.ModelConfig{Endpoint: server.URL, ModelID: "fixture"}}
			p := run.Prepared{Spec: run.ProjectionSpec{Purpose: "task"}, Messages: []run.Message{{Role: "user", Content: "hello"}}}
			text := ""
			reply, err := transport.Stream(ctx, s, p, "secret-only-in-header", func(part string) error { text += part; return nil })
			if (err == nil) != tc.valid {
				t.Fatal("outcome", err)
			}
			if count != 1 {
				t.Fatal("hidden SDK retry", count)
			}
			if !json.Valid(sent) || !bytes.Contains(sent, []byte(`"hello"`)) || strings.Contains(string(sent), "secret-only-in-header") {
				t.Fatal("invalid SDK request")
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
			if !tc.valid {
				entry := readModelLog(t, logPath)
				for _, want := range []string{"attempt_id=attempt-fixture", "purpose=task", fmt.Sprintf("http_status=%d", tc.status), "request_id=fixture-request-123"} {
					if entry.Event != "model.attempt_failed" || !strings.Contains(entry.Message, want) {
						t.Fatalf("model failure missing %s: %+v", want, entry)
					}
				}
				if strings.Contains(entry.Message, "secret-only-in-header") || strings.Contains(entry.Message, "partial") {
					t.Fatal("diagnostic log retained model content or credentials")
				}
			}
		})
	}
}

type chunkModel struct {
	model.ToolCallingChatModel
	chunks []*schema.Message
}

func (m chunkModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray(m.chunks), nil
}

func TestStreamLimitRecordsBothMeasuredCounters(t *testing.T) {
	for _, tc := range []struct {
		trigger string
		count   int
		large   bool
	}{{"chunks", 10001, false}, {"bytes", 1, true}, {"both", 10001, true}} {
		t.Run(tc.trigger, func(t *testing.T) {
			s, ctx, logPath := modelLog(t)
			small := &schema.Message{Role: schema.Assistant}
			chunks := make([]*schema.Message, tc.count)
			for i := range chunks {
				chunks[i] = small
			}
			if tc.large {
				chunks[len(chunks)-1] = &schema.Message{Role: schema.Assistant, Content: strings.Repeat("x", 4<<20)}
			}
			smallJSON, _ := json.Marshal(small)
			lastJSON, _ := json.Marshal(chunks[len(chunks)-1])
			size := (tc.count-1)*len(smallJSON) + len(lastJSON)
			transport := &ModelTransport{Fake: chunkModel{chunks: chunks}}
			_, err := transport.Stream(ctx, s, run.Prepared{}, "fixture", func(string) error { return nil })
			if !errors.Is(err, run.ErrOverflow) {
				t.Fatalf("stream limit returned %v", err)
			}
			entry := readModelLog(t, logPath)
			want := fmt.Sprintf("attempt_id=attempt-fixture received_chunks=%d max_chunks=10000 serialized_chunk_bytes=%d max_serialized_chunk_bytes=4194304 trigger=%s", tc.count, size, tc.trigger)
			if entry.Event != "model.stream_limit_exceeded" || !strings.Contains(entry.Message, want) {
				t.Fatalf("stream limit missing %q: %+v", want, entry)
			}
		})
	}
}

func modelLog(t *testing.T) (run.Snapshot, context.Context, string) {
	t.Helper()
	root := t.TempDir()
	s := run.Snapshot{SpaceID: "aaaaaaaaaaaaaaaa", SessionID: "bbbbbbbbbbbbbbbb", ID: "cccccccccccccccc"}
	log := diagnostic.NewStore(root, "", nil, slog.New(slog.DiscardHandler))
	ctx := diagnostic.WithAttempt(diagnostic.WithRun(context.Background(), log, s), "attempt-fixture")
	return s, ctx, filepath.Join(root, s.SpaceID, "sessions", s.SessionID, s.SessionID+".jsonl")
}

func readModelLog(t *testing.T, name string) diagnostic.Entry {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	var entry diagnostic.Entry
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatalf("expected one diagnostic row: %s (%v)", raw, err)
	}
	return entry
}

func TestModelTimeoutAppliesToStreamingBody(t *testing.T) {
	flushed := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"partial\"}}]}\n\n")
		w.(http.Flusher).Flush()
		flushed <- struct{}{}
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	transport := &ModelTransport{Timeout: 300 * time.Millisecond}
	s := run.Snapshot{Definition: run.Definition{OutputTokens: 256, Model: run.ModelConfig{Endpoint: server.URL, ModelID: "fixture"}}}
	p := run.Prepared{Messages: []run.Message{{Role: "user", Content: "hello"}}}
	_, err := transport.Stream(ctx, s, p, "fixture", func(string) error { return nil })
	if err == nil || ctx.Err() != nil {
		t.Fatalf("model client timeout did not end the stream before the parent deadline: %v", err)
	}
	select {
	case <-flushed:
	default:
		t.Fatal("no partial response was sent")
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
	body := "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"reasoning_content\":\"checking \",\"tool_calls\":[{\"index\":0,\"id\":\"a\",\"type\":\"function\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{\\\"path\\\":\"}},{\"index\":1,\"id\":\"b\",\"type\":\"function\",\"function\":{\"name\":\"list_files\",\"arguments\":\"{\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"files\",\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"a.txt\\\"}\"}},{\"index\":1,\"function\":{\"arguments\":\"}\"}}]},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens_details\":{\"cached_tokens\":4},\"completion_tokens_details\":{\"reasoning_tokens\":9}}}\n\ndata: [DONE]\n\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(body))
	}))
	defer server.Close()
	reply, err := (&ModelTransport{}).Stream(context.Background(), run.Snapshot{Definition: run.Definition{OutputTokens: 256, Model: run.ModelConfig{Endpoint: server.URL, ModelID: "fixture"}}}, run.Prepared{Messages: []run.Message{{Role: "user", Content: "two tools"}}}, "fixture", func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	calls := reply.Message.ToolCalls
	if len(calls) != 2 || calls[0].ID != "a" || calls[1].ID != "b" || calls[0].Arguments != `{"path":"a.txt"}` || calls[1].Arguments != `{}` || !json.Valid(reply.Message.Native) {
		t.Fatal("tool frames reordered or damaged", calls)
	}
	if reply.Trace.Reasoning == nil || *reply.Trace.Reasoning != "checking files" || len(reply.Trace.ToolCalls) != 2 ||
		reply.Trace.ToolCalls[0].Function.Arguments != calls[0].Arguments || reply.Trace.ToolCalls[0].Type != "function" ||
		!reply.Trace.UsagePresent || reply.Trace.ReasoningTokens == nil || *reply.Trace.ReasoningTokens != 9 ||
		reply.Usage.Input != nil || reply.Usage.CacheRead == nil || *reply.Usage.CacheRead != 4 {
		t.Fatalf("missing provider diagnostics: %+v, %+v", reply.Trace, reply.Usage)
	}
}
