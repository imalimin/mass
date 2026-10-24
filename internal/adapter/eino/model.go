package einoadapter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/cloudwego/eino-ext/components/model/ark"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	jschema "github.com/eino-contrib/jsonschema"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/runtime/modelgateway"
)

type ModelTransport struct {
	Base http.RoundTripper
	Fake model.ToolCallingChatModel
}

func (t *ModelTransport) Source() string {
	if t.Fake != nil {
		return "fake"
	}
	return "ark"
}

type wireRecorder struct {
	modelgateway.Recorder
	raw    bytes.Buffer
	status int
	failed error
}

func (w *wireRecorder) Status(status int) { w.status = status }
func (w *wireRecorder) Request(ctx context.Context, b []byte) error {
	err := w.Recorder.Request(ctx, b)
	w.failed = err
	return err
}
func (w *wireRecorder) Response(ctx context.Context, status int, b []byte) error {
	if err := w.Recorder.Response(ctx, status, b); err != nil {
		w.failed = err
		return err
	}
	w.status = status
	w.raw.Write(b)
	return nil
}
func (t *ModelTransport) Stream(ctx context.Context, s run.Snapshot, p run.Prepared, key string, record modelgateway.Recorder, delta func(string) error) (reply run.Reply, retErr error) {
	messages := make([]*schema.Message, 0, len(p.Messages))
	for _, m := range p.Messages {
		var out schema.Message
		if len(m.Native) > 0 {
			if json.Unmarshal(m.Native, &out) != nil {
				return reply, run.ErrProtocol
			}
		} else {
			out.Role = schema.RoleType(m.Role)
			out.Content = m.Content
			out.ToolCallID = m.ToolCallID
			for _, call := range m.ToolCalls {
				out.ToolCalls = append(out.ToolCalls, schema.ToolCall{ID: func() string {
					if call.NativeID != "" {
						return call.NativeID
					}
					return call.ID
				}(), Type: "function", Function: schema.FunctionCall{Name: call.Name, Arguments: call.Arguments}})
			}
		}
		messages = append(messages, &out)
	}
	tools := make([]*schema.ToolInfo, 0, len(p.Tools))
	for _, tool := range p.Tools {
		var schemaValue jschema.Schema
		if err := json.Unmarshal(tool.Schema, &schemaValue); err != nil {
			return reply, run.ErrTool
		}
		tools = append(tools, &schema.ToolInfo{Name: tool.Name, Desc: tool.Description, ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&schemaValue)})
	}
	var chat model.ToolCallingChatModel
	wire := &wireRecorder{Recorder: record}
	started := time.Now()
	observation := &run.ModelObservation{Component: "eino-ark-v0.1.71"}
	if t.Fake != nil {
		observation.Component = "eino-fake"
	}
	defer func() {
		observation.DurationMS = time.Since(started).Milliseconds()
		observation.HTTPStatus = wire.status
		reply.Observation = observation
		if t.Fake == nil {
			reply.Usage = wireUsage(wire.raw.Bytes())
		}
		if retErr != nil && wire.failed == nil && (wire.status == 429 || wire.status == 502 || wire.status == 503 || wire.status == 504) {
			retErr = &run.ModelFailure{Code: "model_http_failed", Status: wire.status, Retryable: true}
		}
	}()

	if t.Fake != nil {
		chat = t.Fake
		raw, _ := json.Marshal(struct {
			Source   string
			Messages []*schema.Message
			Tools    []*schema.ToolInfo
		}{"fake", messages, tools})
		if err := record.Request(ctx, raw); err != nil {
			return reply, err
		}
	} else {
		base := t.Base
		if base == nil {
			base = http.DefaultTransport
		}
		capture := &CaptureTransport{Base: base, Recorder: wire, MaxRequestBytes: 4 << 20, MaxResponseBytes: 8 << 20}
		zero := 0
		temperature := float32(s.Definition.Model.Temperature)
		output := s.Definition.OutputTokens
		client := &http.Client{Transport: capture, Timeout: 120 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		created, err := ark.NewChatModel(ctx, &ark.ChatModelConfig{APIKey: key, Model: s.Definition.Model.ModelID, BaseURL: s.Definition.Model.Endpoint, HTTPClient: client, RetryTimes: &zero, MaxTokens: &output, Temperature: &temperature})
		if err != nil {
			return reply, run.ErrProtocol
		}
		chat = created
	}
	if len(tools) > 0 {
		bound, err := chat.WithTools(tools)
		if err != nil {
			return reply, run.ErrProtocol
		}
		chat = bound
	}
	stream, err := chat.Stream(ctx, messages)
	if err != nil {
		return reply, errors.New("model_transport_failed")
	}
	defer stream.Close()
	chunks := make([]*schema.Message, 0, 32)
	size := 0
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return reply, errors.New("model_stream_failed")
		}
		if chunk == nil {
			return reply, run.ErrProtocol
		}
		raw, _ := json.Marshal(chunk)
		size += len(raw)
		if size > 4<<20 || len(chunks) >= 10000 {
			return reply, run.ErrOverflow
		}
		if t.Fake != nil {
			if err = record.Response(ctx, 200, raw); err != nil {
				return reply, err
			}
		}
		chunks = append(chunks, chunk)
		if chunk.Content != "" {
			if observation.FirstTokenMS == nil {
				ms := time.Since(started).Milliseconds()
				observation.FirstTokenMS = &ms
			}
			if err = delta(chunk.Content); err != nil {
				return reply, err
			}
		}
	}
	if t.Fake == nil && !wireComplete(wire.raw.Bytes()) {
		return reply, run.ErrProtocol
	}
	combined, err := schema.ConcatMessages(chunks)
	if err != nil || combined == nil || combined.ResponseMeta == nil {
		return reply, run.ErrProtocol
	}
	if combined.Role == "" {
		combined.Role = schema.Assistant
	}
	if combined.Role != schema.Assistant {
		return reply, run.ErrProtocol
	}
	native, _ := json.Marshal(combined)
	reply.Message = run.Message{Role: "assistant", Content: combined.Content, Native: native}
	reply.FinishReason = combined.ResponseMeta.FinishReason
	for _, call := range combined.ToolCalls {
		reply.Message.ToolCalls = append(reply.Message.ToolCalls, run.Call{ID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments})
	}
	if usage := combined.ResponseMeta.Usage; usage != nil {
		input, output := int64(usage.PromptTokens), int64(usage.CompletionTokens)
		reply.Usage.Input = &input
		reply.Usage.Output = &output
		// Cache metrics are optional in wire JSON; absence remains unknown.
		if usage.PromptTokenDetails.CachedTokens > 0 {
			v := int64(usage.PromptTokenDetails.CachedTokens)
			reply.Usage.CacheRead = &v
		}
	}
	if t.Fake == nil {
		reply.Usage = wireUsage(wire.raw.Bytes())
	}
	return reply, nil
}

func wireComplete(raw []byte) bool {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 8192), 8<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") && strings.TrimSpace(strings.TrimPrefix(line, "data:")) == "[DONE]" {
			return true
		}
	}
	return false
}
func wireUsage(raw []byte) (result run.Usage) {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 8192), 8<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var item map[string]json.RawMessage
		if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &item) != nil {
			continue
		}
		var usage map[string]json.RawMessage
		if json.Unmarshal(item["usage"], &usage) != nil {
			continue
		}
		value := func(m map[string]json.RawMessage, key string) *int64 {
			raw, ok := m[key]
			if !ok || string(raw) == "null" {
				return nil
			}
			var n int64
			if json.Unmarshal(raw, &n) != nil || n < 0 {
				return nil
			}
			return &n
		}
		result.Input = value(usage, "prompt_tokens")
		result.Output = value(usage, "completion_tokens")
		var details map[string]json.RawMessage
		if json.Unmarshal(usage["prompt_tokens_details"], &details) == nil {
			result.CacheRead = value(details, "cached_tokens")
		}
		if raw, ok := usage["cache_creation_input_tokens"]; ok {
			var n int64
			if json.Unmarshal(raw, &n) == nil && n >= 0 {
				result.CacheWrite = &n
			}
		}
	}
	return
}
