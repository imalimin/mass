package integration

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/imalimin/mass/db/migrations"
	"github.com/imalimin/mass/internal/app/identity"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
	"github.com/imalimin/mass/tests/testdb"
)

func TestSessionJSONLSchemaReset(t *testing.T) {
	if os.Getenv("MASS_TEST_ADMIN_DSN") == "" {
		t.Skip("requires isolated MariaDB")
	}
	ctx := context.Background()
	db, err := testdb.NewVersion(ctx, 15)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	auth, err := identity.New(db.Store, nil, time.Hour, []byte("session-jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	actor, err := auth.CreateUser(ctx, "jsonl-user", "fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	_, token, _, err := auth.Login(ctx, actor.Username, "fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	space, err := auth.CreateSpace(ctx, actor.ID, "Retained space")
	if err != nil {
		t.Fatal(err)
	}
	agent, session := storage.NewShortID(), storage.NewShortID()
	if _, err := db.Admin.DB.ExecContext(ctx, "INSERT INTO definitions(space_id,id,kind,name) VALUES(?,?,'agent','discarded')", space.ID, agent); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Admin.DB.ExecContext(ctx, "INSERT INTO sessions(space_id,id,actor_id) VALUES(?,?,?)", space.ID, session, actor.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateTo(ctx, migrations.CurrentVersion); err != nil {
		t.Fatal(err)
	}
	if err := db.Store.CheckSchema(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Admin.DB.QueryContext(ctx, "SELECT table_name FROM information_schema.tables WHERE table_schema=DATABASE() ORDER BY table_name")
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	err = rows.Err()
	rows.Close()
	slices.Sort(tables)
	expected := []string{"audit_logs", "auth_sessions", "evaluation_datasets", "evaluation_experiments", "goose_db_version", "model_configs", "runs", "sessions", "space_members", "spaces", "users"}
	if err != nil || !slices.Equal(tables, expected) {
		t.Fatalf("unexpected schema tables: %v (%v)", tables, err)
	}
	var count int
	if err := db.Store.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM sessions").Scan(&count); err != nil || count != 0 {
		t.Fatalf("old sessions retained: %d (%v)", count, err)
	}
	if user, err := auth.Me(ctx, token); err != nil || user.ID != actor.ID {
		t.Fatalf("login was not retained: %v", err)
	}
	if err := auth.CheckAccess(ctx, actor.ID, space.ID); err != nil {
		t.Fatalf("space membership was not retained: %v", err)
	}
	if _, err := db.Store.DB.ExecContext(ctx, "INSERT INTO sessions(id,space_id,agent_id,actor_id,created_at,request_key,request_digest) VALUES(?,?,?,?,UTC_TIMESTAMP(6),'new',?)", session, space.ID, agent, actor.ID, journal.Digest("fixture")); err != nil {
		t.Fatal(err)
	}
	const insert = "INSERT INTO runs(id,session_id,status,error_code) VALUES(?,?,'queued','')"
	if _, err := db.Store.DB.ExecContext(ctx, insert, uuid.NewString(), session); err == nil {
		t.Fatal("runs accepted a UUID")
	}
	if _, err := db.Store.DB.ExecContext(ctx, insert, storage.NewShortID(), session); err != nil {
		t.Fatal(err)
	}
}
