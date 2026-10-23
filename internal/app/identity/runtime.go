package identity

import (
	"context"

	"github.com/imalimin/mass/internal/domain/run"
)

type runtimeCredentials interface {
	ResolveRuntimeCredential(context.Context, string, string, string, string) (Credential, error)
}

func (s *Service) ResolveRunSecret(ctx context.Context, actor, space string, m run.ModelConfig) ([]byte, int64, error) {
	repo, ok := s.Repo.(runtimeCredentials)
	if !ok {
		return nil, 0, ErrCapability
	}
	c, err := repo.ResolveRuntimeCredential(ctx, actor, space, m.ID, m.CredentialID)
	if err != nil {
		return nil, 0, err
	}
	value, err := s.Cipher.Open(space, c.ID, c.KeyID, c.Ciphertext)
	return value, c.Revision, err
}
