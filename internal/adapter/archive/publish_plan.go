package archive

import (
	"context"
	"maps"

	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/resource/sessionlog"
)

func (s *conversation) rememberPublishPlan(plan run.CreativityPublishPlan) {
	if s.publishPlans == nil {
		s.publishPlans = make(map[string]run.CreativityPublishPlan)
	}
	s.publishPlans[plan.SnapshotID] = plan
	if len(plan.DebugDrafts) > 0 {
		// Keep identities for Skills temporarily removed from later workspace snapshots.
		history := plan
		history.DebugDrafts = maps.Clone(plan.DebugDrafts)
		history.Skills = make([]run.PublishTarget, 0, len(plan.Skills))
		for _, target := range plan.Skills {
			if plan.DebugDrafts[target.ID] != 0 {
				history.Skills = append(history.Skills, target)
			}
		}
		for _, previous := range s.lastDebugPlan.Skills {
			if revision := s.lastDebugPlan.DebugDrafts[previous.ID]; revision != 0 && history.DebugDrafts[previous.ID] == 0 {
				history.DebugDrafts[previous.ID] = revision
				history.Skills = append(history.Skills, previous)
			}
		}
		s.lastDebugPlan = history
	}
}

func (r *Runtime) LastCreativityDebugPlan(ctx context.Context, actor, space, session string) (run.CreativityPublishPlan, error) {
	s, err := r.memberSession(ctx, actor, space, session)
	if err != nil {
		return run.CreativityPublishPlan{}, err
	}
	defer s.Unlock()
	return s.lastDebugPlan, nil
}

func (r *Runtime) CreativityPublishPlan(ctx context.Context, actor, space, session, snapshot string) (run.CreativityPublishPlan, error) {
	s, err := r.memberSession(ctx, actor, space, session)
	if err != nil {
		return run.CreativityPublishPlan{}, err
	}
	defer s.Unlock()
	return s.publishPlans[snapshot], nil
}

func (r *Runtime) SaveCreativityPublishPlan(ctx context.Context, actor, space, session string, plan run.CreativityPublishPlan) error {
	s, err := r.memberSession(ctx, actor, space, session)
	if err != nil {
		return err
	}
	defer s.Unlock()
	if !s.maintenance || s.active != "" || s.session.LatestSnapshotID != plan.SnapshotID {
		return storage.ErrConflict
	}
	if prior, exists := s.publishPlans[plan.SnapshotID]; exists && (len(prior.DebugDrafts) > 0 || len(plan.DebugDrafts) == 0) {
		return r.uploadLocal(ctx, s)
	}
	entry := sessionlog.Planned{Header: sessionlog.Header{Seq: int64(len(s.records)) + 1, Type: sessionlog.PublishPlanned, Role: "system"}, Plan: plan}
	err = r.commit(ctx, s, "", entry)
	if int64(len(s.records)) == entry.Seq {
		s.rememberPublishPlan(plan)
	}
	return err
}
