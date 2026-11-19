package evaluation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/imalimin/mass/internal/app/catalog"
	"github.com/imalimin/mass/internal/app/identity"
	sessionapp "github.com/imalimin/mass/internal/app/session"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
)

func businessError(err error) bool {
	for _, e := range []error{identity.ErrForbidden, identity.ErrNotFound, identity.ErrInvalid, identity.ErrCapability, identity.ErrConflict, catalog.ErrConfig, catalog.ErrDraft, catalog.ErrVersion, storage.ErrInvalid, storage.ErrActive, storage.ErrConflict, storage.ErrWorkspace, run.ErrState, run.ErrCancelled} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}
func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func call[T any](s *Service, t *task, fn func(context.Context) (T, error)) (v T, err error) {
	for attempt := 0; attempt < 3; attempt++ {
		ctx, stop := context.WithTimeout(t.ctx, s.callTimeout)
		v, err = fn(ctx)
		stop()
		if err == nil || businessError(err) || errors.Is(err, run.ErrUnavailable) || t.ctx.Err() != nil {
			return
		}
		if attempt < 2 {
			if e := sleep(t.ctx, s.pollInterval); e != nil {
				return v, e
			}
		}
	}
	return
}
func (s *Service) interrupt(t *task, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.interrupted {
		return
	}
	t.interrupted = true
	t.cancel()
	if s.Logger != nil {
		s.Logger.Warn("evaluation interrupted", "experiment_id", t.experiment.ID, "error", err)
	}
}
func (s *Service) record(t *task, key string, p Progress) {
	s.mu.Lock()
	t.progress[key] = p
	s.mu.Unlock()
}
func (s *Service) failed(t *task, key string, p Progress, stage string, err error) {
	if t.ctx.Err() != nil {
		return
	}
	if errors.Is(err, run.ErrUnavailable) || !businessError(err) {
		s.interrupt(t, err)
		return
	}
	p.Status, p.FailureStage, p.FailureCode = "failed", stage, err.Error()
	s.record(t, key, p)
	if errors.Is(err, identity.ErrForbidden) {
		s.mu.Lock()
		for i := range t.dataset.Cases {
			k := fmt.Sprint(i + 1)
			if _, ok := t.progress[k]; !ok {
				t.progress[k] = Progress{Status: "failed", RunIDs: []string{}, FailureStage: "create_session", FailureCode: "permission_revoked"}
			}
		}
		s.mu.Unlock()
	}
	if p.SessionID != "" {
		p.Metrics = s.metrics(t, p.RunIDs, p.SessionID)
		s.record(t, key, p)
	}
}
func (s *Service) runCase(t *task, index int) {
	defer s.wg.Done()
	defer func() { s.mu.Lock(); t.active--; s.spaces[t.experiment.SpaceID]--; s.mu.Unlock(); s.notify() }()
	e := t.experiment
	key := fmt.Sprint(index + 1)
	p := Progress{Status: "active", RunIDs: []string{}}
	session, err := call(s, t, func(ctx context.Context) (run.Session, error) {
		return s.Calls.Create(ctx, e.ActorID, e.SpaceID, sessionapp.CreateInput{Key: fmt.Sprintf("eval.%s.%d.session", e.ID, index+1), AgentID: e.AgentID, VersionID: e.AgentVersionID, Hide: true})
	})
	if err != nil {
		s.failed(t, key, p, "create_session", err)
		return
	}
	p.SessionID = session.ID
	s.record(t, key, p)
	for turnIndex, turn := range t.dataset.Cases[index].Turns {
		v, err := call(s, t, func(ctx context.Context) (run.View, error) {
			return s.Calls.Submit(ctx, e.ActorID, e.SpaceID, p.SessionID, sessionapp.SubmitInput{Key: fmt.Sprintf("eval.%s.%d.%d", e.ID, index+1, turnIndex+1), Text: turn.Content[0].Text})
		})
		if err != nil {
			s.failed(t, key, p, "submit_run", err)
			return
		}
		p.RunIDs = append(p.RunIDs, v.ID)
		s.record(t, key, p)
		for !run.Terminal(v.Status) {
			now := time.Now()
			expired := now.Sub(v.CreatedAt) > s.queueTimeout
			if v.StartedAt != nil {
				expired = now.Sub(*v.StartedAt) > time.Duration(v.Definition.TimeoutSeconds)*time.Second+s.SnapshotTimeout+s.grace
			}
			if expired {
				s.interrupt(t, errors.New("evaluation run wait deadline exceeded"))
				return
			}
			if err = sleep(t.ctx, s.pollInterval); err != nil {
				return
			}
			v, err = call(s, t, func(ctx context.Context) (run.View, error) {
				return s.Calls.Run(ctx, e.ActorID, e.SpaceID, p.RunIDs[len(p.RunIDs)-1])
			})
			if err != nil {
				s.failed(t, key, p, "run", err)
				return
			}
		}
		if v.Status != "succeeded" {
			p.Status, p.FailureStage, p.FailureCode = "failed", "run", v.Error
			p.Metrics = s.metrics(t, p.RunIDs, p.SessionID)
			s.record(t, key, p)
			return
		}
	}
	p.Status = "succeeded"
	p.Metrics = s.metrics(t, p.RunIDs, p.SessionID)
	s.record(t, key, p)
}
