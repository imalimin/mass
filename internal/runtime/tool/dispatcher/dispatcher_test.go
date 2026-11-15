package dispatcher

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/resource/trace"
)

type registry struct{ spec run.ToolSpec }

func (r registry) Describe([]run.ToolSpec) ([]run.ToolSpec, error) {
	return []run.ToolSpec{r.spec}, nil
}

type handler struct{ calls int }

func (h *handler) Execute(context.Context, run.Snapshot, run.Call, map[string]any) (run.ToolResult, *run.Artifact, error) {
	h.calls++
	return run.ToolResult{Status: "completed", Data: json.RawMessage(`{}`), SideEffect: "none", Complete: true, SourceRefs: []storage.Ref{}}, nil, nil
}

type control struct{}

func (control) Check(context.Context) error                          { return nil }
func (control) Reserve(context.Context, string, string, int64) error { return nil }

func TestInvalidArgumentsAreRecoverableToolResults(t *testing.T) {
	spec := run.ToolSpec{Name: "update_plan", Version: "1", Schema: json.RawMessage(`{
		"type":"object","additionalProperties":false,
		"properties":{
			"expected_revision":{"type":"integer"},
			"steps":{"type":"array","items":{"type":"object","additionalProperties":false,
				"properties":{"id":{"type":"string"},"title":{"type":"string"},"status":{"type":"string"}},
				"required":["id","title","status"]}}
		},
		"required":["expected_revision","steps"]
	}`)}
	for _, tc := range []struct {
		name      string
		arguments string
	}{
		{"unknown field", `{"expected_revision":0,"steps":[{"id":"setup","title":"Setup","status":"in_progress","content_type":""}]}`},
		{"invalid json", `{"expected_revision":`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder, snapshot, handler := fixture(spec, tc.arguments)
			d := &Dispatcher{Registry: registry{spec: spec}, Handler: handler, Control: control{}, Trace: recorder}
			err := d.DispatchAndRecord(context.Background(), snapshot, run.Call{ID: "call-1", NativeID: "native-1", Name: "update_plan", Arguments: tc.arguments, ResponseEventID: "event-1"})
			if err != nil {
				t.Fatal("argument error aborted the run", err)
			}
			if handler.calls != 0 {
				t.Fatal("invalid arguments reached the handler")
			}
			result := recorder.ToolResult(snapshot.ID, "call-1")
			if !bytes.Contains(result, []byte(`"invalid_tool_input"`)) || !bytes.Contains(result, []byte("error_detail")) {
				t.Fatal("tool result did not report invalid_tool_input", string(result))
			}
		})
	}
}

func TestValidArgumentsExecuteHandler(t *testing.T) {
	spec := run.ToolSpec{Name: "update_plan", Version: "1", Schema: json.RawMessage(`{
		"type":"object","additionalProperties":false,
		"properties":{
			"expected_revision":{"type":"integer"},
			"steps":{"type":"array","items":{"type":"object","additionalProperties":false,
				"properties":{"id":{"type":"string"},"title":{"type":"string"},"status":{"type":"string"}},
				"required":["id","title","status"]}}
		},
		"required":["expected_revision","steps"]
	}`)}
	arguments := `{"expected_revision":0,"steps":[{"id":"setup","title":"Setup","status":"in_progress"}]}`
	recorder, snapshot, handler := fixture(spec, arguments)
	d := &Dispatcher{Registry: registry{spec: spec}, Handler: handler, Control: control{}, Trace: recorder}
	if err := d.DispatchAndRecord(context.Background(), snapshot, run.Call{ID: "call-1", NativeID: "native-1", Name: "update_plan", Arguments: arguments, ResponseEventID: "event-1"}); err != nil {
		t.Fatal(err)
	}
	if handler.calls != 1 {
		t.Fatal("valid arguments were not executed", handler.calls)
	}
}

func fixture(spec run.ToolSpec, arguments string) (*trace.Recorder, run.Snapshot, *handler) {
	snapshot := run.Snapshot{ID: "run", ActorID: "actor", SpaceID: "space", SessionID: "session",
		Definition: run.Definition{Model: run.ModelConfig{ModelID: "model"}, Tools: []run.ToolSpec{spec}}}
	recorder := &trace.Recorder{}
	recorder.Start(snapshot, "attempt", "", "task", time.Now())
	recorder.ModelDone(snapshot.ID, run.Reply{Message: run.Message{ToolCalls: []run.Call{
		{ID: "call-1", NativeID: "native-1", Name: spec.Name, Arguments: arguments},
	}}}, "succeeded", "", time.Millisecond)
	return recorder, snapshot, &handler{}
}
