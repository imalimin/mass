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
	"go.yaml.in/yaml/v4"
)

type sharedModelTransport struct {
	modelgateway.Transport
	key, model, source, target string
}

type publishBlobFixture struct {
	*fakes.Blobs
	blockVersionFor   string
	versionStarted    chan struct{}
	releaseVersion    chan struct{}
	archiveWrites     atomic.Int64
	loseCatalogReply  atomic.Bool
	failPublishRecord atomic.Bool
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
	if b.failPublishRecord.Load() && strings.HasSuffix(key, "/session.jsonl") {
		raw, err := io.ReadAll(body)
		if err != nil {
			return storage.BlobInfo{}, err
		}
		if bytes.Contains(raw, []byte(`"type":"creativity_publish_completed"`)) && b.failPublishRecord.Swap(false) {
			return storage.BlobInfo{}, storage.ErrIntegrity
		}
		body = bytes.NewReader(raw)
	}
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
	conf := catalog.Config{SchemaVersion: 1, Name: agent.Name, Model: model.ID, Tools: []string{"read_skill_resource", "write_file", "update_plan"}, Skills: []catalog.SkillRef{{ID: skill.ID, Version: "v1"}}}
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
	legacyAgentYAML, err := yaml.Marshal(newAgentConfig)
	must(err)
	var draftBinding atomic.Value
	fake := &einoadapter.ModelTransport{Fake: &einoadapter.FakeChatModel{Respond: func(_ context.Context, messages []*schema.Message) ([]*schema.Message, error) {
		var toolMessages []string
		latestUser, latestTools := "", 0
		iteration, createAgent, continueAfterArchive, readDraft := false, false, false, false
		for _, message := range messages {
			if message.Role == schema.Tool {
				toolMessages = append(toolMessages, message.Content)
				latestTools++
			}
			if message.Role == schema.User {
				latestUser, latestTools = message.Content, 0
			}
			if message.Role == schema.User && strings.Contains(message.Content, "Update iteration workspace") {
				iteration = true
			}
			if message.Role == schema.User && strings.Contains(message.Content, "Create project for publication") {
				createAgent = true
			}
			if message.Role == schema.User && (strings.Contains(message.Content, "Continue after archiving") || message.Content == "Keep the project unchanged") {
				continueAfterArchive = true
			}
			if message.Role == schema.User && message.Content == "Read the pinned draft" {
				readDraft = true
			}
		}
		reply := &schema.Message{Role: schema.Assistant, Content: "Done", ResponseMeta: &schema.ResponseMeta{FinishReason: "stop", Usage: &schema.TokenUsage{PromptTokens: 10, CompletionTokens: 5}}}
		tool, args := "", ""
		switch {
		case readDraft && len(toolMessages) == 0:
			tool, args = "read_skill_resource", fmt.Sprintf(`{"skill_binding_id":%q,"path":"guide.txt"}`, draftBinding.Load())
		case readDraft:
			if !strings.Contains(toolMessages[0], "draft-only resource") {
				return nil, fmt.Errorf("pinned Skill resource not returned: %s", toolMessages[0])
			}
		case latestUser == "Prepare next release":
			switch latestTools {
			case 0:
				tool, args = "write_file", fmt.Sprintf(`{"path":"AGENTS.md","content":"Second release from the same session","media_type":"text/plain","expected_sha256":"%x"}`, sha256.Sum256([]byte("Follow the published instructions.")))
			case 1:
				tool, args = "write_file", fmt.Sprintf(`{"path":"skills/source-skill/guide.txt","content":"second improvement","media_type":"text/plain","expected_sha256":"%x"}`, sha256.Sum256([]byte("improved Skill")))
			case 2:
				tool, args = "write_file", `{"path":"skills/extra-skill/addendum.txt","content":"new resource for the same Skill","media_type":"text/plain","expected_sha256":"missing"}`
			}
		case continueAfterArchive:
		case createAgent && len(toolMessages) == 0:
			tool, args = "write_file", `{"path":"AGENTS.md","content":"Created in Creativity","media_type":"text/plain","expected_sha256":"missing"}`
		case createAgent && len(toolMessages) == 1:
			tool, args = "write_file", fmt.Sprintf(`{"path":"agent.yaml","content":%q,"media_type":"text/plain","expected_sha256":"missing"}`, string(legacyAgentYAML))
		case createAgent:
		case iteration && len(toolMessages) == 0:
			tool, args = "write_file", `{"path":"result.txt","content":"iteration result","media_type":"text/plain","expected_sha256":"missing"}`
		case iteration && len(toolMessages) == 1:
			tool, args = "write_file", fmt.Sprintf(`{"path":"skills/source-skill/guide.txt","content":"improved Skill","media_type":"text/plain","expected_sha256":"%x"}`, sha256.Sum256([]byte("latest skill resource")))
		case iteration && len(toolMessages) == 2:
			tool, args = "write_file", `{"path":"skills/extra-skill/SKILL.md","content":"---\nname: extra-skill\ndescription: Extra fixture.\n---\nUse the extra skill.","media_type":"text/plain","expected_sha256":"missing"}`
		case iteration && len(toolMessages) == 3:
			tool, args = "update_plan", `{"expected_revision":0,"steps":[{"id":"review","title":"Review changes","status":"completed"}]}`
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
	modelTransport := sharedModelTransport{Transport: fake, key: providerKey, model: model.ID, source: source.ID, target: target.ID}
	options, lifecycle, err := bootstrap.StartRuntime(ctx, config, db.Store, auth, catalogue, content, manager, snapshots, "public-test", logger, modelTransport, sessions)
	must(err)
	defer func() {
		if lifecycle != nil {
			lifecycle.Close(context.Background())
		}
	}()
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
	assertFile("agent.json", model.ID)
	request("GET", iterationPath+"/workspace/files?path=agent.yaml", ownerCookie, nil, 404)
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
	planEvents, err := snapshots.Journal.ReadRun(ctx, owner.ID, source.ID, iteration.ID, accepted.ID, 0, nil, 100)
	must(err)
	var writtenPlan journal.Event
	for _, event := range planEvents.Events {
		if event.Type == "plan.updated" {
			writtenPlan = event
		}
	}
	if writtenPlan.OperationID == "" || writtenPlan.CausationID == "" {
		t.Fatal("first iteration did not record the plan and its tool call")
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
	debugPath := localLaunch + "/" + iteration.ID + "/draft-debug"
	debugInput := map[string]string{"workspace_snapshot_id": snapshot, "idempotency_key": uuid.NewString()}
	var debugSession, debugRetry run.Session
	request("POST", debugPath, visitorCookie, debugInput, 403)
	must(json.Unmarshal(request("POST", debugPath, ownerCookie, debugInput, 201).Body.Bytes(), &debugSession))
	must(json.Unmarshal(request("POST", debugPath, ownerCookie, debugInput, 201).Body.Bytes(), &debugRetry))
	if debugSession.ID != debugRetry.ID || debugSession.DefinitionID != agent.ID || debugSession.Creativity != nil ||
		debugSession.DraftRevision == 0 || debugSession.LatestSnapshotID != "" || blobs.archiveWrites.Load() != archiveCount {
		t.Fatalf("draft debug did not open a fixed ordinary session: %+v", debugSession)
	}
	debugPlan, err := sessions.CreativityPublishPlan(ctx, owner.ID, source.ID, iteration.ID, snapshot)
	must(err)
	debugDefinition, err := options.Service.Definition(ctx, owner.ID, source.ID, debugSession.DefinitionRef)
	must(err)
	stillFormal, _, err := catalogStore.LatestFormalVersion(ctx, owner.ID, source.ID, "skill", skill.ID)
	must(err)
	unpublishedSkill, _, err := catalogStore.LatestFormalVersion(ctx, owner.ID, source.ID, "skill", debugPlan.Skills[1].ID)
	must(err)
	if stillFormal.ID != latestSkill.ID || unpublishedSkill.ID != "" || len(debugDefinition.Skills) != 2 ||
		!strings.HasPrefix(debugDefinition.Skills[0].Version, "draft-r") || debugDefinition.Skills[1].ID != debugPlan.Skills[1].ID {
		t.Fatalf("debug used the wrong Skill bindings or published a version: %+v", debugDefinition.Skills)
	}
	must(json.Unmarshal(request("POST", iterationPath+"/runs", ownerCookie, map[string]string{
		"idempotency_key": uuid.NewString(), "text": "Keep the project unchanged",
	}, 202).Body.Bytes(), &accepted))
	for !run.Terminal(accepted.Status) && ctx.Err() == nil {
		time.Sleep(25 * time.Millisecond)
		accepted, err = sessions.RuntimeRun(ctx, owner.ID, source.ID, accepted.ID)
		must(err)
	}
	if accepted.Status != "succeeded" || accepted.WorkspaceSnapshotID == "" || accepted.WorkspaceSnapshotID == snapshot {
		t.Fatalf("second iteration did not commit a new snapshot: %+v", accepted)
	}
	request("POST", debugPath, ownerCookie, debugInput, 409)
	snapshot = accepted.WorkspaceSnapshotID
	publishInput["workspace_snapshot_id"] = snapshot
	debugInput["workspace_snapshot_id"], debugInput["idempotency_key"] = snapshot, uuid.NewString()
	archiveCount = blobs.archiveWrites.Load()
	must(json.Unmarshal(request("POST", debugPath, ownerCookie, debugInput, 201).Body.Bytes(), &debugRetry))
	secondDebugPlan, err := sessions.CreativityPublishPlan(ctx, owner.ID, source.ID, iteration.ID, snapshot)
	must(err)
	if debugRetry.ID == debugSession.ID || !catalog.Equal(secondDebugPlan.DebugDrafts, debugPlan.DebugDrafts) || blobs.archiveWrites.Load() != archiveCount {
		t.Fatalf("debug on a later snapshot did not reuse the saved drafts: %+v", secondDebugPlan)
	}
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
	request("POST", debugPath, ownerCookie, debugInput, 409)
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
	if len(pinned.Skills) != 2 || pinned.Agent.ID != agent.ID || pinned.Skills[1].ID != debugPlan.Skills[1].ID {
		t.Fatalf("publish plan not persisted before partial writes: %+v", pinned)
	}
	savedAgent, err := catalogue.Draft(ctx, owner.ID, source.ID, "agent", agent.ID)
	must(err)
	partial, _, err := catalogStore.LatestFormalVersion(ctx, owner.ID, source.ID, "skill", skill.ID)
	must(err)
	newSkill, _, err := catalogStore.LatestFormalVersion(ctx, owner.ID, source.ID, "skill", pinned.Skills[1].ID)
	must(err)
	// Lose the catalog reply after Agent commit, then fail publication log upload.
	// Both retries must find the same planned Agent version.
	blobs.loseCatalogReply.Store(true)
	request("POST", publishPath, ownerCookie, publishInput, 503)
	committed, _, err := catalogStore.LatestFormalVersion(ctx, owner.ID, source.ID, "agent", agent.ID)
	must(err)
	if committed.ID != pinned.Agent.NextVersionID || committed.ID == latestAgent.ID {
		t.Fatalf("unexpected version after losing the catalog reply: %+v", committed)
	}
	blobs.failPublishRecord.Store(true)
	request("POST", publishPath, ownerCookie, publishInput, 503)
	var published, replay catalog.PublishManifest
	must(json.Unmarshal(request("POST", publishPath, ownerCookie, publishInput, 200).Body.Bytes(), &published))
	must(json.Unmarshal(request("POST", publishPath, ownerCookie, publishInput, 200).Body.Bytes(), &replay))
	if !catalog.Equal(published, replay) || published.Agent.ID != agent.ID || published.Agent.VersionID != committed.ID || published.Skills[0].VersionID != partial.ID || published.Skills[1].VersionID != newSkill.ID ||
		published.Skills[0].Label != "v3" || published.Skills[1].Label != "v1" || blobs.archiveWrites.Load() != archiveCount {
		t.Fatalf("retry changed versions or wrote a workspace snapshot: %+v", published)
	}
	afterPublish, err := catalogue.Draft(ctx, owner.ID, source.ID, "agent", agent.ID)
	must(err)
	if afterPublish.Revision != savedAgent.Revision || afterPublish.DefaultVersionID != secondVersion.ID {
		t.Fatalf("retry rewrote saved Agent draft or changed default: %+v", afterPublish)
	}
	publicationCold := &archiveadapter.Runtime{DB: db.Store, Log: &sessionlog.Store{Root: t.TempDir(), Prefix: "shared", Blobs: blobs}}
	basis, err := publicationCold.PreviousCreativityPublication(ctx, owner.ID, source.ID, iteration.ID, "")
	must(err)
	if basis.Agent.Version != published.Agent.VersionID || basis.Agent.Revision != afterPublish.Revision || len(basis.Skills) != 2 {
		t.Fatalf("cold replay lost the preceding publication: %+v", basis)
	}
	must(json.Unmarshal(request("POST", iterationPath+"/runs", ownerCookie, map[string]string{
		"idempotency_key": uuid.NewString(), "text": "Prepare next release",
	}, 202).Body.Bytes(), &accepted))
	for !run.Terminal(accepted.Status) && ctx.Err() == nil {
		time.Sleep(25 * time.Millisecond)
		accepted, err = sessions.RuntimeRun(ctx, owner.ID, source.ID, accepted.ID)
		must(err)
	}
	if accepted.Status != "succeeded" || accepted.WorkspaceSnapshotID == "" || accepted.WorkspaceSnapshotID == snapshot {
		t.Fatalf("next release did not commit a new snapshot: %+v", accepted)
	}
	request("POST", publishPath, ownerCookie, publishInput, 409)
	snapshot = accepted.WorkspaceSnapshotID
	publishInput["workspace_snapshot_id"] = snapshot
	var secondRelease catalog.PublishManifest
	must(json.Unmarshal(request("POST", publishPath, ownerCookie, publishInput, 200).Body.Bytes(), &secondRelease))
	if secondRelease.Agent.ID != agent.ID || secondRelease.Agent.VersionID == published.Agent.VersionID || len(secondRelease.Skills) != 2 ||
		secondRelease.Skills[0].ID != partial.DefinitionID || secondRelease.Skills[0].VersionID == partial.ID ||
		secondRelease.Skills[1].ID != newSkill.DefinitionID || secondRelease.Skills[1].VersionID == newSkill.ID {
		t.Fatalf("next release did not update the same Agent and Skills: %+v", secondRelease)
	}
	newRelease, err := catalogue.Version(ctx, owner.ID, source.ID, "agent", agent.ID, secondRelease.Agent.VersionID)
	must(err)
	if newRelease.Bundle.Document.Markdown != "Second release from the same session" || newRelease.Bundle.Revision != basis.Agent.Revision+1 {
		t.Fatal("next release did not use the latest snapshot and the previous revision")
	}
	afterPublish, err = catalogue.Draft(ctx, owner.ID, source.ID, "agent", agent.ID)
	must(err)
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
	skill, err = catalogue.Upload(ctx, owner.ID, source.ID, "skill", skill.ID, "guide.txt", "text/plain", skill.Revision, strings.NewReader("draft-only resource"))
	must(err)
	afterPublishConfig := conf
	afterPublishConfig.Skills = []catalog.SkillRef{{ID: skill.ID, Version: catalog.Latest}}
	afterPublishYAML, err := json.Marshal(afterPublishConfig)
	must(err)
	draftAgent, err := catalogue.Save(ctx, owner.ID, source.ID, "agent", agent.ID, afterPublish.Revision, catalog.Document{Markdown: "Unpublished instructions.", ConfigYAML: string(afterPublishYAML)})
	must(err)
	_, err = catalogue.Publish(ctx, owner.ID, source.ID, "agent", agent.ID, "", draftAgent.Revision, false)
	if err == nil {
		t.Fatal("formal publish accepted a moving skill reference")
	}
	var conflictRun run.View
	must(json.Unmarshal(request("POST", iterationPath+"/runs", ownerCookie, map[string]string{
		"idempotency_key": uuid.NewString(), "text": "Keep the project unchanged",
	}, 202).Body.Bytes(), &conflictRun))
	for !run.Terminal(conflictRun.Status) && ctx.Err() == nil {
		time.Sleep(25 * time.Millisecond)
		conflictRun, err = sessions.RuntimeRun(ctx, owner.ID, source.ID, conflictRun.ID)
		must(err)
	}
	if conflictRun.Status != "succeeded" || conflictRun.WorkspaceSnapshotID == "" {
		t.Fatalf("conflict check did not get a new snapshot: %+v", conflictRun)
	}
	snapshot = conflictRun.WorkspaceSnapshotID
	conflict = request("POST", publishPath, ownerCookie, map[string]string{"workspace_snapshot_id": snapshot}, 409)
	must(json.Unmarshal(conflict.Body.Bytes(), &failure))
	if failure.Details.Result.Agent.Status != "failed" {
		t.Fatalf("expected Agent draft conflict: %s", conflict.Body.String())
	}
	draftAfterConflict, err := catalogue.Draft(ctx, owner.ID, source.ID, "agent", agent.ID)
	must(err)
	if draftAfterConflict.Revision != draftAgent.Revision {
		t.Fatal("continuous publication overwrote an external Agent draft")
	}
	var pinnedDraft run.Session
	pinInput := map[string]any{"agent_id": agent.ID, "version_id": "latest", "idempotency_key": uuid.NewString(), "expected_draft_revision": draftAgent.Revision}
	must(json.Unmarshal(request("POST", source.ID+"/sessions", ownerCookie, pinInput, 201).Body.Bytes(), &pinnedDraft))
	pinInput["agent_source_space_id"] = source.ID
	request("POST", target.ID+"/sessions", visitorCookie, pinInput, 400)
	definition, err := options.Service.Definition(ctx, owner.ID, source.ID, pinnedDraft.DefinitionRef)
	must(err)
	if pinnedDraft.DraftRevision != draftAgent.Revision || definition.Instructions != "Unpublished instructions." ||
		len(definition.Skills) != 1 || definition.Skills[0].VersionID != skill.ContentID {
		t.Fatalf("latest did not select the saved drafts: %+v", pinnedDraft)
	}
	_, err = catalogue.Upload(ctx, owner.ID, source.ID, "skill", skill.ID, "guide.txt", "text/plain", skill.Revision, strings.NewReader("overwrote draft resource"))
	must(err)
	pinnedContent := definition.Skills[0].VersionID
	if _, err := catalogStore.Lookup(ctx, source.ID, pinnedContent); err == nil {
		t.Fatal("test did not displace the pinned draft from the catalog")
	}
	_, runContent := publishCold.RunServices(run.Snapshot{SpaceID: source.ID, SessionID: pinnedDraft.ID}, content)
	rawResource, _, err := resource.ReadSkillPackageFile(ctx, runContent, owner.ID, source.ID, pinnedContent, "guide.txt")
	must(err)
	if string(rawResource) != "draft-only resource" {
		t.Fatalf("reopened draft session read %q", rawResource)
	}
	draftBinding.Store(pinnedContent)
	must(json.Unmarshal(request("POST", source.ID+"/sessions/"+pinnedDraft.ID+"/runs", ownerCookie, map[string]string{
		"idempotency_key": uuid.NewString(), "text": "Read the pinned draft",
	}, 202).Body.Bytes(), &accepted))
	for !run.Terminal(accepted.Status) && ctx.Err() == nil {
		time.Sleep(25 * time.Millisecond)
		accepted, err = sessions.RuntimeRun(ctx, owner.ID, source.ID, accepted.ID)
		must(err)
	}
	if accepted.Status != "succeeded" {
		t.Fatalf("draft debug run did not use its pinned Skill: status=%s error=%s", accepted.Status, accepted.Error)
	}
	var createSession run.Session
	must(json.Unmarshal(request("POST", localLaunch, ownerCookie, inputs(), 201).Body.Bytes(), &createSession))
	createPublishPath := localLaunch + "/" + createSession.ID + "/publish"
	createDebugPath := localLaunch + "/" + createSession.ID + "/draft-debug"
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
	createDebugInput := map[string]string{"workspace_snapshot_id": accepted.WorkspaceSnapshotID, "idempotency_key": uuid.NewString()}
	request("POST", createDebugPath, ownerCookie, createDebugInput, 400)
	createDebugInput["model_id"] = publishModel.ID
	blobs.loseCatalogReply.Store(true)
	ambiguous := request("POST", createDebugPath, ownerCookie, createDebugInput, 503)
	must(json.Unmarshal(ambiguous.Body.Bytes(), &failure))
	if failure.Details.Result.Agent.Status != "unknown" || failure.Details.Result.Agent.VersionID != "" {
		t.Fatalf("lost write response produced a definitive result: %+v", failure.Details.Result.Agent)
	}
	request("POST", createPublishPath, ownerCookie, map[string]string{"workspace_snapshot_id": accepted.WorkspaceSnapshotID, "model_id": model.ID}, 409)
	var newDebug run.Session
	must(json.Unmarshal(request("POST", createDebugPath, ownerCookie, createDebugInput, 201).Body.Bytes(), &newDebug))
	delete(createPublishInput, "model_id")
	must(json.Unmarshal(request("GET", createPreviewPath, ownerCookie, nil, 200).Body.Bytes(), &preview))
	if preview.ModelID != publishModel.ID || !preview.ModelLocked {
		t.Fatalf("reopened preview lost the pinned model: %+v", preview)
	}
	must(json.Unmarshal(request("POST", createPublishPath, ownerCookie, createPublishInput, 200).Body.Bytes(), &published))
	newAgent, err := catalogue.Draft(ctx, owner.ID, source.ID, "agent", published.Agent.ID)
	must(err)
	if newAgent.ID != newDebug.DefinitionID || newAgent.Name != newAgentConfig.Name || newAgent.Visibility != catalog.VisibilitySpace || newAgent.DefaultVersionID != published.Agent.VersionID ||
		newAgent.Revision != 1 || published.Agent.Status != "published" || published.Agent.Label != "v1" {
		t.Fatalf("unexpected new Agent: %+v", newAgent)
	}
	publishedVersion, err := catalogue.Version(ctx, owner.ID, source.ID, "agent", newAgent.ID, published.Agent.VersionID)
	must(err)
	if publishedVersion.Bundle.Config.Model != publishModel.ID || publishedVersion.Bundle.Model.ID != publishModel.ID {
		t.Fatal("published Agent did not bind the selected model")
	}
	if !json.Valid([]byte(publishedVersion.Bundle.Document.ConfigYAML)) {
		t.Fatal("published Agent kept the legacy YAML format")
	}
	var duplicateSession run.Session
	must(json.Unmarshal(request("POST", localLaunch, ownerCookie, inputs(), 201).Body.Bytes(), &duplicateSession))
	var duplicateRun run.View
	must(json.Unmarshal(request("POST", source.ID+"/sessions/"+duplicateSession.ID+"/runs", ownerCookie, map[string]string{
		"idempotency_key": uuid.NewString(), "text": "Create project for publication",
	}, 202).Body.Bytes(), &duplicateRun))
	for !run.Terminal(duplicateRun.Status) && ctx.Err() == nil {
		time.Sleep(25 * time.Millisecond)
		duplicateRun, err = sessions.RuntimeRun(ctx, owner.ID, source.ID, duplicateRun.ID)
		must(err)
	}
	if duplicateRun.Status != "succeeded" || duplicateRun.WorkspaceSnapshotID == "" {
		t.Fatalf("duplicate project did not commit its workspace: %+v", duplicateRun)
	}
	duplicatePublish := request("POST", localLaunch+"/"+duplicateSession.ID+"/publish", ownerCookie, map[string]string{
		"workspace_snapshot_id": duplicateRun.WorkspaceSnapshotID, "model_id": publishModel.ID,
	}, 409)
	must(json.Unmarshal(duplicatePublish.Body.Bytes(), &failure))
	if !strings.Contains(duplicatePublish.Body.String(), `"code":"definition_name_conflict"`) ||
		!strings.Contains(duplicatePublish.Body.String(), "Agent "+newAgent.Name+"：") ||
		failure.Details.Result.Agent.Status != "failed" {
		t.Fatal(duplicatePublish.Body.String())
	}
	var projectFile resource.WorkspaceEntryView
	must(json.Unmarshal(request("GET", source.ID+"/sessions/"+createSession.ID+"/workspace/files?path=agent.yaml", ownerCookie, nil, 200).Body.Bytes(), &projectFile))
	if projectFile.Text != string(legacyAgentYAML) {
		t.Fatal("publishing modified the legacy workspace file")
	}
	newIterationInput := inputs()
	newIterationInput["target_agent_id"] = newAgent.ID
	var newIteration run.Session
	must(json.Unmarshal(request("POST", localLaunch, ownerCookie, newIterationInput, 201).Body.Bytes(), &newIteration))
	newProjectPath := source.ID + "/sessions/" + newIteration.ID + "/workspace/files?path="
	must(json.Unmarshal(request("GET", newProjectPath+"agent.json", ownerCookie, nil, 200).Body.Bytes(), &projectFile))
	var restoredConfig catalog.Config
	must(json.Unmarshal([]byte(projectFile.Text), &restoredConfig))
	if restoredConfig.Model != publishModel.ID {
		t.Fatal("new iteration did not restore the published JSON config")
	}
	request("GET", newProjectPath+"agent.yaml", ownerCookie, nil, 404)
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

	must(lifecycle.Close(ctx))
	sessions = &archiveadapter.Runtime{DB: db.Store, Objects: objects, CatalogObject: catalogStore.Lookup,
		Log: &sessionlog.Store{Root: t.TempDir(), Prefix: "shared", Blobs: blobs}}
	objects.Lookups = []archiveadapter.ObjectLookup{catalogStore.Lookup, sessions.Lookup}
	content.Inline = sessions
	snapshots.Repo, snapshots.Journal.Repo = sessions, sessions
	options, lifecycle, err = bootstrap.StartRuntime(ctx, config, db.Store, auth, catalogue, content, manager, snapshots, "public-test", logger, modelTransport, sessions)
	must(err)
	handler = transport.NewHandler(fstest.MapFS{"index.html": {Data: []byte("test")}}, "public-test", logger, transport.IdentityOptions{Service: auth, Origin: "http://mass.test", Catalog: catalogue, Runtime: options})
	must(json.Unmarshal(request("POST", iterationPath+"/runs", ownerCookie, map[string]string{
		"idempotency_key": uuid.NewString(), "text": "Continue after archiving",
	}, 202).Body.Bytes(), &accepted))
	for !run.Terminal(accepted.Status) && ctx.Err() == nil {
		time.Sleep(25 * time.Millisecond)
		accepted, err = sessions.RuntimeRun(ctx, owner.ID, source.ID, accepted.ID)
		must(err)
	}
	if accepted.Status != "succeeded" || accepted.FinalResponseEventID == "" || accepted.WorkspaceSnapshotID == "" {
		t.Fatalf("existing conversation failed after archive reload: status=%s error=%s snapshot_error=%s", accepted.Status, accepted.Error, accepted.SnapshotError)
	}
	replayedEvents, err := snapshots.Journal.ReadRun(ctx, owner.ID, source.ID, iteration.ID, writtenPlan.RunID, 0, nil, 100)
	must(err)
	var replayedPlan journal.Event
	for _, event := range replayedEvents.Events {
		if event.Type == "plan.updated" {
			replayedPlan = event
		}
	}
	if replayedPlan.OperationID != writtenPlan.OperationID || replayedPlan.CausationID != writtenPlan.CausationID {
		t.Fatalf("replayed plan lost tool call provenance: operation=%q causation=%q", replayedPlan.OperationID, replayedPlan.CausationID)
	}
	assertFile("skills/source-skill/guide.txt", "second improvement")
	assertFile("skills/extra-skill/addendum.txt", "new resource for the same Skill")
}
