package identity

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/adapter/secrets"
	"github.com/imalimin/mass/internal/domain/run"
)

func TestNormalizeBaseURL(t *testing.T) {
	for input, expected := range map[string]string{
		" https://model.example/api/v3/// ": "https://model.example/api/v3",
		"https://model.example":             "https://model.example",
		"http://127.0.0.1:9000/v1/":         "http://127.0.0.1:9000/v1",
		"http://[::1]:9000/v1":              "http://[::1]:9000/v1",
		"http://localhost:9000/v1":          "http://localhost:9000/v1",
	} {
		got, err := NormalizeBaseURL(input)
		if err != nil || got != expected {
			t.Fatalf("normalize %q: %q %v", input, got, err)
		}
	}
	for _, input := range []string{"", "model.example/api", "file:///tmp/model", "http://model.example/v1", "http://localhost.example/v1", "https://user:secret@model.example/v1", "https://model.example/v1?api_key=secret", "https://model.example/v1?", "https://model.example/#fragment", "https://model.example/\nheader", strings.Repeat("x", 2049)} {
		if _, err := NormalizeBaseURL(input); !errors.Is(err, ErrInvalid) {
			t.Errorf("accepted invalid URL %q", input)
		}
	}
}

type modelMemory struct {
	Repository
	models       map[string]Model
	resolveError error
}

func (r *modelMemory) CheckAccess(context.Context, string, string, bool) error { return nil }
func (r *modelMemory) SaveModel(_ context.Context, _, _ string, m Model, create bool) error {
	if !create {
		old, ok := r.models[m.ID]
		if !ok || old.Revision != m.Revision {
			return ErrConflict
		}
		if len(m.Ciphertext) == 0 {
			m.KeyID, m.Ciphertext, m.SecretBindingID = old.KeyID, old.Ciphertext, old.SecretBindingID
		}
	}
	m.Revision++
	r.models[m.ID] = m
	return nil
}
func (r *modelMemory) ResolveModelSecret(_ context.Context, _, _, id string) (Model, error) {
	return r.models[id], r.resolveError
}
func TestModelConnectionAndRuntimeResolution(t *testing.T) {
	ctx := context.Background()
	repo := &modelMemory{models: map[string]Model{}}
	cipher, _ := secrets.New(bytes.Repeat([]byte{6}, 32))
	svc := &Service{Repo: repo, Cipher: cipher}
	actor, space := uuid.NewString(), uuid.NewString()
	input := Model{Name: "first", Provider: "ark", ModelID: "fixture", BaseURL: "https://first.example/v1/", APIKey: "first-key", ContextWindow: 4096, MaxOutputTokens: 1024, Enabled: true}
	first, err := svc.SaveModel(ctx, actor, space, "", input)
	if err != nil || first.BaseURL != "https://first.example/v1" || first.APIKey != "" || len(first.Ciphertext) != 0 || first.KeyID != "" {
		t.Fatal("create model leaked secret or invalid URL", first, err)
	}
	snapshot := run.ModelConfig{ID: first.ID, CredentialID: uuid.NewString(), Endpoint: "https://old-snapshot.example"}
	first.BaseURL = "https://rotated.example/v1"
	first, err = svc.SaveModel(ctx, actor, space, first.ID, first)
	if err != nil {
		t.Fatal(err)
	}
	key, url, revision, err := svc.ResolveRunSecret(ctx, actor, space, snapshot)
	if err != nil || string(key) != "first-key" || url != first.BaseURL || revision != 2 {
		t.Fatal("URL-only edit lost key", url, revision, err)
	}
	input.Name = "second"
	input.BaseURL = "https://second.example/v1"
	input.APIKey = "second-key"
	second, err := svc.SaveModel(ctx, actor, space, "", input)
	if err != nil {
		t.Fatal(err)
	}
	key, url, _, err = svc.ResolveRunSecret(ctx, actor, space, snapshot)
	if err != nil || string(key) != "first-key" || url != first.BaseURL {
		t.Fatal("another model redirected the fixed model ID", err)
	}
	first.APIKey = "rotated-key"
	first, err = svc.SaveModel(ctx, actor, space, first.ID, first)
	if err != nil {
		t.Fatal(err)
	}
	key, url, revision, err = svc.ResolveRunSecret(ctx, actor, space, snapshot)
	if err != nil || string(key) != "rotated-key" || url != first.BaseURL || revision != 3 {
		t.Fatal("key rotation ignored", err)
	}
	snapshot.ID = second.ID
	key, url, _, err = svc.ResolveRunSecret(ctx, actor, space, snapshot)
	if err != nil || string(key) != "second-key" || url != second.BaseURL {
		t.Fatal("second model modified by rotation", err)
	}
	repo.resolveError = ErrForbidden
	if _, _, _, err = svc.ResolveRunSecret(ctx, actor, space, snapshot); !errors.Is(err, ErrForbidden) {
		t.Fatal("permission failure ignored", err)
	}
	input.APIKey = ""
	if _, err = svc.SaveModel(ctx, actor, space, "", input); !errors.Is(err, ErrInvalid) {
		t.Fatal("creation without API key accepted", err)
	}
	input.APIKey = "key\nheader"
	if _, err = svc.SaveModel(ctx, actor, space, "", input); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid key accepted", err)
	}
}

func TestMigratedModelKeyBinding(t *testing.T) {
	ctx := context.Background()
	cipher, _ := secrets.New(bytes.Repeat([]byte{7}, 32))
	actor, space, modelID, oldCredentialID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	keyID, encrypted, _ := cipher.Seal(space, oldCredentialID, []byte("legacy-key"))
	model := Model{ID: modelID, Name: "migrated", Provider: "ark", ModelID: "fixture", BaseURL: ArkEndpoint, SecretBindingID: oldCredentialID, KeyID: keyID, Ciphertext: encrypted, ContextWindow: 4096, MaxOutputTokens: 1024, Enabled: true, Revision: 1}
	repo := &modelMemory{models: map[string]Model{modelID: model}}
	svc := &Service{Repo: repo, Cipher: cipher}
	snapshot := run.ModelConfig{ID: modelID, CredentialID: oldCredentialID}
	model.BaseURL = "https://edited.example/v1"
	saved, err := svc.SaveModel(ctx, actor, space, modelID, model)
	if err != nil {
		t.Fatal(err)
	}
	key, url, _, err := svc.ResolveRunSecret(ctx, actor, space, snapshot)
	if err != nil || string(key) != "legacy-key" || url != model.BaseURL || repo.models[modelID].SecretBindingID != oldCredentialID {
		t.Fatal("migrated binding lost on URL edit", err)
	}
	saved.APIKey = "new-key"
	if _, err = svc.SaveModel(ctx, actor, space, modelID, saved); err != nil {
		t.Fatal(err)
	}
	key, _, _, err = svc.ResolveRunSecret(ctx, actor, space, snapshot)
	if err != nil || string(key) != "new-key" || repo.models[modelID].SecretBindingID != modelID {
		t.Fatal("rotation did not adopt model binding", err)
	}
}
