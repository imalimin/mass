package integration

import (
	"bytes"
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/adapter/secrets"
	"github.com/imalimin/mass/internal/app/identity"
	sessionapp "github.com/imalimin/mass/internal/app/session"
	"github.com/imalimin/mass/tests/testdb"
	"os"
	"testing"
	"time"
)

func TestRuntimeModelKeyRotation(t *testing.T) {
	if os.Getenv("MASS_TEST_ADMIN_DSN") == "" {
		t.Skip("requires isolated MariaDB")
	}
	ctx := context.Background()
	db, err := testdb.New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cipher, _ := secrets.New(bytes.Repeat([]byte{5}, 32))
	auth, _ := identity.New(db.Store, cipher, time.Hour, []byte("rotation"))
	actor, err := auth.CreateUser(ctx, "alice", "correct-password-alice")
	if err != nil {
		t.Fatal(err)
	}
	space, err := auth.CreateSpace(ctx, actor.ID, "rotation")
	if err != nil {
		t.Fatal(err)
	}
	model, err := auth.SaveModel(ctx, actor.ID, space.ID, "", identity.Model{Name: "fixture", Provider: "ark", BaseURL: identity.ArkEndpoint, APIKey: "first", ModelID: "fixture", ContextWindow: 8192, MaxOutputTokens: 1024, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := sessionapp.Model(model)
	key, url, revision, err := auth.ResolveRunSecret(ctx, actor.ID, space.ID, snapshot)
	if err != nil || string(key) != "first" || url != identity.ArkEndpoint || revision != 1 {
		t.Fatal("initial resolution", err)
	}
	model.BaseURL = "https://rotated.example/api"
	model, err = auth.SaveModel(ctx, actor.ID, space.ID, model.ID, model)
	if err != nil {
		t.Fatal(err)
	}
	key, url, revision, err = auth.ResolveRunSecret(ctx, actor.ID, space.ID, snapshot)
	if err != nil || string(key) != "first" || url != model.BaseURL || revision != 2 {
		t.Fatal("URL edit lost key", err)
	}
	model.APIKey = "second"
	model, err = auth.SaveModel(ctx, actor.ID, space.ID, model.ID, model)
	if err != nil {
		t.Fatal(err)
	}
	key, _, revision, err = auth.ResolveRunSecret(ctx, actor.ID, space.ID, snapshot)
	if err != nil || string(key) != "second" || revision != 3 {
		t.Fatal("rotation ignored", err)
	}
	if _, err = auth.SaveModel(ctx, actor.ID, space.ID, model.ID, identity.Model{Name: model.Name, Provider: model.Provider, ModelID: model.ModelID, BaseURL: model.BaseURL, APIKey: "stale", ContextWindow: 8192, MaxOutputTokens: 1024, Enabled: true, Revision: 1}); !errors.Is(err, identity.ErrConflict) {
		t.Fatal("stale edit accepted", err)
	}
	model.Enabled = false
	if _, err = auth.SaveModel(ctx, actor.ID, space.ID, model.ID, model); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = auth.ResolveRunSecret(ctx, actor.ID, space.ID, snapshot); err == nil {
		t.Fatal("disabled model resolved")
	}
}

func TestModelConnectionMigration(t *testing.T) {
	if os.Getenv("MASS_TEST_ADMIN_DSN") == "" {
		t.Skip("requires isolated MariaDB")
	}
	for _, version := range []int64{7, 8} {
		t.Run(string(rune('0'+version)), func(t *testing.T) {
			ctx := context.Background()
			db, err := testdb.NewVersion(ctx, version)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			cipher, _ := secrets.New(bytes.Repeat([]byte{6}, 32))
			auth, _ := identity.New(db.Store, cipher, time.Hour, []byte("migration"))
			actor, err := auth.CreateUser(ctx, "alice", "correct-password-alice")
			if err != nil {
				t.Fatal(err)
			}
			space, err := auth.CreateSpace(ctx, actor.ID, "migration")
			if err != nil {
				t.Fatal(err)
			}
			credential := uuid.NewString()
			keyID, encrypted, err := cipher.Seal(space.ID, credential, []byte("retained-key"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Admin.DB.Exec("INSERT INTO credentials(space_id,id,name,key_id,ciphertext,enabled) VALUES(?,?,?,?,?,TRUE)", space.ID, credential, "legacy", keyID, encrypted); err != nil {
				t.Fatal(err)
			}
			expected := []string{"https://legacy.example/A", "https://legacy.example/a"}
			ids := []string{uuid.NewString(), uuid.NewString()}
			for i, id := range ids {
				if _, err = db.Admin.DB.Exec("INSERT INTO model_configs(space_id,id,name,provider,model_id,endpoint,credential_id,context_window,max_output_tokens,temperature,enabled) VALUES(?,?,?,?,?,?,?,?,?,?,TRUE)", space.ID, id, "legacy", "ark", "fixture", expected[i], credential, 4096, 1024, 0.7); err != nil {
					t.Fatal(err)
				}
			}
			// Schema 8 credentials with an explicit address override the legacy endpoint.
			if version == 8 {
				expected = []string{"https://configured.example/v1", "https://configured.example/v1"}
				if _, err = db.Admin.DB.Exec("UPDATE credentials SET base_url=? WHERE space_id=?", expected[0], space.ID); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				if err = db.Admin.Migrate(ctx); err != nil {
					t.Fatal(err)
				}
			}
			var count int
			if err = db.Admin.DB.QueryRow("SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='credentials'").Scan(&count); err != nil || count != 0 {
				t.Fatal("credential table remains", err)
			}
			for i, id := range ids {
				model, e := db.Store.Model(ctx, actor.ID, space.ID, id)
				if e != nil || model.BaseURL != expected[i] {
					t.Fatal("migration URL", i, model.BaseURL, e)
				}
				key, _, _, e := auth.ResolveRunSecret(ctx, actor.ID, space.ID, sessionapp.Model(model))
				if e != nil || string(key) != "retained-key" {
					t.Fatal("migrated key failed", e)
				}
				model.APIKey = "independent-key"
				model.BaseURL = "https://independent.example/v1"
				if i == 0 {
					if _, e = auth.SaveModel(ctx, actor.ID, space.ID, id, model); e != nil {
						t.Fatal(e)
					}
				}
			}
			second, _ := db.Store.Model(ctx, actor.ID, space.ID, ids[1])
			key, url, _, err := auth.ResolveRunSecret(ctx, actor.ID, space.ID, sessionapp.Model(second))
			if err != nil || string(key) != "retained-key" || url != expected[1] {
				t.Fatal("shared legacy key remained coupled", err)
			}
		})
	}
}
