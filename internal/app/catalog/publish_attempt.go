package catalog

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/app/identity"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
)

type PublishItemError struct {
	Kind, Name string
	Err        error
}

func (e *PublishItemError) Error() string { return e.Kind + " " + e.Name + ": " + e.Err.Error() }
func (e *PublishItemError) Unwrap() error { return e.Err }

func (s *Service) NewPublishPlan(ctx context.Context, actor, space, snapshot string, session run.Creativity, project PublishProject, result *PublishManifest, previous run.CreativityPublishPlan) (run.CreativityPublishPlan, error) {
	plan := run.CreativityPublishPlan{SnapshotID: snapshot,
		Agent:  run.PublishTarget{ID: session.TargetAgentID, Version: session.TargetVersionID, Revision: session.TargetDraftRevision},
		Skills: make([]run.PublishTarget, 0, len(project.skills))}
	if revision := previous.DebugDrafts[previous.Agent.ID]; revision != 0 && revision >= plan.Agent.Revision {
		plan.Agent = run.PublishTarget{ID: previous.Agent.ID, Version: previous.Agent.Version, Revision: revision}
	}
	plan.Agent.NextVersionID = uuid.NewString()
	if plan.Agent.ID == "" {
		plan.Agent.ID = newDefinition("agent", project.agent.Name).ID
	} else {
		repo, ok := s.Repo.(projectCatalog)
		if !ok {
			return plan, storage.ErrIntegrity
		}
		if _, err := s.inspectProjectTarget(ctx, repo, actor, space, "agent", project.agent.Name, plan.Agent, nil, false); err != nil {
			result.Agent.Status = "failed"
			return plan, &PublishItemError{"Agent", project.agent.Name, err}
		}
	}
	for index, skill := range project.skills {
		target := run.PublishTarget{ID: skill.entry.ID, Version: skill.base.ID, Revision: skill.baseRevision, Path: skill.path}
		if skill.entry.Action != publishReference {
			target.NextVersionID = uuid.NewString()
		}
		switch skill.entry.Action {
		case publishNew:
			for _, old := range previous.Skills {
				if old.Path == skill.path && previous.DebugDrafts[old.ID] != 0 {
					target.ID, target.Revision = old.ID, previous.DebugDrafts[old.ID]
					break
				}
			}
			if target.ID == "" {
				target.ID = newDefinition("skill", skill.entry.Name).ID
			}
		case publishUpdate:
			for _, old := range previous.Skills {
				if revision := previous.DebugDrafts[old.ID]; old.ID == target.ID && old.Version == target.Version && revision > target.Revision {
					target.Revision = revision
					break
				}
			}
			if target.Revision != 0 {
				break
			}
			draft, err := s.Draft(ctx, actor, space, "skill", target.ID)
			if err != nil {
				result.Skills[index].Status = "failed"
				return plan, &PublishItemError{"Skill", skill.entry.Name, err}
			}
			target.Revision = draft.Revision
			if !sameSkill(draft.Document, skill.base.Bundle.Document) {
				repo, ok := s.Repo.(projectCatalog)
				if !ok {
					return plan, storage.ErrIntegrity
				}
				latest, _, err := repo.LatestFormalVersion(ctx, actor, space, "skill", target.ID)
				if err == nil && latest.ID != "" {
					err = s.loadVersion(ctx, actor, space, "skill", &latest)
				}
				if err != nil {
					result.Skills[index].Status = "failed"
					return plan, &PublishItemError{"Skill", skill.entry.Name, err}
				}
				if latest.ID == "" || !sameSkill(latest.Bundle.Document, skill.document) {
					result.Skills[index].Status = "failed"
					return plan, &PublishItemError{"Skill", skill.entry.Name, ErrDraft}
				}
				target.NextVersionID = latest.ID
			}
		}
		plan.Skills = append(plan.Skills, target)
	}
	return plan, nil
}

type projectTargetState struct {
	draft Draft
	done  Version
	saved bool
}

func (s *Service) inspectProjectTarget(ctx context.Context, repo projectCatalog, actor, space, kind, name string, target run.PublishTarget, matches func(Document) bool, reusePublished bool) (projectTargetState, error) {
	var state projectTargetState
	draft, err := s.Draft(ctx, actor, space, kind, target.ID)
	if errors.Is(err, identity.ErrNotFound) && target.Revision == 0 {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if draft.Archived || draft.Name != name {
		return state, ErrDraft
	}
	state.draft = draft
	latest, published, err := repo.LatestFormalVersion(ctx, actor, space, kind, target.ID)
	if err != nil {
		return state, err
	}
	if reusePublished && latest.ID != "" && matches != nil &&
		(target.NextVersionID == "" || latest.ID == target.Version || latest.ID == target.NextVersionID) {
		if err := s.loadVersion(ctx, actor, space, kind, &latest); err != nil {
			return state, err
		}
		if matches(latest.Bundle.Document) {
			state.done = latest
			return state, nil
		}
	}
	if latest.ID != target.Version || latest.ID == "" && published {
		return state, ErrDraft
	}
	if draft.Revision == target.Revision {
		return state, nil
	}
	if matches != nil && draft.Revision == target.Revision+1 && matches(draft.Document) {
		state.saved = true
		return state, nil
	}
	return state, ErrDraft
}

func (s *Service) PublishProject(ctx context.Context, actor, space string, project PublishProject, plan run.CreativityPublishPlan) (PublishManifest, error) {
	result := project.PendingResult()
	repo, ok := s.Repo.(projectCatalog)
	if !ok || len(plan.Skills) != len(project.skills) {
		return result, storage.ErrIntegrity
	}
	states := make([]projectTargetState, len(project.skills))
	allSkillsResolved := true
	for index, skill := range project.skills {
		target := plan.Skills[index]
		if skill.entry.Action == publishReference {
			continue
		}
		state, err := s.inspectProjectTarget(ctx, repo, actor, space, "skill", skill.entry.Name, target, func(doc Document) bool {
			return sameSkill(doc, skill.document)
		}, true)
		if err != nil {
			result.Skills[index].Status = "failed"
			return result, &PublishItemError{"Skill", skill.entry.Name, err}
		}
		states[index] = state
		if state.done.ID == "" {
			allSkillsResolved = false
			if state.saved {
				result.Skills[index].Status = "draft_saved"
			}
		} else {
			result.Skills[index] = completedEntry(skill.entry, target.ID, state.done)
		}
	}
	var agentMatches func(Document) bool
	if allSkillsResolved {
		desired := project.agentDocument(result.Skills)
		agentMatches = func(document Document) bool { return sameAgent(document, desired) }
	}
	agentTarget := plan.Agent
	if revision := plan.DebugDrafts[agentTarget.ID]; revision != 0 {
		agentTarget.Revision = revision
	}
	agentState, err := s.inspectProjectTarget(ctx, repo, actor, space, "agent", project.agent.Name, agentTarget, agentMatches, true)
	if err != nil {
		result.Agent.Status = "failed"
		return result, &PublishItemError{"Agent", project.agent.Name, err}
	}
	for index, skill := range project.skills {
		if skill.entry.Action == publishReference || states[index].done.ID != "" {
			continue
		}
		target := plan.Skills[index]
		err := s.publishProjectTarget(ctx, repo, actor, space, "skill", target, states[index], skill.document, skill.files, &result.Skills[index])
		if err != nil {
			return result, &PublishItemError{"Skill", skill.entry.Name, err}
		}
	}
	if agentState.done.ID == "" {
		err = s.publishProjectTarget(ctx, repo, actor, space, "agent", agentTarget, agentState, project.agentDocument(result.Skills), nil, &result.Agent)
		if err != nil {
			return result, &PublishItemError{"Agent", project.agent.Name, err}
		}
	} else {
		result.Agent = completedEntry(result.Agent, plan.Agent.ID, agentState.done)
	}
	result.Publication = project.publication(plan, agentTarget, result)
	return result, nil
}

func (p PublishProject) publication(plan run.CreativityPublishPlan, agentTarget run.PublishTarget, result PublishManifest) run.CreativityPublication {
	agent := result.Agent
	if !agent.committed && agent.VersionID != agentTarget.NextVersionID && agent.VersionID != agentTarget.Version {
		// Older plans have no reserved ID. An identical version alone does not
		// establish which session published it.
		return run.CreativityPublication{}
	}
	revision := agent.revision
	if agent.VersionID == agentTarget.Version {
		revision = agentTarget.Revision
	}
	publication := run.CreativityPublication{SnapshotID: plan.SnapshotID,
		Agent: run.PublishTarget{ID: agent.ID, Version: agent.VersionID, Revision: revision}}
	for i, skill := range p.skills {
		entry := result.Skills[i]
		if skill.path == "" {
			continue
		}
		revision := entry.revision
		if entry.VersionID == skill.base.ID {
			revision = skill.baseRevision
		} else if !entry.committed && entry.VersionID != plan.Skills[i].NextVersionID {
			continue
		}
		if revision == 0 {
			continue
		}
		publication.Skills = append(publication.Skills, run.PublishedSkill{PublishTarget: run.PublishTarget{
			ID: entry.ID, Version: entry.VersionID, Revision: revision, Path: skill.path,
		}, SourceVersion: skill.sourceVersion})
	}
	return publication
}

func completedEntry(entry PublishEntry, id string, version Version) PublishEntry {
	entry.ID, entry.VersionID, entry.Label = id, version.ID, version.Label
	entry.Status = "published"
	entry.revision = version.Bundle.Revision
	return entry
}

func (s *Service) saveProjectTarget(ctx context.Context, repo projectCatalog, actor, space, kind string, target run.PublishTarget, state projectTargetState, document Document, files map[string][]byte, entry *PublishEntry) (int64, error) {
	revision := state.draft.Revision
	if !state.saved {
		draft := state.draft
		create := draft.ID == ""
		if create {
			draft.Definition = Definition{ID: target.ID, Kind: kind, Name: entry.Name}
			if kind == "agent" {
				draft.Visibility = VisibilitySpace
			}
			draft.Revision = 1
		}
		draft.Document = document
		revision = target.Revision + 1
		if kind == "skill" {
			var err error
			draft, err = s.writeSkillPackage(ctx, actor, space, draft, revision, uuid.NewString(), func(asset Asset) ([]byte, error) {
				return files[asset.Path], nil
			})
			if err != nil {
				entry.Status = "failed"
				return 0, err
			}
		}
		var err error
		if create {
			err = s.Repo.CatalogCreate(ctx, actor, space, draft)
		} else {
			err = repo.CatalogSaveBasedOn(ctx, actor, space, draft, target.Version)
		}
		if err != nil {
			entry.Status = "failed"
			var write *IndexWriteError
			if errors.As(err, &write) {
				entry.Status = "unknown"
				if write.Committed {
					entry.ID, entry.Status = target.ID, "draft_saved"
				}
			}
			return 0, err
		}
	}
	entry.ID, entry.Status = target.ID, "draft_saved"
	return revision, nil
}

func (s *Service) publishProjectTarget(ctx context.Context, repo projectCatalog, actor, space, kind string, target run.PublishTarget, state projectTargetState, document Document, files map[string][]byte, entry *PublishEntry) error {
	if state.draft.ID != "" && !state.saved {
		if kind == "agent" {
			state.saved = sameAgent(state.draft.Document, document)
		} else {
			state.saved = sameSkill(state.draft.Document, document)
		}
	}
	revision, err := s.saveProjectTarget(ctx, repo, actor, space, kind, target, state, document, files, entry)
	if err != nil {
		return err
	}
	version, err := s.publish(ctx, actor, space, kind, target.ID, "", revision, false, &target.Version, target.NextVersionID)
	var write *IndexWriteError
	switch {
	case err == nil || errors.As(err, &write) && write.Committed:
		*entry = completedEntry(*entry, target.ID, version)
		entry.committed = true
	case write != nil:
		entry.Status = "unknown"
	}
	return err
}
