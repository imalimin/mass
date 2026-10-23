package supervisor

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/imalimin/mass/internal/domain/run"
)

type guard struct{}

func (guard) WatchAccess(ctx context.Context, _, _ string) (context.Context, func(), error) {
	return ctx, func() {}, nil
}

type coordinator struct{ cancelled atomic.Bool }

func (c *coordinator) CheckRun(context.Context, run.Snapshot) error {
	if c.cancelled.Load() {
		return run.ErrCancelled
	}
	return nil
}
func (c *coordinator) Heartbeat(ctx context.Context, s run.Snapshot) error { return c.CheckRun(ctx, s) }

type workspaces struct{}

func (workspaces) Ensure(context.Context, run.Snapshot) error { return nil }

type finisher struct{ finished chan run.Outcome }

func (f finisher) Finish(_ context.Context, _ run.Snapshot, outcome run.Outcome, _ bool) error {
	f.finished <- outcome
	return nil
}

type delayedRunner struct {
	entered chan struct{}
	exit    chan struct{}
}

func (r delayedRunner) Execute(context.Context, run.Snapshot) (run.Outcome, error) {
	close(r.entered)
	<-r.exit
	return run.Outcome{Status: "succeeded", FinalResponseEventID: "response"}, nil
}
func TestCancellationWaitsForActualExecutionExit(t *testing.T) {
	runner := delayedRunner{entered: make(chan struct{}), exit: make(chan struct{})}
	coordinator := &coordinator{}
	finished := make(chan run.Outcome, 1)
	released := atomic.Int32{}
	s := &Supervisor{Guard: guard{}, Coordinator: coordinator, Workspaces: workspaces{}, Finisher: finisher{finished: finished}, CleanupTimeout: time.Second, NewRunner: func(context.Context, run.Snapshot, time.Time) (Runner, error) { return runner, nil }}
	snapshot := run.Snapshot{ID: "run", Definition: run.Definition{TimeoutSeconds: 60}}
	if !s.Start(context.Background(), run.View{Snapshot: snapshot}, func() { released.Add(1) }) {
		t.Fatal("handoff refused")
	}
	<-runner.entered
	coordinator.cancelled.Store(true)
	s.Cancel("run")
	short, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if !errors.Is(s.Wait(short), context.DeadlineExceeded) {
		t.Fatal("reported exit before runner stopped")
	}
	if released.Load() != 0 {
		t.Fatal("released admission before runner exit")
	}
	select {
	case <-finished:
		t.Fatal("finished before runner exit")
	default:
	}
	close(runner.exit)
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := s.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	outcome := <-finished
	if outcome.Status != "cancelled" || released.Load() != 1 {
		t.Fatal("wrong finalization", outcome, released.Load())
	}
}

type panicRunner struct{}

func (panicRunner) Execute(context.Context, run.Snapshot) (run.Outcome, error) { panic("injected") }
func TestPanicStillFinishesOnce(t *testing.T) {
	finished := make(chan run.Outcome, 1)
	released := atomic.Int32{}
	s := &Supervisor{Guard: guard{}, Coordinator: &coordinator{}, Workspaces: workspaces{}, Finisher: finisher{finished: finished}, CleanupTimeout: time.Second, NewRunner: func(context.Context, run.Snapshot, time.Time) (Runner, error) { return panicRunner{}, nil }}
	s.Start(context.Background(), run.View{Snapshot: run.Snapshot{ID: "panic", Definition: run.Definition{TimeoutSeconds: 1}}}, func() { released.Add(1) })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	out := <-finished
	if out.Status != "failed" || out.Error != "executor_panic" || released.Load() != 1 {
		t.Fatal(out, released.Load())
	}
}
