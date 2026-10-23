package einoadapter

import "testing"

func TestWireCompletionAndUsagePresence(t *testing.T) {
	if wireComplete([]byte("data: {\"content\":\"data: [DONE]\"}\n\n")) {
		t.Fatal("content spoofed EOF")
	}
	if !wireComplete([]byte("data:[DONE]\r\n\r\n")) {
		t.Fatal("valid sentinel rejected")
	}
	u := wireUsage([]byte("data: {\"usage\":{\"prompt_tokens\":0,\"completion_tokens\":0,\"prompt_tokens_details\":{\"cached_tokens\":0}}}\n\n"))
	if u.Input == nil || *u.Input != 0 || u.Output == nil || u.CacheRead == nil || *u.CacheRead != 0 || u.CacheWrite != nil {
		t.Fatal("unknown and zero conflated")
	}
	if wireUsage([]byte("data: {}\n\n")).Input != nil {
		t.Fatal("missing usage treated as zero")
	}
}
