// Package dispatcher validates completed response calls, executes once and records results.
package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"time"

	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
	"github.com/imalimin/mass/internal/resource"
	"github.com/imalimin/mass/internal/resource/trace"
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
	Trace    *trace.Recorder
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
	at := time.Now()
	d.Trace.Tool(s.ID, call.ID, "running", nil, nil)
	result, artifact, actionErr := d.Handler.Execute(ctx, s, call, args.(map[string]any))
	elapsed := time.Since(at).Milliseconds()
	if actionErr != nil && result.Status == "" {
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
		var detail *run.ToolError
		if errors.As(actionErr, &detail) {
			code = detail.Code
			effect = detail.Effect
		}
		if errors.Is(actionErr, run.ErrOverflow) {
			code = "tool_limit_exceeded"
			effect = "none"
		}
		result = run.ToolResult{CallID: call.ID, Status: "failed", Error: code, SideEffect: effect, Complete: true, SourceRefs: []storage.Ref{}, Data: json.RawMessage(`{}`)}
	}
	result.CallID = call.ID
	raw, _ := json.Marshal(result)
	if len(raw) > 2<<20 {
		return run.ErrOverflow
	}
	if len(spec.ResultSchema) > 0 {
		var doc any
		if json.Unmarshal(spec.ResultSchema, &doc) != nil {
			return run.ErrProtocol
		}
		validator := jsonschema.NewCompiler()
		validator.AssertFormat()
		if err = validator.AddResource("https://mass.local/result", doc); err != nil {
			return err
		}
		compiled, err := validator.Compile("https://mass.local/result")
		if err != nil {
			return err
		}
		var value any
		json.Unmarshal(raw, &value)
		if err = compiled.Validate(value); err != nil {
			return run.ErrProtocol
		}
	}
	d.Trace.Tool(s.ID, call.ID, result.Status, &elapsed, raw)
	recordCtx := ctx
	if ctx.Err() != nil {
		var cancel context.CancelFunc
		recordCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
	}
	var facts []journal.Proposed
	if result.Plan != nil {
		event := journal.New(s.ActorID, "plan.updated", s.ID, *result.Plan)
		event.OperationID = call.ID
		event.CausationID = call.ResponseEventID
		facts = append(facts, event)
	}
	if artifact != nil {
		event := journal.New(s.ActorID, "artifact.published", s.ID, artifact, storage.Ref{Role: "artifact", ObjectID: artifact.ObjectID, SHA256: artifact.SHA256})
		event.OperationID = call.ID
		event.CausationID = call.ResponseEventID
		facts = append(facts, event)
	}
	if len(facts) > 0 {
		if _, err = resource.AppendRun(recordCtx, d.Journal, s, facts...); err != nil {
			return err
		}
	}
	if result.SideEffect == "unknown" {
		return run.ErrUnknownEffect
	}
	if actionErr != nil && ctx.Err() != nil {
		return actionErr
	}
	return nil
}
