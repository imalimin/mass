package httptransport

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/adapter/secrets"
	"github.com/imalimin/mass/internal/app/catalog"
	"github.com/imalimin/mass/internal/app/identity"
	sessionapp "github.com/imalimin/mass/internal/app/session"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/resource"
	"github.com/imalimin/mass/internal/transport/agui"
)

type stubCatalogRepo struct {
	catalog.Repository
	agents   []catalog.Definition
	skills   []catalog.Definition
	drafts   map[string]catalog.Draft
	versions map[string][]catalog.Version
}

func (s *stubCatalogRepo) CatalogList(_ context.Context, _, _, kind string, _ identity.Query) ([]catalog.Definition, error) {
	if kind == "agent" {
		return append([]catalog.Definition(nil), s.agents...), nil
	}
	return append([]catalog.Definition(nil), s.skills...), nil
}

func (s *stubCatalogRepo) CatalogDraft(_ context.Context, _, _, _, id string) (catalog.Draft, error) {
	d, ok := s.drafts[id]
	if !ok {
		return catalog.Draft{}, identity.ErrNotFound
	}
	return d, nil
}

func (s *stubCatalogRepo) CatalogVersions(_ context.Context, _, _, _, id string, _ bool, _ identity.Query) ([]catalog.Version, error) {
	return append([]catalog.Version(nil), s.versions[id]...), nil
}

func (s *stubCatalogRepo) CatalogVersion(_ context.Context, _, _, _, id, versionID string) (catalog.Version, error) {
	for _, v := range s.versions[id] {
		if v.ID == versionID {
			return v, nil
		}
	}
	return catalog.Version{}, identity.ErrNotFound
}

type stubObjectRepo struct {
	resource.ContentRepository
	objects map[string]storage.Object
}

func (s *stubObjectRepo) Object(_ context.Context, _, _, id string, _ bool) (storage.Object, error) {
	o, ok := s.objects[id]
	if !ok {
		return storage.Object{}, identity.ErrNotFound
	}
	return o, nil
}

type stubBlobStore struct {
	storage.BlobStore
	data map[string][]byte
}

func (s *stubBlobStore) Open(_ context.Context, key, _ string) (io.ReadCloser, error) {
	raw, ok := s.data[key]
	if !ok {
		return nil, storage.ErrBlobNotFound
	}
	return io.NopCloser(bytes.NewReader(raw)), nil
}

type modelsAwareRepo struct {
	*openapiTestRepo
	models []identity.Model
}

func (r *modelsAwareRepo) Models(_ context.Context, actor, space string, _ identity.Query, available bool) ([]identity.Model, error) {
	if err := r.CheckAccess(context.Background(), actor, space, false); err != nil {
		return nil, err
	}
	var out []identity.Model
	for _, m := range r.models {
		if available && !m.Enabled {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

func TestSkillCLIReadOnlyResourcesAndScopes(t *testing.T) {
	baseRepo := newOpenAPITestRepo()
	repo := &modelsAwareRepo{
		openapiTestRepo: baseRepo,
		models: []identity.Model{
			{ID: uuid.NewString(), Name: "enabled-model", Provider: "ark", ModelID: "ep-1", BaseURL: "https://example.com", ContextWindow: 32768, MaxOutputTokens: 4096, Temperature: 0.7, Enabled: true, Revision: 1},
			{ID: uuid.NewString(), Name: "disabled-model", Provider: "ark", ModelID: "ep-2", BaseURL: "https://example.com", ContextWindow: 32768, MaxOutputTokens: 4096, Temperature: 0.7, Enabled: false, Revision: 1},
		},
	}
	owner := identity.User{ID: uuid.NewString(), Username: "owner"}
	member := identity.User{ID: uuid.NewString(), Username: "member"}
	spaceA := storage.NewShortID()
	spaceB := storage.NewShortID()
	repo.owners[spaceA] = map[string]bool{owner.ID: true}
	repo.members[spaceA] = map[string]bool{owner.ID: true, member.ID: true}
	repo.owners[spaceB] = map[string]bool{owner.ID: true}
	repo.members[spaceB] = map[string]bool{owner.ID: true, member.ID: true}
	ownerCookie := repo.addCookieSession(owner)

	cipher, err := secrets.New(bytes.Repeat([]byte{8}, 32))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := identity.New(repo, cipher, time.Hour, []byte("skill-cli-cursor"))
	if err != nil {
		t.Fatal(err)
	}

	// Build two skills: one published (description from formal version), one draft-only (description from draft).
	publishedSkillID := uuid.NewString()
	draftSkillID := uuid.NewString()
	noDescSkillID := uuid.NewString()
	verID := uuid.NewString()
	agentID := storage.NewShortID()

	objects := &stubObjectRepo{objects: map[string]storage.Object{}}
	blobs := &stubBlobStore{data: map[string][]byte{}}
	putSkillZip := func(space, defID, name, markdown string, dirs []string, files map[string]string) (string, string) {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		assets := make([]catalog.Asset, 0, len(files))
		for filePath, content := range files {
			sum := sha256.Sum256([]byte(content))
			assets = append(assets, catalog.Asset{
				Path:      filePath,
				SHA256:    hex.EncodeToString(sum[:]),
				Size:      int64(len(content)),
				MediaType: "text/plain",
			})
			fw, _ := zw.Create(filePath)
			_, _ = fw.Write([]byte(content))
		}
		for _, dir := range dirs {
			_, _ = zw.Create(dir + "/")
		}
		manifest, _ := json.Marshal(catalog.Bundle{
			FormatVersion: 1,
			DefinitionID:  defID,
			Kind:          "skill",
			Name:          name,
			Revision:      1,
			Document: catalog.Document{
				Assets:      assets,
				Directories: dirs,
			},
			Bindings: []catalog.Binding{},
		})
		mw, _ := zw.Create(resource.SkillManifestPath)
		_, _ = mw.Write(manifest)
		dw, _ := zw.Create(resource.SkillDocumentPath)
		_, _ = dw.Write([]byte(markdown))
		_ = zw.Close()
		raw := buf.Bytes()
		sum := sha256.Sum256(raw)
		digest := hex.EncodeToString(sum[:])
		id := uuid.NewString()
		key := "objects/" + id
		objects.objects[id] = storage.Object{
			ID:        id,
			SpaceID:   space,
			Key:       key,
			Kind:      resource.SkillPackageKind,
			MediaType: "application/zip",
			Size:      int64(len(raw)),
			SHA256:    digest,
			State:     "ready",
		}
		blobs.data[key] = raw
		return id, digest
	}
	verMarkdown := "---\nname: published-skill\ndescription: From published version\n---\n# Body"
	verHelperScript := "print('hello from skill')\n"
	verContentID, verSHA := putSkillZip(spaceA, publishedSkillID, "published-skill", verMarkdown, []string{"templates"}, map[string]string{"scripts/helper.py": verHelperScript})
	pubDraftContentID, _ := putSkillZip(spaceA, publishedSkillID, "published-skill", "---\ndescription: Draft override\n---\n# Draft", nil, nil)
	draftContentID, _ := putSkillZip(spaceA, draftSkillID, "draft-skill", "---\ndescription: From draft markdown\n---\n# Draft", nil, nil)
	noDescContentID, _ := putSkillZip(spaceA, noDescSkillID, "nodesc-skill", "# Plain skill without frontmatter", nil, nil)

	catRepo := &stubCatalogRepo{
		agents: []catalog.Definition{{ID: agentID, Kind: "agent", Name: "helper-agent"}},
		skills: []catalog.Definition{
			{ID: publishedSkillID, Kind: "skill", Name: "published-skill", LatestVersion: "v1"},
			{ID: draftSkillID, Kind: "skill", Name: "draft-skill"},
			{ID: noDescSkillID, Kind: "skill", Name: "nodesc-skill"},
		},
		drafts: map[string]catalog.Draft{
			publishedSkillID: {Definition: catalog.Definition{ID: publishedSkillID, Kind: "skill", Name: "published-skill"}, Revision: 1, ContentID: pubDraftContentID},
			draftSkillID:     {Definition: catalog.Definition{ID: draftSkillID, Kind: "skill", Name: "draft-skill"}, Revision: 1, ContentID: draftContentID},
			noDescSkillID:    {Definition: catalog.Definition{ID: noDescSkillID, Kind: "skill", Name: "nodesc-skill"}, Revision: 1, ContentID: noDescContentID},
		},
		versions: map[string][]catalog.Version{
			publishedSkillID: {{
				ID: verID, DefinitionID: publishedSkillID, Label: "v1", ContentID: verContentID, SHA256: verSHA, CreatedAt: time.Now().UTC(),
			}},
		},
	}
	catSvc := &catalog.Service{
		Repo:    catRepo,
		Content: &resource.ContentStore{Repo: objects, Blobs: blobs, MaxBytes: 1 << 20},
	}
	rtRepo := stubRuntimeRepo{}
	handler := NewHandler(fstest.MapFS{"index.html": {Data: []byte("MASS")}}, "test", slog.New(slog.NewTextHandler(io.Discard, nil)), IdentityOptions{
		Service: svc,
		Catalog: catSvc,
		Origin:  "http://mass.test",
		Tools: []PlatformTool{
			{Name: "read_file", Version: "1", Description: "Read file contents."},
			{Name: "run_command", Version: "1", Description: "Run installed executable."},
		},
		Runtime: &RuntimeOptions{
			Repo:    rtRepo,
			Service: &sessionapp.Service{Repo: rtRepo, Identity: svc},
			UI:      &agui.Projector{Repo: &stubAGUIRepo{}, Live: &agui.Live{}},
		},
	})

	do := func(method, path string, headers map[string]string, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://mass.test"+path, strings.NewReader(body))
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	// Mint both a run_read token (for member in spaceA) and an openapi token (for owner in spaceA).
	runToken, revokeRun, err := svc.IssueRunReadToken(spaceA, member.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer revokeRun()
	openapiCreated, err := svc.CreateOpenAPIToken(context.Background(), owner.ID, spaceA, "external", nil)
	if err != nil {
		t.Fatal(err)
	}

	// 1. All 6 read-only endpoints succeed with run_read token, openapi token, and Cookie.
	packagePath := "/api/v1/spaces/" + spaceA + "/skills/" + publishedSkillID + "/versions/" + verID + "/package"
	readOnlyPaths := []string{
		"/api/v1/spaces/" + spaceA + "/skills",
		"/api/v1/spaces/" + spaceA + "/skills/" + publishedSkillID + "/versions",
		packagePath,
		"/api/v1/spaces/" + spaceA + "/agents",
		"/api/v1/spaces/" + spaceA + "/models?available=true",
		"/api/v1/spaces/" + spaceA + "/tools",
	}
	for _, cred := range []struct {
		label   string
		headers map[string]string
	}{
		{"run_read", map[string]string{"Authorization": "Bearer " + runToken}},
		{"openapi", map[string]string{"Authorization": "Bearer " + openapiCreated.Token}},
		{"cookie", map[string]string{"Cookie": ownerCookie}},
	} {
		for _, p := range readOnlyPaths {
			w := do("GET", p, cred.headers, "")
			if w.Code != 200 {
				t.Fatalf("%s GET %s: got %d %s", cred.label, p, w.Code, w.Body.String())
			}
		}
	}

	// 2. GET /tools returns only name/version/description (no schema).
	w := do("GET", "/api/v1/spaces/"+spaceA+"/tools", map[string]string{"Authorization": "Bearer " + runToken}, "")
	if w.Code != 200 || strings.Contains(w.Body.String(), `"schema"`) || !strings.Contains(w.Body.String(), `"read_file"`) {
		t.Fatalf("unexpected /tools response: %d %s", w.Code, w.Body.String())
	}

	// 3. GET /skills include_description behavior: default omits description, true populates description, invalid -> 400.
	w = do("GET", "/api/v1/spaces/"+spaceA+"/skills", map[string]string{"Authorization": "Bearer " + runToken}, "")
	if w.Code != 200 || strings.Contains(w.Body.String(), `"description"`) {
		t.Fatalf("default /skills should omit description: %d %s", w.Code, w.Body.String())
	}
	w = do("GET", "/api/v1/spaces/"+spaceA+"/skills?include_description=false", map[string]string{"Authorization": "Bearer " + runToken}, "")
	if w.Code != 200 || strings.Contains(w.Body.String(), `"description"`) {
		t.Fatalf("include_description=false should omit description: %d %s", w.Code, w.Body.String())
	}
	w = do("GET", "/api/v1/spaces/"+spaceA+"/skills?include_description=yes", map[string]string{"Authorization": "Bearer " + runToken}, "")
	if w.Code != 400 {
		t.Fatalf("invalid include_description: got %d want 400", w.Code)
	}
	w = do("GET", "/api/v1/spaces/"+spaceA+"/skills?include_description=true", map[string]string{"Authorization": "Bearer " + runToken}, "")
	if w.Code != 200 {
		t.Fatalf("include_description=true: got %d %s", w.Code, w.Body.String())
	}
	var skillPage struct {
		Items []struct {
			ID          string  `json:"id"`
			Description *string `json:"description"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &skillPage); err != nil {
		t.Fatal(err)
	}
	gotDesc := map[string]string{}
	for _, item := range skillPage.Items {
		if item.Description == nil {
			t.Fatalf("skill %s missing description field when include_description=true", item.ID)
		}
		gotDesc[item.ID] = *item.Description
	}
	if gotDesc[publishedSkillID] != "From published version" {
		t.Fatalf("published skill desc=%q", gotDesc[publishedSkillID])
	}
	if gotDesc[draftSkillID] != "From draft markdown" {
		t.Fatalf("draft skill desc=%q", gotDesc[draftSkillID])
	}
	if gotDesc[noDescSkillID] != "" {
		t.Fatalf("no-desc skill desc=%q", gotDesc[noDescSkillID])
	}

	// 3b. GET /skills/{id}/versions/{version_id}/package returns a clean ZIP (without bundle.json) and metadata headers.
	w = do("GET", packagePath, map[string]string{"Authorization": "Bearer " + runToken}, "")
	if w.Code != 200 {
		t.Fatalf("package download failed: %d %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("unexpected Content-Type: %q", ct)
	}
	if w.Header().Get("X-Mass-Skill-Id") != publishedSkillID ||
		w.Header().Get("X-Mass-Skill-Name") != "published-skill" ||
		w.Header().Get("X-Mass-Skill-Version-Id") != verID ||
		w.Header().Get("X-Mass-Skill-Version") != "v1" {
		t.Fatalf("unexpected metadata headers: %+v", w.Header())
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "published-skill-v1.zip") {
		t.Fatalf("unexpected Content-Disposition: %q", cd)
	}
	zr, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatalf("invalid zip returned: %v", err)
	}
	entries := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		entries[f.Name] = string(raw)
	}
	if _, hasBundle := entries[resource.SkillManifestPath]; hasBundle {
		t.Fatalf("exported skill zip must omit internal %s", resource.SkillManifestPath)
	}
	if entries[resource.SkillDocumentPath] != verMarkdown {
		t.Fatalf("unexpected SKILL.md: %q", entries[resource.SkillDocumentPath])
	}
	if entries["scripts/helper.py"] != verHelperScript {
		t.Fatalf("unexpected asset scripts/helper.py: %q", entries["scripts/helper.py"])
	}
	if _, hasDir := entries["templates/"]; !hasDir {
		t.Fatalf("expected explicit directory templates/ in exported zip, got %v", entries)
	}
	// Missing version returns 404.
	w = do("GET", "/api/v1/spaces/"+spaceA+"/skills/"+publishedSkillID+"/versions/"+uuid.NewString()+"/package", map[string]string{"Authorization": "Bearer " + runToken}, "")
	if w.Code != 404 {
		t.Fatalf("missing version package: got %d want 404", w.Code)
	}

	// 4. Cross-space requests with run_read and openapi tokens return 403.
	for _, p := range []string{
		"/api/v1/spaces/" + spaceB + "/skills",
		"/api/v1/spaces/" + spaceB + "/tools",
		"/api/v1/spaces/" + spaceB + "/agents",
		"/api/v1/spaces/" + spaceB + "/models",
		"/api/v1/spaces/" + spaceB + "/skills/" + publishedSkillID + "/versions",
		"/api/v1/spaces/" + spaceB + "/skills/" + publishedSkillID + "/versions/" + verID + "/package",
	} {
		for _, tok := range []string{runToken, openapiCreated.Token} {
			w := do("GET", p, map[string]string{"Authorization": "Bearer " + tok}, "")
			if w.Code != 403 {
				t.Fatalf("cross-space GET %s: got %d want 403", p, w.Code)
			}
		}
	}

	// 5. run_read token is rejected (401) on session endpoints, draft endpoint, write endpoints, and OpenAPI management.
	sessionID := storage.NewShortID()
	runID := storage.NewShortID()
	for _, tc := range []struct {
		method, path, body string
	}{
		{"POST", "/api/v1/spaces/" + spaceA + "/sessions", `{"idempotency_key":"k1","agent_id":"` + agentID + `"}`},
		{"POST", "/api/v1/spaces/" + spaceA + "/sessions/" + sessionID + "/runs", `{"idempotency_key":"k2","text":"hi"}`},
		{"GET", "/api/v1/spaces/" + spaceA + "/sessions/" + sessionID + "/messages", ""},
		{"GET", "/api/v1/spaces/" + spaceA + "/runs/" + runID + "/events", ""},
		{"GET", "/api/v1/spaces/" + spaceA + "/skills/" + publishedSkillID + "/draft", ""},
		{"POST", "/api/v1/spaces/" + spaceA + "/skills", `{"name":"new-skill"}`},
		{"GET", "/api/v1/spaces/" + spaceA + "/openapi/tokens", ""},
	} {
		w := do(tc.method, tc.path, map[string]string{
			"Authorization": "Bearer " + runToken,
			"Cookie":        ownerCookie,
			"Content-Type":  "application/json",
		}, tc.body)
		if w.Code != 401 {
			t.Fatalf("run_read %s %s: got %d want 401 (%s)", tc.method, tc.path, w.Code, w.Body.String())
		}
	}

	// 6. After revokeRun(), run_read token immediately returns 401.
	revokeRun()
	for _, p := range readOnlyPaths {
		w := do("GET", p, map[string]string{"Authorization": "Bearer " + runToken}, "")
		if w.Code != 401 {
			t.Fatalf("revoked run_read GET %s: got %d want 401", p, w.Code)
		}
	}
}
