package archive

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/imalimin/mass/internal/app/catalog"
	"github.com/imalimin/mass/internal/app/identity"
)

func (c *Catalog) CatalogDraftAgent(ctx context.Context, actor, space, id string, revision int64, skillRevisions map[string]int64) (catalog.DraftAgentSelection, error) {
	var result catalog.DraftAgentSelection
	index, err := c.authorized(ctx, actor, space)
	if err != nil {
		return result, err
	}
	agent, err := findDefinition(index, "agent", id)
	if err != nil {
		return result, err
	}
	if agent.Archived || revision != 0 && revision != agent.Revision {
		return result, catalog.ErrDraft
	}
	if agent.Draft.ID == "" {
		version, _ := latestFormal(agent)
		if revision != 0 || version.ID == "" {
			return result, catalog.ErrDraft
		}
		result.Version = version
		return result, nil
	}
	draft, err := c.draft(ctx, agent)
	if err != nil {
		return result, err
	}
	config, err := catalog.ValidateDocument("agent", draft.Name, draft.Document, false)
	if err != nil || config == nil || strings.TrimSpace(draft.Document.Markdown) == "" {
		return result, catalog.ErrConfig
	}
	model, err := c.Model(ctx, actor, space, config.Model)
	if err != nil {
		return result, err
	}
	if !config.SupportsModel(model) {
		return result, identity.ErrCapability
	}
	bundle := catalog.Bundle{FormatVersion: 1, Kind: "agent", DefinitionID: id, Name: draft.Name, Revision: draft.Revision, Document: draft.Document,
		Config: config, Model: &model, Bindings: []catalog.Binding{}}
	for _, ref := range config.Skills {
		skill, err := findDefinition(index, "skill", ref.ID)
		if err != nil {
			return result, err
		}
		if skill.Archived {
			return result, catalog.ErrConfig
		}
		binding := catalog.Binding{SkillID: skill.ID}
		selected := catalog.DraftSkillSelection{}
		if ref.Version == catalog.Latest && skill.Draft.ID != "" {
			if expected := skillRevisions[skill.ID]; expected != 0 && expected != skill.Revision {
				return result, catalog.ErrDraft
			}
			selected.Revision, selected.Object = skill.Revision, skill.Draft.Metadata()
			binding.VersionID, binding.Version = selected.Object.ID, fmt.Sprintf("draft-r%d", skill.Revision)
		} else {
			var versionID string
			if ref.Version == catalog.Latest {
				if skillRevisions[skill.ID] != 0 {
					return result, catalog.ErrDraft
				}
				latest, _ := latestFormal(skill)
				versionID = latest.ID
			}
			position := slices.IndexFunc(skill.Versions, func(v *version) bool {
				return !v.Debug && !v.Archived && (v.ID == versionID || v.Label == ref.Version)
			})
			if position == -1 {
				return result, catalog.ErrConfig
			}
			version := skill.Versions[position]
			selected.Object = version.Object.Metadata()
			binding.VersionID, binding.Version = version.ID, version.Label
		}
		binding.SHA256 = selected.Object.SHA256
		selected.Binding = binding
		bundle.Bindings = append(bundle.Bindings, binding)
		result.Skills = append(result.Skills, selected)
	}
	result.Revision = draft.Revision
	result.Version = catalog.Version{ID: agent.Draft.ID, DefinitionID: id, Debug: true, Bundle: bundle}
	return result, nil
}
