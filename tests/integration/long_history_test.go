package integration

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/app/catalog"
	"github.com/imalimin/mass/internal/app/identity"
	sessionapp "github.com/imalimin/mass/internal/app/session"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/journal"
	"github.com/imalimin/mass/internal/resource"
	"github.com/imalimin/mass/internal/runtime/context/projector"
	transport "github.com/imalimin/mass/internal/transport/http"
)

func longHistoryAgent(t *testing.T, r *faultRig, space string) (string, string) {
	t.Helper()
	ctx := context.Background()
	m, err := r.auth.SaveModel(ctx, r.actor, space, "", identity.Model{Name: "long fixture", Provider: "ark", BaseURL: identity.ArkEndpoint, ModelID: "fixture", APIKey: "not-a-real-key", ContextWindow: 32768, MaxOutputTokens: 2048, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	svc := &catalog.Service{Repo: r.db.Store, Content: r.content}
	d, err := svc.Create(ctx, r.actor, space, "agent", "long-history")
	if err != nil {
		t.Fatal(err)
	}
	cfg := catalog.Config{SchemaVersion: 1, Name: d.Name, Model: m.ID, Skills: []catalog.SkillRef{}, Tools: []string{"write_file", "read_file", "publish_artifact"}}
	cfg.Context.MaxInputTokens = 24000
	cfg.Context.MaxTokens = 512
	cfg.Context.Compression = "summary"
	cfg.Limits.MaxSteps = 8
	cfg.Limits.TimeoutSeconds = 60
	raw, _ := json.Marshal(cfg)
	d, err = svc.Save(ctx, r.actor, space, "agent", d.ID, d.Revision, catalog.Document{Markdown: "Preserve history and files.", ConfigYAML: string(raw)})
	if err != nil {
		t.Fatal(err)
	}
	v, err := svc.Publish(ctx, r.actor, space, "agent", d.ID, "v1", d.Revision, false)
	if err != nil {
		t.Fatal(err)
	}
	return d.ID, v.ID
}

func seedLongHistory(t *testing.T, r *faultRig, s run.Session, count int) {
	t.Helper()
	j := &journal.Journal{Repo: r.db.Store}
	for remaining := count; remaining > 0; {
		n := min(remaining, 100)
		events := make([]journal.Proposed, n)
		for i := range events {
			events[i] = journal.New(r.actor, "workspace.restored", "", journal.RestoreData{Reason: "explicit_restore"})
		}
		if _, err := j.Append(context.Background(), r.actor, journal.Request{SpaceID: r.space, SessionID: s.ID, AppendKey: uuid.NewString(), Events: events}); err != nil {
			t.Fatal(err)
		}
		remaining -= n
	}
}

func TestLongHistoryContinuesAndRecovers(t *testing.T) {
	r := newFaultRig(t)
	ctx := context.Background()
	wait := func(id string) run.View {
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			v, err := r.db.Store.RuntimeRun(ctx, r.actor, r.space, id)
			if err != nil {
				t.Fatal(err)
			}
			if run.Terminal(v.Status) {
				return v
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("long run did not terminate")
		return run.View{}
	}
	r.agent, r.version = longHistoryAgent(t, r, r.space)
	s := r.session()
	// Cross both the old History limit and the old AG-UI catch-up iteration limit.
	seedLongHistory(t, r, s, 30100)
	v := wait(r.submit(s, "artifact after long history").ID)
	if v.Status != "succeeded" {
		t.Fatal(v.Status, v.Error)
	}
	r.terminalOnce(v)
	cp, err := r.runtime.UI.CatchUp(ctx, r.actor, r.space, v.ID)
	if err != nil || !cp.Closed || len(cp.State.Artifacts) != 1 {
		t.Fatal("long UI", err)
	}
	facts, ranges, through, err := projector.ReadHistory(ctx, &journal.Journal{Repo: r.db.Store}, v.Snapshot)
	if err != nil || through <= 30100 || len(facts) > 200 || len(ranges) == 0 {
		t.Fatal("semantic history", len(facts), len(ranges), through, err)
	}
	var saved run.Prepared
	for _, e := range facts {
		if e.Type == "context.projected" {
			var d journal.ProjectionData
			journal.Decode(e.Data, &d)
			if d.Purpose == "task" {
				if err = resource.ReadJSON(ctx, r.content, r.actor, r.space, d.ContentID, 32<<20, &saved); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	// Full and paged replay agree on the retained messages and validate range digests.
	all := []journal.Event{}
	if _, err = resource.WalkHistory(ctx, &journal.Journal{Repo: r.db.Store}, r.actor, r.space, s.ID, func(e journal.Event) error { all = append(all, e); return nil }); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := (&projector.Projector{Content: r.content}).Build(ctx, v.Snapshot, all, saved.Tools, saved.Spec)
	paged, pageErr := (&projector.Projector{Content: r.content}).Build(ctx, v.Snapshot, facts, saved.Tools, saved.Spec)
	if err != nil || pageErr != nil || rebuilt.Fingerprint != paged.Fingerprint {
		t.Fatal("full replay differs", err, pageErr)
	}
	legacy := saved.Spec
	legacy.Renderer = projector.LegacyRenderer
	legacy.AuditRanges = nil
	oldReplay, err := (&projector.Projector{Content: r.content}).Build(ctx, v.Snapshot, all, saved.Tools, legacy)
	if err != nil || oldReplay.ContentDigest != rebuilt.ContentDigest {
		t.Fatal("v2 replay compatibility", err)
	}
	_, token, _, err := r.auth.Login(ctx, "alice", "correct-password-alice")
	if err != nil {
		t.Fatal(err)
	}
	h := transport.NewHandler(fstest.MapFS{"index.html": {Data: []byte("MASS")}}, "long", slog.New(slog.NewTextHandler(io.Discard, nil)), transport.IdentityOptions{Service: r.auth, Runtime: r.runtime, Storage: &transport.StorageOptions{Content: r.content, Journal: &journal.Journal{Repo: r.db.Store}, Check: func(context.Context) error { return nil }}})
	req := httptest.NewRequest("GET", "http://mass.test/api/v1/spaces/"+r.space+"/runs/"+v.ID+"/contexts/"+saved.ID, nil)
	req.AddCookie(&http.Cookie{Name: "mass_session", Value: token})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal("long context inspection", w.Code, w.Body.String())
	}
	other, err := r.auth.CreateSpace(ctx, r.actor, "unaffected space")
	if err != nil {
		t.Fatal(err)
	}
	agent, version := longHistoryAgent(t, r, other.ID)
	otherSession, err := r.runtime.Service.Create(ctx, r.actor, other.ID, sessionapp.CreateInput{Key: uuid.NewString(), AgentID: agent, VersionID: version})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.db.Store.ReadRunEvents(ctx, r.actor, r.space, otherSession.ID, v.ID, 0, nil, 100); err == nil {
		t.Fatal("run reader accepted another Session")
	}
	if _, err = r.db.Store.ReadRunEvents(ctx, r.actor, other.ID, otherSession.ID, v.ID, 0, nil, 100); err == nil {
		t.Fatal("run reader accepted another space")
	}
	otherRun, err := r.runtime.Service.Submit(ctx, r.actor, other.ID, otherSession.ID, sessionapp.SubmitInput{Key: uuid.NewString(), Text: "unaffected task"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !run.Terminal(otherRun.Status) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		otherRun, err = r.db.Store.RuntimeRun(ctx, r.actor, other.ID, otherRun.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if otherRun.Status != "succeeded" {
		t.Fatal("other space blocked", otherRun.Status, otherRun.Error)
	}
	if err = r.runtime.Service.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	if err = r.close(ctx); err != nil {
		t.Fatal(err)
	}
	r.close = nil
	// Leave a legitimately claimed run under an exited owner, then restart.
	r.runtime.Service.Ready = nil
	pending, err := r.runtime.Service.Submit(ctx, r.actor, r.space, s.ID, sessionapp.SubmitInput{Key: uuid.NewString(), Text: "must not replay"})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := r.db.Store.OpenRuntime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = lease.Claim(ctx, pending.Snapshot); err != nil {
		lease.Close()
		t.Fatal(err)
	}
	if err = lease.Close(); err != nil {
		t.Fatal(err)
	}
	before := r.model.calls.Load()
	r.start()
	interrupted := wait(pending.ID)
	if interrupted.Status != "failed" || interrupted.Error != "interrupted" || r.model.calls.Load() != before {
		t.Fatal("restart replayed or blocked", interrupted.Status, interrupted.Error)
	}
	r.terminalOnce(interrupted)
	if err = r.runtime.Service.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	if err = r.db.Store.BeginWorkspaceMaintenance(ctx, r.actor, r.space, s.ID); err != nil {
		t.Fatal(err)
	}
	err = r.runtime.Workspaces.RestoreIdle(ctx, r.actor, r.space, s.ID, r.runtime.Snapshots)
	endErr := r.db.Store.EndWorkspaceMaintenance(ctx, r.actor, r.space, s.ID)
	if err != nil || endErr != nil {
		t.Fatal("restore after interrupted long history", err, endErr)
	}
	continued := wait(r.submit(s, "continued after restart").ID)
	if continued.Status != "succeeded" {
		t.Fatal(continued.Status, continued.Error)
	}
	if err = r.manager.With(ctx, r.space, s.ID, func(dir resource.WorkspaceDir) error {
		f, e := dir.Root().Open("retained.txt")
		if e != nil {
			return e
		}
		defer f.Close()
		raw, e := io.ReadAll(f)
		if e == nil && string(raw) != "retained D09" {
			t.Fatal("confirmed files lost")
		}
		return e
	}); err != nil {
		t.Fatal(err)
	}
	failed := wait(r.submit(s, strings.Repeat("necessary input ", 4000)).ID)
	if failed.Status != "failed" || failed.Error != run.ErrOverflow.Error() {
		t.Fatal("overflow must terminate only its run", failed.Status, failed.Error)
	}
	r.terminalOnce(failed)
	otherRun, err = r.runtime.Service.Submit(ctx, r.actor, other.ID, otherSession.ID, sessionapp.SubmitInput{Key: uuid.NewString(), Text: "unaffected after overflow"})
	if err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(10 * time.Second)
	for !run.Terminal(otherRun.Status) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		otherRun, err = r.db.Store.RuntimeRun(ctx, r.actor, other.ID, otherRun.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if otherRun.Status != "succeeded" {
		t.Fatal("overflow affected other space", otherRun.Status, otherRun.Error)
	}
	t.Log("30100 historical events: task/artifact/Trace/replay, isolated overflow, other space and interrupted restart passed")
}
