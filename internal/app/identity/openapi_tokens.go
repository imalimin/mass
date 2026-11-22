package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	DefaultOpenAPITokenTTL = 180 * 24 * time.Hour
	OpenAPITokenActive     = "active"
	OpenAPITokenExpired    = "expired"
	OpenAPITokenRevoked    = "revoked"
)

func OpenAPITokenStatus(expiresAt time.Time, revokedAt *time.Time, now time.Time) string {
	if revokedAt != nil {
		return OpenAPITokenRevoked
	}
	if !expiresAt.After(now) {
		return OpenAPITokenExpired
	}
	return OpenAPITokenActive
}

func openapiTokenBinding(id string) string {
	return "openapi-token:" + id
}

func bearerTokenHash(token string) ([]byte, error) {
	if len(token) != 43 || strings.ContainsAny(token, " \t\r\n") {
		return nil, ErrUnauthenticated
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != token {
		return nil, ErrUnauthenticated
	}
	sum := sha256.Sum256([]byte(token))
	return sum[:], nil
}

func (s *Service) ListOpenAPITokens(ctx context.Context, actor, space string, q Query) ([]OpenAPIToken, error) {
	items, err := s.Repo.OpenAPITokens(ctx, actor, space, q)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	for i := range items {
		if items[i].Status == "" {
			items[i].Status = OpenAPITokenStatus(items[i].ExpiresAt, items[i].RevokedAt, now)
		}
		items[i].TokenHash, items[i].KeyID, items[i].Ciphertext = nil, "", nil
	}
	return items, nil
}

func (s *Service) CreateOpenAPIToken(ctx context.Context, actor, space, name string, expiresAt *time.Time) (CreatedOpenAPIToken, error) {
	if !validName(name) {
		return CreatedOpenAPIToken{}, ErrInvalid
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	exp := now.Add(DefaultOpenAPITokenTTL)
	if expiresAt != nil {
		exp = expiresAt.UTC().Truncate(time.Microsecond)
		if !exp.After(now) || exp.Year() > 9999 {
			return CreatedOpenAPIToken{}, ErrInvalid
		}
	}
	var randomBytes [32]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		return CreatedOpenAPIToken{}, err
	}
	rawToken := base64.RawURLEncoding.EncodeToString(randomBytes[:])
	sum := sha256.Sum256([]byte(rawToken))
	id := uuid.NewString()
	keyID, ciphertext, err := s.Cipher.Seal(space, openapiTokenBinding(id), []byte(rawToken))
	if err != nil {
		return CreatedOpenAPIToken{}, err
	}
	tok := OpenAPIToken{
		ID:         id,
		SpaceID:    space,
		IssuerID:   actor,
		Name:       name,
		Status:     OpenAPITokenActive,
		CreatedAt:  now,
		ExpiresAt:  exp,
		RevokedAt:  nil,
		TokenHash:  sum[:],
		KeyID:      keyID,
		Ciphertext: ciphertext,
	}
	if err := s.Repo.CreateOpenAPIToken(ctx, actor, space, tok); err != nil {
		return CreatedOpenAPIToken{}, err
	}
	tok.TokenHash, tok.KeyID, tok.Ciphertext = nil, "", nil
	return CreatedOpenAPIToken{OpenAPIToken: tok, Token: rawToken}, nil
}

func (s *Service) CopyOpenAPIToken(ctx context.Context, actor, space, tokenID string) (OpenAPITokenSecret, error) {
	if !validID(tokenID) {
		return OpenAPITokenSecret{}, ErrNotFound
	}
	tok, err := s.Repo.OpenAPITokenSecret(ctx, actor, space, tokenID)
	if err != nil {
		return OpenAPITokenSecret{}, err
	}
	raw, err := s.Cipher.Open(space, openapiTokenBinding(tok.ID), tok.KeyID, tok.Ciphertext)
	if err != nil {
		return OpenAPITokenSecret{}, err
	}
	return OpenAPITokenSecret{ID: tok.ID, Token: string(raw)}, nil
}

func (s *Service) RenewOpenAPIToken(ctx context.Context, actor, space, tokenID string, expiresAt time.Time) (OpenAPIToken, error) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	if expiresAt.IsZero() {
		return OpenAPIToken{}, ErrInvalid
	}
	exp := expiresAt.UTC().Truncate(time.Microsecond)
	if !exp.After(now) || exp.Year() > 9999 {
		return OpenAPIToken{}, ErrInvalid
	}
	if !validID(tokenID) {
		return OpenAPIToken{}, ErrNotFound
	}
	tok, err := s.Repo.RenewOpenAPIToken(ctx, actor, space, tokenID, exp)
	if err != nil {
		return OpenAPIToken{}, err
	}
	tok.Status = OpenAPITokenStatus(tok.ExpiresAt, tok.RevokedAt, now)
	tok.TokenHash, tok.KeyID, tok.Ciphertext = nil, "", nil
	return tok, nil
}

func (s *Service) RevokeOpenAPIToken(ctx context.Context, actor, space, tokenID string) error {
	if !validID(tokenID) {
		return ErrNotFound
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := s.Repo.RevokeOpenAPIToken(ctx, actor, space, tokenID, now); err != nil {
		return err
	}
	s.notifyWatchers("token:"+tokenID, ErrUnauthenticated)
	return nil
}

// IssueRunReadToken mints an in-memory, read-only Bearer token scoped to the
// current run's space and actor. Calling revoke immediately invalidates it.
func (s *Service) IssueRunReadToken(space, actorID string) (string, func(), error) {
	if space == "" || actorID == "" {
		return "", nil, ErrInvalid
	}
	var randomBytes [32]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		return "", nil, err
	}
	rawToken := base64.RawURLEncoding.EncodeToString(randomBytes[:])
	sum := sha256.Sum256([]byte(rawToken))
	principal := BearerPrincipal{
		User:    User{ID: actorID},
		SpaceID: space,
		TokenID: "run:" + uuid.NewString(),
		Scope:   BearerScopeRunRead,
	}
	s.mu.Lock()
	if s.ephemeral == nil {
		s.ephemeral = map[[32]byte]BearerPrincipal{}
	}
	s.ephemeral[sum] = principal
	s.mu.Unlock()
	var once sync.Once
	revoke := func() {
		once.Do(func() {
			s.mu.Lock()
			delete(s.ephemeral, sum)
			s.mu.Unlock()
		})
	}
	return rawToken, revoke, nil
}

func (s *Service) lookupEphemeralBearer(hash []byte) (BearerPrincipal, bool) {
	if len(hash) != sha256.Size {
		return BearerPrincipal{}, false
	}
	var key [32]byte
	copy(key[:], hash)
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.ephemeral[key]
	return p, ok
}

func (s *Service) AuthenticateBearer(ctx context.Context, rawToken, space string, allowedScopes ...string) (BearerPrincipal, error) {
	hash, err := bearerTokenHash(rawToken)
	if err != nil {
		return BearerPrincipal{}, err
	}
	principal, ok := s.lookupEphemeralBearer(hash)
	if !ok {
		principal, err = s.Repo.AuthenticateBearerToken(ctx, hash)
		if err != nil {
			return BearerPrincipal{}, err
		}
		principal.Scope = BearerScopeOpenAPI
	}
	if len(allowedScopes) > 0 && !slices.Contains(allowedScopes, principal.Scope) {
		return BearerPrincipal{}, ErrUnauthenticated
	}
	if principal.SpaceID != space {
		return BearerPrincipal{}, ErrForbidden
	}
	if err := s.CheckAccess(ctx, principal.User.ID, space); err != nil {
		return BearerPrincipal{}, err
	}
	return principal, nil
}

// WatchBearerAccess registers both space-membership and token-revocation
// watchers before re-checking the Bearer token, closing the subscribe/revoke race.
func (s *Service) WatchBearerAccess(parent context.Context, rawToken, space string, principal BearerPrincipal) (context.Context, func(), error) {
	ctx, releaseAccess := s.SubscribeAccess(parent, principal.User.ID, space)
	ctx, releaseToken := s.subscribeKey(ctx, "token:"+principal.TokenID)
	release := func() {
		releaseToken()
		releaseAccess()
	}
	if _, err := s.AuthenticateBearer(ctx, rawToken, space, BearerScopeOpenAPI); err != nil {
		release()
		return nil, nil, err
	}
	return ctx, release, nil
}

func (s *Service) subscribeKey(parent context.Context, key string) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(parent)
	w := &watch{cancel}
	s.mu.Lock()
	if s.watchers == nil {
		s.watchers = map[string]map[*watch]struct{}{}
	}
	if s.watchers[key] == nil {
		s.watchers[key] = map[*watch]struct{}{}
	}
	s.watchers[key][w] = struct{}{}
	s.mu.Unlock()
	release := func() {
		cancel(nil)
		s.mu.Lock()
		delete(s.watchers[key], w)
		if len(s.watchers[key]) == 0 {
			delete(s.watchers, key)
		}
		s.mu.Unlock()
	}
	return ctx, release
}

func (s *Service) notifyWatchers(key string, cause error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for w := range s.watchers[key] {
		w.cancel(cause)
	}
}
