package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	einoadapter "github.com/imalimin/mass/internal/adapter/eino"
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
	"github.com/imalimin/mass/internal/runtime/context/projector"
	transport "github.com/imalimin/mass/internal/transport/http"
	"github.com/imalimin/mass/tests/fakes"
	"github.com/imalimin/mass/tests/testdb"
)

func TestContextStages(t *testing.T) {
	if os.Getenv("MASS_TEST_ADMIN_DSN") == "" {
		t.Skip("requires isolated MariaDB")
	}
	ctx := context.Background()
	db, err := testdb.NewVersion(ctx, 6)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.MigrateTo(ctx, 15); err != nil {
		t.Fatal(err)
	}
	if err = db.MigrateTo(ctx, 15); err != nil {
		t.Fatal(err)
	}
	cipher, _ := secrets.New(bytes.Repeat([]byte{7}, 32))
	auth, _ := identity.New(db.Store, cipher, time.Hour, []byte("context-test"))
	actor, err := auth.CreateUser(ctx, "alice", "correct-password-alice")
	if err != nil {
		t.Fatal(err)
	}
	space, err := auth.CreateSpace(ctx, actor.ID, "context")
	if err != nil {
		t.Fatal(err)
	}
	model, err := auth.SaveModel(ctx, actor.ID, space.ID, "", identity.Model{Name: "fixture", Provider: "ark", BaseURL: identity.ArkEndpoint, ModelID: "fixture", APIKey: "fixture-key", ContextWindow: 16384, MaxOutputTokens: 2048, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	blobs := fakes.NewBlobs()
	content := &resource.ContentStore{Repo: db.Store, Blobs: blobs, Prefix: "test/context/", MaxBytes: 32 << 20}
	catalogue := &catalog.Service{Repo: db.Store, Content: content}
	agent, err := catalogue.Create(ctx, actor.ID, space.ID, "agent", "context-agent")
	if err != nil {
		t.Fatal(err)
	}
	config := catalog.Config{SchemaVersion: 1, Name: agent.Name, Model: model.ID, Skills: []catalog.SkillRef{}, Tools: []string{}}
	config.Context.MaxInputTokens = 8000
	config.Context.ReservedOutputTokens = 256
	config.Context.Compression = "summary"
	config.Limits.MaxSteps = 8
	config.Limits.TimeoutSeconds = 30
	raw, _ := json.Marshal(config)
	agent, err = catalogue.Save(ctx, actor.ID, space.ID, "agent", agent.ID, agent.Revision, catalog.Document{Markdown: "Preserve task history.", ConfigYAML: string(raw)})
	if err != nil {
		t.Fatal(err)
	}
	version, err := catalogue.Publish(ctx, actor.ID, space.ID, "agent", agent.ID, "v1", agent.Revision, false)
	if err != nil {
		t.Fatal(err)
	}
	var summaries, tasks atomic.Int64
	var failSummary atomic.Bool
	fake := &einoadapter.FakeChatModel{Respond: func(ctx context.Context, messages []*schema.Message) ([]*schema.Message, error) {
		text := "done"
		if strings.HasPrefix(messages[0].Content, "Summarize this historical transcript") {
			summaries.Add(1)
			if failSummary.Load() {
				return nil, errors.New("summary fixture failure")
			}
			text = "Saved facts: user requested reports; completed earlier reports; no tool effects."
		} else {
			tasks.Add(1)
			if strings.Contains(messages[len(messages)-1].Content, "long") {
				text = strings.Repeat("report fact. ", 60)
			}
		}
		return []*schema.Message{{Role: schema.Assistant, Content: text, ResponseMeta: &schema.ResponseMeta{FinishReason: "stop", Usage: &schema.TokenUsage{PromptTokens: 100, CompletionTokens: 20}}}}, nil
	}}
	limits := storage.Limits{MaxFileBytes: 1 << 20, MaxTotalBytes: 8 << 20, MaxEntries: 1000}
	manager, err := workspace.New(t.TempDir(), limits)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	j := &journal.Journal{Repo: db.Store}
	snapshots := &resource.SnapshotStore{Repo: db.Store, Content: content, Journal: j, Workspaces: manager, Timeout: 10 * time.Second}
	cfg := bootstrap.Config{}
	cfg.Runtime.MaxConcurrentRuns = 1
	cfg.Runtime.SnapshotTimeout = 10 * time.Second
	cfg.Storage.MaxFileBytes = limits.MaxFileBytes
	cfg.Storage.MaxWorkspaceBytes = limits.MaxTotalBytes
	cfg.Storage.MaxEntries = limits.MaxEntries
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runtime, lifecycle, err := bootstrap.StartRuntime(ctx, cfg, db.Store, auth, catalogue, content, manager, snapshots, "test", logger, &einoadapter.ModelTransport{Fake: fake})
	if err != nil {
		t.Fatal(err)
	}
	defer lifecycle.Close(ctx)
	create := func() run.Session {
		s, e := runtime.Service.Create(ctx, actor.ID, space.ID, sessionapp.CreateInput{Key: uuid.NewString(), AgentID: agent.ID, VersionID: version.ID, Title: "context"})
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	submit := func(session run.Session, text string) run.View {
		v, e := runtime.Service.Submit(ctx, actor.ID, space.ID, session.ID, sessionapp.SubmitInput{Key: uuid.NewString(), Text: text})
		if e != nil {
			t.Fatal(e)
		}
		for i := 0; i < 1000; i++ {
			v, e = db.Store.RuntimeRun(ctx, actor.ID, space.ID, v.ID)
			if e != nil {
				t.Fatal(e)
			}
			if run.Terminal(v.Status) {
				return v
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("run timeout")
		return v
	}
	session := create()
	var views []run.View
	for i := 0; i < 6 && summaries.Load() == 0; i++ {
		v := submit(session, "long "+strings.Repeat("user fact. ", 60))
		if v.Status != "succeeded" {
			t.Fatalf("iteration %d: %s %s", i, v.Status, v.Error)
		}
		views = append(views, v)
	}
	if summaries.Load() != 1 {
		t.Fatalf("wanted one summary, got %d", summaries.Load())
	}
	for i := 0; i < 2; i++ {
		v := submit(session, "continue briefly")
		if v.Status != "succeeded" {
			t.Fatal(v.Status, v.Error)
		}
		views = append(views, v)
	}
	if summaries.Load() != 1 {
		t.Fatal("regenerated saved summary")
	}
	history, _, err := resource.History(ctx, j, actor.ID, space.ID, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	byRun := map[string]run.Snapshot{}
	for _, v := range views {
		byRun[v.ID] = v.Snapshot
	}
	p := &projector.Projector{Content: content}
	var last *run.Prepared
	var inspectID, inspectRun string
	for _, e := range history {
		if e.Type != "context.projected" {
			continue
		}
		var d journal.ProjectionData
		journal.Decode(e.Data, &d)
		var saved run.Prepared
		if err = resource.ReadJSON(ctx, content, actor.ID, space.ID, d.ContentID, 32<<20, &saved); err != nil {
			t.Fatal(err)
		}
		if saved.Spec.Purpose == "summary" {
			if len(saved.Tools) != 0 {
				t.Fatal("summary tools")
			}
			continue
		}
		replay, replayErr := p.Build(ctx, byRun[e.RunID], history, saved.Tools, saved.Spec)
		if replayErr != nil || replay.Fingerprint != saved.Fingerprint {
			t.Fatal("fixed projection drift", replayErr)
		}
		if last != nil && last.Spec.Epoch == saved.Spec.Epoch {
			if len(saved.Messages) < len(last.Messages) || journal.Digest(saved.Messages[:len(last.Messages)]) != journal.Digest(last.Messages) {
				t.Fatal("same epoch prefix changed")
			}
		}
		copy := saved
		last = &copy
		inspectID = saved.ID
		inspectRun = byRun[e.RunID].ID
	}
	if last == nil || last.Spec.Epoch != 2 || last.Spec.SummaryRef == nil {
		t.Fatal("missing stage checkpoint")
	}
	messages, err := db.Store.RuntimeMessages(ctx, actor.ID, space.ID, session.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != len(views)*2 {
		t.Fatalf("summary polluted messages: %d / %d", len(messages), len(views)*2)
	}
	for _, v := range views {
		cp, e := runtime.UI.CatchUp(ctx, actor.ID, space.ID, v.ID)
		if e != nil {
			t.Fatal(e)
		}
		restored, e := db.Store.LoadStream(ctx, actor.ID, space.ID, v.ID)
		if e != nil {
			t.Fatal(e)
		}
		for id := range cp.SummaryAttempts {
			if !restored.SummaryAttempts[id] {
				t.Fatal("summary checkpoint lost across database reload")
			}
		}
		if len(cp.State.Messages) != 1 || strings.Contains(cp.State.Messages[0].Text, "Saved facts:") {
			t.Fatal("summary polluted AGUI")
		}
	}
	var indexCount int
	if err = db.Store.DB.QueryRow("SELECT COUNT(*) FROM context_projections").Scan(&indexCount); err != nil || indexCount != len(views)+1 {
		t.Fatal("projection index", indexCount, err)
	}
	handler := transport.NewHandler(fstest.MapFS{"index.html": {Data: []byte("MASS")}}, "test", logger, transport.IdentityOptions{Service: auth, Origin: "http://mass.test", Catalog: catalogue, Storage: &transport.StorageOptions{Content: content}, Runtime: runtime})
	_, token, _, _ := auth.Login(ctx, "alice", "correct-password-alice")
	path := "/spaces/" + space.ID + "/runs/" + inspectRun + "/contexts/" + inspectID
	request := httptest.NewRequest("GET", "http://mass.test/api/v1"+path, nil)
	request.Header.Set("Cookie", "mass_session="+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, request)
	if w.Code != 200 {
		t.Fatal("inspect", w.Code, w.Body.String())
	}
	contractValidator(t)("GET", path, w)
	var inspected struct {
		Requests []journal.Event `json:"requests"`
		Sources  []journal.Event `json:"source_events"`
	}
	json.Unmarshal(w.Body.Bytes(), &inspected)
	if len(inspected.Requests) != 0 || len(inspected.Sources) == 0 {
		t.Fatal("inspection provenance")
	}
	// A model configuration revision starts one explained epoch while reusing the
	// saved summary. A later compression recursively cites that summary.
	model.Temperature = 0.5
	model, err = auth.SaveModel(ctx, actor.ID, space.ID, model.ID, model)
	if err != nil {
		t.Fatal(err)
	}

	probeSnapshot := views[len(views)-1].Snapshot
	probeSnapshot.Definition.Model = sessionapp.Model(model)
	probe, probeErr := p.Project(ctx, probeSnapshot, history, history[len(history)-1].Sequence, last.Tools)
	if probeErr != nil && !errors.Is(probeErr, run.ErrOverflow) {
		t.Fatal(probeErr)
	}
	if probe.Spec.Epoch != 3 || probe.Spec.PrefixChangeReason != "configuration_changed" || probe.Spec.SummaryRef.ObjectID != last.Spec.SummaryRef.ObjectID {
		t.Fatal("configuration checkpoint changed saved summary")
	}
	changed := submit(session, "continue after configuration change")
	if changed.Status != "succeeded" {
		t.Fatal(changed.Error)
	}
	views = append(views, changed)
	target := summaries.Load() + 1
	for i := 0; i < 6 && summaries.Load() < target; i++ {
		v := submit(session, "long "+strings.Repeat("user fact. ", 60))
		if v.Status != "succeeded" {
			t.Fatal("recursive summary", v.Error)
		}
		views = append(views, v)
	}
	if summaries.Load() != target {
		t.Fatal("missing recursive summary")
	}
	recursiveHistory, _, _ := resource.History(ctx, j, actor.ID, space.ID, session.ID)
	var recursive run.ContextSummary
	for _, e := range recursiveHistory {
		if e.Type == "context.summary.created" {
			var d journal.SummaryData
			journal.Decode(e.Data, &d)
			if err = resource.ReadJSON(ctx, content, actor.ID, space.ID, d.ContentID, 32<<20, &recursive); err != nil {
				t.Fatal(err)
			}
		}
	}
	if recursive.PreviousRef == nil {
		t.Fatal("recursive provenance lost")
	}
	v := submit(session, "brief after recursive summary")
	if v.Status != "succeeded" {
		t.Fatal(v.Error)
	}
	views = append(views, v)
	if summaries.Load() != target {
		t.Fatal("recursive summary not reused")
	}

	// A current request alone cannot be compacted away and must not call a model.
	before := tasks.Load()
	overflow := submit(create(), strings.Repeat("x", 9000))
	if overflow.Error != run.ErrOverflow.Error() || tasks.Load() != before {
		t.Fatal("required input overflow", overflow.Error)
	}
	// Summary failure never publishes a replacement task projection or epoch.
	failedSession := create()
	failSummary.Store(true)
	var failed run.View
	for i := 0; i < 6; i++ {
		failed = submit(failedSession, "long "+strings.Repeat("user fact. ", 60))
		if failed.Status == "failed" {
			break
		}
	}
	if failed.Error != run.ErrSummary.Error() {
		t.Fatal("summary failure", failed.Status, failed.Error)
	}
	failedHistory, _, _ := resource.History(ctx, j, actor.ID, space.ID, failedSession.ID)
	for _, e := range failedHistory {
		if e.Type == "context.summary.created" {
			t.Fatal("failed summary activated")
		}
	}
	// A failed summary journal commit cannot activate an epoch or dispatch a task.
	failSummary.Store(false)
	if _, err = db.Admin.DB.Exec("CREATE TRIGGER reject_summary BEFORE UPDATE ON model_attempts FOR EACH ROW BEGIN IF NEW.summary_ref IS NOT NULL THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='injected'; END IF; END"); err != nil {
		t.Fatal(err)
	}
	uncommitted := create()
	for i := 0; i < 6; i++ {
		v := submit(uncommitted, "long "+strings.Repeat("user fact. ", 60))
		if v.Status == "failed" {
			break
		}
		if i == 5 {
			t.Fatal("summary commit fault did not fail")
		}
	}
	if _, err = db.Admin.DB.Exec("DROP TRIGGER reject_summary"); err != nil {
		t.Fatal(err)
	}
	orphanHistory, _, _ := resource.History(ctx, j, actor.ID, space.ID, uncommitted.ID)
	for _, e := range orphanHistory {
		if e.Type == "context.summary.created" {
			t.Fatal("failed summary committed")
		}
		if e.Type == "context.projected" {
			var d journal.ProjectionData
			journal.Decode(e.Data, &d)
			if d.Epoch != 1 {
				t.Fatal("uncommitted summary activated")
			}
		}
	}

	// Semantic schema corruption and missing source facts cannot look like empty history.
	broken := append([]journal.Event(nil), history...)
	broken[0].SchemaVersion = 999
	if _, e := p.Build(ctx, views[len(views)-1].Snapshot, broken, last.Tools, last.Spec); e == nil {
		t.Fatal("unknown version accepted")
	}
	if _, e := p.Build(ctx, views[len(views)-1].Snapshot, history[1:], last.Tools, last.Spec); e == nil {
		t.Fatal("missing event accepted")
	}
	// The cache still asks the repository for authorization on every hit.
	bob, e := auth.CreateUser(ctx, "bob", "correct-password-bob")
	if e != nil {
		t.Fatal(e)
	}
	snapshot := views[len(views)-1].Snapshot
	snapshot.ActorID = bob.ID
	if _, e = p.Build(ctx, snapshot, history, last.Tools, last.Spec); e == nil {
		t.Fatal("cache bypassed authorization")
	}
	if err = lifecycle.Close(ctx); err != nil {
		t.Fatal(err)
	}
	messages, err = db.Store.RuntimeMessages(ctx, actor.ID, space.ID, session.ID, 0, 100)
	if err != nil || len(messages) != len(views)*2 {
		t.Fatal("summary polluted stored messages", err)
	}
}
