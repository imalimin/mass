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
	baseURL, err := NormalizeBaseURL(c.BaseURL)
	if err != nil {
		return nil, "", 0, err
	}
	value, err := s.Cipher.Open(space, c.SecretBindingID, c.KeyID, c.Ciphertext)
	return value, baseURL, c.Revision, err
}
