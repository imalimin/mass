package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/adapter/mariadb"
	"github.com/imalimin/mass/internal/adapter/secrets"
	"github.com/imalimin/mass/internal/adapter/workspace"
	"github.com/imalimin/mass/internal/app/catalog"
	"github.com/imalimin/mass/internal/app/identity"
	sessionapp "github.com/imalimin/mass/internal/app/session"
	"github.com/imalimin/mass/internal/bootstrap"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
	"github.com/imalimin/mass/internal/resource"
	transport "github.com/imalimin/mass/internal/transport/http"
	"github.com/imalimin/mass/tests/fakes"
	"github.com/imalimin/mass/tests/testdb"
)

type faultBlobs struct {
	*fakes.Blobs
	rejectOctetStream atomic.Bool
	lostUpload        atomic.Bool
}

func (b *faultBlobs) PutNew(ctx context.Context, key string, src io.Reader, n int64, media string) (storage.BlobInfo, error) {
	if b.rejectOctetStream.Load() && media == "application/octet-stream" {
		return storage.BlobInfo{}, errors.New("injected wire storage outage")
	}
	info, err := b.Blobs.PutNew(ctx, key, src, n, media)
	if err == nil && b.lostUpload.Swap(false) {
		return storage.BlobInfo{}, errors.New("injected lost upload acknowledgement")
	}
	return info, err
}

type faultModel struct {
	retries sync.Map
	calls   atomic.Int64
	entered chan string
	exited  chan struct{}
}

func (*faultModel) Source() string { return "fake" }
func (m *faultModel) Stream(ctx context.Context, snapshot run.Snapshot, p run.Prepared, _ string, delta func(string) error) (run.Reply, error) {
	m.calls.Add(1)
	task := ""
	last := 0
	for i, msg := range p.Messages {
		if msg.Role == "user" && !strings.HasPrefix(msg.Content, "[MASS") {
			task = msg.Content
			last = i
		}
	}

	if endpoint, ok := strings.CutPrefix(task, "code while space locked "); ok && len(p.Messages[last+1:]) == 0 {
		raw, _ := json.Marshal(run.CodeInput{
			Language: "javascript", Input: json.RawMessage("null"),
			Code: "net.request({url:" + strconv.Quote(endpoint) + "});fs.mkdir('first/second');" +
				"fs.writeFile('first/second/finished.txt','ok',{expected_sha256:'missing'});return true;",
		})
		return run.Reply{Message: run.Message{Role: "assistant", ToolCalls: []run.Call{{ID: "local-file-tool", Name: "run_code_js", Arguments: string(raw)}}}, FinishReason: "tool_calls"}, nil
	}
	if request, ok := strings.CutPrefix(task, "code network deadline "); ok {
		for _, msg := range p.Messages[last+1:] {
			if msg.Role == "tool" {
				var result run.ToolResult
				if err := json.Unmarshal([]byte(msg.Content), &result); err != nil || result.Error != "code_timeout" || result.SideEffect != "none" {
					return run.Reply{}, errors.New("agent did not receive the code timeout")
				}
				return run.Reply{Message: run.Message{Role: "assistant", Content: "Handled tool timeout"}, FinishReason: "stop"}, nil
			}
		}
		method, endpoint, _ := strings.Cut(request, " ")
		raw, _ := json.Marshal(run.CodeInput{
			Language: "javascript", Input: json.RawMessage("null"),
			Code: "net.request({method:" + strconv.Quote(method) + ",url:" + strconv.Quote(endpoint) + "});return true;",
		})
		return run.Reply{Message: run.Message{Role: "assistant", ToolCalls: []run.Call{{ID: "network-deadline", Name: "run_code_js", Arguments: string(raw)}}}, FinishReason: "tool_calls"}, nil
	}

	if p.Spec.Purpose == "task" && strings.Contains(task, "retry") {
		if _, seen := m.retries.LoadOrStore(snapshot.ID, true); !seen {
			input := int64(100)
			return run.Reply{Usage: run.Usage{Input: &input}}, &run.ModelFailure{Code: "model_http_failed", Status: 503, Retryable: true}
		}
	}
	if strings.Contains(task, "wait") {
		if strings.Contains(task, "partial") {
			if err := delta("D09 partial output"); err != nil {
				return run.Reply{}, err
			}
		}
		select {
		case m.entered <- task:
		default:
		}
		<-ctx.Done()
		select {
		case m.exited <- struct{}{}:
		default:
		}
		return run.Reply{}, ctx.Err()
	}

	if strings.Contains(task, "code revocation") {
		return run.Reply{Message: run.Message{Role: "assistant", ToolCalls: []run.Call{{ID: "revocation-code", Name: "run_code_js", Arguments: `{"language":"javascript","input":null,"code":"fs.writeFile('revoked.txt','kept',{expected_sha256:'missing'});while(true){}"}`}}}, FinishReason: "tool_calls"}, nil
	}
	if strings.Contains(task, "artifact") {
		count := 0
		for _, msg := range p.Messages[last+1:] {
			if msg.Role == "tool" {
				count++
			}
		}
		sum := sha256.Sum256([]byte("retained D09"))
		name, args := "", ""
		if count == 0 {
			name = "write_file"
			args = `{"path":"retained.txt","content":"retained D09","expected_sha256":"missing","media_type":"text/plain"}`
		}
		if count == 1 {
			name = "publish_artifact"
			raw, _ := json.Marshal(map[string]string{"path": "retained.txt", "expected_sha256": hex.EncodeToString(sum[:]), "display_name": "retained.txt", "media_type": "text/plain"})
			args = string(raw)
		}
		if name != "" {
			return run.Reply{Message: run.Message{Role: "assistant", ToolCalls: []run.Call{{ID: "reused-native-id", Name: name, Arguments: args}}}, FinishReason: "tool_calls"}, nil
		}
	}
	if err := delta("D09 complete"); err != nil {
		return run.Reply{}, err
	}
	input, output := int64(100), int64(10)
	return run.Reply{Message: run.Message{Role: "assistant", Content: "D09 complete"}, FinishReason: "stop", Usage: run.Usage{Input: &input, Output: &output}}, nil
}

type faultRig struct {
	t                            *testing.T
	db                           *testdb.Database
	actor, space, agent, version string
	manager                      *workspace.Manager
	blobs                        *faultBlobs
	model                        *faultModel
	content                      *resource.ContentStore
	auth                         *identity.Service
	runtime                      *transport.RuntimeOptions
	close                        func(context.Context) error
}

func newFaultRig(t *testing.T) *faultRig {
	return newFaultRigWithTimeout(t, 3)
}

func newFaultRigWithTimeout(t *testing.T, timeoutSeconds int) *faultRig {
	t.Helper()
	if os.Getenv("MASS_TEST_ADMIN_DSN") == "" {
		t.Skip("requires isolated MariaDB")
	}
	ctx := context.Background()
	db, err := testdb.NewVersion(ctx, 15)
	if err != nil {
		t.Fatal(err)
	}
	r := &faultRig{t: t, db: db, blobs: &faultBlobs{Blobs: fakes.NewBlobs()}, model: &faultModel{entered: make(chan string, 16), exited: make(chan struct{}, 1)}}
	t.Cleanup(func() {
		if r.close != nil {
			c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = r.close(c)
		}
		if r.manager != nil {
			r.manager.Close()
		}
		db.Close()
	})
	r.bind()
	actor, err := r.auth.CreateUser(ctx, "alice", "correct-password-alice")
	if err != nil {
		t.Fatal(err)
	}
	r.actor = actor.ID
	space, err := r.auth.CreateSpace(ctx, r.actor, "D09 faults")
	if err != nil {
		t.Fatal(err)
	}
	r.space = space.ID
	model, err := r.auth.SaveModel(ctx, r.actor, r.space, "", identity.Model{Name: "fixture", Provider: "ark", BaseURL: identity.ArkEndpoint, ModelID: "fixture", APIKey: "D09-not-a-real-key", ContextWindow: 32768, MaxOutputTokens: 2048, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	catalogue := &catalog.Service{Repo: db.Store, Content: r.content}
	agent, err := catalogue.Create(ctx, r.actor, r.space, "agent", "fault-agent")
	if err != nil {
		t.Fatal(err)
	}
	r.agent = agent.ID
	cfg := catalog.Config{SchemaVersion: 1, Name: agent.Name, Model: model.ID, Tools: []string{"write_file", "read_file", "publish_artifact", "run_code_js"}, Skills: []catalog.SkillRef{}}
	cfg.Context.MaxInputTokens = 24000
	cfg.Context.MaxTokens = 512
	cfg.Context.Compression = "summary"
	cfg.Limits.MaxSteps = 4
	cfg.Limits.TimeoutSeconds = timeoutSeconds
	raw, _ := json.Marshal(cfg)
	agent, err = catalogue.Save(ctx, r.actor, r.space, "agent", r.agent, agent.Revision, catalog.Document{Markdown: "Test deterministic faults.", ConfigYAML: string(raw)})
	if err != nil {
		t.Fatal(err)
	}
	version, err := catalogue.Publish(ctx, r.actor, r.space, "agent", r.agent, "v1", agent.Revision, false)
	if err != nil {
		t.Fatal(err)
	}
	r.version = version.ID
	r.manager, err = workspace.New(t.TempDir(), storage.Limits{MaxArchiveFileBytes: 2 << 20, MaxTotalBytes: 8 << 20})
	if err != nil {
		t.Fatal(err)
	}
	r.start()
	return r
}
func (r *faultRig) bind() {
	cipher, _ := secrets.New(bytes.Repeat([]byte{4}, 32))
	r.auth, _ = identity.New(r.db.Store, cipher, time.Hour, []byte("D09-cursor"))
	r.content = &resource.ContentStore{Repo: r.db.Store, Blobs: r.blobs, Prefix: "test/d09/", MaxBytes: 32 << 20}
}
func (r *faultRig) start() {
	ctx := context.Background()
	j := &journal.Journal{Repo: r.db.Store}
	snapshots := &resource.SnapshotStore{Repo: r.db.Store, Content: r.content, Journal: j, Workspaces: r.manager, Timeout: 2 * time.Second}
	c := bootstrap.Config{}
	c.LLM.MaxAttempts = 2
	c.Runtime.MaxConcurrentRuns = 1
	c.Runtime.SnapshotTimeout = 2 * time.Second
	c.Storage.MaxObjectBytes = 2 << 20
	c.Storage.MaxWorkspaceBytes = 8 << 20

	runtime, lifecycle, err := bootstrap.StartRuntime(ctx, c, r.db.Store, r.auth, &catalog.Service{Repo: r.db.Store, Content: r.content}, r.content, r.manager, snapshots, "D09-test", slog.New(slog.NewTextHandler(io.Discard, nil)), r.model)
	if err != nil {
		r.t.Fatal(err)
	}
	r.runtime = runtime
	r.close = lifecycle.Close
}
func (r *faultRig) session() run.Session {
	s, err := r.runtime.Service.Create(context.Background(), r.actor, r.space, sessionapp.CreateInput{Key: uuid.NewString(), AgentID: r.agent, VersionID: r.version, Title: "fault"})
	if err != nil {
		r.t.Fatal(err)
	}
	return s
}
func (r *faultRig) submit(s run.Session, text string) run.View {
	v, err := r.runtime.Service.Submit(context.Background(), r.actor, r.space, s.ID, sessionapp.SubmitInput{Key: uuid.NewString(), Text: text})
	if err != nil {
		r.t.Fatal(err)
	}
	return v
}
func (r *faultRig) wait(id string) run.View {
	r.t.Helper()
	for i := 0; i < 800; i++ {
		v, err := r.db.Store.RuntimeRun(context.Background(), r.actor, r.space, id)
		if err != nil {
			r.t.Fatal(err)
		}
		if run.Terminal(v.Status) {
			return v
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.t.Fatal("terminal timeout")
	return run.View{}
}

func TestWorkspaceLocalFilesAcrossHosts(t *testing.T) {
	r := newFaultRigWithTimeout(t, 10)
	r.content.MaxBytes = 2 << 20
	ctx := context.Background()
	session := r.session()
	j := &journal.Journal{Repo: r.db.Store}
	const large = ".local.bin"
	runTask := func(text string, wantMissing []string) run.View {
		t.Helper()
		view := r.wait(r.submit(session, text).ID)
		if view.Status != "succeeded" {
			t.Fatalf("run failed: %+v", view)
		}
		events, err := resource.RunHistory(ctx, j, r.actor, r.space, session.ID, view.ID, "context.projected")
		if err != nil || len(events) != 1 {
			t.Fatalf("first model projection missing: %v, got %d", err, len(events))
		}
		var event journal.ProjectionData
		if err := journal.Decode(events[0].Data, &event); err != nil {
			t.Fatal(err)
		}
		var prepared run.Prepared
		if err := resource.ReadJSON(ctx, r.content, r.actor, r.space, event.ContentID, 4<<20, &prepared); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(prepared.Spec.MissingLocalFiles, wantMissing) {
			t.Fatalf("missing paths in model projection: got %v, want %v", prepared.Spec.MissingLocalFiles, wantMissing)
		}
		warned := slices.ContainsFunc(prepared.Messages, func(message run.Message) bool {
			return strings.HasPrefix(message.Content, "[MASS workspace:") && strings.Contains(message.Content, large)
		})
		if warned != (len(wantMissing) > 0) {
			t.Fatalf("unexpected missing-file notice: %+v", prepared.Messages)
		}
		return view
	}
	runTask("Create the workspace", nil)
	largeContents := bytes.Repeat([]byte("x"), 2<<20+1)
	if err := r.manager.With(ctx, r.space, session.ID, func(dir resource.WorkspaceDir) error {
		if err := dir.Root().WriteFile(large, largeContents, 0600); err != nil {
			return err
		}
		for _, name := range []string{"first.bin", "second.bin"} {
			chunk := make([]byte, 1536<<10)
			if _, err := rand.Read(chunk); err != nil {
				return err
			}
			if err := dir.Root().WriteFile(name, chunk, 0600); err != nil {
				return err
			}
		}
		return dir.Root().WriteFile(".small", []byte("portable"), 0600)
	}); err != nil {
		t.Fatal(err)
	}
	saved := runTask("Save the workspace", nil)
	snapshot, err := r.db.Store.Snapshot(ctx, r.actor, r.space, saved.WorkspaceSnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	object, err := r.content.Object(ctx, r.actor, r.space, snapshot.ObjectID)
	if err != nil || object.Size <= 2<<20 {
		t.Fatalf("expected a committed archive above the per-file threshold: %d bytes, %v", object.Size, err)
	}

	if err := r.close(ctx); err != nil {
		t.Fatal(err)
	}
	r.start()
	runTask("Check after the service restart", nil)
	if err := r.runtime.Workspaces.RestoreIdle(ctx, r.actor, r.space, session.ID, r.runtime.Snapshots); err != nil {
		t.Fatal(err)
	}
	if err := r.manager.With(ctx, r.space, session.ID, func(dir resource.WorkspaceDir) error {
		contents, err := dir.Root().ReadFile(large)
		if err != nil || !bytes.Equal(contents, largeContents) {
			t.Fatalf("same-host restore lost local bytes: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := r.close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.manager.Close(); err != nil {
		t.Fatal(err)
	}
	manager, err := workspace.New(t.TempDir(), storage.Limits{MaxArchiveFileBytes: 2 << 20, MaxTotalBytes: 8 << 20})
	if err != nil {
		t.Fatal(err)
	}
	r.manager = manager
	r.start()
	runTask("Check the replacement host", []string{large})
	runTask("Continue on the replacement host", []string{large})
	if err := r.manager.With(ctx, r.space, session.ID, func(dir resource.WorkspaceDir) error {
		contents, err := dir.Root().ReadFile(".small")
		if err != nil || string(contents) != "portable" {
			t.Fatalf("replacement host lost archived bytes: %v", err)
		}
		return dir.Root().WriteFile(large, []byte("small replacement"), 0600)
	}); err != nil {
		t.Fatal(err)
	}
	runTask("Check the replacement file", nil)
}
func (r *faultRig) entered() {
	r.t.Helper()
	select {
	case <-r.model.entered:
	case <-time.After(5 * time.Second):
		r.t.Fatal("model did not enter")
	}
}
func (r *faultRig) terminalOnce(v run.View) {
	r.t.Helper()
	var terminal, occupied int
	r.db.Admin.DB.QueryRow("SELECT COUNT(*) FROM runs WHERE id=? AND finished_at IS NOT NULL", v.ID).Scan(&terminal)
	r.db.Admin.DB.QueryRow("SELECT COUNT(*) FROM session_active_runs WHERE run_id=?", v.ID).Scan(&occupied)
	if terminal != 1 || occupied != 0 {
		r.t.Fatal("terminal/occupancy", terminal, occupied)
	}
}

func (r *faultRig) finishData(v run.View) journal.FinishData {
	r.t.Helper()
	page, err := r.db.Store.RuntimeTrace(context.Background(), r.actor, r.space, v.ID, 0, nil, 100)
	if err != nil {
		r.t.Fatal(err)
	}
	for _, event := range page.Events {
		if event.Type == "run.finished" {
			var data journal.FinishData
			if err := journal.Decode(event.Data, &data); err != nil {
				r.t.Fatal(err)
			}
			return data
		}
	}
	r.t.Fatal("missing run.finished event")
	return journal.FinishData{}
}

func TestFaultTimeoutAndTraceStorage(t *testing.T) {
	r := newFaultRig(t)
	t.Run("partial timeout", func(t *testing.T) {
		v := r.wait(r.submit(r.session(), "partial wait for deadline").ID)
		if v.Status != "failed" || v.Error != "run_timeout" {
			t.Fatal(v.Status, v.Error)
		}
		r.terminalOnce(v)
		detail := r.finishData(v).FailureDetail
		if detail == nil || detail.Source != "run_deadline" || detail.Reason != "deadline_exceeded" || detail.TimeoutMS != 3000 {
			t.Fatalf("wrong timeout detail: %+v", detail)
		}
		cp, err := r.runtime.UI.CatchUp(context.Background(), r.actor, r.space, v.ID)
		if err != nil || len(cp.State.Messages) != 1 || !cp.State.Messages[0].Incomplete || cp.State.Messages[0].Text != "D09 partial output" {
			t.Fatal("partial output lost or successful", err)
		}
	})
	t.Run("raw wire storage is unused", func(t *testing.T) {
		s := r.session()
		r.blobs.rejectOctetStream.Store(true)
		before := r.model.calls.Load()
		v := r.wait(r.submit(s, "reply without wire storage").ID)
		r.blobs.rejectOctetStream.Store(false)
		if v.Status != "succeeded" || v.FinalResponseEventID == "" || r.model.calls.Load() != before+1 {
			t.Fatal("model depended on raw wire storage", v.Error)
		}
		r.terminalOnce(v)
	})
	t.Run("lost upload acknowledgement", func(t *testing.T) {
		s := r.session()
		r.blobs.lostUpload.Store(true)
		v := r.wait(r.submit(s, "normal").ID)
		if v.Status != "succeeded" {
			t.Fatal(v.Error)
		}
		r.terminalOnce(v)
	})
}

func TestToolsDoNotRecheckRunInDatabase(t *testing.T) {
	r := newFaultRigWithTimeout(t, 30)
	entered, release := make(chan struct{}), make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	resume := func() { releaseOnce.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		enterOnce.Do(func() { close(entered) })
		select {
		case <-release:
			w.WriteHeader(http.StatusNoContent)
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer resume()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session := r.session()
	v := r.submit(session, "code while space locked "+server.URL)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("code tool did not start")
	}
	tx, err := r.db.Admin.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var locked string
	if err = tx.QueryRowContext(ctx, "SELECT id FROM spaces WHERE id=? FOR UPDATE", r.space).Scan(&locked); err != nil {
		t.Fatal(err)
	}
	resume()
	output := filepath.Join(r.manager.LocalPath(), r.space+"_"+session.ID, "first", "second", "finished.txt")
	for {
		raw, err := os.ReadFile(output)
		if err == nil && string(raw) == "ok" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("code tool could not write while the space row was locked")
		case <-time.After(20 * time.Millisecond):
		}
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	v = r.wait(v.ID)
	if v.Status != "succeeded" || v.WorkspaceSnapshotID == "" {
		t.Fatalf("unexpected final state: status=%s error=%s snapshot=%s", v.Status, v.Error, v.WorkspaceSnapshotID)
	}
	r.terminalOnce(v)
}

func TestCodeNetworkTimeoutKeepsCauseAndEffect(t *testing.T) {
	r := newFaultRigWithTimeout(t, 30)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		<-req.Context().Done()
	}))
	defer server.Close()
	for _, tc := range []struct{ method, status, code, effect string }{
		{"GET", "succeeded", "", "none"},
		{"POST", "failed", "code_timeout", "unknown"},
	} {
		t.Run(tc.method, func(t *testing.T) {
			v := r.wait(r.submit(r.session(), "code network deadline "+tc.method+" "+server.URL).ID)
			if v.Status != tc.status || v.Error != tc.code {
				t.Fatal("unexpected run outcome", v.Status, v.Error)
			}
			page := awaitToolTrace(t, context.Background(), r.runtime.Trace, r.db.Store, v)
			var step struct {
				Tools []struct{ Result run.ToolResult }
			}
			if len(page.Items) == 0 || json.Unmarshal(page.Items[0], &step) != nil || len(step.Tools) != 1 {
				t.Fatal("missing timed-out tool in trace")
			}
			got := step.Tools[0].Result
			if got.Error != "code_timeout" || got.SideEffect != tc.effect {
				t.Fatal("unexpected tool error or side effect", got.Error, got.SideEffect)
			}
		})
	}
}

func TestFaultQueuedCancellationAndDatabaseLoss(t *testing.T) {
	r := newFaultRig(t)
	first := r.submit(r.session(), "wait for database fault")
	r.entered()
	queued := r.submit(r.session(), "queued survives fault")
	cancelled := r.submit(r.session(), "queued cancel")
	if _, err := r.db.Store.CancelRuntimeRun(context.Background(), r.actor, r.space, cancelled.ID); err != nil {
		t.Fatal(err)
	}
	r.runtime.Cancel(cancelled.ID)
	// Close only this randomly scoped application's pool. The test server and
	// other databases are untouched; the admin observes facts independently.
	if err := r.db.Store.DB.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 500 && r.runtime.Service.Ready(context.Background()) == nil; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if r.runtime.Service.Ready(context.Background()) == nil {
		t.Fatal("database loss did not stop admission")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	_ = r.close(ctx)
	cancel()
	r.close = nil
	var status string
	var occupancy int
	r.db.Admin.DB.QueryRow("SELECT status FROM runs WHERE id=?", first.ID).Scan(&status)
	r.db.Admin.DB.QueryRow("SELECT COUNT(*) FROM session_active_runs WHERE run_id=?", first.ID).Scan(&occupancy)
	if run.Terminal(status) || occupancy != 1 {
		t.Fatal("uncommitted terminal reported", status, occupancy)
	}
	store, err := mariadb.Open(context.Background(), r.db.DSN)
	if err != nil {
		t.Fatal(err)
	}
	r.db.Store = store
	r.bind()
	before := r.model.calls.Load()
	r.start()
	recovered := r.wait(first.ID)
	if recovered.Status != "failed" || recovered.Error != "interrupted" || recovered.WorkspaceSnapshotID != "" {
		t.Fatal("interrupted recovery", recovered.Status, recovered.Error)
	}
	page := awaitToolTrace(t, context.Background(), r.runtime.Trace, r.db.Store, recovered)
	if len(page.Items) != 1 || !bytes.Contains(page.Items[0], []byte(`"type":"run_finished"`)) {
		t.Fatal("lost in-memory steps were reconstructed", page)
	}
	r.terminalOnce(recovered)
	done := r.wait(queued.ID)
	if done.Status != "succeeded" {
		t.Fatal("queued not resumed", done.Error)
	}
	r.terminalOnce(done)
	cancelled = r.wait(cancelled.ID)
	if cancelled.Status != "cancelled" {
		t.Fatal("queued cancellation lost", cancelled.Status)
	}
	r.terminalOnce(cancelled)
	if r.model.calls.Load() != before+1 {
		t.Fatal("interrupted/cancelled model was replayed")
	}
}

func TestFaultHistoryRetentionAndCorruptContent(t *testing.T) {
	r := newFaultRig(t)
	ctx := context.Background()
	s := r.session()
	v := r.wait(r.submit(s, "create retained artifact").ID)
	if v.Status != "succeeded" {
		t.Fatal(v.Error)
	}
	cp, err := r.runtime.UI.CatchUp(ctx, r.actor, r.space, v.ID)
	if err != nil || !cp.Closed {
		t.Fatal(err)
	}
	artifacts, err := r.db.Store.RuntimeArtifacts(ctx, r.actor, r.space, v.ID)
	if err != nil || len(artifacts) != 1 {
		t.Fatal(err)
	}
	_, token, _, err := r.auth.Login(ctx, "alice", "correct-password-alice")
	if err != nil {
		t.Fatal(err)
	}
	stop, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = r.close(stop)
	cancel()
	r.close = nil
	if err != nil {
		t.Fatal(err)
	}
	r.db.Store.DB.SetMaxIdleConns(1)
	r.db.Store.DB.SetMaxOpenConns(1)
	// Advance only this isolated SQL session's clock. Do not rewrite immutable
	// events/digests or change the test server/host clock.
	if _, err = r.db.Store.DB.Exec("SET timestamp=4102444800"); err != nil {
		t.Fatal(err)
	}
	defer r.db.Store.DB.Exec("SET timestamp=0")
	var year int
	if err = r.db.Store.DB.QueryRow("SELECT YEAR(UTC_TIMESTAMP())").Scan(&year); err != nil || year != 2100 {
		t.Fatal("clock not advanced", year, err)
	}
	if _, err = r.auth.Me(ctx, token); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatal("old login did not expire", err)
	}
	r.auth.TTL = 100 * 365 * 24 * time.Hour
	_, fresh, _, err := r.auth.Login(ctx, "alice", "correct-password-alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.auth.Me(ctx, fresh); err != nil {
		t.Fatal("fresh login rejected", err)
	}
	catalogue := &catalog.Service{Repo: r.db.Store, Content: r.content}
	if _, err = catalogue.Version(ctx, r.actor, r.space, "agent", r.agent, r.version); err != nil {
		t.Fatal("version expired", err)
	}
	history, _, err := resource.History(ctx, &journal.Journal{Repo: r.db.Store}, r.actor, r.space, s.ID)
	if err != nil || len(history) == 0 {
		t.Fatal("journal expired", err)
	}
	frames, closed, err := r.db.Store.ReadFrames(ctx, r.actor, r.space, v.ID, 0, 100)
	if err != nil || !closed || len(frames) == 0 {
		t.Fatal("AGUI expired", err)
	}
	raw, err := resource.ReadBytes(ctx, r.content, r.actor, r.space, artifacts[0].ObjectID, 1<<20)
	if err != nil || string(raw) != "retained D09" {
		t.Fatal("artifact expired", err)
	}
	snapshot, err := r.db.Store.Snapshot(ctx, r.actor, r.space, v.WorkspaceSnapshotID)
	if err != nil {
		t.Fatal("snapshot expired", err)
	}
	r.runtime.Workspaces.Forget(r.space, s.ID)
	if err = r.manager.With(ctx, r.space, s.ID, func(d resource.WorkspaceDir) error { return d.Clear(ctx, nil) }); err != nil {
		t.Fatal(err)
	}
	if err = r.runtime.Snapshots.Restore(ctx, r.actor, r.space, s.ID); err != nil {
		t.Fatal("old snapshot restore", err)
	}
	if err = r.manager.With(ctx, r.space, s.ID, func(d resource.WorkspaceDir) error {
		b, e := d.Root().ReadFile("retained.txt")
		if e != nil {
			return e
		}
		if string(b) != "retained D09" {
			return errors.New("restored bytes differ")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, f, e := r.content.Read(ctx, r.actor, r.space, snapshot.ObjectID); e != nil {
		t.Fatal(e)
	} else {
		f.Close()
	}
	if _, err = r.db.Store.DB.Exec("SET timestamp=0"); err != nil {
		t.Fatal(err)
	}
	r.db.Store.DB.SetMaxOpenConns(16)
	object, err := r.db.Store.Object(ctx, r.actor, r.space, artifacts[0].ObjectID, true)
	if err != nil {
		t.Fatal(err)
	}
	r.blobs.Corrupt(object.Key)
	if _, f, err := r.content.Read(ctx, r.actor, r.space, object.ID); !errors.Is(err, storage.ErrIntegrity) {
		if f != nil {
			f.Close()
		}
		t.Fatal("corrupt artifact was readable", err)
	}
	// A second missing-object failure must also be explicit, not an empty result.
	delete(r.blobs.Data, object.Key) // all runtime workers have already exited
	if _, f, e := r.content.Read(ctx, r.actor, r.space, object.ID); e == nil {
		f.Close()
		t.Fatal("missing content read succeeded")
	}
}

func TestFaultSummaryRetryAccounting(t *testing.T) {
	r := newFaultRig(t)
	ctx := context.Background()
	s := r.session()
	warm := r.wait(r.submit(s, "source history "+strings.Repeat("x", 12000)).ID)
	if warm.Status != "succeeded" {
		t.Fatal(warm.Error)
	}
	v := r.wait(r.submit(s, "retry once, retain this current request "+strings.Repeat("x", 7000)).ID)
	if v.Status != "succeeded" {
		t.Fatal("summary and retry", v.Error)
	}
	r.terminalOnce(v)
	var summary, task, failed, requests int
	r.db.Admin.DB.QueryRow("SELECT COALESCE(SUM(purpose='summary'),0),COALESCE(SUM(purpose='task'),0),COALESCE(SUM(status='failed'),0) FROM model_attempts WHERE run_id=?", v.ID).Scan(&summary, &task, &failed)
	r.db.Admin.DB.QueryRow("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='session_facts'").Scan(&requests)
	if summary != 1 || task != 2 || failed != 1 || requests != 0 {
		t.Fatal("attempt accounting", summary, task, failed, requests)
	}
	if v.Usage.Input == nil || *v.Usage.Input != 300 || v.Usage.Output != nil {
		t.Fatal("unknown usage treated as zero or settled twice", v.Usage)
	}
	page, err := r.db.Store.RuntimeTrace(ctx, r.actor, r.space, v.ID, 0, nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	prior := map[string]bool{}
	linked := false
	for _, e := range page.Events {
		if e.Type == "model.attempt.started" {
			var d journal.AttemptData
			journal.Decode(e.Data, &d)
			if d.RetryOf != "" {
				if !prior[d.RetryOf] {
					t.Fatal("invalid retry source")
				}
				linked = true
			}
			prior[d.ID] = true
		}
	}
	if !linked {
		t.Fatal("retry not linked")
	}
	cp, err := r.runtime.UI.CatchUp(ctx, r.actor, r.space, v.ID)
	if err != nil || !cp.Closed || len(cp.State.Messages) != 1 {
		t.Fatal("summary mixed into task UI", err)
	}
}

func TestFaultRevocationDuringCode(t *testing.T) {
	r := newFaultRig(t)
	ctx := context.Background()
	bob, err := r.auth.CreateUser(ctx, "bob", "correct-password-bob")
	if err != nil {
		t.Fatal(err)
	}
	if err = r.auth.AddMember(ctx, r.actor, r.space, "bob"); err != nil {
		t.Fatal(err)
	}
	s := r.session()
	v, err := r.runtime.Service.Submit(ctx, bob.ID, r.space, s.ID, sessionapp.SubmitInput{Key: uuid.NewString(), Text: "code revocation"})
	if err != nil {
		t.Fatal(err)
	}
	written := false
	for i := 0; i < 200; i++ {
		raw, _ := os.ReadFile(filepath.Join(r.manager.LocalPath(), r.space+"_"+s.ID, "revoked.txt"))
		if string(raw) == "kept" {
			written = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !written {
		t.Fatal("code write was not confirmed")
	}
	if err = r.auth.RemoveMember(ctx, r.actor, r.space, bob.ID); err != nil {
		t.Fatal(err)
	}
	v = r.wait(v.ID)
	if v.Status != "failed" || v.Error != "permission_revoked" || v.FinalResponseEventID != "" || v.WorkspaceSnapshotID == "" {
		t.Fatal("revocation cleanup", v.Status, v.Error, v.SnapshotError)
	}
	r.terminalOnce(v)
	if _, err = r.db.Store.RuntimeRun(ctx, bob.ID, r.space, v.ID); !errors.Is(err, identity.ErrForbidden) {
		t.Fatal("revoked actor can read history", err)
	}
	if r.model.calls.Load() != 1 {
		t.Fatal("model continued after revocation")
	}
	if err = r.manager.With(ctx, r.space, s.ID, func(d resource.WorkspaceDir) error {
		raw, e := d.Root().ReadFile("revoked.txt")
		if e != nil {
			return e
		}
		if string(raw) != "kept" {
			return errors.New("confirmed effect lost")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
