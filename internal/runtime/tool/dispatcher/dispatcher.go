// Package dispatcher validates completed response calls, executes once and records results.
package dispatcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"

	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
	"github.com/imalimin/mass/internal/resource"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type Resolver interface {
	Describe([]run.ToolSpec) ([]run.ToolSpec, error)
}
type Handler interface {
	Execute(context.Context, run.Snapshot, run.Call, map[string]any) (run.ToolResult, *run.Artifact, error)
}
type Control interface {
	Check(context.Context) error
	Reserve(context.Context, string, string, int64) error
}
type Dispatcher struct {
	Registry Resolver
	Handler  Handler
	Control  Control
	Journal  *journal.Journal
	Content  *resource.ContentStore
}

func (d *Dispatcher) DispatchAndRecord(ctx context.Context, s run.Snapshot, call run.Call) error {
	tools, err := d.Registry.Describe(s.Definition.Tools)
	if err != nil {
		return err
	}
	var spec *run.ToolSpec
	for i := range tools {
		if tools[i].Name == call.Name {
			spec = &tools[i]
		}
	}
	if spec == nil || call.ResponseEventID == "" {
		return run.ErrTool
	}
	if err = d.Control.Reserve(ctx, call.ID, "tool", 0); err != nil {
		return err
	}
	var schemaDoc, args any
	if json.Unmarshal(spec.Schema, &schemaDoc) != nil || json.Unmarshal([]byte(call.Arguments), &args) != nil {
		return run.ErrProtocol
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err = compiler.AddResource("https://mass.local/tool", schemaDoc); err != nil {
		return err
	}
	compiled, err := compiler.Compile("https://mass.local/tool")
	if err != nil {
		return err
	}
	if err = compiled.Validate(args); err != nil {
		return run.ErrProtocol
	}
	data := journal.ToolData{CallID: call.ID, ResponseEventID: call.ResponseEventID, Name: call.Name, Version: spec.Version, ArgumentsSHA256: journal.Digest(call.Arguments)}
	started := journal.New(s.ActorID, "tool.execution.started", s.ID, data)
	started.CausationID = call.ResponseEventID
	started.OperationID = call.ID
	if _, err = resource.AppendRun(ctx, d.Journal, s, started); err != nil {
		return err
	}
	result, artifact, actionErr := d.Handler.Execute(ctx, s, call, args.(map[string]any))
	if actionErr != nil {
		code := "tool_failed"
		effect := "unknown"
		if errors.Is(actionErr, fs.ErrNotExist) {
			code = "file_not_found"
			effect = "none"
		}
		if errors.Is(actionErr, storage.ErrConflict) {
			code = "file_conflict"
			effect = "none"
		}
		if errors.Is(actionErr, storage.ErrInvalid) {
			code = "invalid_tool_input"
			effect = "none"
		}
		result = run.ToolResult{CallID: call.ID, Status: "failed", Error: code, SideEffect: effect, Complete: true, SourceRefs: []storage.Ref{}, Data: json.RawMessage(`{}`)}
	}
	result.CallID = call.ID
	raw, _ := json.Marshal(result)
	o, err := d.Content.Put(ctx, s.ActorID, s.SpaceID, uuid.NewString(), "tool.result", "application/json", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	data.ContentID = o.ID
	data.Status = result.Status
	data.SideEffect = result.SideEffect
	previewRaw := raw
	if len(previewRaw) > 4096 {
		previewRaw, _ = json.Marshal(map[string]any{"status": result.Status, "truncated": true, "payloadRef": o.ID, "side_effect": result.SideEffect})
	}
	data.Preview = string(previewRaw)
	refs := []storage.Ref{{Role: "result", ObjectID: o.ID, SHA256: o.SHA256}}
	for i, ref := range result.SourceRefs {
		ref.Ordinal = i
		refs = append(refs, ref)
	}
	completed := journal.New(s.ActorID, "tool.execution.completed", s.ID, data, refs...)
	completed.CausationID = started.EventID
	completed.OperationID = call.ID
	events := []journal.Proposed{}
	if artifact != nil {
		e := journal.New(s.ActorID, "artifact.published", s.ID, artifact, storage.Ref{Role: "artifact", ObjectID: artifact.ObjectID, SHA256: artifact.SHA256})
		e.OperationID = call.ID
		events = append(events, e)
	}
	events = append(events, completed)
	_, err = resource.AppendRun(ctx, d.Journal, s, events...)
	if err != nil {
		return err
	}
	if result.SideEffect == "unknown" {
		return errors.New("tool_effect_unknown")
	}
	return nil
}
