package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	"github.com/imalimin/mass/internal/runtime/tool/registry"
	transport "github.com/imalimin/mass/internal/transport/http"
	"github.com/imalimin/mass/tests/fakes"
	"github.com/imalimin/mass/tests/testdb"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"
)

func TestToolsVertical(t *testing.T) {
	if os.Getenv("MASS_TEST_ADMIN_DSN") == "" {
		t.Skip("requires isolated MariaDB")
	}
	exerciseTools(t, fakes.NewBlobs(), "test/tools/")
}
func exerciseTools(t *testing.T, blobs storage.BlobStore, prefix string) {
	t.Helper()
	ctx := context.Background()
	db, err := testdb.NewVersion(ctx, 5)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Admin.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	cipher, _ := secrets.New(bytes.Repeat([]byte{9}, 32))
	auth, _ := identity.New(db.Store, cipher, time.Hour, []byte("tools-test"))
	actor, err := auth.CreateUser(ctx, "alice", "correct-password-alice")
	if err != nil {
		t.Fatal(err)
	}
	space, err := auth.CreateSpace(ctx, actor.ID, "tools")
	if err != nil {
		t.Fatal(err)
	}
	model, err := auth.SaveModel(ctx, actor.ID, space.ID, "", identity.Model{Name: "fixture", Provider: "ark", BaseURL: identity.ArkEndpoint, ModelID: "fixture", APIKey: "fixture-key", ContextWindow: 262144, MaxOutputTokens: 4096, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	content := &resource.ContentStore{Repo: db.Store, Blobs: blobs, Prefix: prefix, MaxBytes: 32 << 20}
	catalogue := &catalog.Service{Repo: db.Store, Content: content}
	skill, err := catalogue.Create(ctx, actor.ID, space.ID, "skill", "rules")
	if err != nil {
		t.Fatal(err)
	}
	skill, err = catalogue.Save(ctx, actor.ID, space.ID, "skill", skill.ID, skill.Revision, catalog.Document{Markdown: "Read the multiplier from assets/rules.txt.", Assets: []catalog.Asset{}})
	if err != nil {
		t.Fatal(err)
	}
	skill, err = catalogue.Upload(ctx, actor.ID, space.ID, "skill", skill.ID, "assets/rules.txt", "text/plain", skill.Revision, strings.NewReader("2"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := blobs.(*fakes.Blobs); ok {
		// A valid resource inventory may exceed the Journal's 64 KiB event limit.
		// Aliases share immutable content while preserving 100 distinct logical paths.
		asset := skill.Document.Assets[0]
		for i := 1; i < 100; i++ {
			alias := asset
			alias.Path = fmt.Sprintf("assets/%0490d.txt", i)
			skill.Document.Assets = append(skill.Document.Assets, alias)
		}
		skill, err = catalogue.Save(ctx, actor.ID, space.ID, "skill", skill.ID, skill.Revision, skill.Document)
		if err != nil {
			t.Fatal(err)
		}
	}
	skillVersion, err := catalogue.Publish(ctx, actor.ID, space.ID, "skill", skill.ID, "v1", skill.Revision, false)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := catalogue.Create(ctx, actor.ID, space.ID, "agent", "tools-agent")
	if err != nil {
		t.Fatal(err)
	}
	config := catalog.Config{SchemaVersion: 1, Name: agent.Name, Model: model.ID, Skills: []catalog.SkillRef{{ID: skill.ID, Version: "v1"}}, Tools: []string{}}
	for _, spec := range registry.Specs() {
		config.Tools = append(config.Tools, spec.Name)
	}
	config.Context.MaxInputTokens = 196000
	config.Context.ReservedOutputTokens = 1024
	config.Context.Compression = "summary"
	config.Limits.MaxSteps = 40
	config.Limits.TimeoutSeconds = 300
	raw, _ := json.Marshal(config)
	agent, err = catalogue.Save(ctx, actor.ID, space.ID, "agent", agent.ID, agent.Revision, catalog.Document{Markdown: "Use the selected tools and preserve source provenance.", ConfigYAML: string(raw), Assets: []catalog.Asset{}})
	if err != nil {
		t.Fatal(err)
	}
	version, err := catalogue.Publish(ctx, actor.ID, space.ID, "agent", agent.ID, "v1", agent.Revision, false)
	if err != nil {
		t.Fatal(err)
	}
	// Repeated migration preserves the fixed tool definition.
	for i := 0; i < 2; i++ {
		if err = db.Admin.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.Store.CheckSchema(ctx); err != nil {
		t.Fatal(err)
	}
	limits := storage.Limits{MaxFileBytes: 1 << 20, MaxTotalBytes: 8 << 20, MaxEntries: 1000}
	manager, err := workspace.New(t.TempDir(), limits)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	j := &journal.Journal{Repo: db.Store}
	snapshots := &resource.SnapshotStore{Repo: db.Store, Content: content, Journal: j, Workspaces: manager, Timeout: 60 * time.Second}
	requests := atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer fixture-network-secret" || r.URL.Query().Get("token") != "fixture-query-secret" {
			t.Error("network arguments changed")
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != "5" {
			t.Error("network body", string(body))
		}
		w.Header().Set("Set-Cookie", "fixture-response-secret")
		w.WriteHeader(201)
		io.WriteString(w, `{"accepted":true}`)
	}))
	defer server.Close()
	modelCalls := atomic.Int32{}
	fake := &einoadapter.ModelTransport{Fake: &einoadapter.FakeChatModel{Respond: func(ctx context.Context, messages []*schema.Message) ([]*schema.Message, error) {
		modelCalls.Add(1)
		task := ""
		lastUser := 0
		for i, m := range messages {
			if m.Role == schema.User && !strings.HasPrefix(m.Content, "[MASS") {
				task = m.Content
				lastUser = i
			}
		}
		results := []run.ToolResult{}
		for _, m := range messages[lastUser+1:] {
			if m.Role == schema.Tool {
				var r run.ToolResult
				if json.Unmarshal([]byte(m.Content), &r) != nil {
					return nil, fmt.Errorf("invalid tool result")
				}
				results = append(results, r)
			}
		}
		if strings.Contains(task, "code error") {
			if len(results) == 0 {
				code := fmt.Sprintf(`fs.writeFile('kept.txt','preserved',{expected_sha256:'missing'});net.request({url:%q,method:'POST',headers:{Authorization:'Bearer fixture-network-secret'},body:'5'});throw Error('after effects');`, server.URL+"?token=fixture-query-secret")
				return toolReply("run_code", map[string]any{"language": "javascript", "code": code, "input": nil}), nil
			}
			if results[0].Status != "failed" || results[0].SideEffect != "committed" {
				t.Error("exception erased effects", results[0])
			}
			return textReply("preserved after code error"), nil
		}
		if strings.Contains(task, "host methods") {
			if len(results) == 0 {
				return toolReply("run_code", map[string]any{"language": "javascript", "input": nil, "code": `const a=fs.writeFile('host.txt','before',{expected_sha256:'missing'});const b=fs.editFile('host.txt',[{old_text:'before',new_text:'after'}],{expected_sha256:a.sha256});fs.mkdir('hostdir');const c=fs.rename('host.txt','hostdir/host.txt',{expected_sha256:b.sha256});const names=fs.list('hostdir');if(names.items.length!==1)throw Error('list');if(fs.readFile('hostdir/host.txt',{expected_sha256:c.sha256}).content!=='after')throw Error('read');fs.remove('hostdir/host.txt',{expected_sha256:c.sha256});if(fs.list('hostdir').items.length!==0)throw Error('remove');return {ok:true};`}), nil
			}
			if results[0].Status != "completed" {
				t.Error("host method failed", string(results[0].Data))
			}
			return textReply("all host methods completed"), nil
		}
		if strings.Contains(task, "host quota") {
			if len(results) == 0 {
				code := `for(let i=0;i<13;i++)fs.readFile('records.json');return true;`
				if strings.Contains(task, "bytes") {
					code = `fs.writeFile('bytes.txt','x'.repeat(300000),{expected_sha256:'missing'});for(let i=0;i<10;i++)fs.readFile('bytes.txt');return true;`
				}
				if strings.Contains(task, "network") {
					code = fmt.Sprintf(`for(let i=0;i<3;i++)net.request({url:%q,method:'POST',headers:{Authorization:'Bearer fixture-network-secret'},body:'5'});return true;`, server.URL+"?token=fixture-query-secret")
				}
				return toolReply("run_code", map[string]any{"language": "javascript", "input": nil, "code": code}), nil
			}
			var data map[string]any
			json.Unmarshal(results[0].Data, &data)
			expectedIO, effect := float64(12), "none"
			if strings.Contains(task, "bytes") {
				expectedIO, effect = 7, "committed"
			}
			if strings.Contains(task, "network") {
				expectedIO, effect = 2, "committed"
			}
			if results[0].Status != "failed" || results[0].SideEffect != effect || data["io_count"] != expectedIO || data["io_complete"] != true {
				t.Error("host quota bypassed", results[0].Status, results[0].SideEffect, string(results[0].Data))
			}
			return textReply("host quota acknowledged"), nil
		}
		if strings.Contains(task, "large code") {
			if len(results) == 0 {
				return toolReply("run_code", map[string]any{"language": "javascript", "input": nil, "code": "//" + strings.Repeat("x", 33000) + "\nreturn 1;"}), nil
			}
			if results[0].Status != "completed" {
				t.Error("valid source over 32 KiB rejected", results[0].Status)
			}
			return textReply("large source completed"), nil
		}
		if strings.Contains(task, "attachment fixtures") {
			return textReply("attachments accepted"), nil
		}
		if strings.Contains(task, "unknown io") {
			return toolReply("run_code", map[string]any{"language": "javascript", "code": `fs.writeFile('unknown.txt','once',{expected_sha256:'missing'});return 1;`, "input": nil}), nil
		}
		if strings.Contains(task, "plan failure") {
			return toolReply("update_plan", map[string]any{"expected_revision": 0, "steps": []any{map[string]any{"id": "only", "title": "Atomic plan", "status": "in_progress"}}}), nil
		}
		if strings.Contains(task, "code limit") {
			if len(results) == 0 {
				return toolReply("run_code", map[string]any{"language": "javascript", "code": `return 'x'.repeat(2097153);`, "input": nil}), nil
			}
			if results[0].Status != "failed" || results[0].SideEffect != "none" {
				t.Error("output limit result", results[0])
			}
			return textReply("output limit acknowledged"), nil
		}
		if strings.Contains(task, "code cancel") {
			return toolReply("run_code", map[string]any{"language": "javascript", "code": `fs.writeFile('cancelled.txt','kept',{expected_sha256:'missing'});while(true){}`, "input": nil}), nil
		}
		data := func(index int) map[string]any {
			var v map[string]any
			if index < len(results) {
				json.Unmarshal(results[index].Data, &v)
			}
			return v
		}
		stringAt := func(index int, key string) string { v, _ := data(index)[key].(string); return v }
		step := len(results)
		var name string
		var args map[string]any
		switch step {
		case 0:
			name = "list_skill_resources"
			args = map[string]any{"skill_binding_id": skillVersion.ID, "limit": 1}
		case 1:
			name = "read_skill_resource"
			args = map[string]any{"skill_binding_id": skillVersion.ID, "path": "assets/rules.txt"}
		case 2:
			name = "read_attachment"
			marker := "[MASS accepted attachments; use read_attachment by ID: "
			pos := strings.Index(task, marker)
			if pos < 0 {
				return nil, fmt.Errorf("attachment inventory absent")
			}
			var list []run.Attachment
			json.Unmarshal([]byte(strings.TrimSuffix(task[pos+len(marker):], "]")), &list)
			if len(list) != 1 {
				return nil, fmt.Errorf("attachment binding missing")
			}
			args = map[string]any{"attachment_id": list[0].ID, "format": "json"}
		case 3:
			name = "write_file"
			raw, _ := json.Marshal(data(2)["value"])
			args = map[string]any{"path": "records.json", "content": string(raw), "media_type": "application/json", "expected_sha256": "missing"}
		case 4:
			name = "read_file"
			args = map[string]any{"path": "records.json", "expected_sha256": stringAt(3, "sha256")}
		case 5:
			name = "list_files"
			args = map[string]any{"directory": ".", "limit": 1}
		case 6:
			name = "glob_files"
			args = map[string]any{"pattern": "**/*.json"}
		case 7:
			name = "search_files"
			args = map[string]any{"pattern": "amount", "mode": "literal", "path_glob": "*.json"}
		case 8:
			name = "update_plan"
			args = map[string]any{"expected_revision": 0, "steps": []any{map[string]any{"id": "report", "title": "Create summary", "status": "in_progress"}}}
		case 9:
			name = "run_code"
			code := fmt.Sprintf(`let all=[];for(let i=0;i<2;i++){const file=fs.readFile('records.json');all=JSON.parse(file.content)};const total=all.reduce((s,r)=>s+r.amount,0)*Number(input.multiplier);const csv=CSV.stringify([['total'],[String(total)]]);if(CSV.parse(csv)[1][0]!==String(total))throw Error('csv');fs.mkdir('reports');const written=fs.writeFile('summary.json',JSON.stringify({total,label:'draft'}),{expected_sha256:'missing',media_type:'application/json'});const response=net.request({method:'POST',url:%q,headers:{Authorization:'Bearer fixture-network-secret'},body:String(total/2)});if(response.status!==201)throw Error('request');log({total});return {total,path:'summary.json',sha256:written.sha256};`, server.URL+"?token=fixture-query-secret")
			args = map[string]any{"language": "javascript", "code": code, "input": map[string]any{"multiplier": stringAt(1, "content")}}
		case 10:
			name = "edit_file"
			value, _ := data(9)["value"].(map[string]any)
			args = map[string]any{"path": "summary.json", "expected_sha256": value["sha256"], "edits": []any{map[string]any{"old_text": "draft", "new_text": "final"}}}
		case 11:
			name = "move_file"
			args = map[string]any{"source": "summary.json", "target": "reports/summary.json", "expected_sha256": stringAt(10, "sha256")}
		case 12:
			name = "publish_artifact"
			args = map[string]any{"path": "reports/summary.json", "expected_sha256": stringAt(11, "sha256"), "display_name": "summary.json", "media_type": "application/json"}
		case 13:
			name = "delete_file"
			args = map[string]any{"path": "reports/summary.json", "expected_sha256": stringAt(11, "sha256")}
		case 14:
			name = "update_plan"
			args = map[string]any{"expected_revision": 1, "steps": []any{map[string]any{"id": "report", "title": "Create summary", "status": "completed"}}}
		default:
			return textReply("D05 summary complete"), nil
		}
		for _, r := range results {
			if r.Status != "completed" {
				return nil, fmt.Errorf("tool failed: %s %s", r.CallID, r.Error)
			}
		}
		return toolReply(name, args), nil
	}}}
	c := bootstrap.Config{}
	c.Runtime.MaxConcurrentRuns = 2
	c.Runtime.SnapshotTimeout = 60 * time.Second
	c.Storage.MaxFileBytes = limits.MaxFileBytes
	c.Storage.MaxWorkspaceBytes = limits.MaxTotalBytes
	c.Storage.MaxEntries = limits.MaxEntries
	c.Tools.Code.Timeout = 60 * time.Second
	c.Tools.Code.MaxFileOperations = 12
	c.Tools.Code.MaxNetworkRequests = 2
	runtime, lifecycle, err := bootstrap.StartRuntime(ctx, c, db.Store, auth, catalogue, content, manager, snapshots, "d05-test", slog.New(slog.NewTextHandler(io.Discard, nil)), fake)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if e := lifecycle.Close(stop); e != nil {
			t.Error(e)
		}
	}()
	skill, err = catalogue.Upload(ctx, actor.ID, space.ID, "skill", skill.ID, "assets/rules.txt", "text/plain", skill.Revision, strings.NewReader("999"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = catalogue.Publish(ctx, actor.ID, space.ID, "skill", skill.ID, "v2", skill.Revision, false); err != nil {
		t.Fatal(err)
	}
	session, err := runtime.Service.Create(ctx, actor.ID, space.ID, sessionapp.CreateInput{Key: uuid.NewString(), AgentID: agent.ID, VersionID: version.ID})
	if err != nil {
		t.Fatal(err)
	}
	attachment, err := runtime.Service.UploadAttachment(ctx, actor.ID, space.ID, session.ID, "records.json", "application/json", strings.NewReader(`[{"amount":2},{"amount":3}]`))
	if err != nil {
		t.Fatal(err)
	}
	first, err := runtime.Service.Submit(ctx, actor.ID, space.ID, session.ID, sessionapp.SubmitInput{Key: uuid.NewString(), Text: "D05 full scenario", Attachments: []string{attachment.ID}})
	if err != nil {
		t.Fatal(err)
	}
	wait := func(id string) run.View {
		t.Helper()
		deadline := time.Now().Add(150 * time.Second)
		for time.Now().Before(deadline) {
			v, e := db.Store.RuntimeRun(ctx, actor.ID, space.ID, id)
			if e != nil {
				t.Fatal(e)
			}
			if run.Terminal(v.Status) {
				return v
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("run timeout")
		return run.View{}
	}
	first = wait(first.ID)
	if first.Status != "succeeded" || requests.Load() != 1 {
		t.Fatal("full scenario", first.Status, first.Error, requests.Load())
	}
	var count int
	db.Admin.DB.QueryRow("SELECT COUNT(DISTINCT c.name) FROM tool_executions t JOIN model_calls c ON c.space_id=t.space_id AND c.run_id=t.run_id AND c.call_id=t.call_id WHERE t.space_id=? AND t.run_id=? AND t.status='completed'", space.ID, first.ID).Scan(&count)
	if count != 14 {
		t.Fatal("tools not covered", count)
	}
	plan, err := db.Store.RuntimePlan(ctx, actor.ID, space.ID, first.ID)
	if err != nil || plan.Revision != 2 || plan.Steps[0].Status != "completed" {
		t.Fatal("plan", plan, err)
	}
	artifacts, err := db.Store.RuntimeArtifacts(ctx, actor.ID, space.ID, first.ID)
	if err != nil || len(artifacts) != 1 {
		t.Fatal(err, len(artifacts))
	}
	artifact := artifacts[0]
	payload, err := resource.ReadBytes(ctx, content, actor.ID, space.ID, artifact.ObjectID, 1<<20)
	if err != nil || string(payload) != `{"total":10,"label":"final"}` {
		t.Fatal("immutable artifact", string(payload), err)
	}
	snap, err := db.Store.Snapshot(ctx, actor.ID, space.ID, first.WorkspaceSnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	var manifest storage.Manifest
	if err = resource.ReadJSON(ctx, content, actor.ID, space.ID, snap.ManifestRef, 4<<20, &manifest); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifest.State), `"revision":2`) || !strings.Contains(string(manifest.State), artifact.ID) {
		t.Fatal("snapshot missing plan or artifact", string(manifest.State))
	}
	history, _, err := resource.History(ctx, j, actor.ID, space.ID, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	ioPhases := map[int]map[string]bool{}
	for _, event := range history {
		if event.Type != "tool.io.recorded" {
			continue
		}
		var d journal.IOData
		journal.Decode(event.Data, &d)
		if ioPhases[d.Sequence] == nil {
			ioPhases[d.Sequence] = map[string]bool{}
		}
		ioPhases[d.Sequence][d.Phase] = true
		if d.Name == "net.request" {
			raw, e := resource.ReadBytes(ctx, content, actor.ID, space.ID, d.ContentID, 4<<20)
			if e != nil {
				t.Fatal(e)
			}
			if strings.Contains(string(raw), "fixture-network-secret") || strings.Contains(string(raw), "fixture-query-secret") || strings.Contains(string(raw), "fixture-response-secret") {
				t.Fatal("network trace disclosed redacted fields")
			}
		}
	}
	if len(ioPhases) < 7 {
		t.Fatal("missing IO audit", len(ioPhases))
	}
	for sequence, phases := range ioPhases {
		if !phases["prepared"] || !phases["completed"] {
			t.Fatal("incomplete IO", sequence, phases)
		}
	}
	cp, err := runtime.UI.CatchUp(ctx, actor.ID, space.ID, first.ID)
	if err != nil || cp.State.Plan == nil || cp.State.Plan.Revision != 2 {
		t.Fatal("AGUI plan", err)
	}
	handler := transport.NewHandler(fstest.MapFS{"index.html": {Data: []byte("MASS")}}, "test", slog.New(slog.NewTextHandler(io.Discard, nil)), transport.IdentityOptions{Service: auth, Origin: "http://mass.test", Catalog: catalogue, Runtime: runtime, Storage: &transport.StorageOptions{Content: content}})
	_, token, _, _ := auth.Login(ctx, "alice", "correct-password-alice")
	validate := contractValidator(t)
	for _, path := range []string{"/spaces/" + space.ID + "/runs/" + first.ID + "/plan", "/spaces/" + space.ID + "/runs/" + first.ID + "/ui-snapshot"} {
		r := httptest.NewRequest("GET", "http://mass.test/api/v1"+path, nil)
		r.Header.Set("Cookie", "mass_session="+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		validate("GET", path, w)
	}
	hosts, err := runtime.Service.Submit(ctx, actor.ID, space.ID, session.ID, sessionapp.SubmitInput{Key: uuid.NewString(), Text: "host methods"})
	if err != nil {
		t.Fatal(err)
	}
	if hosts = wait(hosts.ID); hosts.Status != "succeeded" {
		t.Fatal("host method run", hosts.Error)
	}
	// A code exception preserves confirmed file changes and gives the model a failed result.
	broken, err := runtime.Service.Submit(ctx, actor.ID, space.ID, session.ID, sessionapp.SubmitInput{Key: uuid.NewString(), Text: "code error"})
	if err != nil {
		t.Fatal(err)
	}
	broken = wait(broken.ID)
	if broken.Status != "succeeded" {
		t.Fatal("code error recovery", broken.Error)
	}
	if requests.Load() != 2 {
		t.Fatal("request lost or repeated after exception", requests.Load())
	}
	var actual string
	err = manager.With(ctx, space.ID, session.ID, func(dir resource.WorkspaceDir) error {
		raw, e := dir.Root().ReadFile("kept.txt")
		actual = string(raw)
		return e
	})
	if err != nil || actual != "preserved" {
		t.Fatal("exception rolled back file", actual, err)
	}
	// Raw upload does not make an attachment available to a running tool.
	files := &resource.FileService{Workspaces: manager, Content: content, Cursors: auth, MaxBytes: 1 << 20, Limits: limits, Check: func(ctx context.Context, s run.Snapshot) error { return auth.CheckAccess(ctx, s.ActorID, s.SpaceID) }}
	sources := &resource.Sources{Files: files, Journal: j}
	unaccepted, err := runtime.Service.UploadAttachment(ctx, actor.ID, space.ID, session.ID, "unaccepted.txt", "text/plain", strings.NewReader("not accepted"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sources.ReadAttachment(ctx, first.Snapshot, resource.SourceInput{AttachmentID: unaccepted.ID, Format: "text"}); err == nil {
		t.Fatal("unaccepted attachment readable")
	}
	if _, err = sources.ReadSkill(ctx, first.Snapshot, resource.SourceInput{Binding: uuid.NewString(), Path: "assets/rules.txt"}); err == nil {
		t.Fatal("unbound skill readable")
	}
	otherSession, err := runtime.Service.Create(ctx, actor.ID, space.ID, sessionapp.CreateInput{Key: uuid.NewString(), AgentID: agent.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.Service.Submit(ctx, actor.ID, space.ID, otherSession.ID, sessionapp.SubmitInput{Key: uuid.NewString(), Text: "attachment fixtures", Attachments: []string{attachment.ID}}); err == nil {
		t.Fatal("cross-session attachment accepted")
	}
	wrong := first.Snapshot
	wrong.SessionID = otherSession.ID
	wrong.UserEventID = uuid.NewString()
	if _, err = sources.ReadAttachment(ctx, wrong, resource.SourceInput{AttachmentID: attachment.ID, Format: "json"}); err == nil {
		t.Fatal("cross-session attachment read")
	}
	csvFile, err := runtime.Service.UploadAttachment(ctx, actor.ID, space.ID, session.ID, "rows.csv", "text/csv", strings.NewReader("name,amount\na,2\nb,3\n"))
	if err != nil {
		t.Fatal(err)
	}
	badJSON, err := runtime.Service.UploadAttachment(ctx, actor.ID, space.ID, session.ID, "invalid.json", "application/json", strings.NewReader(`[{"valid":true},broken]`))
	if err != nil {
		t.Fatal(err)
	}
	binary, err := runtime.Service.UploadAttachment(ctx, actor.ID, space.ID, session.ID, "opaque.bin", "application/octet-stream", strings.NewReader("opaque"))
	if err != nil {
		t.Fatal(err)
	}
	bound, err := runtime.Service.Submit(ctx, actor.ID, space.ID, session.ID, sessionapp.SubmitInput{Key: uuid.NewString(), Text: "attachment fixtures", Attachments: []string{csvFile.ID, badJSON.ID, binary.ID}})
	if err != nil {
		t.Fatal(err)
	}
	bound = wait(bound.ID)
	page, err := sources.ReadAttachment(ctx, bound.Snapshot, resource.SourceInput{AttachmentID: csvFile.ID, Format: "csv", RecordLimit: 1})
	if err != nil || page.Complete {
		t.Fatal("structured pagination", err)
	}
	var pageData map[string]any
	json.Unmarshal(page.Data, &pageData)
	if pageData["next_record_offset"] != float64(1) || pageData["parser_complete"] != true {
		t.Fatal(string(page.Data))
	}
	if _, err = sources.ReadAttachment(ctx, bound.Snapshot, resource.SourceInput{AttachmentID: badJSON.ID, Format: "json"}); err == nil {
		t.Fatal("invalid full JSON accepted")
	}
	if _, err = sources.ReadAttachment(ctx, bound.Snapshot, resource.SourceInput{AttachmentID: binary.ID, Format: "text"}); err == nil {
		t.Fatal("binary attachment treated as text")
	}
	// New upload API has the same schema and authority as the application service.
	uploadPath := "/spaces/" + space.ID + "/sessions/" + session.ID + "/attachments"
	uploadBody, _ := json.Marshal(map[string]any{"name": "api.txt", "media_type": "text/plain", "content_base64": base64.StdEncoding.EncodeToString([]byte("API attachment"))})
	request := httptest.NewRequest("POST", "http://mass.test/api/v1"+uploadPath, bytes.NewReader(uploadBody))
	request.Header.Set("Cookie", "mass_session="+token)
	request.Header.Set("Origin", "http://mass.test")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 201 {
		t.Fatal("upload API", response.Code, response.Body.String())
	}
	validate("POST", uploadPath, response)
	// Limit failures are explicit tool results and preserve accurate effects.
	limited, err := runtime.Service.Submit(ctx, actor.ID, space.ID, session.ID, sessionapp.SubmitInput{Key: uuid.NewString(), Text: "code limit"})
	if err != nil {
		t.Fatal(err)
	}
	limited = wait(limited.ID)
	if limited.Status != "succeeded" {
		t.Fatal("limit handling", limited.Error)
	}
	// Cancel after a confirmed write and wait for the VM to actually leave.
	cancelling, err := runtime.Service.Submit(ctx, actor.ID, space.ID, session.ID, sessionapp.SubmitInput{Key: uuid.NewString(), Text: "code cancel"})
	if err != nil {
		t.Fatal(err)
	}
	written := false
	for i := 0; i < 1000; i++ {
		var n int
		db.Admin.DB.QueryRow("SELECT COUNT(*) FROM runtime_events WHERE run_id=? AND event_type='tool.io.recorded' AND JSON_UNQUOTE(JSON_EXTRACT(data,'$.name'))='fs.writeFile' AND JSON_UNQUOTE(JSON_EXTRACT(data,'$.phase'))='completed'", cancelling.ID).Scan(&n)
		if n > 0 {
			written = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !written {
		t.Fatal("write was not confirmed before cancel")
	}
	if _, err = db.Store.CancelRuntimeRun(ctx, actor.ID, space.ID, cancelling.ID); err != nil {
		t.Fatal(err)
	}
	runtime.Cancel(cancelling.ID)
	cancelling = wait(cancelling.ID)
	if cancelling.Status != "cancelled" || cancelling.WorkspaceSnapshotID == "" {
		t.Fatal("code cancellation", cancelling.Status, cancelling.Error)
	}
	manager.With(ctx, space.ID, session.ID, func(dir resource.WorkspaceDir) error {
		raw, e := dir.Root().ReadFile("cancelled.txt")
		if e != nil || string(raw) != "kept" {
			t.Error("cancel erased confirmed effects", e)
		}
		return nil
	})
	if _, ok := blobs.(*fakes.Blobs); ok {
		for _, task := range []string{"host quota file", "host quota network", "host quota bytes"} {
			quota, e := runtime.Service.Submit(ctx, actor.ID, space.ID, session.ID, sessionapp.SubmitInput{Key: uuid.NewString(), Text: task})
			if e != nil {
				t.Fatal(e)
			}
			if quota = wait(quota.ID); quota.Status != "succeeded" {
				t.Fatal("host quota handling", task, quota.Error)
			}
		}
		if requests.Load() != 4 {
			t.Fatal("network quota did not stop third request", requests.Load())
		}
		// A failed terminal IO record must not rerun an already performed write.
		if _, err = db.Admin.DB.Exec("CREATE TRIGGER reject_io_result BEFORE INSERT ON runtime_events FOR EACH ROW BEGIN IF NEW.event_type='tool.io.recorded' AND JSON_UNQUOTE(JSON_EXTRACT(NEW.data,'$.phase'))='completed' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='injected'; END IF; END"); err != nil {
			t.Fatal(err)
		}
		callsBefore := modelCalls.Load()
		unknown, err := runtime.Service.Submit(ctx, actor.ID, space.ID, session.ID, sessionapp.SubmitInput{Key: uuid.NewString(), Text: "unknown io"})
		if err != nil {
			t.Fatal(err)
		}
		unknown = wait(unknown.ID)
		if unknown.Status != "failed" || unknown.Error != "tool_effect_unknown" || modelCalls.Load() != callsBefore+1 {
			t.Fatal("unknown effect repeated or hidden", unknown.Status, unknown.Error)
		}
		if _, err = db.Admin.DB.Exec("DROP TRIGGER reject_io_result"); err != nil {
			t.Fatal(err)
		}
		manager.With(ctx, space.ID, session.ID, func(dir resource.WorkspaceDir) error {
			raw, e := dir.Root().ReadFile("unknown.txt")
			if e != nil || string(raw) != "once" {
				t.Error("performed effect lost", e)
			}
			return nil
		})
		// Plan and its tool result cannot commit independently.
		if _, err = db.Admin.DB.Exec("CREATE TRIGGER reject_plan_result BEFORE INSERT ON runtime_events FOR EACH ROW BEGIN IF NEW.event_type='tool.execution.completed' AND JSON_UNQUOTE(JSON_EXTRACT(NEW.data,'$.name'))='update_plan' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='injected'; END IF; END"); err != nil {
			t.Fatal(err)
		}
		rejected, err := runtime.Service.Submit(ctx, actor.ID, space.ID, session.ID, sessionapp.SubmitInput{Key: uuid.NewString(), Text: "plan failure"})
		if err != nil {
			t.Fatal(err)
		}
		rejected = wait(rejected.ID)
		if rejected.Status != "failed" {
			t.Fatal("plan commit failure hidden")
		}
		if _, err = db.Admin.DB.Exec("DROP TRIGGER reject_plan_result"); err != nil {
			t.Fatal(err)
		}
		var rows int
		db.Admin.DB.QueryRow("SELECT COUNT(*) FROM run_plans WHERE run_id=?", rejected.ID).Scan(&rows)
		if rows != 0 {
			t.Fatal("plan committed without tool result")
		}
		db.Admin.DB.QueryRow("SELECT COUNT(*) FROM runtime_events WHERE run_id=? AND event_type='plan.updated'", rejected.ID).Scan(&rows)
		if rows != 0 {
			t.Fatal("plan fact escaped rollback")
		}
		large, err := runtime.Service.Submit(ctx, actor.ID, space.ID, session.ID, sessionapp.SubmitInput{Key: uuid.NewString(), Text: "large code"})
		if err != nil {
			t.Fatal(err)
		}
		if large = wait(large.ID); large.Status != "succeeded" {
			t.Fatal("large source execution", large.Error)
		}
		largeUI, e := runtime.UI.CatchUp(ctx, actor.ID, space.ID, large.ID)
		if e != nil || len(largeUI.State.Tools) != 1 || largeUI.State.Tools[0].Standard || largeUI.State.Tools[0].Arguments != "" {
			t.Fatal("large arguments were exposed as a complete empty call", e)
		}
	}
	stop, cancelStop := context.WithTimeout(ctx, 10*time.Second)
	err = lifecycle.Close(stop)
	cancelStop()
	if err != nil {
		t.Fatal(err)
	}
	before, _ := db.Store.RuntimeRun(ctx, actor.ID, space.ID, first.ID)
	beforeFrames, _, _ := db.Store.ReadFrames(ctx, actor.ID, space.ID, first.ID, 0, 1000)
	callsBefore := modelCalls.Load()
	requestsBefore := requests.Load()
	for _, table := range []string{"agui_events", "agui_streams", "run_plans", "artifacts", "tool_executions", "model_calls", "model_attempts", "messages", "session_active_runs"} {
		if _, err = db.Admin.DB.Exec("DELETE FROM " + table); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.Store.RebuildViews(ctx, func(ctx context.Context, o storage.Object) error {
		reader, e := blobs.Open(ctx, o.Key, o.VersionID)
		if e != nil {
			return e
		}
		defer reader.Close()
		sum := sha256.New()
		n, e := io.Copy(sum, reader)
		if e != nil {
			return e
		}
		if n != o.Size || hex.EncodeToString(sum.Sum(nil)) != o.SHA256 {
			return storage.ErrIntegrity
		}
		return nil
	}); err != nil {
		t.Fatal("D05 offline rebuild", err)
	}
	after, _ := db.Store.RuntimeRun(ctx, actor.ID, space.ID, first.ID)
	afterFrames, _, _ := db.Store.ReadFrames(ctx, actor.ID, space.ID, first.ID, 0, 1000)
	afterPlan, _ := db.Store.RuntimePlan(ctx, actor.ID, space.ID, first.ID)
	before.HeartbeatAt, after.HeartbeatAt = nil, nil
	if !catalog.Equal(before, after) || !catalog.Equal(beforeFrames, afterFrames) || !catalog.Equal(plan, afterPlan) || callsBefore != modelCalls.Load() || requests.Load() != requestsBefore {
		t.Fatal("D05 rebuild changed views or repeated effects")
	}
	t.Log("D05: all 14 tools, accepted attachment/fixed Skill, file/query/code/network/CSV, atomic plan, immutable artifact, IO redaction, snapshot state and offline rebuild passed")
}
func toolReply(name string, args any) []*schema.Message {
	raw, _ := json.Marshal(args)
	return []*schema.Message{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{Index: pointer(0), ID: "reused-native-id", Type: "function", Function: schema.FunctionCall{Name: name, Arguments: string(raw)}}}, ResponseMeta: &schema.ResponseMeta{FinishReason: "tool_calls", Usage: &schema.TokenUsage{PromptTokens: 100, CompletionTokens: 10}}}}
}
func textReply(text string) []*schema.Message {
	return []*schema.Message{{Role: schema.Assistant, Content: text, ResponseMeta: &schema.ResponseMeta{FinishReason: "stop", Usage: &schema.TokenUsage{PromptTokens: 100, CompletionTokens: 10}}}}
}
