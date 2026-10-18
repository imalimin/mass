package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	transport "github.com/imalimin/mass/internal/transport/http"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/adapter/secrets"
	"github.com/imalimin/mass/internal/adapter/workspace"
	"github.com/imalimin/mass/internal/app/identity"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
	"github.com/imalimin/mass/internal/resource"
	"github.com/imalimin/mass/tests/fakes"
	"github.com/imalimin/mass/tests/testdb"
)

func proposed(actor, kind, run string, data any) journal.Proposed {
	raw, _ := json.Marshal(data)
	return journal.Proposed{EventID: uuid.NewString(), RunID: run, Type: kind, SchemaVersion: 1, Actor: journal.Actor{Kind: "user", ID: actor}, OccurredAt: time.Now(), Data: raw}
}
func appendRequest(space, session string, events ...journal.Proposed) journal.Request {
	return journal.Request{SpaceID: space, SessionID: session, AppendKey: uuid.NewString(), Events: events}
}
func saveRequest(actor, space, session, run string) resource.SaveRequest {
	finish := proposed(actor, "run.finished", run, journal.FinishData{Status: "succeeded"})
	return resource.SaveRequest{SpaceID: space, SessionID: session, RunID: run, SnapshotID: uuid.NewString(), ArchiveID: uuid.NewString(), ManifestID: uuid.NewString(), EventID: uuid.NewString(), AppendKey: uuid.NewString(), OccurredAt: time.Now(), Finish: &finish}
}
func TestD02UpgradeCompatibility(t *testing.T) {
	if os.Getenv("MASS_TEST_ADMIN_DSN") == "" {
		t.Skip("requires isolated MariaDB")
	}
	ctx := context.Background()
	db, err := testdb.NewVersion(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cipher, _ := secrets.New(bytes.Repeat([]byte{8}, 32))
	svc, _ := identity.New(db.Store, cipher, time.Hour, []byte("test"))
	user, err := svc.CreateUser(ctx, "upgrade", "upgrade-password")
	if err != nil {
		t.Fatal(err)
	}
	space, err := svc.CreateSpace(ctx, user.ID, "Existing D02 space")
	if err != nil {
		t.Fatal(err)
	}
	cred, err := svc.SaveCredential(ctx, user.ID, space.ID, "", identity.CredentialInput{Name: "Existing credential", Secret: "upgrade-provider-secret", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	model, err := svc.SaveModel(ctx, user.ID, space.ID, "", identity.Model{Name: "Existing model", Provider: "ark", Endpoint: identity.ArkEndpoint, ModelID: "ep-upgrade", CredentialID: cred.ID, ContextWindow: 1000, MaxOutputTokens: 100, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	_, token, _, err := svc.Login(ctx, "upgrade", "upgrade-password")
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Admin.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = db.Admin.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = db.Store.CheckSchema(ctx); err != nil {
		t.Fatal(err)
	}
	current, err := svc.Me(ctx, token)
	if err != nil || current.ID != user.ID {
		t.Fatal("session changed by upgrade", err)
	}
	secret, err := svc.ModelSecret(ctx, user.ID, space.ID, model.ID)
	if err != nil || string(secret) != "upgrade-provider-secret" {
		t.Fatal("credential changed by upgrade", err)
	}
	spaces, err := db.Store.Spaces(ctx, user.ID, identity.Query{Limit: 100})
	if err != nil || len(spaces) != 1 || spaces[0].ID != space.ID || !spaces[0].IsOwner {
		t.Fatal("space/owner changed", err)
	}
}
func TestStorageJournalAndSnapshots(t *testing.T) {
	if os.Getenv("MASS_TEST_ADMIN_DSN") == "" {
		t.Skip("requires isolated MariaDB")
	}
	exerciseStorage(t, fakes.NewBlobs(), "test/")
}
func exerciseStorage(t *testing.T, blobs storage.BlobStore, prefix string) {
	t.Helper()
	ctx := context.Background()
	db, err := testdb.New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cipher, _ := secrets.New(bytes.Repeat([]byte{8}, 32))
	svc, _ := identity.New(db.Store, cipher, time.Hour, []byte("test"))
	user, err := svc.CreateUser(ctx, "alice", "correct-password-alice")
	if err != nil {
		t.Fatal(err)
	}
	space, err := svc.CreateSpace(ctx, user.ID, "Storage")
	if err != nil {
		t.Fatal(err)
	}
	other, err := svc.CreateUser(ctx, "other", "correct-password-other")
	if err != nil {
		t.Fatal(err)
	}
	otherSpace, err := svc.CreateSpace(ctx, other.ID, "Other")
	if err != nil {
		t.Fatal(err)
	}
	content := &resource.ContentStore{Repo: db.Store, Blobs: blobs, Prefix: prefix, MaxBytes: 1 << 20}
	j := &journal.Journal{Repo: db.Store}
	session, run := uuid.NewString(), uuid.NewString()
	initial := appendRequest(space.ID, session, proposed(user.ID, "session.created", "", journal.SessionData{}), proposed(user.ID, "run.created", run, struct{}{}), proposed(user.ID, "run.started", run, struct{}{}))
	events, err := j.Append(ctx, user.ID, initial)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[2].Sequence != 3 {
		t.Fatal("initial sequence")
	}
	zero := int64(0)
	initial.ExpectedSequence = &zero
	again, err := j.Append(ctx, user.ID, initial)
	if err != nil || again[2].Sequence != 3 {
		t.Fatal("idempotency checked after expected sequence", err)
	}
	initial.AppendKey = uuid.NewString()
	again, err = j.Append(ctx, user.ID, initial)
	if err != nil || again[2].Sequence != 3 {
		t.Fatal("event ID retry", err)
	}
	changed := initial
	changed.Events = append([]journal.Proposed(nil), initial.Events...)
	changed.Events[0].OccurredAt = changed.Events[0].OccurredAt.Add(time.Second)
	if _, err = j.Append(ctx, user.ID, changed); !errors.Is(err, storage.ErrConflict) {
		t.Fatal("changed duplicate accepted", err)
	}
	bad := proposed(user.ID, "operation.reconciled", run, journal.ReconcileData{Outcome: "unknown"})
	bad.OperationID = uuid.NewString()
	bad.SchemaVersion = 999
	if _, err = j.Append(ctx, user.ID, appendRequest(space.ID, session, bad)); !errors.Is(err, storage.ErrSchema) {
		t.Fatal("unknown schema", err)
	}
	if _, err = j.Append(ctx, other.ID, appendRequest(space.ID, session, proposed(other.ID, "workspace.snapshot.failed", run, journal.FailureData{Reason: "test"}))); !errors.Is(err, identity.ErrForbidden) {
		t.Fatal("cross-space append", err)
	}
	objectID := uuid.NewString()
	object, err := content.Put(ctx, user.ID, space.ID, objectID, "trace", "text/plain", bytes.NewBufferString("immutable original"))
	if err != nil {
		t.Fatal("put", err)
	}
	if _, _, err = content.Read(ctx, user.ID, space.ID, objectID); !errors.Is(err, identity.ErrNotFound) {
		t.Fatal("unreferenced object visible", err)
	}
	if _, err = content.Put(ctx, user.ID, space.ID, objectID, "trace", "text/plain", bytes.NewBufferString("changed")); !errors.Is(err, storage.ErrConflict) {
		t.Fatal("mutable object", err)
	}
	if _, err = content.Put(ctx, user.ID, space.ID, objectID, "trace", "text/plain", bytes.NewBufferString("immutable original")); err != nil {
		t.Fatal("upload retry", err)
	}
	fact := proposed(user.ID, "operation.reconciled", run, journal.ReconcileData{Outcome: "unknown"})
	fact.OperationID = uuid.NewString()
	fact.Refs = []storage.Ref{{Role: "raw", ObjectID: object.ID, SHA256: object.SHA256}}
	// Force a view update to fail after event/object rows have been inserted.
	_, err = db.Admin.DB.Exec("CREATE TRIGGER reject_sequence BEFORE UPDATE ON sessions FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='injected view failure'")
	if err != nil {
		t.Fatal(err)
	}
	request := appendRequest(space.ID, session, fact)
	if _, err = j.Append(ctx, user.ID, request); err == nil {
		t.Fatal("injected transaction succeeded")
	}
	db.Admin.DB.Exec("DROP TRIGGER reject_sequence")
	if _, _, err = content.Read(ctx, user.ID, space.ID, object.ID); !errors.Is(err, identity.ErrNotFound) {
		t.Fatal("failed transaction made reference visible", err)
	}
	if _, err = j.Append(ctx, user.ID, request); err != nil {
		t.Fatal(err)
	}
	_, file, err := content.Read(ctx, user.ID, space.ID, object.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(file)
	file.Close()
	if string(raw) != "immutable original" {
		t.Fatal("bytes changed")
	}
	if _, _, err = content.Read(ctx, other.ID, space.ID, object.ID); !errors.Is(err, identity.ErrForbidden) {
		t.Fatal("cross-space object read", err)
	}
	foreign, err := content.Put(ctx, other.ID, otherSpace.ID, uuid.NewString(), "trace", "text/plain", bytes.NewBufferString("foreign"))
	if err != nil {
		t.Fatal(err)
	}
	foreignFact := fact
	foreignFact.EventID = uuid.NewString()
	foreignFact.Refs = []storage.Ref{{Role: "raw", ObjectID: foreign.ID, SHA256: foreign.SHA256}}
	if _, err = j.Append(ctx, user.ID, appendRequest(space.ID, session, foreignFact)); err == nil {
		t.Fatal("cross-space ref accepted")
	}
	root := t.TempDir()
	workspaces, err := workspace.New(root, storage.Limits{MaxFileBytes: 1 << 18, MaxTotalBytes: 1 << 19, MaxEntries: 100})
	if err != nil {
		t.Fatal(err)
	}
	defer workspaces.Close()
	snapshots := &resource.SnapshotStore{Repo: db.Store, Content: content, Journal: j, Workspaces: workspaces, Timeout: 30 * time.Second}
	write := func(text string) {
		t.Helper()
		err := workspaces.With(ctx, space.ID, session, func(dir resource.WorkspaceDir) error {
			if err := dir.Root().Mkdir("empty", 0700); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			f, err := dir.Root().OpenFile("result.txt", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
			_, err = f.WriteString(text)
			f.Close()
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	write("first run")
	save := saveRequest(user.ID, space.ID, session, run)

	// Fail after snapshot metadata and run status are written, when releasing
	// occupancy. The entire snapshot/event/view transaction must roll back.
	_, err = db.Admin.DB.Exec("CREATE TRIGGER reject_release BEFORE DELETE ON session_active_runs FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='injected release failure'")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = snapshots.Save(ctx, user.ID, save); err == nil {
		t.Fatal("failed release committed snapshot")
	}
	db.Admin.DB.Exec("DROP TRIGGER reject_release")
	var savedCount, activeCount int
	db.Admin.DB.QueryRow("SELECT COUNT(*) FROM workspace_snapshots WHERE space_id=? AND id=?", space.ID, save.SnapshotID).Scan(&savedCount)
	db.Admin.DB.QueryRow("SELECT COUNT(*) FROM session_active_runs WHERE space_id=? AND session_id=?", space.ID, session).Scan(&activeCount)
	if savedCount != 0 || activeCount != 1 {
		t.Fatal("snapshot transaction was not atomic")
	}
	if _, f, e := content.Read(ctx, user.ID, space.ID, save.ArchiveID); e == nil {
		f.Close()
		t.Fatal("orphan archive became visible")
	}
	snapshot, err := snapshots.Save(ctx, user.ID, save)
	if err != nil {
		t.Fatal("snapshot", err)
	}

	// Exercise new read routes through the same D02 Cookie/member middleware.
	_, token, _, err := svc.Login(ctx, "alice", "correct-password-alice")
	if err != nil {
		t.Fatal(err)
	}
	storageOptions := &transport.StorageOptions{Content: content, Journal: j, Snapshots: snapshots, Check: func(context.Context) error { return nil }}
	handler := transport.NewHandler(fstest.MapFS{"index.html": {Data: []byte("MASS")}}, "test", slog.New(slog.NewTextHandler(io.Discard, nil)), transport.IdentityOptions{Service: svc, Origin: "http://mass.test", Storage: storageOptions})
	validate := contractValidator(t)
	for _, path := range []string{"/spaces/" + space.ID + "/sessions/" + session + "/journal?limit=2", "/spaces/" + space.ID + "/workspace-snapshots/" + snapshot.ID} {
		r := httptest.NewRequest("GET", "http://mass.test/api/v1"+path, nil)
		r.Header.Set("Cookie", "mass_session="+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("storage GET status %d", w.Code)
		}
		validate("GET", path, w)
		r = httptest.NewRequest("GET", "http://mass.test/api/v1"+path, nil)
		w = httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatal("unauthenticated storage read")
		}
	}
	download := "/api/v1/spaces/" + space.ID + "/payloads/" + object.ID
	r := httptest.NewRequest("GET", "http://mass.test"+download, nil)
	r.Header.Set("Cookie", "mass_session="+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 200 || w.Body.String() != "immutable original" || w.Header().Get("Content-Disposition") == "" {
		t.Fatal("authorized payload route", w.Code)
	}
	db.Admin.DB.Exec("UPDATE auth_sessions SET expires_at=UTC_TIMESTAMP(6)-INTERVAL 1 SECOND WHERE user_id=?", user.ID)
	r = httptest.NewRequest("GET", "http://mass.test"+download, nil)
	r.Header.Set("Cookie", "mass_session="+token)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("expired payload download")
	}
	r = httptest.NewRequest("GET", "http://mass.test/health/ready", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal("healthy readiness")
	}
	storageOptions.Check = func(context.Context) error { return storage.ErrIntegrity }
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 503 {
		t.Fatal("failed readiness")
	}
	// A committed retry must not package subsequent local changes again.
	write("uncommitted later change")
	retry, err := snapshots.Save(ctx, user.ID, save)
	if err != nil || retry.ID != snapshot.ID {
		t.Fatal("snapshot unknown commit recovery", err)
	}
	if err = os.RemoveAll(filepath.Join(root, space.ID+"_"+session)); err != nil {
		t.Fatal(err)
	}
	if err = snapshots.Restore(ctx, user.ID, space.ID, session); err != nil {
		t.Fatal("restore", err)
	}
	data, err := os.ReadFile(filepath.Join(root, space.ID+"_"+session, "result.txt"))
	if err != nil || string(data) != "first run" {
		t.Fatal("restored bytes", err)
	}
	if _, err = os.Stat(filepath.Join(root, space.ID+"_"+session, "empty")); err != nil {
		t.Fatal("empty directory lost")
	}
	clone := uuid.NewString()
	if _, err = j.Append(ctx, user.ID, appendRequest(space.ID, clone, proposed(user.ID, "session.created", "", journal.SessionData{SourceSnapshotID: snapshot.ID}))); err != nil {
		t.Fatal(err)
	}
	_, err = db.Admin.DB.Exec("CREATE TRIGGER reject_restore BEFORE INSERT ON runtime_events FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='injected restore failure'")
	if err != nil {
		t.Fatal(err)
	}
	if err = snapshots.Restore(ctx, user.ID, space.ID, clone); err == nil {
		t.Fatal("restore journal failure ignored")
	}
	db.Admin.DB.Exec("DROP TRIGGER reject_restore")
	if err = snapshots.Restore(ctx, user.ID, space.ID, clone); err != nil {
		t.Fatal("clone restore", err)
	}
	clonePage, e := j.Read(ctx, user.ID, space.ID, clone, 0, nil, 100)
	if e != nil || len(clonePage.Events) != 2 {
		t.Fatal("clone inherited source conversation", e)
	}
	write("second run")
	cloned, _ := os.ReadFile(filepath.Join(root, space.ID+"_"+clone, "result.txt"))
	if string(cloned) != "first run" {
		t.Fatal("clone shared directory")
	}
	if err = snapshots.Restore(ctx, other.ID, otherSpace.ID, uuid.NewString()); err == nil {
		t.Fatal("nonexistent session restore")
	}
	if _, err = j.Append(ctx, other.ID, appendRequest(otherSpace.ID, uuid.NewString(), proposed(other.ID, "session.created", "", journal.SessionData{SourceSnapshotID: snapshot.ID}))); err == nil {
		t.Fatal("cross-space snapshot restore")
	}
	run2 := uuid.NewString()
	if _, err = j.Append(ctx, user.ID, appendRequest(space.ID, session, proposed(user.ID, "run.created", run2, struct{}{}), proposed(user.ID, "run.started", run2, struct{}{}))); err != nil {
		t.Fatal(err)
	}
	if _, err = j.Append(ctx, user.ID, appendRequest(space.ID, session, proposed(user.ID, "run.created", uuid.NewString(), struct{}{}))); !errors.Is(err, storage.ErrActive) {
		t.Fatal("concurrent active run", err)
	}
	// Same session, multiple run IDs, concurrent appenders: all results retained.
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := run
			if i%2 == 0 {
				id = run2
			}
			e := proposed(user.ID, "operation.reconciled", id, journal.ReconcileData{Outcome: "unknown"})
			e.OperationID = uuid.NewString()
			if _, err := j.Append(ctx, user.ID, appendRequest(space.ID, session, e)); err != nil {
				t.Errorf("concurrent append: %v", err)
			}
		}(i)
	}
	wg.Wait()
	page, err := j.Read(ctx, user.ID, space.ID, session, 0, nil, 2)
	if err != nil || !page.HasMore {
		t.Fatal("pagination", err)
	}
	through := page.Through
	later := proposed(user.ID, "operation.reconciled", run2, journal.ReconcileData{Outcome: "unknown"})
	later.OperationID = uuid.NewString()
	if _, err = j.Append(ctx, user.ID, appendRequest(space.ID, session, later)); err != nil {
		t.Fatal(err)
	}
	after := page.Next
	count := len(page.Events)
	for page.HasMore {
		page, err = j.Read(ctx, user.ID, space.ID, session, after, &through, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range page.Events {
			if e.Sequence != after+1 {
				t.Fatal("sequence gap")
			}
			after = e.Sequence
		}
		count += len(page.Events)
	}
	if int64(count) != through {
		t.Fatal("unstable pagination")
	}
	if _, err = snapshots.Save(ctx, user.ID, saveRequest(user.ID, space.ID, session, run2)); err != nil {
		t.Fatal("second snapshot", err)
	}

	// A failed ready transition must retain an invisible uploading intent, and a
	// stable retry must resolve the existing TOS bytes without overwriting them.
	_, err = db.Admin.DB.Exec("CREATE TRIGGER reject_ready BEFORE UPDATE ON content_objects FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='injected ready failure'")
	if err != nil {
		t.Fatal(err)
	}
	pendingID := uuid.NewString()
	if _, err = content.Put(ctx, user.ID, space.ID, pendingID, "trace", "text/plain", bytes.NewBufferString("pending")); err == nil {
		t.Fatal("ready transition succeeded")
	}
	db.Admin.DB.Exec("DROP TRIGGER reject_ready")
	pendingFact := proposed(user.ID, "operation.reconciled", run2, journal.ReconcileData{Outcome: "unknown"})
	pendingFact.OperationID = uuid.NewString()
	pendingFact.Refs = []storage.Ref{{Role: "raw", ObjectID: pendingID, SHA256: journal.Digest("unimportant")}}
	if _, err = j.Append(ctx, user.ID, appendRequest(space.ID, session, pendingFact)); err == nil {
		t.Fatal("uploading object referenced")
	}
	if _, err = content.Put(ctx, user.ID, space.ID, pendingID, "trace", "text/plain", bytes.NewBufferString("pending")); err != nil {
		t.Fatal("uncertain upload retry failed", err)
	}
	if fake, ok := blobs.(*fakes.Blobs); ok {
		run3 := uuid.NewString()
		if _, err = j.Append(ctx, user.ID, appendRequest(space.ID, session, proposed(user.ID, "run.created", run3, struct{}{}), proposed(user.ID, "run.started", run3, struct{}{}))); err != nil {
			t.Fatal(err)
		}
		fake.SetFailure(true)
		failed := saveRequest(user.ID, space.ID, session, run3)
		if _, err = snapshots.Save(ctx, user.ID, failed); err == nil {
			t.Fatal("TOS outage reported snapshot success")
		}
		fake.SetFailure(false)
		var n int
		db.Admin.DB.QueryRow("SELECT COUNT(*) FROM workspace_snapshots WHERE space_id=? AND id=?", space.ID, failed.SnapshotID).Scan(&n)
		if n != 0 {
			t.Fatal("failed snapshot indexed")
		}
		if _, err = j.Append(ctx, user.ID, appendRequest(space.ID, session, proposed(user.ID, "run.finished", run3, journal.FinishData{Status: "succeeded"}))); !errors.Is(err, storage.ErrWorkspace) {
			t.Fatal("success without snapshot")
		}
		fake.Corrupt(object.Key)
		if _, f, err := content.Read(ctx, user.ID, space.ID, object.ID); err == nil {
			f.Close()
			t.Fatal("corrupted object exposed")
		}
	}
}
