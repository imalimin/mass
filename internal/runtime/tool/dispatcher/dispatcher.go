// Package dispatcher validates completed response calls, executes once and records results.
package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/imalimin/mass/internal/diagnostic"
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

func (d *Dispatcher) DispatchAndRecord(ctx context.Context, s run.Snapshot, call run.Call) (err error) {
	stage := "resolve"
	toolName := ""
	defer func() {
		if ctx.Err() == nil {
			err = diagnostic.Failure(ctx, "tool.execution_failed", err, "stage=%s tool=%s call_id=%s", stage, toolName, call.ID)
		}
	}()
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
	toolName = spec.Name
	if err = d.Control.Reserve(ctx, call.ID, "tool", 0); err != nil {
		return err
	}
	stage = "validate_arguments"
	var schemaDoc, args any
	if json.Unmarshal(spec.Schema, &schemaDoc) != nil {
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
	var result run.ToolResult
	var artifact *run.Artifact
	var actionErr error
	var elapsed *int64
	argumentErr := json.Unmarshal([]byte(call.Arguments), &args)
	if argumentErr == nil {
		argumentErr = compiled.Validate(args)
	}
	if argumentErr != nil {
		// Schema violations come from the model's own tool arguments. Return a
		// recoverable tool result so the next step can correct them; protocol
		// errors stay reserved for malformed platform or transport data.
		detail := fmt.Sprintf("Invalid %s arguments: %.1024s", call.Name, argumentErr)
		raw, _ := json.Marshal(map[string]string{"error_detail": detail})
		result = run.ToolResult{
			Status: "failed", Error: "invalid_tool_input", SideEffect: "none",
			Complete: true, SourceRefs: []storage.Ref{}, Data: raw,
		}
	} else {
		stage = "execute"
		diagnostic.Record(ctx, "info", "tool.execution_started", "tool=%s call_id=%s", call.Name, call.ID)
		at := time.Now()
		d.Trace.Tool(s.ID, call.ID, "running", nil, nil)
		result, artifact, actionErr = d.Handler.Execute(ctx, s, call, args.(map[string]any))
		ms := time.Since(at).Milliseconds()
		elapsed = &ms
	}
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
	failureReported := false
	if result.Error == "tool_failed" || result.SideEffect == "unknown" {
		cause := actionErr
		if cause == nil {
			cause = run.ToolFailure(result.Error, result.SideEffect)
		}
		failureReported = diagnostic.Reported(diagnostic.Failure(ctx, "tool.execution_failed", cause, "stage=execute tool=%s call_id=%s side_effect=%s result_code=%s", call.Name, call.ID, result.SideEffect, result.Error))
	}
	result.CallID = call.ID
	stage = "validate_result"
	raw, _ := json.Marshal(result)
	if len(raw) > 2<<20 {
		return diagnostic.Failure(ctx, "tool.execution_failed", run.ErrOverflow, "stage=result_size tool=%s call_id=%s bytes=%d max_bytes=%d side_effect=%s", call.Name, call.ID, len(raw), 2<<20, result.SideEffect)
	}
	d.Trace.Tool(s.ID, call.ID, result.Status, elapsed, raw)
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
		stage = "persist_result"
		if _, err = resource.AppendRun(recordCtx, d.Journal, s, facts...); err != nil {
			return err
		}
	}
	if result.SideEffect == "unknown" {
		if errors.Is(actionErr, run.ErrCodeTimeout) || errors.Is(actionErr, run.ErrNetworkTimeout) {
			if failureReported {
				return diagnostic.ReportedAs(actionErr)
			}
			return actionErr
		}
		if failureReported {
			return diagnostic.ReportedAs(run.ErrUnknownEffect)
		}
		return run.ErrUnknownEffect
	}
	if actionErr != nil && ctx.Err() != nil {
		return actionErr
	}
	return nil
}
