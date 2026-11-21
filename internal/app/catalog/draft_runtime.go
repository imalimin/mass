package catalog

import (
	"context"

	"github.com/imalimin/mass/internal/app/identity"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
)

type DraftSkillSelection struct {
	Binding  Binding
	Object   storage.Object
	Revision int64
}

type DraftAgentSelection struct {
	Version  Version
	Skills   []DraftSkillSelection
	Revision int64
}

func (s *Service) LoadDraftAgent(ctx context.Context, actor, space, id string, revision int64, skillRevisions map[string]int64) (DraftAgentSelection, []Bundle, []run.DraftSkillPackage, error) {
	repo, ok := s.Repo.(interface {
		CatalogDraftAgent(context.Context, string, string, string, int64, map[string]int64) (DraftAgentSelection, error)
	})
	if !ok {
		return DraftAgentSelection{}, nil, nil, identity.ErrInvalid
	}
	selection, err := repo.CatalogDraftAgent(ctx, actor, space, id, revision, skillRevisions)
	if err != nil {
		return selection, nil, nil, err
	}
	if selection.Revision == 0 {
		err = s.loadVersion(ctx, actor, space, "agent", &selection.Version)
		return selection, nil, nil, err
	}
	bundles := make([]Bundle, 0, len(selection.Skills))
	packages := make([]run.DraftSkillPackage, 0, len(selection.Skills))
	for _, skill := range selection.Skills {
		bundle, err := s.readPublishedSkill(ctx, skill.Object)
		if err != nil {
			return selection, nil, nil, err
		}
		if bundle.DefinitionID != skill.Binding.SkillID || skill.Revision != 0 && bundle.Revision != skill.Revision {
			return selection, nil, nil, storage.ErrIntegrity
		}
		bundles = append(bundles, bundle)
		if skill.Revision != 0 {
			packages = append(packages, run.DraftSkillPackage{Object: skill.Object, Key: skill.Object.Key})
		}
	}
	return selection, bundles, packages, nil
}
