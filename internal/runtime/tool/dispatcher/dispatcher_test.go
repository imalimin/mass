package dispatcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/imalimin/mass/internal/diagnostic"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/resource/trace"
)

type registry struct{ spec run.ToolSpec }

func (r registry) Describe([]run.ToolSpec) ([]run.ToolSpec, error) {
	return []run.ToolSpec{r.spec}, nil
}

type handler struct {
	calls int
	err   error
}

func (h *handler) Execute(context.Context, run.Snapshot, run.Call, map[string]any) (run.ToolResult, *run.Artifact, error) {
	h.calls++
	if h.err != nil {
		return run.ToolResult{}, nil, h.err
	}
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

func TestGenericToolFailureLogsClassifiedCauseAndEffect(t *testing.T) {
	spec := run.ToolSpec{Name: "write_file", Version: "1", Schema: json.RawMessage(`{"type":"object"}`)}
	recorder, snapshot, handler := fixture(spec, `{}`)
	handler.err = &os.PathError{Op: "write", Path: "/private/payload.txt", Err: syscall.ENOSPC}
	snapshot.SpaceID, snapshot.SessionID = "aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb"
	root := t.TempDir()
	store := diagnostic.NewStore(root, "", nil, slog.New(slog.DiscardHandler))
	ctx := diagnostic.WithRun(context.Background(), store, snapshot)
	d := &Dispatcher{Registry: registry{spec: spec}, Handler: handler, Control: control{}, Trace: recorder}
	if err := d.DispatchAndRecord(ctx, snapshot, run.Call{ID: "call-1", Name: spec.Name, Arguments: `{}`, ResponseEventID: "event-1"}); !errors.Is(err, run.ErrUnknownEffect) {
		t.Fatalf("failure did not retain unknown-effect outcome: %v", err)
	}
	if result := recorder.ToolResult(snapshot.ID, "call-1"); !bytes.Contains(result, []byte(`"tool_failed"`)) {
		t.Fatalf("tool result lost its original error code: %s", result)
	}
	raw, err := os.ReadFile(filepath.Join(root, snapshot.SpaceID, "sessions", snapshot.SessionID, snapshot.SessionID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	rows := bytes.Split(bytes.TrimSuffix(raw, []byte{'\n'}), []byte{'\n'})
	if len(rows) != 2 {
		t.Fatalf("wanted tool start and one failure: %s", raw)
	}
	var failure diagnostic.Entry
	if err := json.Unmarshal(rows[1], &failure); err != nil || failure.Event != "tool.execution_failed" || !strings.Contains(failure.Message, "stage=execute tool=write_file call_id=call-1 side_effect=unknown") || !strings.Contains(failure.Message, "cause=no_space_left") || strings.Contains(failure.Message, "payload.txt") {
		t.Fatalf("tool diagnostics: %s (%v)", raw, err)
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
