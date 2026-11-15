package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	"github.com/imalimin/mass/internal/resource/trace"
	"github.com/imalimin/mass/internal/runtime/context/projector"
	"github.com/imalimin/mass/internal/runtime/modelgateway"
	transport "github.com/imalimin/mass/internal/transport/http"
	"github.com/imalimin/mass/tests/fakes"
	"github.com/imalimin/mass/tests/testdb"
)

func pointer[T any](v T) *T { return &v }
func TestRuntimeVertical(t *testing.T) {
	if os.Getenv("MASS_TEST_ADMIN_DSN") == "" {
		t.Skip("requires isolated MariaDB")
	}
	exerciseRuntime(t, fakes.NewBlobs(), "test/runtime/", false)
}

type snapshotFaults struct {
	storage.BlobStore
	fail        atomic.Bool
	mu          sync.Mutex
	archiveHook func()
}

func (f *snapshotFaults) PutNew(ctx context.Context, key string, r io.Reader, n int64, media string) (storage.BlobInfo, error) {
	if media == "application/gzip" {
		f.mu.Lock()
		hook := f.archiveHook
		f.archiveHook = nil
		f.mu.Unlock()
		if hook != nil {
			hook()
		}
	}
	if f.fail.Load() && media == "application/gzip" {
		return storage.BlobInfo{}, errors.New("injected snapshot outage")
	}
	return f.BlobStore.PutNew(ctx, key, r, n, media)
}
func exerciseRuntime(t *testing.T, blobs storage.BlobStore, prefix string, wire bool, realOption ...bool) {
	real := len(realOption) > 0 && realOption[0]
	t.Helper()
	ctx := context.Background()
	db, err := testdb.NewVersion(ctx, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.MigrateTo(ctx, 15); err != nil {
		t.Fatal(err)
	}
	cipher, _ := secrets.New(bytes.Repeat([]byte{6}, 32))
	auth, _ := identity.New(db.Store, cipher, time.Hour, []byte("runtime-test"))
	actor, err := auth.CreateUser(ctx, "alice", "correct-password-alice")
	if err != nil {
		t.Fatal(err)
	}
	space, err := auth.CreateSpace(ctx, actor.ID, "runtime")
	if err != nil {
		t.Fatal(err)
	}
	baseURL := identity.ArkEndpoint
	transportModel := &einoadapter.ModelTransport{}
	respond := func(ctx context.Context, messages []*schema.Message) ([]*schema.Message, error) {
		latest := ""
		lastUser := 0
		for i, m := range messages {
			if m.Role == schema.User && !strings.HasPrefix(m.Content, "[MASS") {
				latest = m.Content
				lastUser = i
			}
		}
		if strings.Contains(latest, "wait") {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		tools := 0
		for _, m := range messages[lastUser+1:] {
			if m.Role == schema.Tool {
				tools++
			}
		}
		out := &schema.Message{Role: schema.Assistant, ResponseMeta: &schema.ResponseMeta{FinishReason: "tool_calls", Usage: &schema.TokenUsage{PromptTokens: 100, CompletionTokens: 10}}}
		sum := sha256.Sum256([]byte("hello workspace"))
		sha := hex.EncodeToString(sum[:])
		name, args := "", ""
		if strings.Contains(latest, "second") {
			if tools == 0 {
				name = "read_file"
				args = `{"path":"notes.txt"}`
			} else {
				out.Content = "second run read hello workspace"
				out.ResponseMeta.FinishReason = "stop"
			}
		} else {
			switch tools {
			case 0:
				name = "write_file"
				args = `{"path":"notes.txt","content":"hello workspace","media_type":"text/plain","expected_sha256":"missing"}`
			case 1:
				name = "read_file"
				args = `{"path":"notes.txt"}`
			case 2:
				name = "publish_artifact"
				args = fmt.Sprintf(`{"path":"notes.txt","expected_sha256":%q,"display_name":"notes.txt","media_type":"text/plain"}`, sha)
			default:
				out.Content = "completed and published notes.txt"
				out.ResponseMeta.FinishReason = "stop"
			}
		}
		if name != "" {
			out.ToolCalls = []schema.ToolCall{{Index: pointer(0), ID: uuid.NewString(), Type: "function", Function: schema.FunctionCall{Name: name, Arguments: args}}}
		}
		return []*schema.Message{out}, nil
	}
	transportModel.Fake = &einoadapter.FakeChatModel{Respond: respond}
	if wire {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Messages []struct{ Role, Content, ToolCallID string }
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				w.WriteHeader(400)
				return
			}
			messages := []*schema.Message{}
			for _, m := range body.Messages {
				messages = append(messages, &schema.Message{Role: schema.RoleType(m.Role), Content: m.Content, ToolCallID: m.ToolCallID})
			}
			chunks, e := respond(r.Context(), messages)
			if e != nil {
				return
			}
			m := chunks[0]
			delta := map[string]any{"role": "assistant", "content": m.Content}
			if len(m.ToolCalls) > 0 {
				calls := []any{}
				for _, c := range m.ToolCalls {
					calls = append(calls, map[string]any{"index": 0, "id": c.ID, "type": "function", "function": map[string]any{"name": c.Function.Name, "arguments": c.Function.Arguments}})
				}
				delta["tool_calls"] = calls
			}
			raw, _ := json.Marshal(map[string]any{"id": "completion-test", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": m.ResponseMeta.FinishReason}}, "usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 10, "total_tokens": 110}})
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", raw)
		}))
		defer server.Close()
		baseURL = server.URL
		transportModel.Fake = nil
	}
	if real {
		transportModel.Base = realModelHTTPBudget
		baseURL = strings.TrimSuffix(os.Getenv("MASS_LLM_MODEL_BASE_URL"), "/")
		transportModel.Fake = nil
	}
	model, err := auth.SaveModel(ctx, actor.ID, space.ID, "", identity.Model{Name: "runtime-model", Provider: "ark", BaseURL: baseURL, ModelID: func() string {
		if real {
			return os.Getenv("MASS_LLM_MODEL_ID")
		}
		return "fixture"
	}(), Temperature: 1, APIKey: func() string {
		if real {
			return os.Getenv("MASS_LLM_MODEL_KEY")
		}
		return "fixture-secret"
	}(), ContextWindow: 32768, MaxOutputTokens: 4096, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	faults := &snapshotFaults{BlobStore: blobs}
	content := &resource.ContentStore{Repo: db.Store, Blobs: faults, Prefix: prefix, MaxBytes: 32 << 20}
	catalogue := &catalog.Service{Repo: db.Store, Content: content}
	draft, err := catalogue.Create(ctx, actor.ID, space.ID, "agent", "runner")
	if err != nil {
		t.Fatal(err)
	}
	config := catalog.Config{SchemaVersion: 1, Name: draft.Name, Model: model.ID, Tools: []string{"read_file", "write_file", "publish_artifact"}, Skills: []catalog.SkillRef{}}
	if real {
		config.Tools = []string{}
	}
	config.Context.MaxInputTokens = 24000
	config.Context.MaxTokens = 1024
	if real {
		config.Context.MaxTokens = 512
	}
	config.Context.Compression = "summary"
	config.Limits.MaxSteps = 8
	config.Limits.TimeoutSeconds = 60
	raw, _ := json.Marshal(config)
	draft, err = catalogue.Save(ctx, actor.ID, space.ID, "agent", draft.ID, draft.Revision, catalog.Document{Markdown: "Help with files.", ConfigYAML: string(raw)})
	if err != nil {
		t.Fatal(err)
	}
	version, err := catalogue.Publish(ctx, actor.ID, space.ID, "agent", draft.ID, "v1", draft.Revision, false)
	if err != nil {
		t.Fatal(err)
	}
	// Repeated migrations preserve the published definition and encrypted model.
	for i := 0; i < 2; i++ {
		if err = db.MigrateTo(ctx, 15); err != nil {
			t.Fatal("D03 upgrade", err)
		}
	}
	if err = db.CheckVersion(ctx, 15); err != nil {
		t.Fatal(err)
	}
	upgraded, e := catalogue.Version(ctx, actor.ID, space.ID, "agent", draft.ID, version.ID)
	if e != nil || !catalog.Equal(version, upgraded) {
		t.Fatal("upgrade changed published definition", e)
	}
	root := t.TempDir()
	limits := storage.Limits{MaxFileBytes: 1 << 20, MaxTotalBytes: 8 << 20, MaxEntries: 1000}
	manager, err := workspace.New(root, limits)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	j := &journal.Journal{Repo: db.Store}
	snapshots := &resource.SnapshotStore{Repo: db.Store, Content: content, Journal: j, Workspaces: manager, Timeout: 30 * time.Second}
	c := bootstrap.Config{}
	c.Runtime.MaxConcurrentRuns = 2
	c.Runtime.SnapshotTimeout = 30 * time.Second
	c.Storage.MaxFileBytes = 1 << 20
	c.Storage.MaxWorkspaceBytes = limits.MaxTotalBytes
	c.Storage.MaxEntries = limits.MaxEntries
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runtime, lifecycle, err := bootstrap.StartRuntime(ctx, c, db.Store, auth, catalogue, content, manager, snapshots, "test-build", logger, transportModel)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := lifecycle.Close(stop); err != nil {
			t.Error(err)
		}
	}()
	if extra, err := db.Store.OpenRuntime(ctx); err == nil {
		extra.Close()
		t.Fatal("second dispatcher acquired execution lock")
	}
	input := sessionapp.CreateInput{Key: uuid.NewString(), AgentID: draft.ID, VersionID: version.ID, Title: "test session"}
	session, err := runtime.Service.Create(ctx, actor.ID, space.ID, input)
	if err != nil {
		t.Fatal("create session", err)
	}
	again, err := runtime.Service.Create(ctx, actor.ID, space.ID, input)
	if err != nil || again.ID != session.ID {
		t.Fatal("session idempotency", err)
	}
	submit := sessionapp.SubmitInput{Key: uuid.NewString(), Text: func() string {
		if real {
			return "Reply with only MASS_D04_OK. No tools are needed."
		}
		return "write, read and publish"
	}()}
	first, err := runtime.Service.Submit(ctx, actor.ID, space.ID, session.ID, submit)
	if err != nil {
		t.Fatal("submit", err)
	}
	againRun, err := runtime.Service.Submit(ctx, actor.ID, space.ID, session.ID, submit)
	if err != nil || againRun.ID != first.ID {
		t.Fatal("run idempotency", err)
	}
	changed := submit
	changed.Text = "different"
	if _, err = runtime.Service.Submit(ctx, actor.ID, space.ID, session.ID, changed); !errors.Is(err, storage.ErrConflict) {
		t.Fatal("idempotency changed content", err)
	}
	wait := func(id string) run.View {
		t.Helper()
		deadline := time.Now().Add(90 * time.Second)
		for time.Now().Before(deadline) {
			v, e := db.Store.RuntimeRun(ctx, actor.ID, space.ID, id)
			if e != nil {
				t.Fatal(e)
			}
			if run.Terminal(v.Status) {
				return v
			}
			time.Sleep(30 * time.Millisecond)
		}
		t.Fatal("run timeout")
		return run.View{}
	}
	first = wait(first.ID)
	if first.Status != "succeeded" || first.WorkspaceSnapshotID == "" {
		trace, _ := db.Store.RuntimeTrace(ctx, actor.ID, space.ID, first.ID, 0, nil, 100)
		b, _ := json.Marshal(trace)
		t.Fatalf("first run %s %s %s trace=%s", first.Status, first.Error, first.SnapshotError, b)
	}
	if real {
		var contentID string
		if err = db.Admin.DB.QueryRow("SELECT content_id FROM messages WHERE space_id=? AND source_event_id=?", space.ID, first.FinalResponseEventID).Scan(&contentID); err != nil {
			t.Fatal(err)
		}
		var reply run.Reply
		if err = resource.ReadJSON(ctx, content, actor.ID, space.ID, contentID, 4<<20, &reply); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(reply.Message.Content, "MASS_D04_OK") {
			t.Fatal("real model response did not contain the requested marker")
		}
		var calls int
		db.Admin.DB.QueryRow("SELECT COUNT(*) FROM model_attempts WHERE space_id=? AND run_id=?", space.ID, first.ID).Scan(&calls)
		if calls != 1 {
			t.Fatal("real smoke made unexpected model attempts", calls)
		}
		t.Log("real model: one Eino text attempt completed, usage retained, workspace snapshot committed")
		return
	}
	artifacts, err := db.Store.RuntimeArtifacts(ctx, actor.ID, space.ID, first.ID)
	if err != nil || len(artifacts) != 1 {
		t.Fatal("artifact missing", err)
	}
	data, err := resource.ReadBytes(ctx, content, actor.ID, space.ID, artifacts[0].ObjectID, 1<<20)
	if err != nil || string(data) != "hello workspace" {
		t.Fatal("artifact bytes", err)
	}
	if err = os.RemoveAll(filepath.Join(root, space.ID+"_"+session.ID)); err != nil {
		t.Fatal(err)
	}
	second, err := runtime.Service.Submit(ctx, actor.ID, space.ID, session.ID, sessionapp.SubmitInput{Key: uuid.NewString(), Text: "second read"})
	if err != nil {
		t.Fatal(err)
	}
	second = wait(second.ID)
	if second.Status != "succeeded" {
		t.Fatal("second run", second.Status, second.Error)
	}
	clone, err := runtime.Service.Create(ctx, actor.ID, space.ID, sessionapp.CreateInput{Key: uuid.NewString(), AgentID: draft.ID, SourceSnapshotID: first.WorkspaceSnapshotID, Title: "clone"})
	if err != nil {
		t.Fatal(err)
	}
	third, err := runtime.Service.Submit(ctx, actor.ID, space.ID, clone.ID, sessionapp.SubmitInput{Key: uuid.NewString(), Text: "second clone read"})
	if err != nil {
		t.Fatal(err)
	}
	third = wait(third.ID)
	if third.Status != "succeeded" {
		t.Fatal("clone run", third.Status, third.Error)
	}
	// UI uses its own continuous sequence, and closed streams never grow.
	cp, err := runtime.UI.CatchUp(ctx, actor.ID, space.ID, first.ID)
	if err != nil || !cp.Closed || cp.State.Status != "succeeded" {
		t.Fatal("UI projection", err)
	}
	frames, closed, err := db.Store.ReadFrames(ctx, actor.ID, space.ID, first.ID, 0, 100)
	if err != nil || !closed {
		t.Fatal("UI frames", err)
	}
	for i, f := range frames {
		if f.Sequence != int64(i+1) {
			t.Fatal("UI gap")
		}
	}
	suffix, _, err := db.Store.ReadFrames(ctx, actor.ID, space.ID, first.ID, 2, 100)
	if err != nil || !bytes.Equal(suffix[0].JSON, frames[2].JSON) {
		t.Fatal("resume", err)
	}
	if _, _, err = db.Store.ReadFrames(ctx, actor.ID, space.ID, first.ID, cp.Cursor+1, 100); !errors.Is(err, run.ErrCursor) {
		t.Fatal("future cursor accepted", err)
	}
	handler := transport.NewHandler(fstest.MapFS{"index.html": {Data: []byte("MASS")}}, "test", logger, transport.IdentityOptions{Service: auth, Origin: "http://mass.test", Catalog: catalogue, Storage: &transport.StorageOptions{Content: content}, Runtime: runtime})
	_, token, _, _ := auth.Login(ctx, "alice", "correct-password-alice")
	request := httptest.NewRequest("GET", "http://mass.test/api/v1/spaces/"+space.ID+"/runs/"+first.ID+"/events?after=2", nil)
	request.Header.Set("Cookie", "mass_session="+token)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != 200 || !strings.Contains(recorder.Body.String(), "RUN_FINISHED") || strings.Contains(recorder.Body.String(), "id: 1\n") {
		t.Fatal("SSE resume failed", recorder.Code)
	}
	// This fixture exercises the schema 15 replay contract. The current
	// session-log response shape is exercised by the schema 16 browser fixture.
	base := "/spaces/" + space.ID
	for _, p := range []string{base + "/sessions", base + "/sessions/" + session.ID, base + "/sessions/" + session.ID + "/runs", base + "/sessions/" + session.ID + "/messages", base + "/sessions/" + session.ID + "/workspace-snapshots", base + "/runs/" + first.ID, base + "/runs/" + first.ID + "/trace", base + "/runs/" + first.ID + "/contexts", base + "/runs/" + first.ID + "/metrics", base + "/runs/" + first.ID + "/artifacts", base + "/runs/" + first.ID + "/ui-snapshot"} {
		r := httptest.NewRequest("GET", "http://mass.test/api/v1"+p, nil)
		r.Header.Set("Cookie", "mass_session="+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal("runtime API", p, w.Code, w.Body.String())
		}
	}
	type diagnostics struct {
		Run      run.View
		Snapshot *storage.Snapshot
		Trace    trace.Page
	}
	getDiagnostics := func(query, sessionToken string, status int) diagnostics {
		t.Helper()
		path := "/diagnostics" + query
		r := httptest.NewRequest("GET", "http://mass.test/api/v1"+path, nil)
		if sessionToken != "" {
			r.AddCookie(&http.Cookie{Name: "mass_session", Value: sessionToken})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("diagnostics %s: got %d, want %d: %s", path, w.Code, status, w.Body.String())
		}
		if status == http.StatusOK {
			return jsonValue[diagnostics](t, w)
		}
		return diagnostics{}
	}
	bySnapshot := "?snapshot_id=" + first.WorkspaceSnapshotID
	byRun := "?run_id=" + first.ID
	var firstPage diagnostics
	for i := 0; i < 100; i++ {
		firstPage = getDiagnostics(bySnapshot+"&limit=1", token, 200)
		if firstPage.Trace.UploadStatus == "uploaded" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if firstPage.Trace.UploadStatus != "uploaded" {
		t.Fatal("session trace was not uploaded", firstPage.Trace)
	}
	if firstPage.Run.ID != first.ID || firstPage.Run.Status != first.Status || firstPage.Snapshot == nil ||
		firstPage.Snapshot.ID != first.WorkspaceSnapshotID || len(firstPage.Trace.Items) != 1 || !firstPage.Trace.HasMore {
		t.Fatal("unexpected snapshot diagnostics", firstPage.Run.ID, firstPage.Snapshot, firstPage.Trace)
	}
	cursor := fmt.Sprintf("&after_sequence=%d&through_sequence=%d", firstPage.Trace.Next, firstPage.Trace.Through)
	nextPage := getDiagnostics(byRun+cursor+"&limit=100", token, 200)
	if nextPage.Snapshot == nil || nextPage.Snapshot.ID != first.WorkspaceSnapshotID ||
		nextPage.Trace.Through != firstPage.Trace.Through || len(nextPage.Trace.Items) == 0 {
		t.Fatal("unexpected diagnostics continuation")
	}
	var firstStep trace.ModelStep
	if err := json.Unmarshal(firstPage.Trace.Items[0], &firstStep); err != nil || firstStep.Type != "model_step" || firstStep.RunID != first.ID || firstStep.Step != 1 ||
		len(firstStep.Model.ToolCalls) == 0 || len(firstStep.Tools) != len(firstStep.Model.ToolCalls) || firstStep.Tools[0].NativeCallID != firstStep.Model.ToolCalls[0].ID {
		t.Fatalf("model and tools not grouped: %+v, %v", firstStep, err)
	}
	var end trace.RunFinished
	if err := json.Unmarshal(nextPage.Trace.Items[len(nextPage.Trace.Items)-1], &end); err != nil || end.Type != "run_finished" || end.RunID != first.ID ||
		end.Status != "succeeded" || end.SnapshotID == nil || *end.SnapshotID != first.WorkspaceSnapshotID {
		t.Fatalf("missing terminal trace: %+v, %v", end, err)
	}
	var dropped int
	if err := db.Admin.DB.QueryRow("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name IN ('runtime_events','session_facts','event_objects','tool_executions','agui_events','agui_streams')").Scan(&dropped); err != nil || dropped != 0 {
		t.Fatal("legacy stream tables still exist", dropped, err)
	}

	getDiagnostics("", token, 400)
	getDiagnostics(byRun+"&snapshot_id="+first.WorkspaceSnapshotID, token, 400)
	getDiagnostics("?snapshot_id="+uuid.NewString(), token, 404)
	getDiagnostics(bySnapshot, "", 401)
	body, _ := json.Marshal(submit)
	r := httptest.NewRequest("POST", "http://mass.test/api/v1"+base+"/sessions/"+session.ID+"/runs", bytes.NewReader(body))
	r.Header.Set("Cookie", "mass_session="+token)
	r.Header.Set("Origin", "http://mass.test")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 202 {
		t.Fatal("repeat HTTP request", w.Code, w.Body.String())
	}
	if replay := jsonValue[run.View](t, w); replay.ID != first.ID {
		t.Fatal("repeat HTTP request returned a different run", replay.ID)
	}
	// Fixed journal boundaries reproduce the retained inputs deterministically.
	history, _, e := resource.History(ctx, j, actor.ID, space.ID, session.ID)
	if e != nil {
		t.Fatal(e)
	}
	for _, event := range history {
		if event.RunID != first.ID || event.Type != "context.projected" {
			continue
		}
		var d journal.ProjectionData
		journal.Decode(event.Data, &d)
		var original run.Prepared
		if e = resource.ReadJSON(ctx, content, actor.ID, space.ID, d.ContentID, 4<<20, &original); e != nil {
			t.Fatal(e)
		}
		p := &projector.Projector{Content: content}
		a, e := p.Project(ctx, first.Snapshot, history, d.Through, original.Tools)
		if e != nil {
			t.Fatal(e)
		}
		b, e := p.Project(ctx, first.Snapshot, history, d.Through, original.Tools)
		if e != nil {
			t.Fatal(e)
		}
		if !catalog.Equal(a, b) || a.Spec.Through != d.Through {
			t.Fatal("projection changed at fixed boundary")
		}
		for _, m := range original.Messages {
			if m.Role == "tool" && m.Content != "" {
				t.Fatal("persisted projection retained an in-memory tool result")
			}
		}
	}
	// Running cancellation waits for actual stream exit and still saves a snapshot.
	waiting, err := runtime.Service.Submit(ctx, actor.ID, space.ID, session.ID, sessionapp.SubmitInput{Key: uuid.NewString(), Text: "wait"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200; i++ {
		v, _ := db.Store.RuntimeRun(ctx, actor.ID, space.ID, waiting.ID)
		if v.Status == "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err = runtime.Service.Submit(ctx, actor.ID, space.ID, session.ID, sessionapp.SubmitInput{Key: uuid.NewString(), Text: "second"}); !errors.Is(err, storage.ErrActive) {
		t.Fatal("session occupancy", err)
	}
	blocker, e := runtime.Service.Submit(ctx, actor.ID, space.ID, clone.ID, sessionapp.SubmitInput{Key: uuid.NewString(), Text: "wait for queue"})
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 200; i++ {
		v, _ := db.Store.RuntimeRun(ctx, actor.ID, space.ID, blocker.ID)
		if v.Status == "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	queuedSession, e := runtime.Service.Create(ctx, actor.ID, space.ID, sessionapp.CreateInput{Key: uuid.NewString(), AgentID: draft.ID})
	if e != nil {
		t.Fatal(e)
	}
	queued, e := runtime.Service.Submit(ctx, actor.ID, space.ID, queuedSession.ID, sessionapp.SubmitInput{Key: uuid.NewString(), Text: "must never execute"})
	if e != nil {
		t.Fatal(e)
	}
	queued, e = db.Store.CancelRuntimeRun(ctx, actor.ID, space.ID, queued.ID)
	if e != nil || queued.Status != "cancelled" {
		t.Fatal("queued cancel", queued.Status, e)
	}
	var queuedAttempts int
	db.Admin.DB.QueryRow("SELECT COUNT(*) FROM model_attempts WHERE run_id=?", queued.ID).Scan(&queuedAttempts)
	if queuedAttempts != 0 {
		t.Fatal("queued cancellation invoked model")
	}
	if _, e = db.Store.CancelRuntimeRun(ctx, actor.ID, space.ID, blocker.ID); e != nil {
		t.Fatal(e)
	}
	runtime.Cancel(blocker.ID)
	wait(blocker.ID)
	if _, err = db.Store.CancelRuntimeRun(ctx, actor.ID, space.ID, waiting.ID); err != nil {
		t.Fatal(err)
	}
	runtime.Cancel(waiting.ID)
	waiting = wait(waiting.ID)
	if waiting.Status != "cancelled" {
		t.Fatal("cancel", waiting.Status, waiting.Error)
	}

	if _, ok := blobs.(*fakes.Blobs); ok {
		// Cancellation after the final model reply, while saving the archive, wins success.
		archiveReached, resumeArchive := make(chan struct{}), make(chan struct{})
		faults.mu.Lock()
		faults.archiveHook = func() { close(archiveReached); <-resumeArchive }
		faults.mu.Unlock()
		racing, e := runtime.Service.Submit(ctx, actor.ID, space.ID, session.ID, sessionapp.SubmitInput{Key: uuid.NewString(), Text: "second cancel during finish"})
		if e != nil {
			t.Fatal(e)
		}
		select {
		case <-archiveReached:
		case <-time.After(10 * time.Second):
			close(resumeArchive)
			t.Fatal("archive hook was not reached")
		}
		_, e = db.Store.CancelRuntimeRun(ctx, actor.ID, space.ID, racing.ID)
		runtime.Cancel(racing.ID)
		close(resumeArchive)
		if e != nil {
			t.Fatal(e)
		}
		racing = wait(racing.ID)
		if racing.Status != "cancelled" || racing.FinalResponseEventID != "" || racing.WorkspaceSnapshotID == "" {
			t.Fatal("late cancellation lost", racing.Status, racing.Error)
		}
		var finishes int
		db.Admin.DB.QueryRow("SELECT COUNT(*) FROM runs WHERE id=? AND finished_at IS NOT NULL", racing.ID).Scan(&finishes)
		if finishes != 1 {
			t.Fatal("duplicate finish", finishes)
		}
		// Snapshot-only outage must change success to failure and block new work.
		faults.fail.Store(true)
		failed, e := runtime.Service.Submit(ctx, actor.ID, space.ID, session.ID, sessionapp.SubmitInput{Key: uuid.NewString(), Text: "second snapshot failure"})
		if e != nil {
			t.Fatal(e)
		}
		failed = wait(failed.ID)
		if failed.Status != "failed" || failed.Error != "workspace_snapshot_failed" || failed.SnapshotError == "" {
			t.Fatal("snapshot failure reported success", failed.Status, failed.Error)
		}
		withoutSnapshot := getDiagnostics("?run_id="+failed.ID, token, 200)
		if withoutSnapshot.Snapshot != nil || withoutSnapshot.Run.Error != failed.Error {
			t.Fatal("diagnostics lost the failed run without a snapshot")
		}
		if _, e = runtime.Service.Submit(ctx, actor.ID, space.ID, session.ID, sessionapp.SubmitInput{Key: uuid.NewString(), Text: "second blocked"}); !errors.Is(e, storage.ErrWorkspace) {
			t.Fatal("new run accepted while workspace failed", e)
		}
		faults.fail.Store(false)
		plan, e := db.Store.RuntimeCleanupPlan(ctx, actor.ID, space.ID, failed.ID)
		if e != nil {
			t.Fatal(e)
		}
		snapshot, e := runtime.Finisher.Retry(ctx, actor.ID, failed.Snapshot, plan.At, snapshots)
		if e != nil || snapshot.ID == "" {
			t.Fatal("snapshot-only retry", e)
		}
		unchanged, _ := db.Store.RuntimeRun(ctx, actor.ID, space.ID, failed.ID)
		if unchanged.Status != "failed" {
			t.Fatal("retry rewrote old terminal state")
		}
		// Revocation stops the principal, while a scoped system capability saves existing files.
		bob, e := auth.CreateUser(ctx, "bob", "correct-password-bob")
		if e != nil {
			t.Fatal(e)
		}
		if e = auth.AddMember(ctx, actor.ID, space.ID, "bob"); e != nil {
			t.Fatal(e)
		}
		_, bobToken, _, e := auth.Login(ctx, "bob", "correct-password-bob")
		if e != nil {
			t.Fatal(e)
		}
		getDiagnostics(bySnapshot, bobToken, 200)
		revoked, e := runtime.Service.Submit(ctx, bob.ID, space.ID, session.ID, sessionapp.SubmitInput{Key: uuid.NewString(), Text: "wait for revocation"})
		if e != nil {
			t.Fatal(e)
		}
		for i := 0; i < 200; i++ {
			var n int
			db.Admin.DB.QueryRow("SELECT COUNT(*) FROM model_attempts WHERE space_id=? AND run_id=?", space.ID, revoked.ID).Scan(&n)
			if n > 0 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if e = auth.RemoveMember(ctx, actor.ID, space.ID, bob.ID); e != nil {
			t.Fatal(e)
		}
		revoked = wait(revoked.ID)
		if revoked.Status != "failed" || revoked.Error != "permission_revoked" || revoked.WorkspaceSnapshotID == "" {
			t.Fatal("revocation cleanup", revoked.Status, revoked.Error, revoked.SnapshotError)
		}
		if _, e = db.Store.RuntimeRun(ctx, bob.ID, space.ID, revoked.ID); !errors.Is(e, identity.ErrForbidden) {
			t.Fatal("revoked subject can read run", e)
		}
		getDiagnostics(bySnapshot, bobToken, 404)
		getDiagnostics(byRun, bobToken, 404)
		forged := journal.New(actor.ID, "workspace.snapshot.failed", first.ID, journal.FailureData{Reason: "forged"})
		forged.Actor.Kind = "system"
		if _, e = resource.AppendRun(ctx, j, first.Snapshot, forged); !errors.Is(e, identity.ErrForbidden) {
			t.Fatal("forged system actor accepted", e)
		}
		// A failed semantic journal write aborts this attempt; it is never retried invisibly.
		if _, e = db.Admin.DB.Exec("CREATE TRIGGER reject_response BEFORE INSERT ON messages FOR EACH ROW BEGIN IF NEW.role='assistant' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='injected'; END IF; END"); e != nil {
			t.Fatal(e)
		}
		broken, e := runtime.Service.Submit(ctx, actor.ID, space.ID, session.ID, sessionapp.SubmitInput{Key: uuid.NewString(), Text: "second log failure"})
		if e != nil {
			t.Fatal(e)
		}
		broken = wait(broken.ID)
		if broken.Status != "failed" || broken.FinalResponseEventID != "" {
			t.Fatal("journal failure produced final response", broken.Status, broken.Error)
		}
		if _, e = db.Admin.DB.Exec("DROP TRIGGER reject_response"); e != nil {
			t.Fatal(e)
		}
		// Capacity rejection happens before a model request and does not truncate inputs.
		overflow, e := runtime.Service.Submit(ctx, actor.ID, space.ID, session.ID, sessionapp.SubmitInput{Key: uuid.NewString(), Text: strings.Repeat("x", 26000)})
		if e != nil {
			t.Fatal(e)
		}
		overflow = wait(overflow.ID)
		if overflow.Error != "context_overflow" {
			t.Fatal("overflow", overflow.Status, overflow.Error)
		}
		var attempts int
		db.Admin.DB.QueryRow("SELECT COUNT(*) FROM model_attempts WHERE space_id=? AND run_id=?", space.ID, overflow.ID).Scan(&attempts)
		if attempts != 0 {
			t.Fatal("overflow still called model")
		}

		// If the finish transaction fails, neither the snapshot nor occupancy is committed.
		if _, e = db.Admin.DB.Exec("CREATE TRIGGER reject_finish BEFORE UPDATE ON runs FOR EACH ROW BEGIN IF NEW.finished_at IS NOT NULL AND OLD.finished_at IS NULL THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='injected'; END IF; END"); e != nil {
			t.Fatal(e)
		}
		stuck, e := runtime.Service.Submit(ctx, actor.ID, space.ID, session.ID, sessionapp.SubmitInput{Key: uuid.NewString(), Text: "second finish failure"})
		if e != nil {
			t.Fatal(e)
		}
		stopped := false
		for i := 0; i < 1000; i++ {
			if runtime.Service.Ready(ctx) != nil {
				stopped = true
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !stopped {
			t.Fatal("finish failure did not stop dispatcher")
		}
		stuck, e = db.Store.RuntimeRun(ctx, actor.ID, space.ID, stuck.ID)
		if e != nil {
			t.Fatal(e)
		}
		var occupancy int
		db.Admin.DB.QueryRow("SELECT COUNT(*) FROM session_active_runs WHERE run_id=?", stuck.ID).Scan(&occupancy)
		if run.Terminal(stuck.Status) || stuck.WorkspaceSnapshotID != "" || occupancy != 1 {
			t.Fatal("finish failure released occupancy or committed snapshot", stuck.Status, occupancy)
		}
		if _, e = db.Admin.DB.Exec("DROP TRIGGER reject_finish"); e != nil {
			t.Fatal(e)
		}
		if e = runtime.Finisher.Finish(ctx, stuck.Snapshot, run.Outcome{Status: "failed", Error: "interrupted"}, true); e != nil {
			t.Fatal("retry fixed cleanup plan", e)
		}
		wait(stuck.ID)
	}
	stop, cancelStop := context.WithTimeout(ctx, 10*time.Second)
	err = lifecycle.Close(stop)
	cancelStop()
	if err != nil {
		t.Fatal(err)
	}

	t.Log("runtime vertical: persisted model/tool/file/artifact/snapshot, next-run restore, independent clone, idempotency, cancellation and continuous SSE passed")
}

var _ modelgateway.Transport = (*einoadapter.ModelTransport)(nil)

func TestRuntimeActualEinoWire(t *testing.T) {
	if os.Getenv("MASS_TEST_ADMIN_DSN") == "" {
		t.Skip("requires isolated MariaDB")
	}
	exerciseRuntime(t, fakes.NewBlobs(), "test/runtime-wire/", true)
}
