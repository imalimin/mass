package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
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
	archiveadapter "github.com/imalimin/mass/internal/adapter/archive"
	einoadapter "github.com/imalimin/mass/internal/adapter/eino"
	"github.com/imalimin/mass/internal/adapter/secrets"
	"github.com/imalimin/mass/internal/adapter/workspace"
	"github.com/imalimin/mass/internal/app/catalog"
	"github.com/imalimin/mass/internal/app/identity"
	"github.com/imalimin/mass/internal/bootstrap"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
	"github.com/imalimin/mass/internal/resource"
	"github.com/imalimin/mass/internal/resource/sessionlog"
	"github.com/imalimin/mass/internal/runtime/modelgateway"
	transport "github.com/imalimin/mass/internal/transport/http"
	"github.com/imalimin/mass/tests/fakes"
	"github.com/imalimin/mass/tests/testdb"
)

type sharedModelTransport struct {
	modelgateway.Transport
	key, model, source, target string
}

type publishBlobFixture struct {
	*fakes.Blobs
	blockVersionFor  string
	versionStarted   chan struct{}
	releaseVersion   chan struct{}
	archiveWrites    atomic.Int64
	loseCatalogReply atomic.Bool
}

func (b *publishBlobFixture) PutNew(ctx context.Context, key string, body io.Reader, size int64, media string) (storage.BlobInfo, error) {
	if strings.HasSuffix(key, ".tar.gz") {
		b.archiveWrites.Add(1)
	}
	if strings.Contains(key, "/versions/") && b.blockVersionFor != "" && strings.Contains(key, "/agents/"+b.blockVersionFor+"/versions/") {
		close(b.versionStarted)
		select {
		case <-b.releaseVersion:
		case <-ctx.Done():
		}
		return storage.BlobInfo{}, storage.ErrIntegrity
	}
	return b.Blobs.PutNew(ctx, key, body, size, media)
}

func (b *publishBlobFixture) Overwrite(ctx context.Context, key string, body io.Reader, size int64, media string) (storage.BlobInfo, error) {
	info, err := b.Blobs.Overwrite(ctx, key, body, size, media)
	if err == nil && strings.HasSuffix(key, "/catalog.json") && b.loseCatalogReply.Swap(false) {
		return info, storage.ErrIntegrity
	}
	return info, err
}

func (m sharedModelTransport) Stream(ctx context.Context, snapshot run.Snapshot, prepared run.Prepared, key string, delta func(string) error) (run.Reply, error) {
	crossSpace := snapshot.AgentSourceSpaceID == m.source && snapshot.SpaceID == m.target
	inSource := snapshot.AgentSourceSpaceID == "" && snapshot.SpaceID == m.source
	if key != m.key || snapshot.Definition.Model.ID != m.model || snapshot.Definition.Model.Endpoint != identity.ArkEndpoint || !crossSpace && !inSource {
		return run.Reply{}, fmt.Errorf("unexpected shared model or execution scope")
	}
	return m.Transport.Stream(ctx, snapshot, prepared, key, delta)
}

func TestPublicAgentCrossSpace(t *testing.T) {
	if os.Getenv("MASS_TEST_ADMIN_DSN") == "" {
		t.Skip("requires isolated MariaDB")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	db, err := testdb.New(ctx)
	must(err)
	defer db.Close()
	cipher, err := secrets.New(bytes.Repeat([]byte{8}, 32))
	must(err)
	auth, err := identity.New(db.Store, cipher, time.Hour, []byte("shared Agent fixture"))
	must(err)
	newSpace := func(name string) (identity.User, identity.Space, string) {
		user, err := auth.CreateUser(ctx, name, "fixture-password")
		must(err)
		space, err := auth.CreateSpace(ctx, user.ID, name)
		must(err)
		_, cookie, _, err := auth.Login(ctx, name, "fixture-password")
		must(err)
		return user, space, cookie
	}
	owner, source, ownerCookie := newSpace("author")
	visitor, target, visitorCookie := newSpace("visitor")
	blobs := &publishBlobFixture{Blobs: fakes.NewBlobs()}
	objects := &archiveadapter.Objects{Authorize: db.Store.CheckAccess}
	sessions := &archiveadapter.Runtime{DB: db.Store, Objects: objects, Log: &sessionlog.Store{Root: t.TempDir(), Prefix: "shared", Blobs: blobs}}
	content := &resource.ContentStore{Repo: objects, Blobs: blobs, Prefix: "shared", MaxBytes: storage.MaxContentObjectBytes, Inline: sessions}
	catalogStore := &archiveadapter.Catalog{Blobs: blobs, Prefix: "shared", Objects: objects, Content: content, Model: db.Store.Model, Audit: db.Store.Audit}
	objects.Lookups = []archiveadapter.ObjectLookup{catalogStore.Lookup, sessions.Lookup}
	sessions.CatalogObject = catalogStore.Lookup
	catalogue := &catalog.Service{Repo: catalogStore, Content: content}
	const providerKey = "source-provider-key"
	model, err := auth.SaveModel(ctx, owner.ID, source.ID, "", identity.Model{Name: "source-model", Provider: "ark", ModelID: "fixture", BaseURL: identity.ArkEndpoint, APIKey: providerKey, ContextWindow: 32768, MaxOutputTokens: 4096, Enabled: true})
	must(err)
	publishModel, err := auth.SaveModel(ctx, owner.ID, source.ID, "", identity.Model{Name: "publish-model", Provider: "ark", ModelID: "fixture", BaseURL: identity.ArkEndpoint, APIKey: providerKey, ContextWindow: 32768, MaxOutputTokens: 4096, Enabled: true})
	must(err)
	skill, err := catalogue.Create(ctx, owner.ID, source.ID, "skill", "source-skill")
	must(err)
	skill, err = catalogue.Save(ctx, owner.ID, source.ID, "skill", skill.ID, skill.Revision, catalog.Document{Markdown: "---\nname: source-skill\ndescription: Use a fixed source.\n---\nRead the bound resource."})
	must(err)
	skill, err = catalogue.Upload(ctx, owner.ID, source.ID, "skill", skill.ID, "guide.txt", "text/plain", skill.Revision, strings.NewReader("bound published resource"))
	must(err)
	skillVersion, err := catalogue.Publish(ctx, owner.ID, source.ID, "skill", skill.ID, "v1", skill.Revision, false)
	must(err)
	agent, err := catalogue.Create(ctx, owner.ID, source.ID, "agent", "shared-agent")
	must(err)
	conf := catalog.Config{SchemaVersion: 1, Name: agent.Name, Model: model.ID, Tools: []string{"read_skill_resource", "write_file"}, Skills: []catalog.SkillRef{{ID: skill.ID, Version: "v1"}}}
	conf.Context = catalog.ContextConfig{MaxInputTokens: 20000, MaxTokens: 1024, Compression: "summary"}
	conf.Limits.MaxSteps, conf.Limits.TimeoutSeconds = 6, 30
	publish := func(label string) catalog.Version {
		raw, err := json.Marshal(conf)
		must(err)
		agent, err = catalogue.Save(ctx, owner.ID, source.ID, "agent", agent.ID, agent.Revision, catalog.Document{Markdown: "Follow the published instructions.", ConfigYAML: string(raw)})
		must(err)
		version, err := catalogue.Publish(ctx, owner.ID, source.ID, "agent", agent.ID, label, agent.Revision, false)
		must(err)
		return version
	}
	firstVersion := publish("v1")
	conf.Skills = []catalog.SkillRef{}
	secondVersion := publish("v2")
	manager, err := workspace.New(t.TempDir(), storage.Limits{MaxArchiveFileBytes: 1 << 20, MaxTotalBytes: 4 << 20})
	must(err)
	defer manager.Close()
	snapshots := &resource.SnapshotStore{Repo: sessions, Content: content, Journal: &journal.Journal{Repo: sessions}, Workspaces: manager, Timeout: 5 * time.Second}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	var config bootstrap.Config
	config.Runtime.SnapshotTimeout = 5 * time.Second
	config.Storage.MaxObjectBytes, config.Storage.MaxWorkspaceBytes = 1<<20, 4<<20
	config.Creativity.AgentID, config.Creativity.AgentWorkspaceID = agent.ID, source.ID
	newAgentConfig := conf
	newAgentConfig.Name, newAgentConfig.Skills = "agent-from-creativity", nil
	newAgentConfig.Model = ""
	newAgentYAML, err := json.Marshal(newAgentConfig)
	must(err)
	fake := &einoadapter.ModelTransport{Fake: &einoadapter.FakeChatModel{Respond: func(_ context.Context, messages []*schema.Message) ([]*schema.Message, error) {
		var toolMessages []string
		iteration, createAgent, continueAfterArchive := false, false, false
		for _, message := range messages {
			if message.Role == schema.Tool {
				toolMessages = append(toolMessages, message.Content)
			}
			if message.Role == schema.User && strings.Contains(message.Content, "Update iteration workspace") {
				iteration = true
			}
			if message.Role == schema.User && strings.Contains(message.Content, "Create project for publication") {
				createAgent = true
			}
			if message.Role == schema.User && strings.Contains(message.Content, "Continue after archiving") {
				continueAfterArchive = true
			}
		}
		reply := &schema.Message{Role: schema.Assistant, Content: "Done", ResponseMeta: &schema.ResponseMeta{FinishReason: "stop", Usage: &schema.TokenUsage{PromptTokens: 10, CompletionTokens: 5}}}
		tool, args := "", ""
		switch {
		case continueAfterArchive:
		case createAgent && len(toolMessages) == 0:
			tool, args = "write_file", `{"path":"AGENTS.md","content":"Created in Creativity","media_type":"text/plain","expected_sha256":"missing"}`
		case createAgent && len(toolMessages) == 1:
			tool, args = "write_file", fmt.Sprintf(`{"path":"agent.yaml","content":%q,"media_type":"text/plain","expected_sha256":"missing"}`, string(newAgentYAML))
		case createAgent:
		case iteration && len(toolMessages) == 0:
			tool, args = "write_file", `{"path":"result.txt","content":"iteration result","media_type":"text/plain","expected_sha256":"missing"}`
		case iteration && len(toolMessages) == 1:
			tool, args = "write_file", fmt.Sprintf(`{"path":"skills/source-skill/guide.txt","content":"improved Skill","media_type":"text/plain","expected_sha256":"%x"}`, sha256.Sum256([]byte("latest skill resource")))
		case iteration && len(toolMessages) == 2:
			tool, args = "write_file", `{"path":"skills/extra-skill/SKILL.md","content":"---\nname: extra-skill\ndescription: Extra fixture.\n---\nUse the extra skill.","media_type":"text/plain","expected_sha256":"missing"}`
		case !iteration && len(toolMessages) == 0:
			tool, args = "read_skill_resource", fmt.Sprintf(`{"skill_binding_id":%q,"path":"guide.txt"}`, skillVersion.ID)
		case !iteration && len(toolMessages) == 1:
			if !strings.Contains(toolMessages[0], "bound published resource") {
				return nil, fmt.Errorf("bound Skill resource not returned: %s", toolMessages[0])
			}
			tool, args = "write_file", `{"path":"result.txt","content":"created in target","media_type":"text/plain","expected_sha256":"missing"}`
		}
		if tool != "" {
			reply.Content, reply.ResponseMeta.FinishReason = "", "tool_calls"
			reply.ToolCalls = []schema.ToolCall{{Index: pointer(0), ID: uuid.NewString(), Type: "function", Function: schema.FunctionCall{Name: tool, Arguments: args}}}
		}
		return []*schema.Message{reply}, nil
	}}}
	options, lifecycle, err := bootstrap.StartRuntime(ctx, config, db.Store, auth, catalogue, content, manager, snapshots, "public-test", logger,
		sharedModelTransport{Transport: fake, key: providerKey, model: model.ID, source: source.ID, target: target.ID}, sessions)
	must(err)
	defer lifecycle.Close(context.Background())
	handler := transport.NewHandler(fstest.MapFS{"index.html": {Data: []byte("test")}}, "public-test", logger, transport.IdentityOptions{Service: auth, Origin: "http://mass.test", Catalog: catalogue, Runtime: options})
	request := func(method, path, cookie string, body any, status int) *httptest.ResponseRecorder {
		t.Helper()
		raw, err := json.Marshal(body)
		must(err)
		req := httptest.NewRequest(method, "http://mass.test/api/v1/spaces/"+path, bytes.NewReader(raw))
		req.Header.Set("Cookie", "mass_session="+cookie)
		req.Header.Set("Origin", "http://mass.test")
		req.Header.Set("Content-Type", "application/json")
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		if out.Code != status || strings.Contains(out.Body.String(), providerKey) {
			t.Fatalf("%s %s: got %d, want %d; body=%s", method, path, out.Code, status, out.Body.String())
		}
		return out
	}
	inputs := func() map[string]string { return map[string]string{"idempotency_key": uuid.NewString()} }
	visibility := source.ID + "/agents/" + agent.ID + "/visibility"
	launch := target.ID + "/creativity/sessions"
	available := request("GET", target.ID+"/creativity", visitorCookie, nil, 200)
	if !strings.Contains(available.Body.String(), `"available":false`) {
		t.Fatal(available.Body.String())
	}
	request("POST", launch, visitorCookie, inputs(), 403)
	request("PUT", visibility, visitorCookie, map[string]string{"visibility": "public"}, 403)
	request("PUT", visibility, ownerCookie, map[string]string{"visibility": "public"}, 200)
	available = request("GET", target.ID+"/creativity", visitorCookie, nil, 200)
	if !strings.Contains(available.Body.String(), `"available":true`) {
		t.Fatal(available.Body.String())
	}
	request("GET", source.ID+"/agents/"+agent.ID+"/draft", visitorCookie, nil, 403)
	request("GET", source.ID+"/skills/"+skill.ID+"/versions/"+skillVersion.ID, visitorCookie, nil, 403)
	var session run.Session
	must(json.Unmarshal(request("POST", launch, visitorCookie, inputs(), 201).Body.Bytes(), &session))
	if session.SpaceID != target.ID || session.AgentSourceSpaceID != source.ID || session.VersionID != firstVersion.ID || session.Creativity == nil || session.AgentName != agent.Name {
		t.Fatalf("incorrect shared session: %+v", session)
	}
	cold := &archiveadapter.Runtime{DB: db.Store, Log: &sessionlog.Store{Root: t.TempDir(), Prefix: "shared", Blobs: blobs}}
	reloaded, err := cold.RuntimeSession(ctx, visitor.ID, target.ID, session.ID)
	must(err)
	if reloaded.AgentSourceSpaceID != source.ID || reloaded.VersionID != firstVersion.ID {
		t.Fatalf("incorrect restored shared session: %+v", reloaded)
	}
	request("PUT", source.ID+"/agents/"+agent.ID+"/default-version", ownerCookie, map[string]string{"version_id": secondVersion.ID}, 204)
	ordinaryInput := map[string]string{"agent_id": agent.ID, "agent_source_space_id": source.ID, "idempotency_key": uuid.NewString(), "version_id": firstVersion.ID}
	request("POST", target.ID+"/sessions", visitorCookie, ordinaryInput, 400)
	delete(ordinaryInput, "version_id")
	var ordinary run.Session
	must(json.Unmarshal(request("POST", target.ID+"/sessions", visitorCookie, ordinaryInput, 201).Body.Bytes(), &ordinary))
	if ordinary.VersionID != secondVersion.ID || ordinary.Creativity != nil {
		t.Fatalf("new ordinary shared session did not use the new default: %+v", ordinary)
	}
	path := target.ID + "/sessions/" + session.ID
	var accepted run.View
	runInput := map[string]string{"idempotency_key": uuid.NewString(), "text": "Use the published resource"}
	must(json.Unmarshal(request("POST", path+"/runs", visitorCookie, runInput, 202).Body.Bytes(), &accepted))
	for !run.Terminal(accepted.Status) && ctx.Err() == nil {
		time.Sleep(25 * time.Millisecond)
		accepted, err = sessions.RuntimeRun(ctx, visitor.ID, target.ID, accepted.ID)
		must(err)
	}
	if accepted.Status != "succeeded" || accepted.Definition.VersionID != firstVersion.ID || accepted.Definition.Model.Endpoint != "" {
		t.Fatalf("unexpected shared Run: status=%s error=%s", accepted.Status, accepted.Error)
	}
	workspace := request("GET", path+"/workspace/files?path=result.txt", visitorCookie, nil, 200)
	if !strings.Contains(workspace.Body.String(), "created in target") || manager.Exists(source.ID, session.ID) {
		t.Fatal(workspace.Body.String())
	}
	request("PUT", visibility, ownerCookie, map[string]string{"visibility": "space"}, 200)
	request("POST", launch, visitorCookie, inputs(), 403)
	request("POST", path+"/runs", visitorCookie, runInput, 202)
	request("POST", path+"/runs", visitorCookie, map[string]string{"idempotency_key": uuid.NewString(), "text": "Must not be accepted"}, 403)
	request("GET", path, visitorCookie, nil, 200)

	skill, err = catalogue.Upload(ctx, owner.ID, source.ID, "skill", skill.ID, "guide.txt", "text/plain", skill.Revision, strings.NewReader("latest skill resource"))
	must(err)
	latestSkill, err := catalogue.Publish(ctx, owner.ID, source.ID, "skill", skill.ID, "v2", skill.Revision, false)
	must(err)
	conf.Skills = []catalog.SkillRef{{ID: skill.ID, Version: "v1"}}
	latestAgent := publish("v3")
	archivedAgent := publish("archived")
	request("PUT", source.ID+"/agents/"+agent.ID+"/versions/"+archivedAgent.ID+"/archive", ownerCookie, map[string]bool{"archived": true}, 204)
	var iteration run.Session
	iterationInput := inputs()
	iterationInput["target_agent_id"] = agent.ID
	localLaunch := source.ID + "/creativity/sessions"
	must(json.Unmarshal(request("POST", localLaunch, ownerCookie, iterationInput, 201).Body.Bytes(), &iteration))
	if iteration.VersionID != secondVersion.ID || iteration.Creativity == nil || iteration.Creativity.TargetVersionID != latestAgent.ID ||
		len(iteration.Creativity.SkillBases) != 1 || iteration.Creativity.SkillBases[0].VersionID != latestSkill.ID {
		t.Fatalf("unexpected iteration base: %+v", iteration.Creativity)
	}
	iterationPath := source.ID + "/sessions/" + iteration.ID
	assertFile := func(name, expected string) {
		t.Helper()
		var file resource.WorkspaceEntryView
		must(json.Unmarshal(request("GET", iterationPath+"/workspace/files?path="+name, ownerCookie, nil, 200).Body.Bytes(), &file))
		if !strings.Contains(file.Text, expected) {
			t.Fatalf("%s: expected %q, got %q", name, expected, file.Text)
		}
	}
	assertFile("AGENTS.md", "Follow the published instructions.")
	assertFile("agent.yaml", model.ID)
	assertFile("proj.yaml", latestSkill.ID)
	assertFile("skills/source-skill/SKILL.md", "Read the bound resource.")
	assertFile("skills/source-skill/guide.txt", "latest skill resource")
	reloaded, err = cold.RuntimeSession(ctx, owner.ID, source.ID, iteration.ID)
	must(err)
	if reloaded.Creativity == nil || reloaded.Creativity.TargetAgentID != agent.ID || reloaded.Creativity.TargetVersionID != latestAgent.ID || reloaded.Creativity.SkillBases[0].VersionID != latestSkill.ID {
		t.Fatalf("iteration base did not survive TOS reload: %+v", reloaded.Creativity)
	}
	must(manager.With(ctx, source.ID, iteration.ID, func(dir resource.WorkspaceDir) error { return dir.Clear(ctx, nil) }))
	options.Workspaces.Forget(source.ID, iteration.ID)
	assertFile("skills/source-skill/guide.txt", "latest skill resource")
	var retried run.Session
	must(json.Unmarshal(request("POST", localLaunch, ownerCookie, iterationInput, 201).Body.Bytes(), &retried))
	if retried.ID != iteration.ID || retried.Creativity.TargetVersionID != latestAgent.ID || retried.LatestSnapshotID != "" {
		t.Fatalf("iteration retry changed the session or created a snapshot: %+v", retried)
	}
	must(json.Unmarshal(request("POST", iterationPath+"/runs", ownerCookie, map[string]string{
		"idempotency_key": uuid.NewString(), "text": "Update iteration workspace",
	}, 202).Body.Bytes(), &accepted))
	for !run.Terminal(accepted.Status) && ctx.Err() == nil {
		time.Sleep(25 * time.Millisecond)
		accepted, err = sessions.RuntimeRun(ctx, owner.ID, source.ID, accepted.ID)
		must(err)
	}
	if accepted.Status != "succeeded" || accepted.WorkspaceSnapshotID == "" {
		t.Fatalf("iteration did not commit its workspace: status=%s error=%s snapshot_error=%s", accepted.Status, accepted.Error, accepted.SnapshotError)
	}
	removeIterationWorkspace := func() {
		var dirPath string
		must(manager.With(ctx, source.ID, iteration.ID, func(dir resource.WorkspaceDir) error {
			dirPath = dir.Root().Name()
			return dir.Clear(ctx, nil)
		}))
		must(os.Remove(dirPath))
	}
	removeIterationWorkspace()
	beforeBrowse := blobs.archiveWrites.Load()
	request("GET", iterationPath+"/workspace/files", visitorCookie, nil, 403)
	if manager.Exists(source.ID, iteration.ID) {
		t.Fatal("unauthorized browse restored a workspace")
	}
	var root resource.WorkspaceEntryView
	must(json.Unmarshal(request("GET", iterationPath+"/workspace/files", ownerCookie, nil, 200).Body.Bytes(), &root))
	if !root.Directory || len(root.Entries) == 0 {
		t.Fatal("opening the file tree did not restore the last committed workspace")
	}
	assertFile("result.txt", "iteration result")
	assertFile("skills/source-skill/guide.txt", "improved Skill")
	runsAfterBrowse, err := sessions.RuntimeRuns(ctx, owner.ID, source.ID, iteration.ID, identity.Query{Limit: 10})
	must(err)
	if len(runsAfterBrowse) != 1 || runsAfterBrowse[0].WorkspaceSnapshotID != accepted.WorkspaceSnapshotID || blobs.archiveWrites.Load() != beforeBrowse {
		t.Fatal("browsing created a Run or wrote a new snapshot")
	}
	must(manager.With(ctx, source.ID, iteration.ID, func(dir resource.WorkspaceDir) error {
		return dir.Root().WriteFile("result.txt", []byte("uncommitted local edit"), 0600)
	}))
	assertFile("result.txt", "uncommitted local edit")
	removeIterationWorkspace()
	publishPath := localLaunch + "/" + iteration.ID + "/publish"
	snapshot := accepted.WorkspaceSnapshotID
	publishInput := map[string]string{"workspace_snapshot_id": snapshot}
	var runHTTP run.View
	must(json.Unmarshal(request("GET", source.ID+"/runs/"+accepted.ID, ownerCookie, nil, 200).Body.Bytes(), &runHTTP))
	if runHTTP.WorkspaceSnapshotID != snapshot {
		t.Fatal("committed snapshot missing from run response")
	}
	request("GET", publishPath+"?workspace_snapshot_id="+uuid.NewString(), ownerCookie, nil, 409)
	request("POST", publishPath, ownerCookie, map[string]string{"workspace_snapshot_id": snapshot, "target_agent_id": agent.ID}, 400)
	request("POST", publishPath, ownerCookie, map[string]string{"workspace_snapshot_id": snapshot, "model_id": publishModel.ID}, 400)
	var preview catalog.PublishManifest
	must(json.Unmarshal(request("GET", publishPath+"?workspace_snapshot_id="+snapshot, ownerCookie, nil, 200).Body.Bytes(), &preview))
	if preview.Agent.ID != agent.ID || len(preview.Skills) != 2 || preview.Skills[0].Action != "update" || preview.Skills[1].Action != "create" {
		t.Fatalf("unexpected publish preview: %+v", preview)
	}
	archiveCount := blobs.archiveWrites.Load()
	blobs.blockVersionFor = agent.ID
	blobs.versionStarted, blobs.releaseVersion = make(chan struct{}), make(chan struct{})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest("POST", "http://mass.test/api/v1/spaces/"+publishPath, strings.NewReader(fmt.Sprintf(`{"workspace_snapshot_id":%q}`, snapshot))).WithContext(ctx)
		req.Header.Set("Cookie", "mass_session="+ownerCookie)
		req.Header.Set("Origin", "http://mass.test")
		req.Header.Set("Content-Type", "application/json")
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		done <- out
	}()
	select {
	case <-blobs.versionStarted:
	case response := <-done:
		t.Fatalf("publish stopped before Agent version upload: %d %s", response.Code, response.Body.String())
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	request("POST", iterationPath+"/runs", ownerCookie, map[string]string{"idempotency_key": uuid.NewString(), "text": "Blocked by publish"}, 409)
	request("POST", publishPath, ownerCookie, publishInput, 409)
	close(blobs.releaseVersion)
	var failure struct {
		Details struct {
			Result catalog.PublishManifest `json:"result"`
		} `json:"details"`
	}
	failed := <-done
	if failed.Code != 503 {
		t.Fatalf("injected Agent publish failure returned %d", failed.Code)
	}
	must(json.Unmarshal(failed.Body.Bytes(), &failure))
	entries := failure.Details.Result
	if entries.Agent.Status != "draft_saved" || entries.Agent.ID != agent.ID || entries.Agent.VersionID != "" ||
		len(entries.Skills) != 2 || entries.Skills[0].Status != "published" || entries.Skills[0].Label != "v3" ||
		entries.Skills[1].Status != "published" || entries.Skills[1].Label != "v1" {
		t.Fatalf("incorrect partial result: %+v", entries)
	}
	blobs.blockVersionFor = ""
	publishCold := &archiveadapter.Runtime{DB: db.Store, Log: &sessionlog.Store{Root: t.TempDir(), Prefix: "shared", Blobs: blobs}}
	pinned, err := publishCold.CreativityPublishPlan(ctx, owner.ID, source.ID, iteration.ID, snapshot)
	must(err)
	if len(pinned.Skills) != 2 || pinned.Agent.ID != agent.ID {
		t.Fatalf("publish plan not persisted before partial writes: %+v", pinned)
	}
	savedAgent, err := catalogue.Draft(ctx, owner.ID, source.ID, "agent", agent.ID)
	must(err)
	partial, _, err := catalogStore.LatestFormalVersion(ctx, owner.ID, source.ID, "skill", skill.ID)
	must(err)
	newSkill, _, err := catalogStore.LatestFormalVersion(ctx, owner.ID, source.ID, "skill", pinned.Skills[1].ID)
	must(err)
	var published, replay catalog.PublishManifest
	must(json.Unmarshal(request("POST", publishPath, ownerCookie, publishInput, 200).Body.Bytes(), &published))
	must(json.Unmarshal(request("POST", publishPath, ownerCookie, publishInput, 200).Body.Bytes(), &replay))
	if !catalog.Equal(published, replay) || published.Agent.ID != agent.ID || published.Skills[0].VersionID != partial.ID || published.Skills[1].VersionID != newSkill.ID ||
		published.Skills[0].Label != "v3" || published.Skills[1].Label != "v1" || blobs.archiveWrites.Load() != archiveCount {
		t.Fatalf("retry changed versions or wrote a workspace snapshot: %+v", published)
	}
	afterPublish, err := catalogue.Draft(ctx, owner.ID, source.ID, "agent", agent.ID)
	must(err)
	if afterPublish.Revision != savedAgent.Revision || afterPublish.DefaultVersionID != secondVersion.ID {
		t.Fatalf("retry rewrote saved Agent draft or changed default: %+v", afterPublish)
	}
	skill, err = catalogue.Draft(ctx, owner.ID, source.ID, "skill", skill.ID)
	must(err)
	skill, err = catalogue.Upload(ctx, owner.ID, source.ID, "skill", skill.ID, "guide.txt", "text/plain", skill.Revision, strings.NewReader("new content from management"))
	must(err)
	competing, err := catalogue.Publish(ctx, owner.ID, source.ID, "skill", skill.ID, "", skill.Revision, false)
	must(err)
	conflict := request("POST", publishPath, ownerCookie, publishInput, 409)
	must(json.Unmarshal(conflict.Body.Bytes(), &failure))
	entries = failure.Details.Result
	if entries.Skills[0].Status != "failed" || entries.Skills[1].Status != "unprocessed" || entries.Agent.Status != "unprocessed" {
		t.Fatalf("incorrect conflict result: %+v", entries)
	}
	afterConflict, _, err := catalogStore.LatestFormalVersion(ctx, owner.ID, source.ID, "skill", skill.ID)
	must(err)
	if afterConflict.ID != competing.ID {
		t.Fatal("publish retry overwrote a competing Skill version")
	}
	var createSession run.Session
	must(json.Unmarshal(request("POST", localLaunch, ownerCookie, inputs(), 201).Body.Bytes(), &createSession))
	createPublishPath := localLaunch + "/" + createSession.ID + "/publish"
	must(json.Unmarshal(request("POST", source.ID+"/sessions/"+createSession.ID+"/runs", ownerCookie, map[string]string{
		"idempotency_key": uuid.NewString(), "text": "Create project for publication",
	}, 202).Body.Bytes(), &accepted))
	for !run.Terminal(accepted.Status) && ctx.Err() == nil {
		time.Sleep(25 * time.Millisecond)
		accepted, err = sessions.RuntimeRun(ctx, owner.ID, source.ID, accepted.ID)
		must(err)
	}
	if accepted.Status != "succeeded" || accepted.WorkspaceSnapshotID == "" {
		t.Fatalf("new project did not commit its workspace: status=%s error=%s", accepted.Status, accepted.Error)
	}
	createPublishInput := map[string]string{"workspace_snapshot_id": accepted.WorkspaceSnapshotID}
	createPreviewPath := createPublishPath + "?workspace_snapshot_id=" + accepted.WorkspaceSnapshotID
	chooseModel := request("GET", createPreviewPath, ownerCookie, nil, 400)
	if !strings.Contains(chooseModel.Body.String(), "publish_model_required") {
		t.Fatal(chooseModel.Body.String())
	}
	request("POST", createPublishPath, ownerCookie, createPublishInput, 400)
	unavailableModel := request("GET", createPreviewPath+"&model_id="+uuid.NewString(), ownerCookie, nil, 400)
	if !strings.Contains(unavailableModel.Body.String(), "publish_model_unavailable") {
		t.Fatal(unavailableModel.Body.String())
	}
	must(json.Unmarshal(request("GET", createPreviewPath+"&model_id="+publishModel.ID, ownerCookie, nil, 200).Body.Bytes(), &preview))
	if preview.ModelID != publishModel.ID || preview.ModelLocked {
		t.Fatalf("selection was not validated or was locked by preview: %+v", preview)
	}
	clearPlan, err := sessions.CreativityPublishPlan(ctx, owner.ID, source.ID, createSession.ID, accepted.WorkspaceSnapshotID)
	must(err)
	if clearPlan.SnapshotID != "" {
		t.Fatal("preview created a publish plan")
	}
	createPublishInput["model_id"] = publishModel.ID
	blobs.loseCatalogReply.Store(true)
	ambiguous := request("POST", createPublishPath, ownerCookie, createPublishInput, 503)
	must(json.Unmarshal(ambiguous.Body.Bytes(), &failure))
	if failure.Details.Result.Agent.Status != "unknown" || failure.Details.Result.Agent.VersionID != "" {
		t.Fatalf("lost write response produced a definitive result: %+v", failure.Details.Result.Agent)
	}
	request("POST", createPublishPath, ownerCookie, map[string]string{"workspace_snapshot_id": accepted.WorkspaceSnapshotID, "model_id": model.ID}, 409)
	delete(createPublishInput, "model_id")
	must(json.Unmarshal(request("GET", createPreviewPath, ownerCookie, nil, 200).Body.Bytes(), &preview))
	if preview.ModelID != publishModel.ID || !preview.ModelLocked {
		t.Fatalf("reopened preview lost the pinned model: %+v", preview)
	}
	must(json.Unmarshal(request("POST", createPublishPath, ownerCookie, createPublishInput, 200).Body.Bytes(), &published))
	newAgent, err := catalogue.Draft(ctx, owner.ID, source.ID, "agent", published.Agent.ID)
	must(err)
	if newAgent.Name != newAgentConfig.Name || newAgent.Visibility != catalog.VisibilitySpace || newAgent.DefaultVersionID != published.Agent.VersionID ||
		newAgent.Revision != 1 || published.Agent.Status != "published" || published.Agent.Label != "v1" {
		t.Fatalf("unexpected new Agent: %+v", newAgent)
	}
	publishedVersion, err := catalogue.Version(ctx, owner.ID, source.ID, "agent", newAgent.ID, published.Agent.VersionID)
	must(err)
	if publishedVersion.Bundle.Config.Model != publishModel.ID || publishedVersion.Bundle.Model.ID != publishModel.ID {
		t.Fatal("published Agent did not bind the selected model")
	}
	var projectFile resource.WorkspaceEntryView
	must(json.Unmarshal(request("GET", source.ID+"/sessions/"+createSession.ID+"/workspace/files?path=agent.yaml", ownerCookie, nil, 200).Body.Bytes(), &projectFile))
	var unchangedConfig catalog.Config
	must(json.Unmarshal([]byte(projectFile.Text), &unchangedConfig))
	if unchangedConfig.Model != "" {
		t.Fatal("publishing a selected model modified the workspace")
	}
	unpublished, err := catalogue.Create(ctx, owner.ID, source.ID, "agent", "first-release")
	must(err)
	draftInput := inputs()
	draftInput["target_agent_id"] = unpublished.ID
	var draftSession run.Session
	must(json.Unmarshal(request("POST", localLaunch, ownerCookie, draftInput, 201).Body.Bytes(), &draftSession))
	if draftSession.Creativity.TargetVersionID != "" || draftSession.Creativity.TargetDraftRevision != unpublished.Revision {
		t.Fatalf("did not pin unpublished draft: %+v", draftSession.Creativity)
	}
	_, err = catalogue.Save(ctx, owner.ID, source.ID, "agent", unpublished.ID, unpublished.Revision, catalog.Document{Markdown: "Different draft"})
	must(err)
	options.Workspaces.Forget(source.ID, draftSession.ID)
	refused := request("GET", source.ID+"/sessions/"+draftSession.ID+"/workspace/files", ownerCookie, nil, 409)
	if !strings.Contains(refused.Body.String(), "iteration_draft_changed") {
		t.Fatal(refused.Body.String())
	}
	missingSkill, err := catalogue.Create(ctx, owner.ID, source.ID, "skill", "not-published")
	must(err)
	conf.Name, conf.Skills = unpublished.Name, []catalog.SkillRef{{ID: missingSkill.ID, Version: "v1"}}
	raw, err := json.Marshal(conf)
	must(err)
	unpublished, err = catalogue.Draft(ctx, owner.ID, source.ID, "agent", unpublished.ID)
	must(err)
	_, err = catalogue.Save(ctx, owner.ID, source.ID, "agent", unpublished.ID, unpublished.Revision, catalog.Document{Markdown: "Draft", ConfigYAML: string(raw)})
	must(err)
	draftInput["idempotency_key"] = uuid.NewString()
	unavailable := request("POST", localLaunch, ownerCookie, draftInput, 409)
	if !strings.Contains(unavailable.Body.String(), "iteration_version_unavailable") || !strings.Contains(unavailable.Body.String(), missingSkill.Name) {
		t.Fatal(unavailable.Body.String())
	}

	request("PUT", source.ID+"/skills/"+skill.ID+"/archive", ownerCookie, map[string]bool{"archived": true}, 204)
	sessionsBefore, err := sessions.RuntimeSessions(ctx, owner.ID, source.ID, agent.ID, identity.Query{Limit: 100})
	must(err)
	archivedInput := inputs()
	archivedInput["target_agent_id"] = agent.ID
	unavailable = request("POST", localLaunch, ownerCookie, archivedInput, 409)
	if !strings.Contains(unavailable.Body.String(), "iteration_version_unavailable") ||
		!strings.Contains(unavailable.Body.String(), "Skill "+skill.Name+" 已归档") {
		t.Fatal(unavailable.Body.String())
	}
	sessionsAfter, err := sessions.RuntimeSessions(ctx, owner.ID, source.ID, agent.ID, identity.Query{Limit: 100})
	must(err)
	if len(sessionsAfter) != len(sessionsBefore) {
		t.Fatal("an archived Skill produced a new iteration session")
	}
	blockedPreview := request("GET", publishPath+"?workspace_snapshot_id="+snapshot, ownerCookie, nil, 400)
	if !strings.Contains(blockedPreview.Body.String(), "Skill 已归档："+skill.Name) {
		t.Fatal(blockedPreview.Body.String())
	}
	must(json.Unmarshal(request("POST", iterationPath+"/runs", ownerCookie, map[string]string{
		"idempotency_key": uuid.NewString(), "text": "Continue after archiving",
	}, 202).Body.Bytes(), &accepted))
	for !run.Terminal(accepted.Status) && ctx.Err() == nil {
		time.Sleep(25 * time.Millisecond)
		accepted, err = sessions.RuntimeRun(ctx, owner.ID, source.ID, accepted.ID)
		must(err)
	}
	if accepted.Status != "succeeded" || accepted.WorkspaceSnapshotID == "" {
		t.Fatalf("archiving a project Skill broke the existing conversation: %+v", accepted)
	}
	assertFile("skills/source-skill/guide.txt", "improved Skill")
}
