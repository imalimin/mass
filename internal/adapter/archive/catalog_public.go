package archive

import (
	"context"

	"github.com/imalimin/mass/internal/app/catalog"
	"github.com/imalimin/mass/internal/domain/storage"
)

func (c *Catalog) SetAgentVisibility(ctx context.Context, actor, space, id string, visibility catalog.Visibility) (catalog.Definition, error) {
	defer c.lock(space)()
	index, err := c.authorized(ctx, actor, space)
	if err != nil {
		return catalog.Definition{}, err
	}
	agent, err := findDefinition(index, "agent", id)
	if err != nil {
		return catalog.Definition{}, err
	}
	agent.Visibility = visibility
	return agent.Definition, c.save(ctx, actor, space, "definition.visibility", id, index)
}

func (c *Catalog) PublicAgentVersion(ctx context.Context, space, id, versionID string) (catalog.Definition, catalog.Version, storage.Object, error) {
	index, err := c.load(ctx, space)
	if err != nil {
		return catalog.Definition{}, catalog.Version{}, storage.Object{}, err
	}
	agent, err := findDefinition(index, "agent", id)
	if err != nil || agent.Archived || agent.Visibility != catalog.VisibilityPublic {
		return catalog.Definition{}, catalog.Version{}, storage.Object{}, catalog.ErrAgentNotPublic
	}
	if versionID == "" {
		latest, _ := latestFormal(agent)
		versionID = latest.ID
	}
	selected, err := findVersion(agent, versionID)
	if err != nil || selected.Debug || selected.Archived {
		return catalog.Definition{}, catalog.Version{}, storage.Object{}, catalog.ErrAgentNotPublic
	}
	return agent.Definition, selected.metadata(id), selected.Object.Metadata(), nil
}

func (c *Catalog) WithPublicAgent(ctx context.Context, space, id, versionID string, fn func() error) error {
	defer c.lock(space)()
	if _, _, _, err := c.PublicAgentVersion(ctx, space, id, versionID); err != nil {
		return err
	}
	return fn()
}

func (c *Catalog) PublishedSkillObject(ctx context.Context, space, id, versionID string) (storage.Object, error) {
	index, err := c.load(ctx, space)
	if err != nil {
		return storage.Object{}, err
	}
	skill, err := findDefinition(index, "skill", id)
	if err != nil {
		return storage.Object{}, err
	}
	selected, err := findVersion(skill, versionID)
	if err != nil {
		return storage.Object{}, err
	}
	if selected.Debug {
		return storage.Object{}, storage.ErrIntegrity
	}
	return selected.Object.Metadata(), nil
}
