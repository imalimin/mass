package identity

import (
	"context"

	"github.com/imalimin/mass/internal/domain/run"
)

func (s *Service) ResolveRunSecret(ctx context.Context, actor, space string, m run.ModelConfig) ([]byte, string, int64, error) {
	c, err := s.Repo.ResolveModelSecret(ctx, actor, space, m.ID)
	if err != nil {
		return nil, "", 0, err
	}
	return s.openRunSecret(space, c)
}

func (s *Service) openRunSecret(space string, c Model) ([]byte, string, int64, error) {
	baseURL, err := NormalizeBaseURL(c.BaseURL)
	if err != nil {
		return nil, "", 0, err
	}
	value, err := s.Cipher.Open(space, c.SecretBindingID, c.KeyID, c.Ciphertext)
	return value, baseURL, c.Revision, err
}

func (s *Service) sharedRunModel(ctx context.Context, actor, space, source, id string) (Model, error) {
	repo, ok := s.Repo.(interface {
		SharedRunModel(context.Context, string, string, string, string) (Model, error)
	})
	if !ok {
		return Model{}, ErrNotFound
	}
	return repo.SharedRunModel(ctx, actor, space, source, id)
}

func (s *Service) CheckSharedRunModel(ctx context.Context, actor, space, source, id string) (Model, error) {
	model, err := s.sharedRunModel(ctx, actor, space, source, id)
	if err != nil {
		return Model{}, err
	}
	key, _, _, err := s.openRunSecret(source, model)
	clear(key)
	model.Ciphertext, model.KeyID, model.SecretBindingID, model.BaseURL = nil, "", "", ""
	return model, err
}

type SharedRunCredentials struct {
	Identity *Service
	Run      run.Snapshot
}

func (c SharedRunCredentials) ResolveRunSecret(ctx context.Context, actor, space string, m run.ModelConfig) ([]byte, string, int64, error) {
	if c.Run.AgentSourceSpaceID == "" || actor != c.Run.ActorID || space != c.Run.SpaceID || m.ID != c.Run.Definition.Model.ID {
		return nil, "", 0, ErrForbidden
	}
	model, err := c.Identity.sharedRunModel(ctx, actor, space, c.Run.AgentSourceSpaceID, m.ID)
	if err != nil {
		return nil, "", 0, err
	}
	return c.Identity.openRunSecret(c.Run.AgentSourceSpaceID, model)
}
