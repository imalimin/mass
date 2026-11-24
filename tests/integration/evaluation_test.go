package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

func TestEvaluationRevokedMemberResult(t *testing.T) {
	if os.Getenv("MASS_TEST_ADMIN_DSN") == "" {
		t.Skip("requires isolated MariaDB")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := testdb.New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	auth, err := identity.New(db.Store, nil, time.Hour, []byte("evaluation revocation"))
	if err != nil {
		t.Fatal(err)
	}
	newUser := func(name string) (identity.User, string) {
		t.Helper()
		user, err := auth.CreateUser(ctx, name, "fixture-password")
		if err != nil {
			t.Fatal(err)
		}
		_, cookie, _, err := auth.Login(ctx, name, "fixture-password")
		if err != nil {
			t.Fatal(err)
		}
		return user, cookie
	}
	owner, _ := newUser("evaluation-owner")
	launcher, launcherCookie := newUser("evaluation-launcher")
	viewer, viewerCookie := newUser("evaluation-viewer")
	space, err := auth.CreateSpace(ctx, owner.ID, "evaluation revocation")
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range []identity.User{launcher, viewer} {
		if err := auth.AddMember(ctx, owner.ID, space.ID, user.Username); err != nil {
			t.Fatal(err)
		}
	}

	firstRun, parallelRun := storage.NewShortID(), storage.NewShortID()
	firstSession, parallelSession := storage.NewShortID(), storage.NewShortID()
	polling, revoked := make(chan string, 2), make(chan struct{})
	firstMetricsRead := make(chan struct{})
	var creates, submits atomic.Int64
	checkAccess := func(ctx context.Context, actor, space string) error {
		if actor != launcher.ID {
			t.Errorf("evaluation session operation changed principal to %q", actor)
		}
		return auth.CheckAccess(ctx, actor, space)
	}
	eval := &evaluation.Service{Repo: db.Store, Calls: evaluation.Calls{
		CheckAgent: func(ctx context.Context, actor, space, _, _ string) error {
			return checkAccess(ctx, actor, space)
		},
		Create: func(ctx context.Context, actor, space string, in sessionapp.CreateInput) (run.Session, error) {
			creates.Add(1)
			id := firstSession
			if strings.HasSuffix(in.Key, ".2.session") {
				id = parallelSession
			}
			return run.Session{ID: id}, checkAccess(ctx, actor, space)
		},
		Submit: func(ctx context.Context, actor, space, _ string, in sessionapp.SubmitInput) (run.View, error) {
			submits.Add(1)
			id := firstRun
			if in.Text == "parallel" {
				id = parallelRun
			}
			return run.View{Snapshot: run.Snapshot{ID: id, CreatedAt: time.Now()}, Status: "queued"}, checkAccess(ctx, actor, space)
		},
		Run: func(ctx context.Context, spaceID, session, id string) (run.View, error) {
			if spaceID != space.ID || (id == firstRun && session != firstSession) || (id == parallelRun && session != parallelSession) {
				t.Errorf("Run lookup received wrong scope: %s/%s/%s", spaceID, session, id)
			}
			polling <- id
			gate := firstMetricsRead
			if id == firstRun {
				gate = revoked
			}
			select {
			case <-gate:
				return run.View{Snapshot: run.Snapshot{ID: id}, Status: "succeeded"}, nil
			case <-ctx.Done():
				return run.View{}, ctx.Err()
			}
		},
		Diagnostics: func(_ context.Context, spaceID, session string, ids []string) ([]json.RawMessage, error) {
			expected := parallelRun
			if session == firstSession {
				expected = firstRun
				close(firstMetricsRead)
			}
			if spaceID != space.ID || len(ids) != 1 || ids[0] != expected {
				t.Errorf("diagnostics received wrong scope: %s/%s/%v", spaceID, session, ids)
			}
			return []json.RawMessage{json.RawMessage(fmt.Sprintf(`{"seq":1,"type":"model_step","run_id":%q,"step":1,"model":{"usage":{"input_tokens":7}}}`, expected))}, nil
		},
	}}
	eval.Start(ctx)
	defer eval.Close(context.Background())
	turn := func(text string) evaluation.Turn {
		return evaluation.Turn{Content: []evaluation.Part{{Type: "text", Text: text}}}
	}
	dataset, err := eval.SaveDataset(ctx, owner.ID, space.ID, "", evaluation.DatasetInput{
		Name: "revoked",
		Cases: []evaluation.Case{
			{Turns: []evaluation.Turn{turn("first"), turn("must not submit")}},
			{Turns: []evaluation.Turn{turn("parallel")}},
			{Turns: []evaluation.Turn{turn("must not claim")}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := transport.NewHandler(fstest.MapFS{"index.html": {Data: []byte("test")}}, "evaluation-test", logger, transport.IdentityOptions{
		Service: auth, Origin: "http://mass.test", Evaluation: eval,
	})
	path := "/api/v1/spaces/" + space.ID + "/evaluation/experiments"
	request := func(method, path, cookie string, data []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://mass.test"+path, bytes.NewReader(data)).WithContext(ctx)
		req.Header.Set("Cookie", "mass_session="+cookie)
		req.Header.Set("Origin", "http://mass.test")
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		return resp
	}
	input, err := json.Marshal(evaluation.ExperimentInput{DatasetID: dataset.ID, AgentID: storage.NewShortID(), AgentVersionID: uuid.NewString(), Concurrency: 2, Key: "revoked"})
	if err != nil {
		t.Fatal(err)
	}
	resp := request("POST", path, launcherCookie, input)
	var launched evaluation.Experiment
	if err := json.Unmarshal(resp.Body.Bytes(), &launched); err != nil || resp.Code != 202 {
		t.Fatalf("launch failed: %d %s", resp.Code, resp.Body)
	}
	seen := make(map[string]bool)
	for range 2 {
		select {
		case id := <-polling:
			seen[id] = true
		case <-ctx.Done():
			t.Fatal("both cases did not reach Run polling")
		}
	}
	if !seen[firstRun] || !seen[parallelRun] {
		t.Fatalf("wrong Run IDs entering poll: %v", seen)
	}
	if err := db.Store.FinishEvaluationExperiment(ctx, owner.ID, space.ID, launched.ID, map[string]evaluation.Progress{}, time.Now()); !errors.Is(err, identity.ErrConflict) {
		t.Fatalf("result write matched a different actor: %v", err)
	}
	if err := auth.RemoveMember(ctx, owner.ID, space.ID, launcher.ID); err != nil {
		t.Fatal(err)
	}
	close(revoked)

	path += "/" + launched.ID
	var result evaluation.Experiment
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp := request("GET", path, viewerCookie, nil)
		if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil || resp.Code != 200 {
			t.Fatalf("viewer cannot read experiment: %d %s", resp.Code, resp.Body)
		}
		if result.Status == "completed" {
			break
		}
		if result.Status == "interrupted" || time.Now().After(deadline) {
			t.Fatalf("revocation result was not saved: %s", resp.Body)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if result.ActorID != launcher.ID || result.Failed != 2 || result.Succeeded != 1 || result.CompletedAt == nil {
		t.Fatalf("wrong final experiment: %+v", result)
	}
	first, parallel, pending := result.Result["1"], result.Result["2"], result.Result["3"]
	if first.Status != "failed" || first.FailureStage != "submit_run" || first.FailureCode != run.ErrPermission.Error() {
		t.Fatalf("next turn did not record revocation: %+v", first)
	}
	if parallel.Status != "succeeded" || parallel.FailureCode != "" {
		t.Fatalf("last accepted turn did not retain its actual outcome: %+v", parallel)
	}
	for _, expected := range []struct {
		progress       evaluation.Progress
		session, runID string
	}{
		{first, firstSession, firstRun}, {parallel, parallelSession, parallelRun},
	} {
		p := expected.progress
		if p.SessionID != expected.session || len(p.RunIDs) != 1 || p.RunIDs[0] != expected.runID || p.InputTokens == nil || *p.InputTokens != 7 {
			t.Fatalf("accepted Run or metrics were lost: %+v", p)
		}
	}
	if pending.Status != "failed" || pending.FailureStage != "create_session" || pending.FailureCode != run.ErrPermission.Error() || pending.SessionID != "" || len(pending.RunIDs) != 0 {
		t.Fatalf("pending case was started or lacked a revocation result: %+v", pending)
	}
	if creates.Load() != 2 || submits.Load() != 3 {
		t.Fatalf("revocation allowed extra work: %d sessions, %d Run submissions", creates.Load(), submits.Load())
	}
	if resp := request("GET", path, launcherCookie, nil); resp.Code != 403 {
		t.Fatalf("revoked launcher can still read experiment: %d %s", resp.Code, resp.Body)
	}
	if err := db.Store.FinishEvaluationExperiment(ctx, launcher.ID, space.ID, launched.ID, map[string]evaluation.Progress{}, time.Now()); !errors.Is(err, identity.ErrConflict) {
		t.Fatalf("completed result could be overwritten: %v", err)
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
	const parallelCases = 5
	started := make(chan struct{}, parallelCases)
	release := make(chan struct{})
	fake := &einoadapter.ModelTransport{Fake: &einoadapter.FakeChatModel{Respond: func(ctx context.Context, messages []*schema.Message) ([]*schema.Message, error) {
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
		if tools == 0 && (latest == "remember" || latest == "independent") {
			started <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		reply := &schema.Message{Role: schema.Assistant, Content: "bluebird", ResponseMeta: &schema.ResponseMeta{FinishReason: "stop", Usage: &schema.TokenUsage{PromptTokens: 100, CompletionTokens: 10}}}
		if tools == 0 && (latest == "remember" || latest == "recall") {
			name, args := "write_file", `{"path":"memory.txt","content":"bluebird"}`
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
	cfg.Runtime.SnapshotTimeout = 5 * time.Second
	cfg.Runtime.WorkspaceTempRoot = t.TempDir()
	cfg.LLM.MaxAttempts = 1
	runtime, lifecycle, err := bootstrap.StartRuntime(ctx, cfg, db.Store, auth, catalogue, content, manager, snapshots, "evaluation-test", logger, fake, sessions)
	if err != nil {
		t.Fatal(err)
	}
	defer lifecycle.Close(ctx)
	eval := &evaluation.Service{Repo: db.Store, SnapshotTimeout: 5 * time.Second, Logger: logger, Calls: evaluation.Calls{CheckAgent: func(ctx context.Context, actor, space, agent, version string) error {
		_, e := catalogue.Version(ctx, actor, space, "agent", agent, version)
		return e
	}, Create: runtime.Service.Create, Submit: runtime.Service.Submit, Run: sessions.AcceptedRunStatus, Diagnostics: sessions.AcceptedRunDiagnostics}}
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
	input := evaluation.DatasetInput{Name: "memory", Cases: []evaluation.Case{{Turns: []evaluation.Turn{turn("remember"), turn("recall")}}}}
	for range parallelCases - 1 {
		input.Cases = append(input.Cases, evaluation.Case{Turns: []evaluation.Turn{turn("independent")}})
	}
	out := request("POST", base+"/datasets", input)
	if out.Code != 201 {
		t.Fatal(out.Body.String())
	}
	var d evaluation.Dataset
	json.Unmarshal(out.Body.Bytes(), &d)
	in := evaluation.ExperimentInput{DatasetID: d.ID, AgentID: draft.ID, AgentVersionID: version.ID, Concurrency: parallelCases, Key: "run"}
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
	barrier := time.NewTimer(10 * time.Second)
	defer barrier.Stop()
	for i := range parallelCases {
		select {
		case <-started:
		case <-barrier.C:
			t.Fatalf("only %d of %d Runs entered the model concurrently in one space", i, parallelCases)
		}
	}
	close(release)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		out = request("GET", base+"/experiments/"+experiment.ID, nil)
		json.Unmarshal(out.Body.Bytes(), &experiment)
		if experiment.Status == "completed" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if experiment.Status != "completed" || experiment.Succeeded != parallelCases || experiment.DatasetVersion != 1 {
		t.Fatalf("experiment failed: %s", out.Body.String())
	}
	p := experiment.Result["1"]
	if len(p.RunIDs) != 2 || p.SessionID == experiment.Result["2"].SessionID || p.InputTokens == nil || *p.InputTokens != 400 || *p.OutputTokens != 40 || *p.StepCount != 4 {
		t.Fatalf("wrong multi-turn aggregate: %+v", p)
	}
	other := experiment.Result["2"].RunIDs[0]
	if _, err := sessions.AcceptedRunStatus(ctx, space.ID, p.SessionID, other); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("background read accepted a Run in another session: %v", err)
	}
	if _, err := sessions.AcceptedRunDiagnostics(ctx, space.ID, p.SessionID, []string{p.RunIDs[0], other}); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("background diagnostic read accepted a Run in another session: %v", err)
	}
	records, err := sessions.AcceptedRunDiagnostics(ctx, space.ID, p.SessionID, p.RunIDs[1:])
	if err != nil || len(records) == 0 {
		t.Fatalf("background diagnostic read returned no records: %v", err)
	}
	for _, record := range records {
		var header sessionlog.Header
		if err := json.Unmarshal(record, &header); err != nil || header.RunID != p.RunIDs[1] {
			t.Fatalf("background diagnostic read included a different Run: %s", record)
		}
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
	if original.Name != "memory" || len(original.Cases) != parallelCases {
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
	if _, err := sessions.RuntimeRun(ctx, stranger.ID, space.ID, p.RunIDs[0]); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("ordinary Run read did not check membership: %v", err)
	}
	if _, err := sessions.Diagnostics(ctx, stranger.ID, p.SessionID, ""); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("ordinary diagnostic read did not check membership: %v", err)
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
