package identity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/adapter/secrets"
	"github.com/imalimin/mass/internal/domain/storage"
)

type openapiTokenMemory struct {
	Repository
	users   map[string]User
	owners  map[string]map[string]bool
	members map[string]map[string]bool
	tokens  map[string]OpenAPIToken
	audits  []string
}

func newOpenAPITokenMemory() *openapiTokenMemory {
	return &openapiTokenMemory{
		users:   map[string]User{},
		owners:  map[string]map[string]bool{},
		members: map[string]map[string]bool{},
		tokens:  map[string]OpenAPIToken{},
	}
}

func (m *openapiTokenMemory) CheckAccess(_ context.Context, actor, space string, owner bool) error {
	if !m.members[space][actor] {
		return ErrForbidden
	}
	if owner && !m.owners[space][actor] {
		return ErrForbidden
	}
	return nil
}

func (m *openapiTokenMemory) OpenAPITokens(_ context.Context, actor, space string, q Query) ([]OpenAPIToken, error) {
	if err := m.CheckAccess(context.Background(), actor, space, true); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	var out []OpenAPIToken
	for _, tok := range m.tokens {
		if tok.SpaceID == space && tok.ID > q.After {
			tok.Status = OpenAPITokenStatus(tok.ExpiresAt, tok.RevokedAt, now)
			out = append(out, tok)
		}
	}
	return out, nil
}

func (m *openapiTokenMemory) CreateOpenAPIToken(_ context.Context, actor, space string, tok OpenAPIToken) error {
	if err := m.CheckAccess(context.Background(), actor, space, true); err != nil {
		return err
	}
	m.tokens[tok.ID] = tok
	m.audits = append(m.audits, "openapi_token.created:"+tok.ID)
	return nil
}

func (m *openapiTokenMemory) OpenAPITokenSecret(_ context.Context, actor, space, id string) (OpenAPIToken, error) {
	if err := m.CheckAccess(context.Background(), actor, space, true); err != nil {
		return OpenAPIToken{}, err
	}
	tok, ok := m.tokens[id]
	if !ok || tok.SpaceID != space {
		return OpenAPIToken{}, ErrNotFound
	}
	if tok.RevokedAt != nil || !tok.ExpiresAt.After(time.Now().UTC()) {
		return OpenAPIToken{}, ErrConflict
	}
	m.audits = append(m.audits, "openapi_token.copied:"+tok.ID)
	return tok, nil
}

func (m *openapiTokenMemory) RenewOpenAPIToken(_ context.Context, actor, space, id string, expiresAt time.Time) (OpenAPIToken, error) {
	if err := m.CheckAccess(context.Background(), actor, space, true); err != nil {
		return OpenAPIToken{}, err
	}
	tok, ok := m.tokens[id]
	if !ok || tok.SpaceID != space {
		return OpenAPIToken{}, ErrNotFound
	}
	if tok.RevokedAt != nil {
		return OpenAPIToken{}, ErrConflict
	}
	tok.ExpiresAt = expiresAt
	m.tokens[id] = tok
	m.audits = append(m.audits, "openapi_token.renewed:"+tok.ID)
	return tok, nil
}

func (m *openapiTokenMemory) RevokeOpenAPIToken(_ context.Context, actor, space, id string, revokedAt time.Time) error {
	if err := m.CheckAccess(context.Background(), actor, space, true); err != nil {
		return err
	}
	tok, ok := m.tokens[id]
	if !ok || tok.SpaceID != space {
		return ErrNotFound
	}
	if tok.RevokedAt != nil {
		return nil
	}
	t := revokedAt
	tok.RevokedAt = &t
	m.tokens[id] = tok
	m.audits = append(m.audits, "openapi_token.revoked:"+tok.ID)
	return nil
}

func (m *openapiTokenMemory) AuthenticateBearerToken(_ context.Context, hash []byte) (BearerPrincipal, error) {
	now := time.Now().UTC()
	for _, tok := range m.tokens {
		if bytes.Equal(tok.TokenHash, hash) {
			if tok.RevokedAt != nil || !tok.ExpiresAt.After(now) {
				return BearerPrincipal{}, ErrUnauthenticated
			}
			u, ok := m.users[tok.IssuerID]
			if !ok {
				return BearerPrincipal{}, ErrUnauthenticated
			}
			return BearerPrincipal{User: u, SpaceID: tok.SpaceID, TokenID: tok.ID}, nil
		}
	}
	return BearerPrincipal{}, ErrUnauthenticated
}

func TestOpenAPITokenLifecycleAndBearerAuth(t *testing.T) {
	ctx := context.Background()
	repo := newOpenAPITokenMemory()
	owner := User{ID: uuid.NewString(), Username: "owner"}
	member := User{ID: uuid.NewString(), Username: "member"}
	spaceA := storage.NewShortID()
	spaceB := storage.NewShortID()
	repo.users[owner.ID] = owner
	repo.users[member.ID] = member
	repo.owners[spaceA] = map[string]bool{owner.ID: true}
	repo.members[spaceA] = map[string]bool{owner.ID: true, member.ID: true}
	repo.owners[spaceB] = map[string]bool{owner.ID: true}
	repo.members[spaceB] = map[string]bool{owner.ID: true}

	cipher, err := secrets.New(bytes.Repeat([]byte{5}, 32))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(repo, cipher, time.Hour, []byte("cursor-secret"))
	if err != nil {
		t.Fatal(err)
	}

	// Default 180d creation.
	before := time.Now().UTC()
	created, err := svc.CreateOpenAPIToken(ctx, owner.ID, spaceA, "integration", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(created.Token) != 43 {
		t.Fatalf("expected 43-char base64url token, got %d (%q)", len(created.Token), created.Token)
	}
	rawBytes, err := base64.RawURLEncoding.DecodeString(created.Token)
	if err != nil || len(rawBytes) != 32 {
		t.Fatalf("token does not decode to 32 bytes: %v len=%d", err, len(rawBytes))
	}
	if delta := created.ExpiresAt.Sub(before.Add(DefaultOpenAPITokenTTL)); delta < -5*time.Second || delta > 5*time.Second {
		t.Fatalf("unexpected default expiry delta: %v", delta)
	}
	stored := repo.tokens[created.ID]
	wantHash := sha256.Sum256([]byte(created.Token))
	if !bytes.Equal(stored.TokenHash, wantHash[:]) {
		t.Fatal("stored token_hash mismatch")
	}
	if bytes.Contains(stored.Ciphertext, []byte(created.Token)) {
		t.Fatal("ciphertext contains plaintext token")
	}

	// Non-owner cannot manage tokens.
	if _, err := svc.CreateOpenAPIToken(ctx, member.ID, spaceA, "nope", nil); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden for member create, got %v", err)
	}
	if _, err := svc.ListOpenAPITokens(ctx, member.ID, spaceA, Query{Limit: 20}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden for member list, got %v", err)
	}

	// Invalid inputs.
	past := time.Now().UTC().Add(-time.Minute)
	if _, err := svc.CreateOpenAPIToken(ctx, owner.ID, spaceA, "bad-exp", &past); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for past expiry, got %v", err)
	}
	tooFar := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := svc.CreateOpenAPIToken(ctx, owner.ID, spaceA, "too-far", &tooFar); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for year>9999, got %v", err)
	}
	if _, err := svc.CreateOpenAPIToken(ctx, owner.ID, spaceA, "  padded  ", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for padded name, got %v", err)
	}

	// Copy active token returns exact plaintext.
	copied, err := svc.CopyOpenAPIToken(ctx, owner.ID, spaceA, created.ID)
	if err != nil || copied.Token != created.Token || copied.ID != created.ID {
		t.Fatalf("copy mismatch: %+v %v", copied, err)
	}

	// Authenticate Bearer: valid in spaceA, forbidden in spaceB, invalid token -> 401.
	principal, err := svc.AuthenticateBearer(ctx, created.Token, spaceA)
	if err != nil || principal.User.ID != owner.ID || principal.TokenID != created.ID {
		t.Fatalf("auth failed: %+v %v", principal, err)
	}
	if _, err := svc.AuthenticateBearer(ctx, created.Token, spaceB); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden cross-space, got %v", err)
	}
	if _, err := svc.AuthenticateBearer(ctx, "not-a-valid-token", spaceA); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("expected ErrUnauthenticated, got %v", err)
	}

	// Expired token: 401 even on wrong space (401 before 403), copy returns 409, renew restores active.
	expiredRow := repo.tokens[created.ID]
	expiredRow.ExpiresAt = time.Now().UTC().Add(-time.Second)
	repo.tokens[created.ID] = expiredRow
	if _, err := svc.AuthenticateBearer(ctx, created.Token, spaceB); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("expected ErrUnauthenticated for expired token cross-space, got %v", err)
	}
	if _, err := svc.CopyOpenAPIToken(ctx, owner.ID, spaceA, created.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict copying expired token, got %v", err)
	}
	newExp := time.Now().UTC().Add(365 * 24 * time.Hour)
	renewed, err := svc.RenewOpenAPIToken(ctx, owner.ID, spaceA, created.ID, newExp)
	if err != nil || renewed.Status != OpenAPITokenActive {
		t.Fatalf("renew failed: %+v %v", renewed, err)
	}
	if _, err := svc.AuthenticateBearer(ctx, created.Token, spaceA); err != nil {
		t.Fatalf("renewed token should authenticate: %v", err)
	}

	// WatchBearerAccess cancels immediately on revoke.
	watchCtx, release, err := svc.WatchBearerAccess(ctx, created.Token, spaceA, principal)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := svc.RevokeOpenAPIToken(ctx, owner.ID, spaceA, created.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-watchCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("WatchBearerAccess did not cancel on revoke")
	}

	// Idempotent revoke; revoked token cannot copy, renew, or authenticate.
	auditsBefore := len(repo.audits)
	if err := svc.RevokeOpenAPIToken(ctx, owner.ID, spaceA, created.ID); err != nil {
		t.Fatalf("idempotent revoke failed: %v", err)
	}
	if len(repo.audits) != auditsBefore {
		t.Fatal("idempotent revoke recorded duplicate audit")
	}
	if _, err := svc.CopyOpenAPIToken(ctx, owner.ID, spaceA, created.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict copying revoked token, got %v", err)
	}
	if _, err := svc.RenewOpenAPIToken(ctx, owner.ID, spaceA, created.ID, newExp); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict renewing revoked token, got %v", err)
	}
	if _, err := svc.AuthenticateBearer(ctx, created.Token, spaceA); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("expected ErrUnauthenticated for revoked token, got %v", err)
	}
}

func TestRunReadTokenLifecycleAndScope(t *testing.T) {
	ctx := context.Background()
	repo := newOpenAPITokenMemory()
	member := User{ID: uuid.NewString(), Username: "member"}
	spaceA := storage.NewShortID()
	spaceB := storage.NewShortID()
	repo.users[member.ID] = member
	repo.members[spaceA] = map[string]bool{member.ID: true}
	repo.members[spaceB] = map[string]bool{member.ID: true}

	cipher, err := secrets.New(bytes.Repeat([]byte{6}, 32))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(repo, cipher, time.Hour, []byte("cursor-secret"))
	if err != nil {
		t.Fatal(err)
	}

	tok, revoke, err := svc.IssueRunReadToken(spaceA, member.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) != 43 {
		t.Fatalf("expected 43-char token, got %d", len(tok))
	}
	// No DB or audit pollution.
	if len(repo.tokens) != 0 || len(repo.audits) != 0 {
		t.Fatalf("ephemeral token polluted repo: tokens=%d audits=%d", len(repo.tokens), len(repo.audits))
	}

	// Succeeds for run_read scope in spaceA.
	p, err := svc.AuthenticateBearer(ctx, tok, spaceA, BearerScopeOpenAPI, BearerScopeRunRead)
	if err != nil || p.Scope != BearerScopeRunRead || p.User.ID != member.ID || p.SpaceID != spaceA {
		t.Fatalf("unexpected principal: %+v err=%v", p, err)
	}

	// Rejected with 401 for openapi-only scope.
	if _, err := svc.AuthenticateBearer(ctx, tok, spaceA, BearerScopeOpenAPI); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("expected ErrUnauthenticated for openapi-only scope, got %v", err)
	}

	// Rejected with 403 for cross-space even with run_read scope.
	if _, err := svc.AuthenticateBearer(ctx, tok, spaceB, BearerScopeRunRead); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden cross-space, got %v", err)
	}

	// Revoke invalidates immediately and is idempotent.
	revoke()
	revoke()
	if _, err := svc.AuthenticateBearer(ctx, tok, spaceA, BearerScopeRunRead); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("expected ErrUnauthenticated after revoke, got %v", err)
	}
}
