package integration

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/adapter/secrets"
	"github.com/imalimin/mass/internal/app/catalog"
	"github.com/imalimin/mass/internal/app/identity"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
	"github.com/imalimin/mass/internal/resource"
	transport "github.com/imalimin/mass/internal/transport/http"
	"github.com/imalimin/mass/tests/fakes"
	"github.com/imalimin/mass/tests/testdb"
)

func TestCatalogDatabaseAndHTTP(t *testing.T) {
	if os.Getenv("MASS_TEST_ADMIN_DSN") == "" {
		t.Skip("requires isolated MariaDB")
	}
	exerciseCatalog(t, fakes.NewBlobs(), "test/catalog/")
}
func jsonValue[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

type hookBlobs struct {
	storage.BlobStore
	hook func()
}

func (b *hookBlobs) PutNew(ctx context.Context, key string, r io.Reader, n int64, media string) (storage.BlobInfo, error) {
	v, e := b.BlobStore.PutNew(ctx, key, r, n, media)
	if b.hook != nil {
		f := b.hook
		b.hook = nil
		f()
	}
	return v, e
}
func exerciseCatalog(t *testing.T, blobs storage.BlobStore, prefix string) {
	t.Helper()
	ctx := context.Background()
	db, e := testdb.New(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	cipher, _ := secrets.New(bytes.Repeat([]byte{3}, 32))
	auth, _ := identity.New(db.Store, cipher, time.Hour, []byte("catalog-test"))
	u, e := auth.CreateUser(ctx, "alice", "correct-password-alice")
	if e != nil {
		t.Fatal(e)
	}
	outsider, e := auth.CreateUser(ctx, "bob", "correct-password-bob")
	if e != nil {
		t.Fatal(e)
	}
	space, e := auth.CreateSpace(ctx, u.ID, "catalog")
	if e != nil {
		t.Fatal(e)
	}
	other, e := auth.CreateSpace(ctx, u.ID, "other")
	if e != nil {
		t.Fatal(e)
	}
	model, e := auth.SaveModel(ctx, u.ID, space.ID, "", identity.Model{Name: "model", Provider: "ark", ModelID: "ep-test", BaseURL: identity.ArkEndpoint, APIKey: "fixture-key", ContextWindow: 32768, MaxOutputTokens: 4096, Enabled: true})
	if e != nil {
		t.Fatal(e)
	}
	content := &resource.ContentStore{Repo: db.Store, Blobs: blobs, Prefix: prefix, MaxBytes: 32 << 20}
	svc := &catalog.Service{Repo: db.Store, Content: content}
	handler := transport.NewHandler(fstest.MapFS{"index.html": {Data: []byte("MASS")}}, "test", slog.New(slog.NewTextHandler(io.Discard, nil)), transport.IdentityOptions{Service: auth, Origin: "http://mass.test", Catalog: svc, Storage: &transport.StorageOptions{Content: content}})
	_, token, _, e := auth.Login(ctx, "alice", "correct-password-alice")
	if e != nil {
		t.Fatal(e)
	}
	validate := contractValidator(t)
	api := func(method, path string, body any, status int) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "http://mass.test/api/v1"+path, bytes.NewReader(raw))
		r.Header.Set("Origin", "http://mass.test")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Cookie", "mass_session="+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s want %d got %d %s", method, path, status, w.Code, w.Body.String())
		}
		if !strings.Contains(path, "/payloads/") {
			validate(method, path, w)
		}
		return w
	}
	root := "/spaces/" + space.ID
	create := func(kind, name string) catalog.Draft {
		return jsonValue[catalog.Draft](t, api("POST", root+"/"+kind+"s", map[string]any{"name": name}, 201))
	}
	path := func(d catalog.Draft) string { return root + "/" + d.Kind + "s/" + d.ID }
	save := func(d catalog.Draft, markdown, config string) catalog.Draft {
		doc := d.Document
		doc.Markdown = markdown
		doc.ConfigYAML = config
		return jsonValue[catalog.Draft](t, api("PUT", path(d)+"/draft", map[string]any{"expected_revision": d.Revision, "document": doc}, 200))
	}
	publish := func(d catalog.Draft, label string) catalog.Version {
		return jsonValue[catalog.Version](t, api("POST", path(d)+"/versions", map[string]any{"expected_revision": d.Revision, "label": label}, 201))
	}
	s1 := create("skill", "extract")
	s2 := create("skill", "report")
	s1 = save(s1, "# Extract v1", "")
	s2 = save(s2, "# Report v1", "")
	s1 = jsonValue[catalog.Draft](t, api("POST", path(s1)+"/resources", map[string]any{"expected_revision": s1.Revision, "path": "assets/template.txt", "media_type": "text/plain", "data_base64": base64.StdEncoding.EncodeToString([]byte("original template"))}, 200))
	v11 := publish(s1, "v1")
	v21 := publish(s2, "v1")
	api("POST", path(s1)+"/versions", map[string]any{"expected_revision": s1.Revision, "label": "v1"}, 409)
	agent := create("agent", "assistant")
	draftResponse := api("GET", path(agent)+"/draft", nil, 200)
	if strings.Contains(draftResponse.Body.String(), `"assets"`) {
		t.Fatal("Agent draft contains assets")
	}
	api("POST", path(agent)+"/resources", map[string]any{}, 404)
	api("PUT", path(agent)+"/draft", map[string]any{"expected_revision": agent.Revision, "document": map[string]any{"assets": []any{}}}, 400)
	config := func(version string) string {
		c := catalog.Config{SchemaVersion: 1, Name: agent.Name, Model: model.ID, Skills: []catalog.SkillRef{{ID: s1.ID, Version: version}, {ID: s2.ID, Version: "v1"}}, Tools: []string{"read_file", "run_code"}}
		c.Context.MaxInputTokens = 24000
		c.Context.ReservedOutputTokens = 4000
		c.Context.Compression = "summary"
		c.Limits.MaxSteps = 30
		c.Limits.TimeoutSeconds = 300
		b, _ := json.Marshal(c)
		return string(b)
	}
	agent = save(agent, "# Agent v1", config("v1"))
	a1 := publish(agent, "v1")
	if a1.Bundle.Bindings[0].VersionID != v11.ID || a1.Bundle.Bindings[1].VersionID != v21.ID {
		t.Fatal("inexact dependency resolution")
	}
	debug := jsonValue[catalog.Version](t, api("POST", path(agent)+"/debug-snapshots", map[string]any{"expected_revision": agent.Revision}, 201))
	s1 = save(s1, "# Extract v2", "")
	v12 := publish(s1, "v2")
	old := jsonValue[catalog.Version](t, api("GET", path(agent)+"/versions/"+a1.ID, nil, 200))
	if old.Bundle.Bindings[0].VersionID != v11.ID {
		t.Fatal("Skill upgrade changed Agent")
	}
	agent = save(agent, "# Agent v2", config("v2"))
	a2 := publish(agent, "v2")
	if a2.Bundle.Bindings[0].VersionID != v12.ID {
		t.Fatal("new Agent did not resolve new exact version")
	}
	snap := jsonValue[catalog.Version](t, api("GET", path(agent)+"/debug-snapshots/"+debug.ID, nil, 200))
	if snap.Bundle.Document.Markdown != "# Agent v1" || snap.Bundle.Bindings[0].VersionID != v11.ID {
		t.Fatal("debug snapshot changed")
	}
	api("GET", path(agent)+"/diff?from="+a1.ID+"&to="+a2.ID, nil, 200)
	refs := jsonValue[identity.Page[catalog.Reference]](t, api("GET", path(s1)+"/versions/"+v11.ID+"/references", nil, 200))
	if len(refs.Items) != 2 {
		t.Fatal("reverse references missing")
	}
	api("DELETE", path(s1)+"/versions/"+v11.ID, nil, 409)
	if _, e = db.Admin.DB.Exec("DELETE FROM skill_versions WHERE space_id=? AND id=?", space.ID, v11.ID); e == nil {
		t.Fatal("referential integrity allowed deletion")
	}
	api("PUT", path(agent)+"/versions/"+a1.ID+"/archive", map[string]bool{"archived": true}, 409)
	api("PUT", path(agent)+"/default-version", map[string]string{"version_id": a2.ID}, 204)
	api("PUT", path(agent)+"/versions/"+a1.ID+"/archive", map[string]bool{"archived": true}, 204)
	api("GET", path(agent)+"/versions/"+a1.ID, nil, 200)
	api("PUT", path(s1)+"/versions/"+v12.ID+"/archive", map[string]bool{"archived": true}, 204)
	api("POST", path(agent)+"/versions", map[string]any{"expected_revision": agent.Revision, "label": "blocked"}, 404)
	api("PUT", path(s1)+"/versions/"+v12.ID+"/archive", map[string]bool{"archived": false}, 204)
	copied := jsonValue[catalog.Draft](t, api("POST", path(agent)+"/copy", map[string]string{"name": "copied-agent"}, 201))
	publish(copied, "v1")
	// Removing a draft resource preserves historical published bytes.
	asset := s1.Document.Assets[0]
	var draftContent, versionContent, uri string
	if e := db.Admin.DB.QueryRow(`SELECT d.content_id,v.content_id,o.tos_uri
		FROM skill_drafts d JOIN skill_versions v ON v.space_id=d.space_id AND v.definition_id=d.definition_id
		JOIN content_objects o ON o.space_id=v.space_id AND o.id=v.content_id WHERE d.space_id=? AND v.id=?`, space.ID, v12.ID).Scan(&draftContent, &versionContent, &uri); e != nil {
		t.Fatal(e)
	}
	if draftContent != v12.ContentID || versionContent != v12.ContentID || !strings.HasPrefix(uri, "tos://") || !asset.Package || asset.ObjectID != v12.ContentID {
		t.Fatal("Skill content was not stored as one referenced TOS package", draftContent, versionContent, uri)
	}
	var draftRefs, versionAssetRefs int
	if e := db.Admin.DB.QueryRow(`SELECT
		(SELECT COUNT(*) FROM definition_drafts WHERE space_id=? AND definition_id=?),
		(SELECT COUNT(*) FROM definition_versions WHERE space_id=? AND id=?)`, space.ID, s1.ID, space.ID, v12.ID).Scan(&draftRefs, &versionAssetRefs); e != nil || draftRefs != 0 || versionAssetRefs != 0 {
		t.Fatal("unexpected Skill package reference count", draftRefs, versionAssetRefs, e)
	}
	s1.Document.Assets = []catalog.Asset{}
	s1 = save(s1, s1.Document.Markdown, "")
	if got := api("GET", root+"/payloads/"+asset.ObjectID+"?path="+url.QueryEscape(asset.Path), nil, 200).Body.String(); got != "original template" {
		t.Fatal("old resource changed")
	}
	api("GET", "/spaces/"+other.ID+"/skills/"+s1.ID+"/draft", nil, 404)
	api("GET", "/spaces/"+other.ID+"/payloads/"+asset.ObjectID, nil, 404)
	if _, e = svc.Repo.CatalogDraft(ctx, outsider.ID, space.ID, "skill", s1.ID); !errors.Is(e, identity.ErrForbidden) {
		t.Fatal("outsider allowed", e)
	}
	foreign, e := svc.Create(ctx, u.ID, other.ID, "skill", "foreign")
	if e != nil {
		t.Fatal(e)
	}
	doc := foreign.Document
	doc.Markdown = "foreign"
	doc.Assets = []catalog.Asset{asset}
	if _, e = svc.Save(ctx, u.ID, other.ID, "skill", foreign.ID, foreign.Revision, doc); !errors.Is(e, identity.ErrNotFound) {
		t.Fatal("foreign asset accepted", e)
	}
	// Two editors with the same revision cannot overwrite each other.
	doc = agent.Document
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.Save(ctx, u.ID, space.ID, "agent", agent.ID, agent.Revision, doc)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for e := range results {
		if e == nil {
			successes++
		} else if errors.Is(e, catalog.ErrDraft) {
			conflicts++
		} else {
			t.Fatal(e)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatal("optimistic lock failed")
	}
	agent, e = svc.Repo.CatalogDraft(ctx, u.ID, space.ID, "agent", agent.ID)
	if e != nil {
		t.Fatal(e)
	}
	// A change after object upload must invalidate publication, including model changes.
	hooked := &hookBlobs{BlobStore: blobs}
	content.Blobs = hooked
	hooked.hook = func() {
		m := model
		m.Name = "changed model"
		model, e = auth.SaveModel(ctx, u.ID, space.ID, model.ID, m)
		if e != nil {
			t.Fatal(e)
		}
	}
	if _, e = svc.Publish(ctx, u.ID, space.ID, "agent", agent.ID, "stale-model", agent.Revision, false); !errors.Is(e, catalog.ErrDraft) {
		t.Fatal("published stale model", e)
	}
	content.Blobs = blobs
	// Audit failure rolls back versions, dependencies, default pointer and visibility.
	if _, e = db.Admin.DB.Exec("CREATE TRIGGER reject_publish BEFORE INSERT ON audit_logs FOR EACH ROW BEGIN IF NEW.action='definition.published' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='injected'; END IF; END"); e != nil {
		t.Fatal(e)
	}
	failed, e := svc.Publish(ctx, u.ID, space.ID, "agent", agent.ID, "rollback", agent.Revision, false)
	if e == nil {
		t.Fatal("publication survived audit failure")
	}
	var count int
	db.Admin.DB.QueryRow("SELECT COUNT(*) FROM definition_versions WHERE space_id=? AND id=?", space.ID, failed.ID).Scan(&count)
	if count != 0 {
		t.Fatal("partial version committed")
	}
	if _, _, e = content.Read(ctx, u.ID, space.ID, failed.ContentID); !errors.Is(e, identity.ErrNotFound) {
		t.Fatal("orphan visible", e)
	}
	if _, e = db.Admin.DB.Exec("DROP TRIGGER reject_publish"); e != nil {
		t.Fatal(e)
	}
	publish(agent, "rollback")

	if fake, ok := blobs.(*fakes.Blobs); ok {
		fake.SetFailure(true)
		if _, err := svc.Publish(ctx, u.ID, space.ID, "agent", agent.ID, "tos-failure", agent.Revision, false); err == nil {
			t.Fatal("TOS failure published a version")
		}
		fake.SetFailure(false)
		db.Admin.DB.QueryRow("SELECT COUNT(*) FROM definition_versions WHERE space_id=? AND definition_id=? AND label='tos-failure'", space.ID, agent.ID).Scan(&count)
		if count != 0 {
			t.Fatal("version visible after upload failure")
		}
	}
	// A same-user different-space Skill cannot be bound even when its version exists.
	foreign, e = svc.Save(ctx, u.ID, other.ID, "skill", foreign.ID, foreign.Revision, catalog.Document{Markdown: "foreign", Assets: []catalog.Asset{}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Publish(ctx, u.ID, other.ID, "skill", foreign.ID, "v1", foreign.Revision, false); e != nil {
		t.Fatal(e)
	}
	cross := agent.Document
	cross.ConfigYAML = strings.Replace(config("v2"), s2.ID, foreign.ID, 1)
	agent, e = svc.Save(ctx, u.ID, space.ID, "agent", agent.ID, agent.Revision, cross)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Publish(ctx, u.ID, space.ID, "agent", agent.ID, "foreign", agent.Revision, false); !errors.Is(e, identity.ErrNotFound) {
		t.Fatal("foreign dependency accepted", e)
	}
	// Missing dependency and latest are rejected, model availability is rechecked.
	missing := strings.Replace(config("v2"), s2.ID, uuid.NewString(), 1)
	agent = save(agent, agent.Document.Markdown, missing)
	api("POST", path(agent)+"/versions", map[string]any{"expected_revision": agent.Revision, "label": "missing"}, 404)
	bad := agent.Document
	bad.ConfigYAML = config("latest")
	api("PUT", path(agent)+"/draft", map[string]any{"expected_revision": agent.Revision, "document": bad}, 400)
	agent = save(agent, agent.Document.Markdown, config("v2"))
	model.Enabled = false
	if _, e = auth.SaveModel(ctx, u.ID, space.ID, model.ID, model); e != nil {
		t.Fatal(e)
	}
	api("POST", path(agent)+"/debug-snapshots", map[string]any{"expected_revision": agent.Revision}, 404)
	api("GET", path(agent)+"/versions/"+a1.ID, nil, 200)
	api("PUT", path(agent)+"/archive", map[string]bool{"archived": true}, 204)
	api("PUT", path(agent)+"/draft", map[string]any{"expected_revision": agent.Revision, "document": agent.Document}, 400)
	api("GET", root+"/agents?limit=1", nil, 200)
	api("GET", root+"/skills", nil, 200)
	publish(s2, "v9")
	tenth := publish(s2, "v10")
	publish(s2, "nightly")
	api("PUT", path(s2)+"/versions/"+tenth.ID+"/archive", map[string]bool{"archived": true}, 204)
	type publication struct {
		version catalog.Version
		err     error
	}
	publications := make(chan publication, 2)
	for range 2 {
		go func() {
			version, err := svc.Publish(ctx, u.ID, space.ID, "skill", s2.ID, "", s2.Revision, false)
			publications <- publication{version, err}
		}()
	}
	labels := map[string]bool{}
	for range 2 {
		result := <-publications
		if result.err != nil {
			t.Fatal(result.err)
		}
		labels[result.version.Label] = true
	}
	if !labels["v11"] || !labels["v12"] {
		t.Fatalf("unexpected automatic labels %v", labels)
	}
	t.Log("catalog: exact versions, immutable debug/resource content, CAS, publication rollback, cross-space checks, archive/default and API contracts passed")
}

func TestD01UpgradeCompatibility(t *testing.T) {
	if os.Getenv("MASS_TEST_ADMIN_DSN") == "" {
		t.Skip("requires isolated MariaDB")
	}
	ctx := context.Background()
	db, err := testdb.NewVersion(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cipher, _ := secrets.New(bytes.Repeat([]byte{7}, 32))
	auth, _ := identity.New(db.Store, cipher, time.Hour, []byte("test"))
	user, err := auth.CreateUser(ctx, "legacy", "legacy-password")
	if err != nil {
		t.Fatal(err)
	}
	space, err := auth.CreateSpace(ctx, user.ID, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	blobs := fakes.NewBlobs()
	content := &resource.ContentStore{Repo: db.Store, Blobs: blobs, Prefix: "test/", MaxBytes: 1 << 20}
	o, err := content.Put(ctx, user.ID, space.ID, uuid.NewString(), "trace", "text/plain", strings.NewReader("D01 original payload"))
	if err != nil {
		t.Fatal(err)
	}
	session, runID, eventID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, seed := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO sessions(space_id,id,actor_id,last_sequence) VALUES(?,?,?,1)", []any{space.ID, session, user.ID}},
		{"INSERT INTO runs(space_id,session_id,id,actor_id,status,source_event_id,applied_sequence) VALUES(?,?,?,?,'queued',?,1)", []any{space.ID, session, runID, user.ID, eventID}},
		{"INSERT INTO runtime_events(space_id,session_id,event_id,sequence,run_id,event_type,schema_version,actor_kind,actor_id,occurred_at,data,event_digest) VALUES(?,?,?,1,?,'run.created',1,'user',?,UTC_TIMESTAMP(6),'{}',?)", []any{space.ID, session, eventID, runID, user.ID, journal.Digest("legacy")}},
		{"INSERT INTO event_objects(space_id,event_id,object_id,role,ordinal) VALUES(?,?,?,'payload',0)", []any{space.ID, eventID, o.ID}},
	} {
		if _, err := db.Admin.DB.ExecContext(ctx, seed.sql, seed.args...); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err = db.MigrateTo(ctx, 14); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"sessions", "runs"} {
		var count int
		if err := db.Admin.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatal("legacy runtime data was retained", table, count, err)
		}
	}
	if _, err := content.Repo.Object(ctx, user.ID, space.ID, o.ID, true); !errors.Is(err, identity.ErrNotFound) {
		t.Fatal("old payload still exposed after runtime reset", err)
	}
	stored, err := blobs.Open(ctx, o.Key, o.VersionID)
	if err != nil {
		t.Fatal(err)
	}
	defer stored.Close()
	raw, _ := io.ReadAll(stored)
	if string(raw) != "D01 original payload" {
		t.Fatal("runtime reset deleted the old TOS object")
	}
}

func TestSkillPackageUpgrade(t *testing.T) {
	if os.Getenv("MASS_TEST_ADMIN_DSN") == "" {
		t.Skip("requires isolated MariaDB")
	}
	ctx := context.Background()
	db, err := testdb.NewVersion(ctx, 9)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cipher, _ := secrets.New(bytes.Repeat([]byte{3}, 32))
	auth, _ := identity.New(db.Store, cipher, time.Hour, []byte("skill-package-upgrade"))
	actor, err := auth.CreateUser(ctx, "alice", "correct-password-alice")
	if err != nil {
		t.Fatal(err)
	}
	space, err := auth.CreateSpace(ctx, actor.ID, "upgrade")
	if err != nil {
		t.Fatal(err)
	}
	content := &resource.ContentStore{Repo: db.Store, Blobs: fakes.NewBlobs(), Prefix: "test/skills/", MaxBytes: 8 << 20}
	svc := &catalog.Service{Repo: db.Store, Content: content}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Admin.DB.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	seed := func(kind, name string, doc catalog.Document) (string, string) {
		t.Helper()
		id, version := uuid.NewString(), uuid.NewString()
		bundle, _ := json.Marshal(catalog.Bundle{FormatVersion: 1, DefinitionID: id, Kind: kind, Name: name, Revision: 1, Document: doc})
		o, err := content.Put(ctx, actor.ID, space.ID, uuid.NewString(), "definition.bundle", "application/json", bytes.NewReader(bundle))
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(doc)
		exec("INSERT INTO definitions(space_id,id,kind,name) VALUES(?,?,?,?)", space.ID, id, kind, name)
		exec("INSERT INTO definition_drafts(space_id,definition_id,document) VALUES(?,?,?)", space.ID, id, raw)
		exec(`INSERT INTO definition_versions(space_id,id,definition_id,label,debug,content_id,sha256,bundle,created_at)
			VALUES(?,?,?,'v1',FALSE,?,?,?,UTC_TIMESTAMP(6))`, space.ID, version, id, o.ID, o.SHA256, bundle)
		exec("INSERT INTO definition_version_objects(space_id,version_id,path,object_id) VALUES(?,?,?,?)", space.ID, version, "bundle.json", o.ID)
		return id, version
	}
	file, err := content.Put(ctx, actor.ID, space.ID, uuid.NewString(), "definition.asset", "text/plain", strings.NewReader("legacy bytes"))
	if err != nil {
		t.Fatal(err)
	}
	asset := catalog.Asset{Path: "assets/sample.txt", ObjectID: file.ID, SHA256: file.SHA256, Size: file.Size, MediaType: file.MediaType}
	skill, skillVersion := seed("skill", "legacy", catalog.Document{Markdown: "# Legacy Skill", Assets: []catalog.Asset{asset}})
	agent, agentVersion := seed("agent", "legacy-agent", catalog.Document{Markdown: "# Legacy Agent", Assets: []catalog.Asset{asset}})
	exec("INSERT INTO definition_draft_objects(space_id,definition_id,path,object_id) VALUES(?,?,?,?)", space.ID, skill, asset.Path, file.ID)
	exec("INSERT INTO definition_version_objects(space_id,version_id,path,object_id) VALUES(?,?,?,?)", space.ID, skillVersion, asset.Path, file.ID)
	exec("INSERT INTO definition_draft_objects(space_id,definition_id,path,object_id) VALUES(?,?,?,?)", space.ID, agent, asset.Path, file.ID)
	exec("INSERT INTO definition_version_objects(space_id,version_id,path,object_id) VALUES(?,?,?,?)", space.ID, agentVersion, asset.Path, file.ID)
	exec("INSERT INTO definition_dependencies(space_id,version_id,ordinal,skill_id,skill_version_id) VALUES(?,?,0,?,?)", space.ID, agentVersion, skill, skillVersion)
	exec("UPDATE definitions SET default_version_id=? WHERE space_id=? AND id=?", agentVersion, space.ID, agent)
	session := uuid.NewString()
	exec("INSERT INTO sessions(space_id,id,actor_id,definition_id,version_id) VALUES(?,?,?,?,?)", space.ID, session, actor.ID, agent, agentVersion)

	if err = db.MigrateTo(ctx, 14); err != nil {
		t.Fatal(err)
	}
	items, err := db.Store.CatalogList(ctx, actor.ID, space.ID, "skill", identity.Query{Limit: 10})
	if err != nil || len(items) != 0 {
		t.Fatal("legacy Skills were retained", items, err)
	}
	if _, err = content.Repo.Object(ctx, actor.ID, space.ID, file.ID, true); !errors.Is(err, identity.ErrNotFound) {
		t.Fatal("legacy asset still has a catalog reference", err)
	}
	draft, err := svc.Draft(ctx, actor.ID, space.ID, "agent", agent)
	if err != nil || draft.DefaultVersionID != agentVersion || draft.Document.Markdown != "# Legacy Agent" || draft.Document.Assets != nil {
		t.Fatal("legacy Agent draft or default changed", draft, err)
	}
	v, err := svc.Version(ctx, actor.ID, space.ID, "agent", agent, agentVersion)
	if err != nil || v.Bundle.Document.Markdown != "# Legacy Agent" || v.Bundle.Document.Assets != nil {
		t.Fatal("legacy Agent version changed", v, err)
	}
	var sessions int
	if err := db.Admin.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM sessions WHERE space_id=? AND id=?", space.ID, session).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatal("legacy session survived the schema 13 reset", sessions, err)
	}
	newSkill, err := svc.Create(ctx, actor.ID, space.ID, "skill", "legacy")
	if err != nil || newSkill.ID == skill || newSkill.ContentID == "" {
		t.Fatal("could not create a fresh Skill under the old name", newSkill, err)
	}
}
