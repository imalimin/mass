package einoadapter

import (
	"reflect"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestWireCompletionAndUsagePresence(t *testing.T) {
	var wire responseWire
	wire.observe([]byte("data: {\"content\":\"data: [DONE]\"}\n\n"))
	if wire.complete() {
		t.Fatal("content spoofed EOF")
	}
	wire.observe([]byte("data: {\"usage\":{\"prompt_tokens\":0,\"completion_tokens\":"))
	wire.observe([]byte("0,\"prompt_tokens_details\":{\"cached_tokens\":0}}}\r\n\r\nda"))
	wire.observe([]byte("ta:[DONE]\r\n\r\n"))
	if !wire.complete() {
		t.Fatal("valid sentinel rejected")
	}
	_, u, present, _, _ := wire.observation()
	if !present || u.Input == nil || *u.Input != 0 || u.Output == nil || u.CacheRead == nil || *u.CacheRead != 0 || u.CacheWrite != nil {
		t.Fatal("unknown and zero conflated")
	}
	var noUsage responseWire
	noUsage.observe([]byte("data: {}\n\n"))
	if _, usage, present, _, _ := noUsage.observation(); present || usage.Input != nil {
		t.Fatal("missing usage treated as zero")
	}
}

func TestStreamedMessageMatchesBatchMerge(t *testing.T) {
	first, second := 1, 4
	chunks := []*schema.Message{
		{
			Role: schema.Assistant, Content: "a", ReasoningContent: "why ",
			ToolCalls: []schema.ToolCall{{Index: &second, ID: "b", Function: schema.FunctionCall{Name: "second", Arguments: "{"}}},
			Extra:     map[string]any{"reasoning": "why ", "number": 1},
			ResponseMeta: &schema.ResponseMeta{
				Usage: &schema.TokenUsage{PromptTokens: 3}, LogProbs: &schema.LogProbs{Content: []schema.LogProb{{Token: "a"}}},
			},
		},
		{
			Content: "b", ReasoningContent: "so",
			ToolCalls: []schema.ToolCall{
				{Index: &first, ID: "a", Function: schema.FunctionCall{Name: "first", Arguments: "{}"}},
				{ID: "u", Function: schema.FunctionCall{Name: "unindexed", Arguments: "{}"}},
				{Index: &second, Type: "function", Function: schema.FunctionCall{Arguments: "}"}},
			},
			Extra: map[string]any{"reasoning": "so", "number": 2},
			ResponseMeta: &schema.ResponseMeta{
				FinishReason: "tool_calls", Usage: &schema.TokenUsage{CompletionTokens: 2},
				LogProbs: &schema.LogProbs{Content: []schema.LogProb{{Token: "b"}}},
			},
		},
	}
	want, err := schema.ConcatMessages(chunks)
	if err != nil {
		t.Fatal(err)
	}
	var message streamedMessage
	for _, chunk := range chunks {
		message.add(chunk)
	}
	got := message.result()
	if message.invalid || !reflect.DeepEqual(&got, want) {
		t.Fatalf("stream merge = %s, batch merge = %s", &got, want)
	}
}
