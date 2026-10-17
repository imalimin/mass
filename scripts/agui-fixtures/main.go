// Command agui-fixtures generates cross-language wire fixtures from the locked
// Go SDK. Files are deterministic and checked for drift by make generate-check.
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strconv"

	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	"github.com/imalimin/mass/internal/transport/agui"
)

func main() {
	role := "tool"
	result := events.NewToolCallResultEvent("tool-message-1", "call-1", `{"status":"completed"}`)
	result.Role = &role
	items := []events.Event{
		events.NewRunStartedEvent("session-1", "run-1"),
		events.NewTextMessageStartEvent("message-1", events.WithRole("assistant")),
		events.NewTextMessageContentEvent("message-1", "你好\nMASS"), events.NewTextMessageEndEvent("message-1"),
		events.NewToolCallStartEvent("call-1", "read_file", events.WithParentMessageID("message-1")),
		events.NewToolCallArgsEvent("call-1", `{"path":"notes.txt"}`), events.NewToolCallEndEvent("call-1"), result,
		events.NewRunFinishedEvent("session-1", "run-1"),
		events.NewRunErrorEvent("Run cancelled", events.WithErrorCode("RUN_CANCELLED")),
	}
	var profile struct {
		CustomNames []string `json:"customNames"`
	}
	data, err := os.ReadFile("api/events/agui-profile.json")
	must(err)
	must(json.Unmarshal(data, &profile))
	for _, name := range profile.CustomNames {
		items = append(items, events.NewCustomEvent(name, events.WithValue(map[string]any{"schemaVersion": 1, "threadId": "session-1", "runId": "run-1", "sourceEventIds": []string{"event-1"}})))
	}
	var frames bytes.Buffer
	for i, e := range items {
		e.(interface{ SetTimestamp(int64) }).SetTimestamp(0)
		must(agui.EncodeFrame(&frames, strconv.Itoa(i+1), e))
	}
	data, err = json.MarshalIndent(items, "", "  ")
	must(err)
	must(os.MkdirAll("api/events/fixtures", 0755))
	must(os.WriteFile("api/events/fixtures/go-events.json", append(data, '\n'), 0644))
	must(os.WriteFile("api/events/fixtures/go-events.sse", frames.Bytes(), 0644))
}
func must(err error) {
	if err != nil {
		panic(err)
	}
}
