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

const checkTimeout = 3 * time.Second

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
	for _, candidate := range []error{run.ErrSummary, run.ErrUnknownEffect, run.ErrBudget, run.ErrOverflow, run.ErrCancelled, run.ErrProtocol, run.ErrTool, run.ErrUnavailable} {
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
	var heartbeatFailure *run.FailureDetail
	runTimeout := time.Duration(snapshot.Definition.TimeoutSeconds) * time.Second
	runStarted := time.Now()
	ctx, stop := context.WithTimeout(parent, runTimeout)
	defer func() {
		if recover() != nil {
			outcome = run.Outcome{Status: "failed", Error: "executor_panic"}
		}
		if outcome.FailureDetail == nil && outcome.Status == "failed" {
			switch {
			case outcome.Error == "interrupted" && heartbeatFailure != nil:
				outcome.FailureDetail = heartbeatFailure
			case outcome.Error == "run_timeout" && errors.Is(ctx.Err(), context.DeadlineExceeded):
				deadline, _ := ctx.Deadline()
				outcome.FailureDetail = failureDetail("run_deadline", ctx.Err(), nil, runStarted, deadline, runTimeout)
			}
		}
		cleanup, cancel := context.WithTimeout(context.Background(), s.CleanupTimeout)
		defer cancel()
		if err := s.Finisher.Finish(cleanup, snapshot, outcome, workspaceReady); err != nil && s.Fault != nil {
			s.Fault(err)
		}
	}()
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
				started := time.Now()
				probe, cancel := context.WithTimeout(authorized, checkTimeout)
				err := s.Coordinator.Heartbeat(probe, snapshot)
				failedAt := time.Now()
				probeErr := probe.Err()
				cancel()
				if err != nil {
					hbMu.Lock()
					heartbeatErr = err
					if authorized.Err() == nil {
						heartbeatFailure = failureDetail("heartbeat", err, probeErr, started, failedAt, checkTimeout)
					}
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
	var detail *run.FailureDetail
	if err != nil {
		started := time.Now()
		probe, cancel := context.WithTimeout(context.Background(), checkTimeout)
		current := s.Coordinator.CheckRun(probe, snapshot)
		failedAt := time.Now()
		probeErr := probe.Err()
		cancel()
		if current != nil && current.Error() != "context canceled" {
			err = current
			if errors.Is(current, context.DeadlineExceeded) || errors.Is(probeErr, context.DeadlineExceeded) {
				detail = failureDetail("final_check", current, probeErr, started, failedAt, checkTimeout)
			}
		}
	}
	hbMu.Lock()
	hbErr, hbDetail := heartbeatErr, heartbeatFailure
	hbMu.Unlock()
	if hbErr != nil && Code(err) != "permission_revoked" && !errors.Is(err, run.ErrCancelled) {
		err = hbErr
		detail = hbDetail
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
