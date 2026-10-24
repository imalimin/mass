package integration

import (
	"bytes"
	"context"
	"github.com/imalimin/mass/internal/adapter/secrets"
	"github.com/imalimin/mass/internal/app/identity"
	sessionapp "github.com/imalimin/mass/internal/app/session"
	"github.com/imalimin/mass/tests/testdb"
	"os"
	"testing"
	"time"
)

func TestRuntimeCredentialRotation(t *testing.T) {
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
	cred, err := auth.SaveCredential(ctx, actor.ID, space.ID, "", identity.CredentialInput{Name: "key", Secret: "first", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	model, err := auth.SaveModel(ctx, actor.ID, space.ID, "", identity.Model{Name: "fixture", Provider: "ark", Endpoint: identity.ArkEndpoint, ModelID: "fixture", CredentialID: cred.ID, ContextWindow: 8192, MaxOutputTokens: 1024, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := sessionapp.Model(model)
	key, revision, err := auth.ResolveRunSecret(ctx, actor.ID, space.ID, snapshot)
	if err != nil || string(key) != "first" || revision != 1 {
		t.Fatal("initial resolution", err)
	}
	cred, err = auth.SaveCredential(ctx, actor.ID, space.ID, cred.ID, identity.CredentialInput{Name: cred.Name, Secret: "second", Enabled: true, Revision: cred.Revision})
	if err != nil {
		t.Fatal(err)
	}
	key, revision, err = auth.ResolveRunSecret(ctx, actor.ID, space.ID, snapshot)
	if err != nil || string(key) != "second" || revision != 2 {
		t.Fatal("rotation ignored", err)
	}
	cred, err = auth.SaveCredential(ctx, actor.ID, space.ID, cred.ID, identity.CredentialInput{Name: cred.Name, Enabled: false, Revision: cred.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = auth.ResolveRunSecret(ctx, actor.ID, space.ID, snapshot); err == nil {
		t.Fatal("disabled credential resolved")
	}
	cred, err = auth.SaveCredential(ctx, actor.ID, space.ID, cred.ID, identity.CredentialInput{Name: cred.Name, Enabled: true, Revision: cred.Revision})
	if err != nil {
		t.Fatal(err)
	}
	model.Enabled = false
	if _, err = auth.SaveModel(ctx, actor.ID, space.ID, model.ID, model); err != nil {
		t.Fatal(err)
	}
	if _, _, err = auth.ResolveRunSecret(ctx, actor.ID, space.ID, snapshot); err == nil {
		t.Fatal("disabled model resolved")
	}
}
