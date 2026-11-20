// Package scheduler polls durable queued runs and claims them serially.
package scheduler

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/imalimin/mass/internal/domain/run"
)

type Repository interface {
	Check(context.Context) error
	Candidates(context.Context, int) ([]run.Snapshot, error)
	Claim(context.Context, run.Snapshot) (run.View, error)
	RejectQueued(context.Context, run.Snapshot, string) error
}
type Launcher interface {
	Start(context.Context, run.View) bool
}
type Scheduler struct {
	Repo     Repository
	Launcher Launcher
	mu       sync.Mutex
	wake     chan struct{}
	stopped  bool
	fault    error
}

func (s *Scheduler) Notify() {
	s.mu.Lock()
	if s.wake == nil {
		s.wake = make(chan struct{}, 1)
	}
	ch := s.wake
	s.mu.Unlock()
	select {
	case ch <- struct{}{}:
	default:
	}
}
func (s *Scheduler) Ready(ctx context.Context) error {
	s.mu.Lock()
	stop := s.stopped
	fault := s.fault
	s.mu.Unlock()
	if stop || fault != nil {
		return run.ErrUnavailable
	}
	return s.Repo.Check(ctx)
}
func (s *Scheduler) Fault(err error) { s.mu.Lock(); s.fault = err; s.mu.Unlock() }
func (s *Scheduler) Run(ctx context.Context) error {
	s.Notify()
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	defer func() { s.mu.Lock(); s.stopped = true; s.mu.Unlock() }()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		case <-s.wake:
		}
		if err := s.Ready(ctx); err != nil {
			return err
		}
		candidates, err := s.Repo.Candidates(ctx, 100)
		if err != nil {
			return err
		}
		for _, candidate := range candidates {
			view, err := s.Repo.Claim(ctx, candidate)
			if err != nil {
				if errors.Is(err, run.ErrState) || errors.Is(err, run.ErrCancelled) {
					continue
				}
				if err.Error() == "forbidden" || err.Error() == "not_found" {
					if err = s.Repo.RejectQueued(ctx, candidate, "permission_revoked"); err != nil {
						return err
					}
					continue
				}
				return err
			}
			if !s.Launcher.Start(ctx, view) {
				return run.ErrUnavailable
			}
		}
	}
}
