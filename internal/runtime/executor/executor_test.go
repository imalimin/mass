package executor

import (
	"context"
	"errors"
	"github.com/imalimin/mass/internal/domain/run"
	"testing"
)

type plannerFake struct{ calls int }

func (p *plannerFake) Prepare(context.Context, run.Snapshot) (run.Prepared, error) {
	p.calls++
	return run.Prepared{}, nil
}

type modelFake struct {
	reply run.Reply
	err   error
	calls int
}

func (m *modelFake) Call(context.Context, run.Snapshot, run.Prepared) (run.Reply, error) {
	m.calls++
	return m.reply, m.err
}

type toolFake struct{ calls int }

func (t *toolFake) DispatchAndRecord(context.Context, run.Snapshot, run.Call) error {
	t.calls++
	return nil
}

type controlFake struct{ err error }

func (c controlFake) Check(context.Context) error { return c.err }
func TestConsumerContracts(t *testing.T) {
	for _, tc := range []struct {
		name            string
		reply           run.Reply
		err, stop, want error
		toolCalls       int
	}{
		{name: "text", reply: run.Reply{ResponseEventID: "completed", FinishReason: "stop"}},
		{name: "failed model", err: run.ErrProtocol, want: run.ErrProtocol},
		{name: "wrong response source", reply: run.Reply{ResponseEventID: "completed", Message: run.Message{ToolCalls: []run.Call{{ResponseEventID: "other"}}}}, want: run.ErrProtocol},
		{name: "step budget", reply: run.Reply{ResponseEventID: "completed", Message: run.Message{ToolCalls: []run.Call{{ResponseEventID: "completed"}}}}, want: run.ErrBudget, toolCalls: 1},
		{name: "cancelled", stop: run.ErrCancelled, want: run.ErrCancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, m, tools := &plannerFake{}, &modelFake{reply: tc.reply, err: tc.err}, &toolFake{}
			e := Executor{Planner: p, Model: m, Tools: tools, Control: controlFake{tc.stop}}
			out, err := e.Execute(context.Background(), run.Snapshot{Definition: run.Definition{MaxSteps: 1}})
			if !errors.Is(err, tc.want) || tools.calls != tc.toolCalls {
				t.Fatal(out, err, tools.calls)
			}
			if tc.stop != nil && (p.calls != 0 || m.calls != 0) {
				t.Fatal("work after stop")
			}
			if tc.want == nil && (out.Status != "succeeded" || out.FinalResponseEventID != "completed") {
				t.Fatal(out)
			}
		})
	}
}
