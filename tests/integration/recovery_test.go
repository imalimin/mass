package integration

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/adapter/secrets"
	"github.com/imalimin/mass/internal/adapter/tos"
	"github.com/imalimin/mass/internal/adapter/workspace"
	"github.com/imalimin/mass/internal/app/catalog"
	"github.com/imalimin/mass/internal/app/identity"
	sessionapp "github.com/imalimin/mass/internal/app/session"
	"github.com/imalimin/mass/internal/bootstrap"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
	"github.com/imalimin/mass/internal/resource"
	"github.com/imalimin/mass/internal/resource/trace"
	"github.com/imalimin/mass/internal/transport/agui"
	"github.com/imalimin/mass/tests/testdb"
	"go.yaml.in/yaml/v4"
)

// TestTOSRecovery uses the delivered binary and an empty restored database.
// Real mode changes only TOS. Model credentials/endpoint are synthetic loopback
// values; existing paid-model environment variables are never passed to mass.
func TestTOSRecovery(t *testing.T) {
	if os.Getenv("MASS_TEST_RECOVERY") != "1" {
		t.Skip("use make test-recovery or explicitly authorized make smoke-recovery")
	}
	if os.Getenv("MASS_TEST_ADMIN_DSN") == "" {
		t.Fatal("isolated MASS_TEST_ADMIN_DSN required")
	}
	real := os.Getenv("MASS_TEST_RECOVERY_TOS") == "1"
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	binary, template, build, archiveHash := recoveryBinary(t)
	endpoint, region, bucket, access, secret := "", "offline-fixture", "offline-fixture", "offline-fixture", "offline-fixture"
	if real {
		names := []string{"MASS_TOS_ENDPOINT", "MASS_TOS_REGION", "MASS_TOS_BUCKET", "MASS_TOS_ACCESS_KEY", "MASS_TOS_SECRET_KEY"}
		values := make([]string, len(names))
		for i, name := range names {
			values[i] = os.Getenv(name)
			if values[i] == "" {
				t.Fatalf("missing %s", name)
			}
		}
		endpoint, region, bucket, access, secret = values[0], values[1], values[2], values[3], values[4]
	} else {
		endpoint = recoveryTOSFixture(t)
	}
	provider, providerCalls := recoveryModelFixture(t)
	prefix := "tmp/mass-d10-" + uuid.NewString() + "/"
	sourcePrefix, targetPrefix := prefix+"source/", prefix+"restored/"
	blobs, err := tos.New(endpoint, region, bucket, access, secret)
	if err != nil {
		t.Fatal(err)
	}
	defer blobs.Client.Close()
	ledger := &recoveryLedger{Store: blobs, Prefix: prefix, Entries: map[string]*recoveryEntry{}, Path: recoveryPath(t, "d10-recovery-"+strings.TrimSuffix(strings.TrimPrefix(prefix, "tmp/"), "/")+".json")}
	var source, target *testdb.Database
	var manager *workspace.Manager
	var closeRuntime func(context.Context) error
	var server *exec.Cmd
	var serverExit chan error
	var logFile *os.File
	report := map[string]any{"at": time.Now().UTC().Format(time.RFC3339), "real_tos": real, "real_model_requests": 0, "prefix": prefix, "archive_sha256": archiveHash, "build": build, "model": "loopback fixture"}
	reportPath := recoveryPath(t, "d10-recovery-local.json")
	if real {
		reportPath = recoveryPath(t, "d10-recovery-real.json")
	}
	// Cleanup runs before any database is dropped, including a fatal assertion.
	// Keep a durable exact-key ledger if a remote cleanup cannot finish.
	defer func() {
		stop, c := context.WithTimeout(context.Background(), 4*time.Minute)
		defer c()
		if server != nil {
			_ = server.Process.Signal(syscall.SIGTERM)
			select {
			case e := <-serverExit:
				if e != nil {
					t.Error("restored service did not stop cleanly")
				}
			case <-time.After(12 * time.Second):
				_ = server.Process.Kill()
				<-serverExit
				t.Error("restored service shutdown timed out")
			}
		}
		if closeRuntime != nil {
			if e := closeRuntime(stop); e != nil {
				t.Error(e)
			}
		}
		if manager != nil {
			_ = manager.Close()
		}
		for _, db := range []*testdb.Database{source, target} {
			if db != nil {
				var tables int
				e := db.Admin.DB.QueryRowContext(stop, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='content_objects'").Scan(&tables)
				if e != nil {
					t.Error("cleanup inventory unavailable")
				} else if tables > 0 {
					if e = ledger.inventory(stop, db); e != nil {
						t.Error(e)
					}
				}
			}
		}
		if e := ledger.remove(stop, ledger.keys(prefix)); e != nil {
			t.Error(e)
		}
		allAbsent := true
		for _, e := range ledger.Entries {
			allAbsent = allAbsent && e.Absent
		}
		report["cleanup_all_absent"], report["unique_test_keys"], report["deleted_object_versions"], report["fixture_model_http_requests"] = allAbsent, len(ledger.Entries), ledger.Deleted, providerCalls.Load()
		report["passed"] = !t.Failed()
		raw, e := json.MarshalIndent(report, "", "  ")
		if e == nil {
			e = os.WriteFile(reportPath, append(raw, '\n'), 0600)
		}
		if e != nil {
			t.Error(e)
		}
		if source != nil {
			source.Close()
		}
		if target != nil {
			target.Close()
		}
		if logFile != nil {
			_ = logFile.Close()
		}
		t.Logf("D10 recovery cleanup: %d exact keys, %d object versions deleted, absence confirmed=%t; real model requests=0", len(ledger.Entries), ledger.Deleted, allAbsent)
	}()
	if err = blobs.Check(ctx); err != nil {
		t.Fatal(err)
	}
	source, err = testdb.NewVersion(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"PATH": "", "MASS_MARIADB_DSN": source.DSN, "MASS_MARIADB_MIGRATION_DSN": source.MigrationDSN, "MASS_TOS_ENDPOINT": endpoint, "MASS_TOS_REGION": region, "MASS_TOS_BUCKET": bucket, "MASS_TOS_ACCESS_KEY": access, "MASS_TOS_SECRET_KEY": secret, "MASS_MASTER_KEY": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{4}, 32))}
	cfg, err := bootstrap.ParseConfig(template)
	if err != nil {
		t.Fatal(err)
	}
	cfg.TOS.Prefix = sourcePrefix
	cfg.Runtime.WorkspaceTempRoot = filepath.Join(t.TempDir(), "source-workspaces")
	cfg.Runtime.SnapshotTimeout = 30 * time.Second
	sourcePath := filepath.Join(t.TempDir(), "source.yaml")
	recoveryConfig(t, sourcePath, cfg)
	cli := func(config string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, binary, append(args, "--config", config)...)
		cmd.Env = recoveryEnv(env)
		out, e := cmd.CombinedOutput()
		return out, e
	}
	if out, e := cli(sourcePath, "migrate"); e != nil {
		t.Fatalf("clean source migration failed: %s", out)
	}
	if out, e := cli(sourcePath, "migrate"); e != nil {
		t.Fatalf("repeated migration failed: %s", out)
	}
	delete(env, cfg.Database.MigrationDSNEnv)
	cipher, _ := secrets.New(bytes.Repeat([]byte{4}, 32))
	auth, _ := identity.New(source.Store, cipher, time.Hour, []byte("recovery"))
	actor, err := auth.CreateUser(ctx, "alice", "correct-password-alice")
	if err != nil {
		t.Fatal(err)
	}
	space, err := auth.CreateSpace(ctx, actor.ID, "D10 cloud recovery")
	if err != nil {
		t.Fatal(err)
	}
	model, err := auth.SaveModel(ctx, actor.ID, space.ID, "", identity.Model{Name: "recovery fixture", Provider: "ark", BaseURL: provider, ModelID: "loopback-fixture", APIKey: "recovery-fixture-key", ContextWindow: 32768, MaxOutputTokens: 1024, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	content := &resource.ContentStore{Repo: source.Store, Blobs: ledger, Prefix: sourcePrefix, MaxBytes: 32 << 20}
	catalogue := &catalog.Service{Repo: source.Store, Content: content}
	skill, err := catalogue.Create(ctx, actor.ID, space.ID, "skill", "retained-rules")
	if err != nil {
		t.Fatal(err)
	}
	skill, err = catalogue.Save(ctx, actor.ID, space.ID, "skill", skill.ID, skill.Revision, catalog.Document{Markdown: "# Retained rules", Assets: []catalog.Asset{}})
	if err != nil {
		t.Fatal(err)
	}
	skill, err = catalogue.Upload(ctx, actor.ID, space.ID, "skill", skill.ID, "assets/rules.txt", "text/plain", skill.Revision, strings.NewReader("Retained rule bytes"))
	if err != nil {
		t.Fatal(err)
	}
	skillVersion, err := catalogue.Publish(ctx, actor.ID, space.ID, "skill", skill.ID, "v1", skill.Revision, false)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := catalogue.Create(ctx, actor.ID, space.ID, "agent", "recovery-agent")
	if err != nil {
		t.Fatal(err)
	}
	definition := catalog.Config{SchemaVersion: 1, Name: agent.Name, Model: model.ID, Skills: []catalog.SkillRef{{ID: skill.ID, Version: "v1"}}, Tools: []string{"write_file", "read_file", "publish_artifact"}}
	definition.Context.MaxInputTokens = 24000
	definition.Context.MaxTokens = 512
	definition.Context.Compression = "summary"
	definition.Limits.MaxSteps = 4
	definition.Limits.TimeoutSeconds = 180
	raw, _ := json.Marshal(definition)
	agent, err = catalogue.Save(ctx, actor.ID, space.ID, "agent", agent.ID, agent.Revision, catalog.Document{Markdown: "Preserve recovery files.", ConfigYAML: string(raw)})
	if err != nil {
		t.Fatal(err)
	}
	version, err := catalogue.Publish(ctx, actor.ID, space.ID, "agent", agent.ID, "v1", agent.Revision, false)
	if err != nil {
		t.Fatal(err)
	}
	manager, err = workspace.New(cfg.Runtime.WorkspaceTempRoot, storage.Limits{MaxArchiveFileBytes: 1 << 20, MaxTotalBytes: 8 << 20})
	if err != nil {
		t.Fatal(err)
	}
	j := &journal.Journal{Repo: source.Store}
	snapshots := &resource.SnapshotStore{Repo: source.Store, Content: content, Journal: j, Workspaces: manager, Timeout: 30 * time.Second}
	fixture := &faultModel{entered: make(chan string, 1)}
	options, lifecycle, err := bootstrap.StartRuntime(ctx, cfg, source.Store, auth, catalogue, content, manager, snapshots, "D10-recovery", slog.New(slog.NewTextHandler(io.Discard, nil)), fixture)
	if err != nil {
		t.Fatal(err)
	}
	closeRuntime = lifecycle.Close
	session, err := options.Service.Create(ctx, actor.ID, space.ID, sessionapp.CreateInput{Key: uuid.NewString(), AgentID: agent.ID, VersionID: version.ID, Title: "recovered session"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := options.Service.Submit(ctx, actor.ID, space.ID, session.ID, sessionapp.SubmitInput{Key: uuid.NewString(), Text: "create retained artifact"})
	if err != nil {
		t.Fatal(err)
	}
	first = recoveryWait(t, ctx, source, actor.ID, space.ID, first.ID)
	if first.Status != "succeeded" {
		t.Fatal("source fixture failed", first.Error, first.SnapshotError)
	}
	for !first.TraceUploaded {
		select {
		case <-ctx.Done():
			t.Fatal("source trace was not uploaded", ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
		first, err = source.Store.RuntimeRun(ctx, actor.ID, space.ID, first.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	before, err := options.UI.CatchUp(ctx, actor.ID, space.ID, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := source.Store.RuntimeArtifacts(ctx, actor.ID, space.ID, first.ID)
	if err != nil || len(artifacts) != 1 {
		t.Fatal("source artifact missing", err)
	}
	if err = closeRuntime(ctx); err != nil {
		t.Fatal(err)
	}
	closeRuntime = nil
	if err = manager.Close(); err != nil {
		t.Fatal(err)
	}
	manager = nil
	// Back up real failed ContentStore operations, rather than manufacturing an
	// uploading row. A new-ID retry succeeds but must not erase the old intent.
	failedContent := *content
	failedContent.Blobs = &rejectedRecoveryUpload{BlobStore: ledger}
	missing, e := failedContent.Put(ctx, actor.ID, space.ID, uuid.NewString(), "trace", "text/plain", strings.NewReader("failed upload"))
	if e == nil || missing.State != "uploading" {
		t.Fatal("failed upload did not leave an intent", e)
	}
	if _, err = source.Admin.DB.Exec("CREATE TRIGGER reject_ready BEFORE UPDATE ON content_objects FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='injected ready failure'"); err != nil {
		t.Fatal(err)
	}
	uploaded, e := content.Put(ctx, actor.ID, space.ID, uuid.NewString(), "trace", "text/plain", strings.NewReader("uploaded before SQL failure"))
	if e == nil || uploaded.State != "uploading" {
		t.Fatal("ready failure did not retain intent", e)
	}
	if _, err = source.Admin.DB.Exec("DROP TRIGGER reject_ready"); err != nil {
		t.Fatal(err)
	}
	if _, err = content.Put(ctx, actor.ID, space.ID, uuid.NewString(), "trace", "text/plain", strings.NewReader("successful fresh retry")); err != nil {
		t.Fatal(err)
	}
	pending := []storage.Object{missing, uploaded}
	if err = ledger.inventory(ctx, source); err != nil {
		t.Fatal(err)
	}
	target, err = testdb.NewVersion(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	var tables int
	if err = target.Admin.DB.QueryRow("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE()").Scan(&tables); err != nil || tables != 0 {
		t.Fatal("restore requires an empty database", err)
	}
	backup := recoverySQLBackup(t, ctx, source, target)
	report["database_dump_sha256"] = recoveryHash(backup)
	env["MASS_RECOVERY_DSN"] = target.DSN
	targetConfig := cfg
	targetConfig.Database.DSNEnv = "MASS_RECOVERY_DSN"
	targetConfig.TOS.Prefix = targetPrefix
	targetConfig.Runtime.WorkspaceTempRoot = filepath.Join(t.TempDir(), "restored-workspaces")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	origin := "http://" + listener.Addr().String()
	_ = listener.Close()
	targetConfig.HTTP.Listen = strings.TrimPrefix(origin, "http://")
	targetConfig.HTTP.PublicURL = origin
	targetPath := filepath.Join(t.TempDir(), "restored.yaml")
	recoveryConfig(t, targetPath, targetConfig)
	objects, err := source.Admin.DB.Query("SELECT space_id,id,object_key,tos_uri FROM content_objects WHERE state='ready' ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.register(trace.Key(targetPrefix, space.ID, session.ID), nil); err != nil {
		objects.Close()
		t.Fatal(err)
	}
	var targetKeys []string
	for objects.Next() {
		var spaceID, id, key, uri string
		if err = objects.Scan(&spaceID, &id, &key, &uri); err != nil {
			objects.Close()
			t.Fatal(err)
		}
		next := targetPrefix + spaceID + "/objects/" + id
		if err = ledger.register(next, nil); err != nil {
			objects.Close()
			t.Fatal(err)
		}
		targetKeys = append(targetKeys, next)
	}
	err = objects.Err()
	objects.Close()
	if err != nil || len(targetKeys) == 0 {
		t.Fatal("empty source inventory", err)
	}
	report["restored_objects"] = len(targetKeys)
	restore := func() ([]byte, error) {
		return cli(targetPath, "restore-storage", "--source-config", sourcePath, "--expect-database", target.Name)
	}
	// Seed only an owned, not-yet-created destination key with wrong bytes. The
	// immutable destination must be rejected without changing any SQL mappings.
	badKey := targetKeys[0]
	if _, err = ledger.PutNew(ctx, badKey, strings.NewReader("corrupt recovery fixture"), 24, "text/plain"); err != nil {
		t.Fatal(err)
	}
	if _, err = restore(); err == nil {
		t.Fatal("corrupt target accepted")
	}
	var changed int
	if err = target.Admin.DB.QueryRow("SELECT COUNT(*) FROM content_objects WHERE object_key LIKE ?", targetPrefix+"%").Scan(&changed); err != nil || changed != 0 {
		t.Fatal("failed restore changed locations", err)
	}
	if err = ledger.remove(ctx, []string{badKey}); err != nil {
		t.Fatal(err)
	}
	if out, e := restore(); e != nil {
		t.Fatalf("restore-storage failed: %s", out)
	} else if !strings.Contains(string(out), "preserved 2 unreferenced uploading intents (not copied)") {
		t.Fatalf("restore did not report preserved intents: %s", out)
	}
	if err = ledger.inventory(ctx, target); err != nil {
		t.Fatal(err)
	}
	if err = target.Admin.DB.QueryRow("SELECT COUNT(*) FROM content_objects WHERE state='ready' AND object_key NOT LIKE ?", targetPrefix+"%").Scan(&changed); err != nil || changed != 0 {
		t.Fatal("object location not relocated", err)
	}
	for _, want := range pending {
		var state, key, uri, hash string
		if err = target.Admin.DB.QueryRow("SELECT state,object_key,tos_uri,sha256 FROM content_objects WHERE space_id=? AND id=?", want.SpaceID, want.ID).Scan(&state, &key, &uri, &hash); err != nil || state != "uploading" || key != want.Key || uri != want.URI || hash != want.SHA256 {
			t.Fatal("restore altered upload intent", err)
		}
		r, e := blobs.Open(ctx, targetPrefix+want.SpaceID+"/objects/"+want.ID, "")
		if e == nil {
			r.Close()
			t.Fatal("unconfirmed upload was copied")
		}
	}
	report["preserved_unreferenced_uploads"] = len(pending)
	if err = target.Admin.DB.QueryRow("SELECT COUNT(*) FROM workspace_snapshots s JOIN content_objects o ON o.space_id=s.space_id AND o.id=s.object_id WHERE s.tos_uri<>o.tos_uri").Scan(&changed); err != nil || changed != 0 {
		t.Fatal("snapshot mapping mismatch", err)
	}
	if err = ledger.remove(ctx, ledger.keys(sourcePrefix)); err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(cfg.Runtime.WorkspaceTempRoot); err != nil {
		t.Fatal(err)
	}
	if out, e := restore(); e != nil {
		t.Fatalf("idempotent restore depended on removed source: %s", out)
	}

	after, err := (&agui.Projector{Repo: target.Store, Live: target.Store.LiveUI}).CatchUp(ctx, actor.ID, space.ID, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(before.State)
	b, _ := json.Marshal(after.State)
	if !bytes.Equal(a, b) {
		t.Fatal("restored UI history changed")
	}
	restoredRun, err := target.Store.RuntimeRun(ctx, actor.ID, space.ID, first.ID)
	if err != nil || !restoredRun.TraceUploaded {
		t.Fatal("restored trace metadata missing", err)
	}
	archive := &trace.Archive{Root: t.TempDir(), Prefix: targetPrefix, Blobs: ledger}
	if page, err := archive.Read(ctx, restoredRun, 0, nil, 100); err != nil || len(page.Items) < 2 {
		t.Fatal("restored trace missing", err)
	}
	report["history_rebuild_without_execution"] = providerCalls.Load() == 0
	if providerCalls.Load() != 0 {
		t.Fatal("history restore called the model")
	}
	logFile, err = os.Create(recoveryPath(t, "d10-recovery-server.log"))
	if err != nil {
		t.Fatal(err)
	}
	server = exec.Command(binary, "serve", "--config", targetPath)
	server.Env = recoveryEnv(env)
	server.Stdout = logFile
	server.Stderr = logFile
	if err = server.Start(); err != nil {
		server = nil
		t.Fatal(err)
	}
	serverExit = make(chan error, 1)
	go func() { serverExit <- server.Wait() }()
	client := &http.Client{Timeout: 10 * time.Second}
	jar, _ := cookiejar.New(nil)
	client.Jar = jar
	for deadline := time.Now().Add(15 * time.Second); ; {
		response, e := client.Get(origin + "/health/ready")
		if e == nil {
			response.Body.Close()
			if response.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("restored service is not ready")
		}
		time.Sleep(50 * time.Millisecond)
	}
	request := func(method, path string, data any) []byte {
		t.Helper()
		var body io.Reader
		if data != nil {
			raw, _ := json.Marshal(data)
			body = bytes.NewReader(raw)
		}
		req, e := http.NewRequestWithContext(ctx, method, origin+path, body)
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("Origin", origin)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		response, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer response.Body.Close()
		raw, e := io.ReadAll(io.LimitReader(response.Body, 4<<20))
		if e != nil || response.StatusCode >= 300 {
			t.Fatalf("restored API %s returned %d", path, response.StatusCode)
		}
		return raw
	}
	request("POST", "/api/v1/auth/login", map[string]string{"username": "alice", "password": "correct-password-alice"})
	base := "/api/v1/spaces/" + space.ID
	request("GET", base+"/agents/"+agent.ID+"/versions/"+version.ID, nil)
	request("GET", base+"/skills/"+skill.ID+"/versions/"+skillVersion.ID, nil)
	if string(request("GET", base+"/artifacts/"+artifacts[0].ID+"/download", nil)) != "retained D09" {
		t.Fatal("restored artifact bytes changed")
	}
	request("GET", base+"/runs/"+first.ID+"/trace", nil)
	request("GET", base+"/runs/"+first.ID+"/ui-snapshot", nil)
	if providerCalls.Load() != 0 {
		t.Fatal("history API replay called model")
	}
	raw = request("POST", base+"/sessions/"+session.ID+"/runs", map[string]string{"idempotency_key": uuid.NewString(), "text": "read restored retained.txt"})
	var next run.View
	if err = json.Unmarshal(raw, &next); err != nil {
		t.Fatal(err)
	}
	next = recoveryWait(t, ctx, target, actor.ID, space.ID, next.ID)
	if next.Status != "succeeded" || providerCalls.Load() != 2 {
		t.Fatal("restored continuation failed", next.Error, providerCalls.Load())
	}
	if !bytes.Contains(request("GET", base+"/runs/"+next.ID+"/ui-snapshot", nil), []byte("Recovered retained D09 from TOS snapshot")) {
		t.Fatal("restored continuation text missing")
	}
	report["continued_from_restored_snapshot"] = true
	report["immutable_collision_rejected"] = true
	report["source_removed"] = true
	report["location_mapping_verified"] = true
	t.Logf("D10 TOS recovery passed: %d objects, isolated SQL dump/restore, immutable collision rejected, index remapping, source deletion, history/Trace/artifact rebuild, delivered binary continuation; real TOS=%t", len(targetKeys), real)
}

type rejectedRecoveryUpload struct{ storage.BlobStore }

func (*rejectedRecoveryUpload) PutNew(context.Context, string, io.Reader, int64, string) (storage.BlobInfo, error) {
	return storage.BlobInfo{}, errors.New("injected upload outage")
}
func (*rejectedRecoveryUpload) Open(context.Context, string, string) (io.ReadCloser, error) {
	return nil, errors.New("injected upload outage")
}

func recoveryWait(t *testing.T, ctx context.Context, db *testdb.Database, actor, space, id string) run.View {
	t.Helper()
	for deadline := time.Now().Add(180 * time.Second); time.Now().Before(deadline); {
		v, e := db.Store.RuntimeRun(ctx, actor, space, id)
		if e != nil {
			t.Fatal(e)
		}
		if run.Terminal(v.Status) {
			return v
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("recovery run timeout")
	return run.View{}
}
func recoveryConfig(t *testing.T, path string, c bootstrap.Config) {
	t.Helper()
	raw, e := yaml.Marshal(c)
	if e == nil {
		e = os.WriteFile(path, raw, 0600)
	}
	if e != nil {
		t.Fatal(e)
	}
}
func recoveryEnv(values map[string]string) []string {
	env := make([]string, 0, len(values))
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	return env
}
func recoveryHash(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func recoverySQLBackup(t *testing.T, ctx context.Context, source, target *testdb.Database) []byte {
	t.Helper()
	client, dump := os.Getenv("MASS_TEST_MARIADB_CLIENT"), os.Getenv("MASS_TEST_MARIADB_DUMP")
	if client == "" {
		client = "mariadb"
	}
	if dump == "" {
		dump = "mariadb-dump"
	}
	options := func(db *testdb.Database) string {
		cfg, e := mysql.ParseDSN(db.MigrationDSN)
		if e != nil {
			t.Fatal(e)
		}
		quote := func(s string) string {
			return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`).Replace(s) + `"`
		}
		text := "[client]\nuser=" + quote(cfg.User) + "\npassword=" + quote(cfg.Passwd) + "\n"
		if cfg.Net == "unix" {
			text += "protocol=socket\nsocket=" + quote(cfg.Addr) + "\n"
		} else if cfg.Net == "tcp" {
			host, port, e := net.SplitHostPort(cfg.Addr)
			if e != nil {
				t.Fatal(e)
			}
			text += "protocol=tcp\nhost=" + quote(host) + "\nport=" + port + "\n"
		} else {
			t.Fatal("unsupported test client network")
		}
		path := filepath.Join(t.TempDir(), "client.cnf")
		if e = os.WriteFile(path, []byte(text), 0600); e != nil {
			t.Fatal(e)
		}
		return path
	}
	cmd := exec.CommandContext(ctx, dump, "--defaults-file="+options(source), "--single-transaction", "--hex-blob", "--skip-add-drop-table", "--skip-add-locks", "--skip-comments", "--skip-dump-date", "--no-tablespaces", source.Name)
	backup, e := cmd.Output()
	if e != nil {
		t.Fatal("isolated SQL dump failed")
	}
	cmd = exec.CommandContext(ctx, client, "--defaults-file="+options(target), target.Name)
	cmd.Stdin = bytes.NewReader(backup)
	if e = cmd.Run(); e != nil {
		t.Fatal("isolated SQL import failed")
	}
	return backup
}
func recoveryBinary(t *testing.T) (string, []byte, map[string]any, string) {
	t.Helper()
	path := recoveryPath(t, "release/mass-"+runtime.GOOS+"-"+runtime.GOARCH+".tar.gz")
	archive, e := os.ReadFile(path)
	if e != nil {
		t.Fatal("make release is required")
	}
	gz, e := gzip.NewReader(bytes.NewReader(archive))
	if e != nil {
		t.Fatal(e)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	contents := map[string][]byte{}
	for {
		header, e := reader.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		if header.Name != "bin/mass" && header.Name != "mass.yaml" && header.Name != "manifest.json" && header.Name != "build-info.json" {
			continue
		}
		if header.Size < 0 || header.Size > 128<<20 {
			t.Fatal("invalid release entry")
		}
		raw, e := io.ReadAll(reader)
		if e != nil {
			t.Fatal(e)
		}
		contents[header.Name] = raw
	}
	var manifest struct {
		SHA256 map[string]string `json:"sha256"`
	}
	if e = json.Unmarshal(contents["manifest.json"], &manifest); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"bin/mass", "mass.yaml", "build-info.json"} {
		if len(contents[name]) == 0 || recoveryHash(contents[name]) != manifest.SHA256[name] {
			t.Fatal("release checksum mismatch", name)
		}
	}
	var build map[string]any
	if e = json.Unmarshal(contents["build-info.json"], &build); e != nil {
		t.Fatal(e)
	}
	binary := filepath.Join(t.TempDir(), "mass")
	if e = os.WriteFile(binary, contents["bin/mass"], 0700); e != nil {
		t.Fatal(e)
	}
	return binary, contents["mass.yaml"], build, recoveryHash(archive)
}
