package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/imalimin/mass/db/migrations"
	archiveadapter "github.com/imalimin/mass/internal/adapter/archive"
	einoadapter "github.com/imalimin/mass/internal/adapter/eino"
	"github.com/imalimin/mass/internal/adapter/secrets"
	"github.com/imalimin/mass/internal/adapter/workspace"
	"github.com/imalimin/mass/internal/app/catalog"
	"github.com/imalimin/mass/internal/app/evaluation"
	"github.com/imalimin/mass/internal/app/identity"
	sessionapp "github.com/imalimin/mass/internal/app/session"
	"github.com/imalimin/mass/internal/bootstrap"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
	"github.com/imalimin/mass/internal/resource"
	"github.com/imalimin/mass/internal/resource/sessionlog"
	transport "github.com/imalimin/mass/internal/transport/http"
	"github.com/imalimin/mass/tests/fakes"
	"github.com/imalimin/mass/tests/testdb"
)

func TestEvaluationUpgradePreservesSessions(t *testing.T) {
	if os.Getenv("MASS_TEST_ADMIN_DSN") == "" {
		t.Skip("requires isolated MariaDB")
	}
	ctx := context.Background()
	db, err := testdb.NewVersion(ctx, 16)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	auth, _ := identity.New(db.Store, nil, time.Hour, []byte("evaluation migration"))
	actor, err := auth.CreateUser(ctx, "evaluation-user", "fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	space, err := auth.CreateSpace(ctx, actor.ID, "retained")
	if err != nil {
		t.Fatal(err)
	}
	id := storage.NewShortID()
	_, err = db.Store.DB.ExecContext(ctx, "INSERT INTO sessions(id,space_id,agent_id,actor_id,created_at,request_key,request_digest) VALUES(?,?,?,?,UTC_TIMESTAMP(6),'old',?)", id, space.ID, storage.NewShortID(), actor.ID, journal.Digest("old"))
	if err != nil {
		t.Fatal(err)
	}
	if err = db.MigrateTo(ctx, migrations.CurrentVersion); err != nil {
		t.Fatal(err)
	}
	var hide bool
	if err = db.Store.DB.QueryRowContext(ctx, "SELECT hide FROM sessions WHERE id=?", id).Scan(&hide); err != nil || hide {
		t.Fatalf("old session not retained and visible: %v %v", hide, err)
	}
	if err = db.MigrateTo(ctx, migrations.CurrentVersion); err != nil {
		t.Fatal(err)
	}
}
func TestEvaluationHTTPMultiTurn(t *testing.T) {
	if os.Getenv("MASS_TEST_ADMIN_DSN") == "" {
		t.Skip("requires isolated MariaDB")
	}
	ctx := context.Background()
	db, err := testdb.New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cipher, _ := secrets.New(bytes.Repeat([]byte{9}, 32))
	auth, _ := identity.New(db.Store, cipher, time.Hour, []byte("evaluation HTTP"))
	actor, err := auth.CreateUser(ctx, "evaluation-user", "fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	space, err := auth.CreateSpace(ctx, actor.ID, "evaluation")
	if err != nil {
		t.Fatal(err)
	}
	_, cookie, _, err := auth.Login(ctx, actor.Username, "fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	blobs := fakes.NewBlobs()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager, err := workspace.New(t.TempDir(), storage.Limits{MaxArchiveFileBytes: 1 << 20, MaxTotalBytes: 4 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	objects := &archiveadapter.Objects{Authorize: db.Store.CheckAccess}
	sessions := &archiveadapter.Runtime{DB: db.Store, Objects: objects, Log: &sessionlog.Store{Root: t.TempDir(), Prefix: "evaluation", Blobs: blobs}}
	content := &resource.ContentStore{Repo: objects, Blobs: blobs, Prefix: "evaluation", MaxBytes: storage.MaxContentObjectBytes, Inline: sessions}
	catalogStore := &archiveadapter.Catalog{Blobs: blobs, Prefix: "evaluation", Objects: objects, Content: content, Model: db.Store.Model, Audit: db.Store.Audit}
	objects.Lookups = []archiveadapter.ObjectLookup{catalogStore.Lookup, sessions.Lookup}
	sessions.CatalogObject = catalogStore.Lookup
	catalogue := &catalog.Service{Repo: catalogStore, Content: content}
	model, err := auth.SaveModel(ctx, actor.ID, space.ID, "", identity.Model{Name: "model", Provider: "ark", ModelID: "fixture", BaseURL: identity.ArkEndpoint, APIKey: "fixture", ContextWindow: 32768, MaxOutputTokens: 4096, Temperature: 1, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := catalogue.Create(ctx, actor.ID, space.ID, "agent", "evaluation-agent")
	if err != nil {
		t.Fatal(err)
	}
	conf := catalog.Config{SchemaVersion: 1, Name: draft.Name, Model: model.ID, Tools: []string{"write_file", "read_file"}, Skills: []catalog.SkillRef{}}
	conf.Context.MaxInputTokens = 20000
	conf.Context.MaxTokens = 1024
	conf.Context.Compression = "summary"
	conf.Limits.MaxSteps = 6
	conf.Limits.TimeoutSeconds = 30
	raw, _ := json.Marshal(conf)
	draft, err = catalogue.Save(ctx, actor.ID, space.ID, "agent", draft.ID, draft.Revision, catalog.Document{Markdown: "Answer user requests.", ConfigYAML: string(raw)})
	if err != nil {
		t.Fatal(err)
	}
	version, err := catalogue.Publish(ctx, actor.ID, space.ID, "agent", draft.ID, "v1", draft.Revision, false)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	fake := &einoadapter.ModelTransport{Fake: &einoadapter.FakeChatModel{Respond: func(_ context.Context, messages []*schema.Message) ([]*schema.Message, error) {
		calls.Add(1)
		latest := ""
		last := 0
		users := []string{}
		for i, m := range messages {
			if m.Role == schema.User && !strings.HasPrefix(m.Content, "[MASS") {
				latest = m.Content
				last = i
				users = append(users, m.Content)
			}
		}
		tools := 0
		for _, m := range messages[last+1:] {
			if m.Role == schema.Tool {
				tools++
			}
		}
		if latest == "recall" && (len(users) != 2 || users[0] != "remember") {
			t.Errorf("multi-turn context missing: %v", users)
		}
		if latest == "independent" && len(users) != 1 {
			t.Errorf("cases share history: %v", users)
		}
		reply := &schema.Message{Role: schema.Assistant, Content: "bluebird", ResponseMeta: &schema.ResponseMeta{FinishReason: "stop", Usage: &schema.TokenUsage{PromptTokens: 100, CompletionTokens: 10}}}
		if tools == 0 && (latest == "remember" || latest == "recall") {
			name, args := "write_file", `{"path":"memory.txt","content":"bluebird","media_type":"text/plain","expected_sha256":"missing"}`
			if latest == "recall" {
				name, args = "read_file", `{"path":"memory.txt"}`
			}
			reply.Content = ""
			reply.ResponseMeta.FinishReason = "tool_calls"
			reply.ToolCalls = []schema.ToolCall{{Index: pointer(0), ID: uuid.NewString(), Type: "function", Function: schema.FunctionCall{Name: name, Arguments: args}}}
		}
		return []*schema.Message{reply}, nil
	}}}
	snapshots := &resource.SnapshotStore{Repo: sessions, Content: content, Journal: &journal.Journal{Repo: sessions}, Workspaces: manager, Timeout: 5 * time.Second}
	cfg := bootstrap.Config{}
	cfg.Runtime.MaxConcurrentRuns = 4
	cfg.Runtime.SnapshotTimeout = 5 * time.Second
	cfg.Runtime.WorkspaceTempRoot = t.TempDir()
	cfg.LLM.MaxAttempts = 1
	runtime, lifecycle, err := bootstrap.StartRuntime(ctx, cfg, db.Store, auth, catalogue, content, manager, snapshots, "evaluation-test", logger, fake, sessions)
	if err != nil {
		t.Fatal(err)
	}
	defer lifecycle.Close(ctx)
	eval := &evaluation.Service{Repo: db.Store, MaxPerSpace: 2, SnapshotTimeout: 5 * time.Second, Logger: logger, Calls: evaluation.Calls{CheckAgent: func(ctx context.Context, actor, space, agent, version string) error {
		_, e := catalogue.Version(ctx, actor, space, "agent", agent, version)
		return e
	}, Create: runtime.Service.Create, Submit: runtime.Service.Submit, Run: runtime.Repo.RuntimeRun, Diagnostics: sessions.Diagnostics}}
	eval.Start(ctx)
	defer eval.Close(ctx)
	handler := transport.NewHandler(fstest.MapFS{"index.html": {Data: []byte("test")}}, "evaluation-test", logger, transport.IdentityOptions{Service: auth, Origin: "http://mass.test", Catalog: catalogue, Runtime: runtime, Evaluation: eval})
	validate := contractValidator(t)
	request := func(method, path string, body any) *httptest.ResponseRecorder {
		var b []byte
		if body != nil {
			b, _ = json.Marshal(body)
		}
		req := httptest.NewRequest(method, "http://mass.test/api/v1"+path, bytes.NewReader(b))
		req.Header.Set("Cookie", "mass_session="+cookie)
		req.Header.Set("Origin", "http://mass.test")
		req.Header.Set("Content-Type", "application/json")
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		validate(method, path, out)
		return out
	}
	base := "/spaces/" + space.ID + "/evaluation"
	turn := func(text string) evaluation.Turn {
		return evaluation.Turn{Content: []evaluation.Part{{Type: "text", Text: text}}}
	}
	input := evaluation.DatasetInput{Name: "memory", Cases: []evaluation.Case{{Turns: []evaluation.Turn{turn("remember"), turn("recall")}}, {Turns: []evaluation.Turn{turn("independent")}}}}
	out := request("POST", base+"/datasets", input)
	if out.Code != 201 {
		t.Fatal(out.Body.String())
	}
	var d evaluation.Dataset
	json.Unmarshal(out.Body.Bytes(), &d)
	in := evaluation.ExperimentInput{DatasetID: d.ID, AgentID: draft.ID, AgentVersionID: version.ID, Concurrency: 2, Key: "run"}
	out = request("POST", base+"/experiments", in)
	if out.Code != 202 {
		t.Fatal(out.Body.String())
	}
	var experiment evaluation.Experiment
	json.Unmarshal(out.Body.Bytes(), &experiment)
	// New inputs must not affect the frozen experiment.
	changed := evaluation.DatasetInput{Name: "changed", Cases: []evaluation.Case{{Turns: []evaluation.Turn{turn("new version")}}}}
	if out = request("POST", base+"/datasets/"+d.ID+"/versions", changed); out.Code != 201 {
		t.Fatal(out.Body.String())
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		out = request("GET", base+"/experiments/"+experiment.ID, nil)
		json.Unmarshal(out.Body.Bytes(), &experiment)
		if experiment.Status == "completed" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if experiment.Status != "completed" || experiment.Succeeded != 2 || experiment.DatasetVersion != 1 {
		t.Fatalf("experiment failed: %s", out.Body.String())
	}
	p := experiment.Result["1"]
	if len(p.RunIDs) != 2 || p.SessionID == experiment.Result["2"].SessionID || p.InputTokens == nil || *p.InputTokens != 400 || *p.OutputTokens != 40 || *p.StepCount != 4 {
		t.Fatalf("wrong multi-turn aggregate: %+v", p)
	}
	out = request("GET", "/spaces/"+space.ID+"/sessions/"+p.SessionID, nil)
	if out.Code != 200 {
		t.Fatal(out.Body.String())
	}
	var hidden run.Session
	json.Unmarshal(out.Body.Bytes(), &hidden)
	if !hidden.Hide {
		t.Fatal("evaluation session is visible")
	}
	normal, err := runtime.Service.Create(ctx, actor.ID, space.ID, sessionapp.CreateInput{Key: "normal", AgentID: draft.ID, VersionID: version.ID})
	if err != nil {
		t.Fatal(err)
	}
	out = request("GET", "/spaces/"+space.ID+"/agents/"+draft.ID+"/sessions", nil)
	var page identity.Page[run.Session]
	json.Unmarshal(out.Body.Bytes(), &page)
	if len(page.Items) != 1 || page.Items[0].ID != normal.ID {
		t.Fatalf("hidden sessions leaked into list: %s", out.Body.String())
	}
	// Ordinal cursors are signed and can page every case.
	out = request("GET", base+"/experiments/"+experiment.ID+"/cases?limit=1", nil)
	var cases identity.Page[evaluation.CaseView]
	json.Unmarshal(out.Body.Bytes(), &cases)
	if cases.NextCursor == nil {
		t.Fatal("no case cursor")
	}
	out = request("GET", base+"/experiments/"+experiment.ID+"/cases?limit=1&cursor="+*cases.NextCursor, nil)
	json.Unmarshal(out.Body.Bytes(), &cases)
	if out.Code != 200 || cases.Items[0].Ordinal != 2 {
		t.Fatal(out.Body.String())
	}
	before := calls.Load()
	out = request("POST", base+"/experiments", in)
	var duplicate evaluation.Experiment
	json.Unmarshal(out.Body.Bytes(), &duplicate)
	if duplicate.ID != experiment.ID || calls.Load() != before {
		t.Fatal("duplicate replayed experiment")
	}
	out = request("GET", base+"/datasets/"+d.ID+"/versions/1", nil)
	var original evaluation.Dataset
	json.Unmarshal(out.Body.Bytes(), &original)
	if original.Name != "memory" || len(original.Cases) != 2 {
		t.Fatal("frozen inputs changed")
	}
	large := evaluation.DatasetInput{Name: "large", Cases: []evaluation.Case{{Turns: []evaluation.Turn{turn(strings.Repeat("x", 20<<10))}}}}
	if out = request("POST", base+"/datasets", large); out.Code != 201 {
		t.Fatalf("16KiB limit not relaxed: %s", out.Body.String())
	}
	oversized := evaluation.DatasetInput{Name: "oversized", Cases: []evaluation.Case{}}
	for range 5 {
		oversized.Cases = append(oversized.Cases, evaluation.Case{Turns: []evaluation.Turn{turn(strings.Repeat("x", 250<<10))}})
	}
	if out = request("POST", base+"/datasets", oversized); out.Code < 400 {
		t.Fatal("accepted >1MiB request")
	}
	media := evaluation.DatasetInput{Name: "media", Cases: []evaluation.Case{{Turns: []evaluation.Turn{{Content: []evaluation.Part{{Type: "image_url", ImageURL: &evaluation.URL{URL: "https://example.test/image.png"}}}}}}}}
	if out = request("POST", base+"/datasets", media); out.Code < 400 {
		t.Fatal("accepted reserved media")
	}
	stranger, err := auth.CreateUser(ctx, "evaluation-other", "fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = eval.Experiment(ctx, stranger.ID, space.ID, experiment.ID); err == nil {
		t.Fatal("cross-space read accepted")
	}
	// A record left by an earlier process has no task or saved result.
	lost, err := db.Store.CreateEvaluationExperiment(ctx, evaluation.Experiment{
		ID: storage.NewShortID(), ActorID: actor.ID, SpaceID: space.ID,
		CreatedAt: time.Now().UTC(), DatasetID: d.ID, AgentID: draft.ID,
		AgentVersionID: version.ID, Concurrency: 1, Key: "lost", Digest: journal.Digest("lost"),
	})
	if err != nil {
		t.Fatal(err)
	}
	out = request("GET", base+"/experiments/"+lost.ID, nil)
	var interrupted evaluation.Experiment
	if err := json.Unmarshal(out.Body.Bytes(), &interrupted); err != nil || out.Code != 200 || interrupted.Status != "interrupted" {
		t.Fatalf("lost experiment status: %d %s", out.Code, out.Body.String())
	}
	if out = request("GET", base+"/experiments", nil); out.Code != 200 {
		t.Fatal(fmt.Sprint(out.Code, out.Body.String()))
	}
	var experiments identity.Page[evaluation.Experiment]
	if err := json.Unmarshal(out.Body.Bytes(), &experiments); err != nil {
		t.Fatal(err)
	}
	statuses := map[string]string{}
	for _, item := range experiments.Items {
		statuses[item.ID] = item.Status
	}
	if statuses[lost.ID] != "interrupted" || statuses[experiment.ID] != "completed" {
		t.Fatalf("wrong experiment list states: %v", statuses)
	}
}
