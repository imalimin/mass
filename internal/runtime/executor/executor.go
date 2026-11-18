// Package executor owns the only ReAct loop and returns outcomes, never status writes.
package executor

import (
	"context"

	"github.com/imalimin/mass/internal/diagnostic"
	"github.com/imalimin/mass/internal/domain/run"
)

type ContextPreparer interface {
	Prepare(context.Context, run.Snapshot) (run.Prepared, error)
}
type ModelCaller interface {
	Call(context.Context, run.Snapshot, run.Prepared) (run.Reply, error)
}
type ToolDispatcher interface {
	DispatchAndRecord(context.Context, run.Snapshot, run.Call) error
}
type RunControl interface{ Check(context.Context) error }
type Executor struct {
	Planner ContextPreparer
	Model   ModelCaller
	Tools   ToolDispatcher
	Control RunControl
}

func (e *Executor) Execute(ctx context.Context, s run.Snapshot) (run.Outcome, error) {
	for step := 0; step < s.Definition.MaxSteps; step++ {
		if err := e.Control.Check(ctx); err != nil {
			return run.Outcome{}, err
		}
		prepared, err := e.Planner.Prepare(ctx, s)
		if err != nil {
			return run.Outcome{}, err
		}
		reply, err := e.Model.Call(ctx, s, prepared)
		if err != nil {
			return run.Outcome{}, err
		}
		if len(reply.Message.ToolCalls) == 0 {
			if reply.ResponseEventID == "" || reply.FinishReason != "stop" {
				return run.Outcome{}, run.ErrProtocol
			}
			return run.Outcome{Status: "succeeded", FinalResponseEventID: reply.ResponseEventID}, nil
		}
		for _, call := range reply.Message.ToolCalls {
			if err = e.Control.Check(ctx); err != nil {
				return run.Outcome{}, err
			}
			if call.ResponseEventID != reply.ResponseEventID {
				return run.Outcome{}, run.ErrProtocol
			}
			if err = e.Tools.DispatchAndRecord(ctx, s, call); err != nil {
				return run.Outcome{}, err
			}
		}
	}
	return run.Outcome{}, diagnostic.Failure(ctx, "run.limit_exceeded", run.ErrBudget, "limit=steps steps=%d max_steps=%d", s.Definition.MaxSteps, s.Definition.MaxSteps)
}
