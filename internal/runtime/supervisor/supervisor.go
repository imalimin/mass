// Package supervisor owns execution lifetime and waits for real I/O exit before finish.
package supervisor

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
)

type Runner interface {
	Execute(context.Context, run.Snapshot) (run.Outcome, error)
}
type Guard interface {
	SubscribeAccess(context.Context, string, string) (context.Context, func())
}
type Coordinator interface {
	CheckRun(context.Context, run.Snapshot) error
}
type Workspaces interface {
	Ensure(context.Context, run.Snapshot) ([]string, error)
}

const checkTimeout = 3 * time.Second

type Finisher interface {
	Finish(context.Context, run.Snapshot, run.CleanupPlan) error
}
type Supervisor struct {
	Guard          Guard
	Coordinator    Coordinator
	Workspaces     Workspaces
	Finisher       Finisher
	NewRunner      func(context.Context, run.Snapshot, time.Time, []string) (Runner, error)
	CleanupTimeout time.Duration
	mu             sync.Mutex
	active         map[string]context.CancelCauseFunc
	wg             sync.WaitGroup
	Fault          func(error)
}

func (s *Supervisor) Start(parent context.Context, v run.View, release func()) bool {
	s.mu.Lock()
	if s.active == nil {
		s.active = map[string]context.CancelCauseFunc{}
	}
	if _, exists := s.active[v.ID]; exists {
		s.mu.Unlock()
		return false
	}
	ctx, cancel := context.WithCancelCause(parent)
	s.active[v.ID] = cancel
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		defer release()
		defer func() { s.mu.Lock(); delete(s.active, v.ID); s.mu.Unlock(); cancel(nil) }()
		s.run(ctx, v)
	}()
	return true
}
func (s *Supervisor) Cancel(id string) {
	s.mu.Lock()
	cancel := s.active[id]
	s.mu.Unlock()
	if cancel != nil {
		cancel(run.ErrCancelled)
	}
}
func (s *Supervisor) Stop() {
	s.mu.Lock()
	for _, cancel := range s.active {
		cancel(nil)
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
	for _, candidate := range []error{run.ErrSummary, run.ErrCodeTimeout, run.ErrNetworkTimeout, run.ErrUnknownEffect, run.ErrBudget, run.ErrOverflow, run.ErrCancelled, run.ErrProtocol, run.ErrTool, run.ErrUnavailable, run.ErrPermission, storage.ErrWorkspace} {
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

func failureDetail(source string, err, probeErr error, started, failedAt time.Time, timeout time.Duration) *run.FailureDetail {
	detail := &run.FailureDetail{
		Source: source, Reason: Code(err), FailedAt: failedAt.UTC(),
		DurationMS: failedAt.Sub(started).Milliseconds(), TimeoutMS: timeout.Milliseconds(),
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(probeErr, context.DeadlineExceeded) {
		detail.Reason = "deadline_exceeded"
	}
	var check *run.CheckError
	if errors.As(err, &check) {
		detail.Stage, detail.MySQLCode = check.Stage, check.MySQLCode
	}
	return detail
}

func (s *Supervisor) run(parent context.Context, v run.View) {
	snapshot := v.Snapshot
	outcome := run.Outcome{Status: "failed", Error: "interrupted"}
	workspaceReady := false
	var missingLocalFiles []string
	runTimeout := time.Duration(snapshot.Definition.TimeoutSeconds) * time.Second
	runStarted := time.Now()
	ctx, stop := context.WithTimeout(parent, runTimeout)
	defer func() {
		if recover() != nil {
			outcome = run.Outcome{Status: "failed", Error: "executor_panic"}
		}
		if outcome.FailureDetail == nil && outcome.Status == "failed" && outcome.Error == "run_timeout" && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			deadline, _ := ctx.Deadline()
			outcome.FailureDetail = failureDetail("run_deadline", ctx.Err(), nil, runStarted, deadline, runTimeout)
		}
		cleanup, cancel := context.WithTimeout(context.Background(), s.CleanupTimeout)
		defer cancel()
		plan := run.CleanupPlan{Outcome: outcome, WorkspaceReady: workspaceReady, MissingLocalFiles: missingLocalFiles}
		if err := s.Finisher.Finish(cleanup, snapshot, plan); err != nil && s.Fault != nil {
			s.Fault(err)
		}
	}()
	defer stop()
	authorized, unwatch := s.Guard.SubscribeAccess(ctx, snapshot.ActorID, snapshot.SpaceID)
	defer unwatch()
	started := time.Now()
	probe, cancel := context.WithTimeout(authorized, checkTimeout)
	err := s.Coordinator.CheckRun(probe, snapshot)
	failedAt, probeErr := time.Now(), probe.Err()
	cancel()
	var detail *run.FailureDetail
	if err != nil && authorized.Err() == nil && (errors.Is(err, context.DeadlineExceeded) || errors.Is(probeErr, context.DeadlineExceeded)) {
		detail = failureDetail("start_check", err, probeErr, started, failedAt, checkTimeout)
	}
	if err == nil && authorized.Err() == nil {
		missingLocalFiles, err = s.Workspaces.Ensure(authorized, snapshot)
		if err == nil {
			workspaceReady = true
			started = time.Now()
			if v.StartedAt != nil {
				started = *v.StartedAt
			}
			var runner Runner
			if runner, err = s.NewRunner(authorized, snapshot, started, missingLocalFiles); err == nil {
				outcome, err = runner.Execute(authorized, snapshot)
			}
		}
	}
	if cause := context.Cause(authorized); cause != nil {
		err = cause
	}
	if err != nil {
		outcome = run.Outcome{Status: "failed", Error: Code(err), FailureDetail: detail}
		if errors.Is(err, run.ErrCancelled) {
			outcome.Status = "cancelled"
		}
	}
	if outcome.Status == "" {
		outcome = run.Outcome{Status: "failed", Error: "execution_failed"}
	}
}
