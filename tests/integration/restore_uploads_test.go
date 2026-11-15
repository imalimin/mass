package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/adapter/secrets"
	"github.com/imalimin/mass/internal/app/catalog"
	"github.com/imalimin/mass/internal/app/identity"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
	"github.com/imalimin/mass/internal/resource"
	"github.com/imalimin/mass/tests/fakes"
	"github.com/imalimin/mass/tests/testdb"
)

func TestRestoreSkipsOnlyUnreferencedUploads(t *testing.T) {
	if os.Getenv("MASS_TEST_ADMIN_DSN") == "" {
		t.Skip("requires isolated MariaDB")
	}
	ctx := context.Background()
	db, err := testdb.New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cipher, _ := secrets.New(bytes.Repeat([]byte{8}, 32))
	auth, _ := identity.New(db.Store, cipher, time.Hour, []byte("restore-test"))
	actor, err := auth.CreateUser(ctx, "restore-test", "correct-password-restore")
	if err != nil {
		t.Fatal(err)
	}
	space, err := auth.CreateSpace(ctx, actor.ID, "Restore intents")
	if err != nil {
		t.Fatal(err)
	}
	blobs := fakes.NewBlobs()
	content := &resource.ContentStore{Repo: db.Store, Blobs: blobs, Prefix: "source/", MaxBytes: 1 << 20}
	put := func(kind, text string) storage.Object {
		t.Helper()
		o, err := content.Put(ctx, actor.ID, space.ID, uuid.NewString(), kind, "text/plain", strings.NewReader(text))
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	// Both pre-upload failure and lost SQL-ready transitions produce real
	// uploading intents. Some retained bytes may even be corrupt; none is a fact.
	blobs.SetFailure(true)
	missing, err := content.Put(ctx, actor.ID, space.ID, uuid.NewString(), "trace", "text/plain", strings.NewReader("never uploaded"))
	if err == nil || missing.State != "uploading" {
		t.Fatal("outage did not retain an upload intent", err)
	}
	blobs.SetFailure(false)
	if _, err = db.Admin.DB.Exec("CREATE TRIGGER reject_ready BEFORE UPDATE ON content_objects FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='injected ready failure'"); err != nil {
		t.Fatal(err)
	}
	var pending []storage.Object
	for range 2 {
		o, err := content.Put(ctx, actor.ID, space.ID, uuid.NewString(), "trace", "text/plain", strings.NewReader("uploaded without ready"))
		if err == nil {
			t.Fatal("ready failure not injected")
		}
		pending = append(pending, o)
	}
	if _, err = db.Admin.DB.Exec("DROP TRIGGER reject_ready"); err != nil {
		t.Fatal(err)
	}
	blobs.Corrupt(pending[1].Key)
	pending = append(pending, missing)
	confirmed := put("trace", "confirmed bytes")
	j := &journal.Journal{Repo: db.Store}
	session := storage.NewShortID()
	created := proposed(actor.ID, "session.created", "", journal.SessionData{})
	message := proposed(actor.ID, "user.message.accepted", "", journal.UserMessageData{ID: uuid.NewString(), ContentID: confirmed.ID})
	message.Refs = []storage.Ref{{Role: "content", ObjectID: confirmed.ID, SHA256: confirmed.SHA256}}
	if _, err = j.Append(ctx, actor.ID, appendRequest(space.ID, session, created, message)); err != nil {
		t.Fatal(err)
	}
	attachment := put("attachment", "unaccepted attachment")
	if err = db.Store.RegisterAttachment(ctx, actor.ID, space.ID, run.Attachment{ID: uuid.NewString(), SessionID: session, Name: "pending.txt", Size: attachment.Size, MediaType: attachment.MediaType, Ref: storage.Ref{ObjectID: attachment.ID, SHA256: attachment.SHA256}}); err != nil {
		t.Fatal(err)
	}
	catalogue := &catalog.Service{Repo: db.Store, Content: content}
	skill, err := catalogue.Create(ctx, actor.ID, space.ID, "skill", "restore-skill")
	if err != nil {
		t.Fatal(err)
	}
	skill, err = catalogue.Save(ctx, actor.ID, space.ID, "skill", skill.ID, skill.Revision, catalog.Document{Markdown: "# Restore skill", Assets: []catalog.Asset{}})
	if err != nil {
		t.Fatal(err)
	}
	skill, err = catalogue.Upload(ctx, actor.ID, space.ID, "skill", skill.ID, "assets/rules.txt", "text/plain", skill.Revision, strings.NewReader("draft-only resource"))
	if err != nil {
		t.Fatal(err)
	}
	var draftID string
	if err = db.Store.DB.QueryRow("SELECT content_id FROM skill_drafts WHERE space_id=? AND definition_id=?", space.ID, skill.ID).Scan(&draftID); err != nil {
		t.Fatal(err)
	}
	copies := 0
	copyObject := func(ctx context.Context, o storage.Object) (storage.Object, error) {
		copies++
		r, err := blobs.Open(ctx, o.Key, o.VersionID)
		if err != nil {
			return o, err
		}
		defer r.Close()
		raw, err := io.ReadAll(r)
		hash := sha256.Sum256(raw)
		if err != nil || int64(len(raw)) != o.Size || hex.EncodeToString(hash[:]) != o.SHA256 {
			return o, storage.ErrIntegrity
		}
		o.Key = "restored/" + o.SpaceID + "/objects/" + o.ID
		o.URI = blobs.URI(o.Key)
		_, err = blobs.PutNew(ctx, o.Key, bytes.NewReader(raw), o.Size, o.MediaType)
		return o, err
	}
	restore := func() (storage.RestoreResult, error) {
		return db.Store.RelocateStorage(ctx, db.Name, func(string, string, string, []byte) error { return nil }, copyObject, nil)
	}
	rejectUnfinished := func(t *testing.T, id string) {
		t.Helper()
		if _, err = db.Admin.DB.Exec("UPDATE content_objects SET state='uploading' WHERE space_id=? AND id=?", space.ID, id); err != nil {
			t.Fatal(err)
		}
		result, err := restore()
		if !errors.Is(err, storage.ErrIntegrity) || result != (storage.RestoreResult{}) || copies != 0 {
			t.Fatal("referenced upload must fail before copying", result, err, copies)
		}
		if _, err = db.Admin.DB.Exec("UPDATE content_objects SET state='ready' WHERE space_id=? AND id=?", space.ID, id); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("message", func(t *testing.T) { rejectUnfinished(t, confirmed.ID) })
	t.Run("unaccepted_attachment", func(t *testing.T) { rejectUnfinished(t, attachment.ID) })
	t.Run("draft_without_event", func(t *testing.T) { rejectUnfinished(t, draftID) })
	version, err := catalogue.Publish(ctx, actor.ID, space.ID, "skill", skill.ID, "v1", skill.Revision, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("published_bundle_without_event", func(t *testing.T) { rejectUnfinished(t, version.ContentID) })
	var ready int
	if err = db.Store.DB.QueryRow("SELECT COUNT(*) FROM content_objects WHERE state='ready'").Scan(&ready); err != nil {
		t.Fatal(err)
	}
	result, err := restore()
	if err != nil || result.Relocated != ready || result.UnreferencedUploads != len(pending) || copies != ready {
		t.Fatal("unreferenced intents blocked relocation", result, err, copies, ready)
	}
	for _, want := range pending {
		var state, key, uri, hash string
		if err = db.Store.DB.QueryRow("SELECT state,object_key,tos_uri,sha256 FROM content_objects WHERE space_id=? AND id=?", space.ID, want.ID).Scan(&state, &key, &uri, &hash); err != nil || state != "uploading" || key != want.Key || uri != want.URI || hash != want.SHA256 {
			t.Fatal("upload intent changed", want.ID, err)
		}
		if _, f, err := content.Read(ctx, actor.ID, space.ID, want.ID); err == nil {
			f.Close()
			t.Fatal("unreferenced upload became visible")
		}
		if _, ok := blobs.Data["restored/"+space.ID+"/objects/"+want.ID]; ok {
			t.Fatal("upload intent was copied")
		}
	}
}
