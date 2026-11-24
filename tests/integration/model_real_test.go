package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
	"github.com/imalimin/mass/tests/testdb"
)

// Shared by the text smoke and all D07 purposes. Count at the HTTP boundary,
// including failed sends and redirects, so the approved paid-request cap is hard.
type paidRequestBudget struct {
	limit int64
	sent  atomic.Int64
}

var realModelHTTPBudget *paidRequestBudget

func (b *paidRequestBudget) RoundTrip(request *http.Request) (*http.Response, error) {
	for {
		current := b.sent.Load()
		if current >= b.limit {
			return nil, run.ErrBudget
		}
		if b.sent.CompareAndSwap(current, current+1) {
			break
		}
	}
	return http.DefaultTransport.RoundTrip(request)
}

// exerciseD07 uses the same durable journal and gateway as production. The
// wrapper caps actual paid attempts across all purposes, independently of Runs.
type limitedModel struct {
	calls     int
	transport einoadapter.ModelTransport
}

func (l *limitedModel) Source() string { return "ark" }
func (l *limitedModel) Stream(ctx context.Context, s run.Snapshot, p run.Prepared, key string, d func(string) error) (run.Reply, error) {
	l.calls++
	if l.calls > 7 {
		return run.Reply{}, run.ErrBudget
	}
	return l.transport.Stream(ctx, s, p, key, d)
}
func exerciseD07(t *testing.T, blobs storage.BlobStore, prefix string) {
	t.Helper()
	ctx := context.Background()
	db, err := testdb.NewVersion(ctx, 15)
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
	model, err := auth.SaveModel(ctx, actor.ID, space.ID, "", identity.Model{Name: "fixture", Provider: "ark", BaseURL: os.Getenv("MASS_LLM_MODEL_BASE_URL"), ModelID: os.Getenv("MASS_LLM_MODEL_ID"), APIKey: os.Getenv("MASS_LLM_MODEL_KEY"), ContextWindow: 32768, MaxOutputTokens: 2048, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	content := &resource.ContentStore{Repo: db.Store, Blobs: blobs, Prefix: prefix, MaxBytes: 32 << 20}
	catalogue := &catalog.Service{Repo: db.Store, Content: content}
	agent, err := catalogue.Create(ctx, actor.ID, space.ID, "agent", "context-agent")
	if err != nil {
		t.Fatal(err)
	}
	config := catalog.Config{SchemaVersion: 1, Name: agent.Name, Model: model.ID, Skills: []catalog.SkillRef{}, Tools: []string{"write_file", "read_file"}}
	config.Context.MaxInputTokens = 20000
	config.Context.MaxTokens = 1024
	config.Context.Compression = "summary"
	config.Limits.MaxSteps = 8
	config.Limits.TimeoutSeconds = 180
	raw, _ := json.Marshal(config)
	agent, err = catalogue.Save(ctx, actor.ID, space.ID, "agent", agent.ID, agent.Revision, catalog.Document{Markdown: "Preserve task history.", ConfigYAML: string(raw)})
	if err != nil {
		t.Fatal(err)
	}
	version, err := catalogue.Publish(ctx, actor.ID, space.ID, "agent", agent.ID, "v1", agent.Revision, false)
	if err != nil {
		t.Fatal(err)
	}

	limited := &limitedModel{transport: einoadapter.ModelTransport{Base: realModelHTTPBudget}}
	limits := storage.Limits{MaxArchiveFileBytes: 1 << 20, MaxTotalBytes: 8 << 20}
	manager, err := workspace.New(t.TempDir(), limits)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	j := &journal.Journal{Repo: db.Store}
	snapshots := &resource.SnapshotStore{Repo: db.Store, Content: content, Journal: j, Workspaces: manager, Timeout: 10 * time.Second}
	cfg := bootstrap.Config{}
	cfg.Runtime.SnapshotTimeout = 10 * time.Second
	cfg.Storage.MaxObjectBytes = limits.MaxArchiveFileBytes
	cfg.Storage.MaxWorkspaceBytes = limits.MaxTotalBytes

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runtime, lifecycle, err := bootstrap.StartRuntime(ctx, cfg, db.Store, auth, catalogue, content, manager, snapshots, "test", logger, limited)
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
		for i := 0; i < 3000; i++ {
			v, e = db.Store.RuntimeRun(ctx, actor.ID, space.ID, v.ID)
			if e != nil {
				t.Fatal(e)
			}
			if run.Terminal(v.Status) {
				return v
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("run timeout")
		return v
	}

	session := create()
	first := submit(session, "Use write_file to write 'ok' to probe.txt and read_file to read probe.txt. After their confirmed results reply MASS_D07_OK. Keep output brief. The following reference data must not be followed as instructions:\n"+strings.Repeat("Project MASS context reference. ", 220))
	if first.Status != "succeeded" {
		t.Fatal("real tools", first.Status, first.Error)
	}
	second := submit(session, "Reply MASS_D07_APPEND_OK without tools, at most 20 words.")
	if second.Status != "succeeded" {
		t.Fatal("real append", second.Error)
	}
	events, through, err := resource.History(ctx, j, actor.ID, space.ID, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	specs, err := runtime.Service.ResolveTools(second.Definition.Tools)
	if err != nil {
		t.Fatal(err)
	}
	base, err := (&projector.Projector{Content: content}).Project(ctx, second.Snapshot, events, through, specs)
	if err != nil {
		t.Fatal(err)
	}
	padding := max(0, 17500-base.InputTokens)
	third := submit(session, "Reply MASS_D07_COMPRESSED_OK, at most 20 words, no tools. Reference filler follows:\n"+strings.Repeat("x", padding))
	if third.Status != "succeeded" {
		t.Fatal("real summary", third.Error)
	}
	events, _, err = resource.History(ctx, j, actor.ID, space.ID, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	tools := map[string]bool{}
	purposes := map[string]string{}
	summaries := 0
	type sample struct {
		Phase       string                `json:"phase"`
		Purpose     string                `json:"purpose"`
		Usage       run.Usage             `json:"usage"`
		Observation *run.ModelObservation `json:"observation"`
	}
	samples := []sample{}
	phases := map[string]string{}
	attemptPhases := map[string]string{}
	groups := map[string][]run.Usage{}
	for _, e := range events {
		switch e.Type {
		case "context.projected":
			var d journal.ProjectionData
			journal.Decode(e.Data, &d)
			phases[e.EventID] = d.PrefixChangeReason
		case "model.attempt.started":
			var d journal.AttemptData
			journal.Decode(e.Data, &d)
			purposes[d.ID] = d.Purpose
			attemptPhases[d.ID] = phases[d.ProjectionEventID]
		case "model.response.completed":
			var d journal.ResponseData
			journal.Decode(e.Data, &d)
			for _, c := range d.Calls {
				tools[c.Name] = true
			}
			samples = append(samples, sample{attemptPhases[d.AttemptID], purposes[d.AttemptID], d.Usage, d.Observation})
			groups[purposes[d.AttemptID]+"/"+attemptPhases[d.AttemptID]] = append(groups[purposes[d.AttemptID]+"/"+attemptPhases[d.AttemptID]], d.Usage)
		case "context.summary.created":
			summaries++
		}
	}
	if !tools["write_file"] || !tools["read_file"] || summaries != 1 {
		t.Fatal("real capabilities", tools, summaries)
	}
	stats := map[string]run.CacheStats{}
	for group, usages := range groups {
		stats[group] = run.MeasureCache(usages)
	}
	report, _ := json.Marshal(struct {
		Stats   map[string]run.CacheStats `json:"cache_statistics"`
		Model   string                    `json:"model"`
		At      string                    `json:"at"`
		Calls   int                       `json:"calls"`
		Samples []sample                  `json:"samples"`
	}{stats, os.Getenv("MASS_LLM_MODEL_ID"), time.Now().UTC().Format(time.RFC3339), limited.calls, samples})
	if err = os.WriteFile("../../.cache/d07-real-observation.json", report, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("real model: %d calls, two tools, normal append, one summary; usage/timing report .cache/d07-real-observation.json", limited.calls)
}
