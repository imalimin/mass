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

func (t *ModelTransport) Stream(ctx context.Context, s run.Snapshot, p run.Prepared, key string, delta func(string) error) (reply run.Reply, retErr error) {
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
	wire := &responseWire{}
	started := time.Now()
	observation := &run.ModelObservation{Component: "eino-ark-v0.1.71"}
	if t.Fake != nil {
		observation.Component = "eino-fake"
	}
	defer func() {
		observation.DurationMS = time.Since(started).Milliseconds()
		reply.Observation = observation
		if t.Fake == nil {
			observation.HTTPStatus, reply.Usage, reply.Trace.UsagePresent, reply.Trace.ReasoningTokens = wire.observation()
		}
		status := observation.HTTPStatus
		if retErr != nil && (status == 429 || status == 502 || status == 503 || status == 504) {
			retErr = &run.ModelFailure{Code: "model_http_failed", Status: status, Retryable: true}
		}
	}()

	if t.Fake != nil {
		chat = t.Fake
	} else {
		base := t.Base
		if base == nil {
			base = http.DefaultTransport
		}
		capture := &observedTransport{base: base, wire: wire}
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
	var text, reasoning strings.Builder
	defer func() {
		if reasoning.Len() != 0 {
			value := reasoning.String()
			reply.Trace.Reasoning = &value
		}
		if retErr != nil {
			reply.Message.Content = text.String()
			if partial, err := schema.ConcatMessages(chunks); err == nil && partial != nil {
				reply.Trace.ToolCalls = nativeToolCalls(partial.ToolCalls)
				if partial.ResponseMeta != nil {
					reply.FinishReason = partial.ResponseMeta.FinishReason
				}
			}
		}
	}()
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
		chunks = append(chunks, chunk)
		text.WriteString(chunk.Content)
		reasoning.WriteString(chunk.ReasoningContent)
		if t.Fake != nil {
			observation.HTTPStatus = 200
		}
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
	if t.Fake == nil && !wire.complete() {
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
	reply.Trace.ToolCalls = nativeToolCalls(combined.ToolCalls)
	for _, call := range combined.ToolCalls {
		reply.Message.ToolCalls = append(reply.Message.ToolCalls, run.Call{ID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments})
	}
	if usage := combined.ResponseMeta.Usage; t.Fake != nil && usage != nil {
		reply.Trace.UsagePresent = true
		input, output := int64(usage.PromptTokens), int64(usage.CompletionTokens)
		reply.Usage.Input = &input
		reply.Usage.Output = &output
		reasoning := int64(usage.CompletionTokensDetails.ReasoningTokens)
		reply.Trace.ReasoningTokens = &reasoning
		// Cache metrics are optional in wire JSON; absence remains unknown.
		if usage.PromptTokenDetails.CachedTokens > 0 {
			v := int64(usage.PromptTokenDetails.CachedTokens)
			reply.Usage.CacheRead = &v
		}
	}
	return reply, nil
}

func nativeToolCalls(calls []schema.ToolCall) []run.NativeToolCall {
	result := make([]run.NativeToolCall, 0, len(calls))
	for _, call := range calls {
		result = append(result, run.NativeToolCall{ID: call.ID, Type: call.Type, Function: run.NativeFunctionCall{
			Name: call.Function.Name, Arguments: call.Function.Arguments,
		}})
	}
	return result
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
func wireUsage(raw []byte) run.Usage {
	usage, _, _ := wireTraceUsage(raw)
	return usage
}

func wireTraceUsage(raw []byte) (result run.Usage, present bool, reasoning *int64) {
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
		if json.Unmarshal(item["usage"], &usage) != nil || usage == nil {
			continue
		}
		present = true
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
		merge := func(target **int64, n *int64) {
			if n != nil {
				*target = n
			}
		}
		merge(&result.Input, value(usage, "prompt_tokens"))
		merge(&result.Output, value(usage, "completion_tokens"))
		merge(&result.CacheWrite, value(usage, "cache_creation_input_tokens"))
		var prompt, completion map[string]json.RawMessage
		if json.Unmarshal(usage["prompt_tokens_details"], &prompt) == nil {
			merge(&result.CacheRead, value(prompt, "cached_tokens"))
		}
		if json.Unmarshal(usage["completion_tokens_details"], &completion) == nil {
			merge(&reasoning, value(completion, "reasoning_tokens"))
		}
	}
	return
}
