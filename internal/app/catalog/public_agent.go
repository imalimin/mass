package catalog

import (
	"context"
	"errors"

	"github.com/imalimin/mass/internal/app/identity"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/resource"
)

var ErrAgentNotPublic = errors.New("agent_not_public")

type publicAgentRepository interface {
	SetAgentVisibility(context.Context, string, string, string, Visibility) (Definition, error)
	PublicAgentVersion(context.Context, string, string, string) (Definition, Version, storage.Object, error)
	WithPublicAgent(context.Context, string, string, string, func() error) error
	PublishedSkillObject(context.Context, string, string, string) (storage.Object, error)
}

type PublicAgent struct {
	Name    string
	Version Version
	Skills  []Bundle
}

func (s *Service) SetAgentVisibility(ctx context.Context, actor, space, id string, visibility Visibility) (Definition, error) {
	if visibility != VisibilitySpace && visibility != VisibilityPublic {
		return Definition{}, identity.ErrInvalid
	}
	repo, ok := s.Repo.(publicAgentRepository)
	if !ok {
		return Definition{}, identity.ErrNotFound
	}
	return repo.SetAgentVisibility(ctx, actor, space, id, visibility)
}

func (s *Service) PublicAgentAvailable(ctx context.Context, space, agent string) (bool, error) {
	repo, ok := s.Repo.(publicAgentRepository)
	if !ok {
		return false, nil
	}
	_, _, _, err := repo.PublicAgentVersion(ctx, space, agent, "")
	if errors.Is(err, ErrAgentNotPublic) {
		return false, nil
	}
	return err == nil, err
}

// AgentVersionLabel resolves the display label and debug flag for a fixed Agent
// version on a local or cross-space session. Missing or inaccessible versions
// return empty metadata so session reads stay resilient.
func (s *Service) AgentVersionLabel(ctx context.Context, actor, space, sourceSpace, agentID, versionID string) (string, bool) {
	if s == nil || s.Repo == nil || agentID == "" || versionID == "" {
		return "", false
	}
	if sourceSpace != "" && sourceSpace != space {
		repo, ok := s.Repo.(publicAgentRepository)
		if !ok {
			return "", false
		}
		_, ver, _, err := repo.PublicAgentVersion(ctx, sourceSpace, agentID, versionID)
		if err != nil {
			return "", false
		}
		return ver.Label, ver.Debug
	}
	ver, err := s.Repo.CatalogVersion(ctx, actor, space, "agent", agentID, versionID)
	if err != nil {
		return "", false
	}
	return ver.Label, ver.Debug
}

func (s *Service) LoadPublicAgent(ctx context.Context, space, agent string) (PublicAgent, error) {
	repo, ok := s.Repo.(publicAgentRepository)
	if !ok {
		return PublicAgent{}, ErrAgentNotPublic
	}
	definition, version, object, err := repo.PublicAgentVersion(ctx, space, agent, "")
	if err != nil {
		return PublicAgent{}, err
	}
	file, err := s.Content.ReadVerified(ctx, object)
	if err != nil {
		return PublicAgent{}, err
	}
	defer file.Close()
	if err := decodeAgentVersion(file, &version); err != nil {
		return PublicAgent{}, err
	}
	if version.Bundle.DefinitionID != agent || version.Bundle.Kind != "agent" || version.Bundle.Model == nil || version.Bundle.Config == nil || version.Bundle.Model.ID != version.Bundle.Config.Model {
		return PublicAgent{}, storage.ErrIntegrity
	}
	result := PublicAgent{Name: definition.Name, Version: version, Skills: make([]Bundle, 0, len(version.Bundle.Bindings))}
	for _, binding := range version.Bundle.Bindings {
		object, err := repo.PublishedSkillObject(ctx, space, binding.SkillID, binding.VersionID)
		if err != nil {
			return PublicAgent{}, err
		}
		if object.SHA256 != binding.SHA256 {
			return PublicAgent{}, storage.ErrIntegrity
		}
		bundle, err := s.readPublishedSkill(ctx, object)
		if err != nil {
			return PublicAgent{}, err
		}
		if bundle.DefinitionID != binding.SkillID {
			return PublicAgent{}, storage.ErrIntegrity
		}
		result.Skills = append(result.Skills, bundle)
	}
	return result, nil
}

func (s *Service) readPublishedSkill(ctx context.Context, object storage.Object) (Bundle, error) {
	file, err := s.Content.ReadVerified(ctx, object)
	if err != nil {
		return Bundle{}, err
	}
	defer file.Close()
	return decodeSkillBundle(object, file, object.SHA256)
}

func (s *Service) WithPublicAgent(ctx context.Context, space, agent, version string, fn func() error) error {
	repo, ok := s.Repo.(publicAgentRepository)
	if !ok {
		return ErrAgentNotPublic
	}
	return repo.WithPublicAgent(ctx, space, agent, version, fn)
}

func (s *Service) OpenPublicAgentSkillPackage(ctx context.Context, space string, skill run.Skill) (storage.Object, *resource.File, error) {
	repo, ok := s.Repo.(publicAgentRepository)
	if !ok {
		return storage.Object{}, nil, storage.ErrIntegrity
	}
	object, err := repo.PublishedSkillObject(ctx, space, skill.ID, skill.VersionID)
	if err != nil {
		return object, nil, err
	}
	if object.SHA256 != skill.SHA256 {
		return object, nil, storage.ErrIntegrity
	}
	file, err := s.Content.ReadVerified(ctx, object)
	if err != nil {
		return object, nil, err
	}
	return object, file, nil
}
