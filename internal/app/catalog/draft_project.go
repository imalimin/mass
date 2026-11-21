package catalog

import (
	"context"

	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
)

func (s *Service) SaveProjectDrafts(ctx context.Context, actor, space string, project PublishProject, plan run.CreativityPublishPlan) (PublishManifest, map[string]int64, error) {
	result := project.PendingResult()
	repo, ok := s.Repo.(projectCatalog)
	if !ok || len(plan.Skills) != len(project.skills) {
		return result, nil, storage.ErrIntegrity
	}
	states := make([]projectTargetState, len(project.skills))
	for index, skill := range project.skills {
		if skill.entry.Action == publishReference {
			continue
		}
		target := plan.Skills[index]
		matches := func(document Document) bool { return sameSkill(document, skill.document) }
		state, err := s.inspectProjectTarget(ctx, repo, actor, space, "skill", skill.entry.Name, target, matches, true)
		if err != nil {
			result.Skills[index].Status = "failed"
			return result, nil, &PublishItemError{"Skill", skill.entry.Name, err}
		}
		if state.done.ID != "" {
			result.Skills[index] = completedEntry(skill.entry, target.ID, state.done)
			continue
		}
		states[index] = state
		result.Skills[index].ID = target.ID
		result.Skills[index].Label = Latest
	}
	document := project.agentDocument(result.Skills)
	matchesAgent := func(saved Document) bool { return sameAgent(saved, document) }
	agent, err := s.inspectProjectTarget(ctx, repo, actor, space, "agent", project.agent.Name, plan.Agent, matchesAgent, false)
	if err != nil {
		result.Agent.Status = "failed"
		return result, nil, &PublishItemError{"Agent", project.agent.Name, err}
	}
	revisions := make(map[string]int64)
	for index, skill := range project.skills {
		if result.Skills[index].Label != Latest {
			continue
		}
		target := plan.Skills[index]
		state := states[index]
		state.saved = state.saved || state.draft.ID != "" && sameSkill(state.draft.Document, skill.document)
		revision, err := s.saveProjectTarget(ctx, repo, actor, space, "skill", target, state, skill.document, skill.files, &result.Skills[index])
		if err != nil {
			return result, nil, &PublishItemError{"Skill", skill.entry.Name, err}
		}
		revisions[target.ID] = revision
	}
	agent.saved = agent.saved || agent.draft.ID != "" && matchesAgent(agent.draft.Document)
	revision, err := s.saveProjectTarget(ctx, repo, actor, space, "agent", plan.Agent, agent, document, nil, &result.Agent)
	if err != nil {
		return result, nil, &PublishItemError{"Agent", project.agent.Name, err}
	}
	revisions[plan.Agent.ID] = revision
	return result, revisions, nil
}
