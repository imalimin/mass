// Package supervisor owns execution lifetime and waits for real I/O exit before finish.
package supervisor

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/imalimin/mass/internal/domain/run"
)

type Runner interface {
	Execute(context.Context, run.Snapshot) (run.Outcome, error)
}
type Guard interface {
	WatchAccess(context.Context, string, string) (context.Context, func(), error)
}
type Coordinator interface {
	CheckRun(context.Context, run.Snapshot) error
	Heartbeat(context.Context, run.Snapshot) error
}
type Workspaces interface {
	Ensure(context.Context, run.Snapshot) error
}
type Finisher interface {
	Finish(context.Context, run.Snapshot, run.Outcome, bool) error
}
type Supervisor struct {
	Guard          Guard
	Coordinator    Coordinator
	Workspaces     Workspaces
	Finisher       Finisher
	NewRunner      func(context.Context, run.Snapshot, time.Time) (Runner, error)
	CleanupTimeout time.Duration
	mu             sync.Mutex
	active         map[string]context.CancelFunc
	wg             sync.WaitGroup
	Fault          func(error)
}

func (s *Supervisor) Start(parent context.Context, v run.View, release func()) bool {
	s.mu.Lock()
	if s.active == nil {
		s.active = map[string]context.CancelFunc{}
	}
	if _, exists := s.active[v.ID]; exists {
		s.mu.Unlock()
		return false
	}
	ctx, cancel := context.WithCancel(parent)
	s.active[v.ID] = cancel
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		defer release()
		defer func() { s.mu.Lock(); delete(s.active, v.ID); s.mu.Unlock(); cancel() }()
		s.run(ctx, v)
	}()
	return true
}
func (s *Supervisor) Cancel(id string) {
	s.mu.Lock()
	cancel := s.active[id]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
func (s *Supervisor) Stop() {
	s.mu.Lock()
	for _, cancel := range s.active {
		cancel()
	}
	s.mu.Unlock()
}
func (s *Supervisor) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func Code(err error) string {
	if err == nil {
		return ""
	}
	for _, candidate := range []error{run.ErrBudget, run.ErrOverflow, run.ErrCancelled, run.ErrProtocol, run.ErrTool, run.ErrUnavailable} {
		if errors.Is(err, candidate) {
			return candidate.Error()
		}
	}
	switch err.Error() {
	case "forbidden", "not_found", "unauthenticated":
		return "permission_revoked"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "run_timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "interrupted"
	}
	return "execution_failed"
}
func (s *Supervisor) run(parent context.Context, v run.View) {
	snapshot := v.Snapshot
	outcome := run.Outcome{Status: "failed", Error: "interrupted"}
	workspaceReady := false
	defer func() {
		if recover() != nil {
			outcome = run.Outcome{Status: "failed", Error: "executor_panic"}
		}
		cleanup, cancel := context.WithTimeout(context.Background(), s.CleanupTimeout)
		defer cancel()
		if err := s.Finisher.Finish(cleanup, snapshot, outcome, workspaceReady); err != nil && s.Fault != nil {
			s.Fault(err)
		}
	}()
	ctx, stop := context.WithTimeout(parent, time.Duration(snapshot.Definition.TimeoutSeconds)*time.Second)
	defer stop()
	authorized, unwatch, err := s.Guard.WatchAccess(ctx, snapshot.ActorID, snapshot.SpaceID)
	if err != nil {
		outcome.Error = Code(err)
		return
	}
	defer unwatch()
	heartbeatDone := make(chan struct{})
	var heartbeatErr error
	var hbMu sync.Mutex
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-authorized.Done():
				return
			case <-ticker.C:
				probe, cancel := context.WithTimeout(authorized, 3*time.Second)
				err := s.Coordinator.Heartbeat(probe, snapshot)
				cancel()
				if err != nil {
					hbMu.Lock()
					heartbeatErr = err
					hbMu.Unlock()
					stop()
					return
				}
			}
		}
	}()
	defer func() { stop(); <-heartbeatDone }()
	if err = s.Coordinator.CheckRun(authorized, snapshot); err != nil {
		outcome.Error = Code(err)
		return
	}
	if err = s.Workspaces.Ensure(authorized, snapshot); err != nil {
		outcome.Error = Code(err)
		return
	}
	workspaceReady = true
	started := time.Now()
	if v.StartedAt != nil {
		started = *v.StartedAt
	}
	runner, err := s.NewRunner(authorized, snapshot, started)
	if err == nil {
		outcome, err = runner.Execute(authorized, snapshot)
	}
	if err == nil {
		err = s.Coordinator.CheckRun(authorized, snapshot)
	}
	if err != nil {
		probe, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		current := s.Coordinator.CheckRun(probe, snapshot)
		cancel()
		if current != nil && current.Error() != "context canceled" {
			err = current
		}
	}
	hbMu.Lock()
	hbErr := heartbeatErr
	hbMu.Unlock()
	if hbErr != nil && Code(err) != "permission_revoked" && !errors.Is(err, run.ErrCancelled) {
		err = hbErr
	}
	if err != nil {
		outcome = run.Outcome{Status: "failed", Error: Code(err)}
		if errors.Is(err, run.ErrCancelled) {
			outcome.Status = "cancelled"
		}
	}
	if outcome.Status == "" {
		outcome = run.Outcome{Status: "failed", Error: "execution_failed"}
	}
}
