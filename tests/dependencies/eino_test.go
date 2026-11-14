package dependencies_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino-ext/components/model/ark"
	"github.com/cloudwego/eino/schema"
)

func TestArkHTTPProtocol(t *testing.T) {
	stream := "data: {\"id\":\"completion-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"completion-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":1,\"total_tokens\":8}}\n\ndata: [DONE]\n\n"
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		streaming bool
	}{
		{"stream", 200, stream, true},
		{"nonstream", 200, `{"id":"completion-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":1,"total_tokens":8}}`, false},
		{"provider_error_no_retry", 429, `{"error":{"code":"RateLimitExceeded","message":"try later","type":"rate_limit_error"}}`, false},
		{"malformed_response", 200, `{broken-json`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			var actual []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				actual, _ = io.ReadAll(r.Body)
				if r.Header.Get("Authorization") == "" {
					t.Error("SDK omitted authentication")
				}
				if tc.streaming {
					w.Header().Set("Content-Type", "text/event-stream")
				} else {
					w.Header().Set("Content-Type", "application/json")
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			client := &http.Client{Transport: server.Client().Transport, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			zero := 0
			model, err := ark.NewChatModel(context.Background(), &ark.ChatModelConfig{APIKey: "fixture-secret-not-for-trace", Model: "fixture-model", BaseURL: server.URL, HTTPClient: client, RetryTimes: &zero})
			if err != nil {
				t.Fatal(err)
			}
			bound, err := model.WithTools([]*schema.ToolInfo{{Name: "read_file", Desc: "Read a file", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{"path": {Type: schema.String, Required: true}})}})
			if err != nil {
				t.Fatal(err)
			}
			messages := []*schema.Message{schema.UserMessage("read notes.txt")}
			var output strings.Builder
			if tc.streaming {
				reader, e := bound.Stream(context.Background(), messages)
				err = e
				if e == nil {
					defer reader.Close()
					for {
						chunk, e := reader.Recv()
						if errors.Is(e, io.EOF) {
							break
						}
						if e != nil {
							err = e
							break
						}
						output.WriteString(chunk.Content)
					}
				}
			} else {
				reply, e := bound.Generate(context.Background(), messages)
				err = e
				if reply != nil {
					output.WriteString(reply.Content)
				}
			}
			shouldFail := tc.status != 200 || tc.name == "malformed_response"
			if shouldFail && err == nil {
				t.Fatal("expected failure")
			}
			if !shouldFail && (err != nil || output.String() != "hello") {
				t.Fatalf("reply=%q err=%v", output.String(), err)
			}
			if calls.Load() != 1 {
				t.Fatalf("unexpected hidden retries: calls=%d", calls.Load())
			}
			if !bytes.Contains(actual, []byte(`"tools"`)) || !bytes.Contains(actual, []byte(`read_file`)) {
				t.Fatalf("tool serialization missing: %s", actual)
			}
			if bytes.Contains(actual, []byte("fixture-secret-not-for-trace")) {
				t.Fatal("credential leaked into request body")
			}
		})
	}
}
