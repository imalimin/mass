package catalog

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/app/identity"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/resource"
)

type publishAttemptRepo struct {
	Repository
	drafts    map[string]Draft
	versions  map[string][]Version
	published map[string][]Version
	created   map[string]Draft
	model     identity.Model
}

func (r *publishAttemptRepo) CatalogDraft(_ context.Context, _, _, _, id string) (Draft, error) {
	d, ok := r.drafts[id]
	if !ok {
		return Draft{}, identity.ErrNotFound
	}
	return d, nil
}

func (r *publishAttemptRepo) CatalogVersion(_ context.Context, _, _, _, id, versionID string) (Version, error) {
	for _, v := range r.versions[id] {
		if v.ID == versionID {
			return v, nil
		}
	}
	return Version{}, identity.ErrNotFound
}

func (r *publishAttemptRepo) LatestFormalVersion(_ context.Context, _, _, _, id string) (Version, bool, error) {
	var latest Version
	var published bool
	for _, v := range r.versions[id] {
		if v.Debug {
			continue
		}
		published = true
		if !v.Archived {
			latest = v
		}
	}
	return latest, published, nil
}

func (r *publishAttemptRepo) CatalogCreate(_ context.Context, _, _ string, input Draft) error {
	r.drafts[input.ID] = input
	if r.created == nil {
		r.created = map[string]Draft{}
	}
	r.created[input.ID] = input
	return nil
}

func (r *publishAttemptRepo) CatalogSaveBasedOn(_ context.Context, _, _ string, input Draft, _ string) error {
	input.Revision++
	r.drafts[input.ID] = input
	return nil
}

func (r *publishAttemptRepo) CatalogPrepare(_ context.Context, _, _, id string, revision int64) (Bundle, error) {
	d, ok := r.drafts[id]
	if !ok {
		return Bundle{}, identity.ErrNotFound
	}
	if d.Revision != revision {
		return Bundle{}, ErrDraft
	}
	cfg, err := ValidateDocument("agent", d.Name, d.Document, true)
	if err != nil {
		return Bundle{}, err
	}
	bindings := make([]Binding, 0, len(cfg.Skills))
	for _, ref := range cfg.Skills {
		var matched Version
		for _, v := range r.versions[ref.ID] {
			if v.Label == ref.Version {
				matched = v
				break
			}
		}
		if matched.ID == "" {
			return Bundle{}, ErrConfig
		}
		bindings = append(bindings, Binding{SkillID: ref.ID, VersionID: matched.ID, Version: matched.Label, SHA256: matched.SHA256})
	}
	model := r.model
	return Bundle{FormatVersion: 1, DefinitionID: d.ID, Kind: "agent", Name: d.Name, Revision: d.Revision, Document: d.Document, Config: cfg, Model: &model, Bindings: bindings}, nil
}

func (r *publishAttemptRepo) CatalogPublishBasedOn(_ context.Context, _, _ string, input *Version, _ string) error {
	if input.Label == "" {
		input.Label = "v1"
	}
	r.versions[input.DefinitionID] = append(r.versions[input.DefinitionID], *input)
	if r.published == nil {
		r.published = map[string][]Version{}
	}
	r.published[input.DefinitionID] = append(r.published[input.DefinitionID], *input)
	return nil
}

type publishAttemptContentRepo struct {
	resource.ContentRepository
	objects map[string]storage.Object
}

func (r *publishAttemptContentRepo) BeginObject(_ context.Context, _ string, o storage.Object) (storage.Object, error) {
	r.objects[o.ID] = o
	return o, nil
}

func (r *publishAttemptContentRepo) ReadyObject(_ context.Context, _ string, o storage.Object) error {
	o.State = "ready"
	r.objects[o.ID] = o
	return nil
}

func (r *publishAttemptContentRepo) Object(_ context.Context, _, _, id string, _ bool) (storage.Object, error) {
	o, ok := r.objects[id]
	if !ok {
		return storage.Object{}, identity.ErrNotFound
	}
	return o, nil
}

type publishAttemptBlobs struct {
	storage.BlobStore
	data map[string][]byte
}

func (b *publishAttemptBlobs) PutNew(_ context.Context, key string, r io.Reader, _ int64, _ string) (storage.BlobInfo, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return storage.BlobInfo{}, err
	}
	b.data[key] = raw
	return storage.BlobInfo{ETag: "etag"}, nil
}

func (b *publishAttemptBlobs) Open(_ context.Context, key, _ string) (io.ReadCloser, error) {
	raw, ok := b.data[key]
	if !ok {
		return nil, storage.ErrBlobNotFound
	}
	return io.NopCloser(bytes.NewReader(raw)), nil
}

func (b *publishAttemptBlobs) URI(key string) string { return "mem://" + key }

func TestNewPublishPlanReusesPreviouslyPublishedMatchingSkillVersion(t *testing.T) {
	ctx := context.Background()
	spaceID := storage.NewShortID()
	skillID := uuid.NewString()
	v1ID := uuid.NewString()
	v2ID := uuid.NewString()
	modelID := uuid.NewString()

	blobs := &publishAttemptBlobs{data: map[string][]byte{}}
	objRepo := &publishAttemptContentRepo{objects: map[string]storage.Object{}}
	storeSkillZip := func(defID, name string, revision int64, markdown string) (string, string) {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		manifest, _ := json.Marshal(Bundle{
			FormatVersion: 1,
			DefinitionID:  defID,
			Kind:          "skill",
			Name:          name,
			Revision:      revision,
			Bindings:      []Binding{},
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
		objRepo.objects[id] = storage.Object{
			ID: id, SpaceID: spaceID, Key: key, Kind: resource.SkillPackageKind,
			MediaType: "application/zip", Size: int64(len(raw)), SHA256: digest, State: "ready",
		}
		blobs.data[key] = raw
		return id, digest
	}

	v1Markdown := "---\nname: weekly-report\ndescription: v1\n---\n# Old"
	v2Markdown := "---\nname: weekly-report\ndescription: v2\n---\n# Upgraded"
	v1Content, v1SHA := storeSkillZip(skillID, "weekly-report", 1, v1Markdown)
	v2Content, v2SHA := storeSkillZip(skillID, "weekly-report", 2, v2Markdown)

	// Simulate state after an earlier publish attempt succeeded on Skill (publishing v2)
	// and failed on Agent (e.g. name conflict): Skill draft is at revision 2 and latest formal is v2.
	repo := &publishAttemptRepo{
		model: identity.Model{ID: modelID, Name: "deepseek", Provider: "ark", Enabled: true, ContextWindow: 400000, MaxOutputTokens: 64000},
		drafts: map[string]Draft{
			skillID: {
				Definition: Definition{ID: skillID, Kind: "skill", Name: "weekly-report"},
				Revision:   2,
				ContentID:  v2Content,
			},
		},
		versions: map[string][]Version{
			skillID: {
				{ID: v1ID, DefinitionID: skillID, Label: "v1", ContentID: v1Content, SHA256: v1SHA, CreatedAt: time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)},
				{ID: v2ID, DefinitionID: skillID, Label: "v2", ContentID: v2Content, SHA256: v2SHA, CreatedAt: time.Date(2026, 11, 22, 10, 15, 0, 0, time.UTC)},
			},
		},
	}
	svc := &Service{
		Repo:    repo,
		Content: &resource.ContentStore{Repo: objRepo, Blobs: blobs, MaxBytes: 4 << 20},
	}

	// Workspace on the follow-up snapshot: Agent renamed to weekly-report-02,
	// proj.json still pins v1, local skills/weekly-report matches already-published v2.
	projJSON, _ := json.Marshal(map[string]any{
		"skills": []map[string]string{{
			"skill_id":   skillID,
			"version_id": v1ID,
			"path":       "skills/weekly-report",
		}},
	})
	agentJSON := `{
  "schema_version": 1,
  "name": "weekly-report-02",
  "model": "",
  "skills": [],
  "tools": ["read_file"],
  "context": {"max_input_tokens": 300000, "max_tokens": 32000, "compression": "summary"},
  "limits": {"max_steps": 40, "timeout_seconds": 600}
}`
	files := resource.ProjectFiles{
		Files: map[string][]byte{
			"AGENTS.md":                     []byte("# weekly-report-02"),
			resource.AgentConfigPath:        []byte(agentJSON),
			resource.ProjectBindingsPath:    projJSON,
			"skills/weekly-report/SKILL.md": []byte(v2Markdown),
		},
	}

	project, err := svc.PreparePublishAgent(ctx, "actor", spaceID, run.Creativity{}, files, modelID)
	if err != nil {
		t.Fatalf("PreparePublishAgent failed: %v", err)
	}
	project, err = svc.ScanPublishSkills(ctx, "actor", spaceID, files, project, nil)
	if err != nil {
		t.Fatalf("ScanPublishSkills failed: %v", err)
	}

	result := project.PendingResult()
	plan, err := svc.NewPublishPlan(ctx, "actor", spaceID, uuid.NewString(), run.Creativity{}, project, &result, run.CreativityPublishPlan{})
	if err != nil {
		t.Fatalf("NewPublishPlan failed: %v", err)
	}
	if len(plan.Skills) != 1 || plan.Skills[0].NextVersionID != v2ID {
		t.Fatalf("expected plan.Skills[0].NextVersionID=%s, got %+v", v2ID, plan.Skills)
	}

	manifest, err := svc.PublishProject(ctx, "actor", spaceID, project, plan)
	if err != nil {
		t.Fatalf("PublishProject failed: %v", err)
	}
	if manifest.Skills[0].Status != "published" || manifest.Skills[0].VersionID != v2ID || manifest.Skills[0].Label != "v2" {
		t.Fatalf("expected Skill to reuse v2, got %+v", manifest.Skills[0])
	}
	if len(repo.published[skillID]) != 0 {
		t.Fatalf("expected no duplicate Skill version publish, got %d", len(repo.published[skillID]))
	}
	if manifest.Agent.Status != "published" || manifest.Agent.Name != "weekly-report-02" {
		t.Fatalf("expected Agent published, got %+v", manifest.Agent)
	}
	if len(manifest.Publication.Skills) != 1 || manifest.Publication.Skills[0].Version != v2ID || manifest.Publication.Skills[0].SourceVersion != v1ID {
		t.Fatalf("unexpected publication skill record: %+v", manifest.Publication.Skills)
	}

	// Divergent local Skill content when catalog has already moved past v1 must still fail with ErrDraft.
	files.Files["skills/weekly-report/SKILL.md"] = []byte("---\nname: weekly-report\ndescription: v3\n---\n# Divergent")
	divergentProject, err := svc.PreparePublishAgent(ctx, "actor", spaceID, run.Creativity{}, files, modelID)
	if err != nil {
		t.Fatal(err)
	}
	divergentProject, err = svc.ScanPublishSkills(ctx, "actor", spaceID, files, divergentProject, nil)
	if err != nil {
		t.Fatal(err)
	}
	divergentResult := divergentProject.PendingResult()
	_, err = svc.NewPublishPlan(ctx, "actor", spaceID, uuid.NewString(), run.Creativity{}, divergentProject, &divergentResult, run.CreativityPublishPlan{})
	if !errors.Is(err, ErrDraft) {
		t.Fatalf("expected ErrDraft for divergent skill content, got %v", err)
	}
}
