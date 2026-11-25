package archive

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/imalimin/mass/internal/app/catalog"
	"github.com/imalimin/mass/internal/app/identity"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/resource"
	"github.com/imalimin/mass/tests/fakes"
)

func TestCatalogListMetadataUsesPublishedAndRecordedChanges(t *testing.T) {
	day := func(n int) time.Time { return time.Date(2026, time.November, n, 0, 0, 0, 0, time.UTC) }
	index := catalogIndex{
		"a": {Definition: catalog.Definition{ID: "a", Kind: "agent", DefaultVersionID: "first"}, Versions: []*version{
			{ID: "first", Label: "v1", CreatedAt: day(1)},
			{ID: "latest", Label: "release", CreatedAt: day(2)},
			{ID: "archived", Label: "v3", CreatedAt: day(3), Archived: true},
			{ID: "debug", CreatedAt: day(4), Debug: true},
		}},
		"b": {Definition: catalog.Definition{ID: "b", Kind: "agent"}},
		"c": {Definition: catalog.Definition{ID: "c", Kind: "agent"}},
		"skill": {Definition: catalog.Definition{ID: "skill", Kind: "skill"}, Versions: []*version{
			{ID: "skill-release", Label: "v1", CreatedAt: day(3)},
		}},
	}
	raw, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	blobs := fakes.NewBlobs()
	blobs.Data["space/catalog.json"] = raw
	c := &Catalog{
		Blobs:   blobs,
		Objects: &Objects{Authorize: func(context.Context, string, string, bool) error { return nil }},
		UpdateTimes: func(_ context.Context, _, _ string, ids []string) (map[string]time.Time, error) {
			if slices.Equal(ids, []string{"skill"}) {
				return nil, nil
			}
			if !slices.Equal(ids, []string{"a", "b"}) {
				t.Fatalf("read updates outside the requested page: %v", ids)
			}
			return map[string]time.Time{"a": day(5), "b": day(1)}, nil
		},
	}
	items, err := c.CatalogList(t.Context(), "actor", "space", "agent", identity.Query{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].LatestVersion != "release" || !items[0].UpdatedAt.Equal(day(5)) ||
		items[1].LatestVersion != "" || !items[1].UpdatedAt.Equal(day(1)) {
		t.Fatalf("unexpected list metadata: %+v", items)
	}
	items, err = c.CatalogList(t.Context(), "actor", "space", "skill", identity.Query{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].LatestVersion != "v1" || !items[0].UpdatedAt.Equal(day(3)) {
		t.Fatalf("unexpected Skill list metadata: %+v", items)
	}
}

func TestSkillUsageAndDelete(t *testing.T) {
	ctx := t.Context()
	space := storage.NewShortID()
	blobs := fakes.NewBlobs()
	objects := &Objects{Authorize: func(context.Context, string, string, bool) error { return nil }}
	content := &resource.ContentStore{Repo: objects, Blobs: blobs, Prefix: "test", MaxBytes: 8 << 20}
	model := identity.Model{
		ID: "00000000-0000-4000-8000-000000000001", Name: "fixture", Provider: "ark",
		ModelID: "fixture", BaseURL: identity.ArkEndpoint, ContextWindow: 32768, MaxOutputTokens: 4096, Enabled: true,
	}
	repo := &Catalog{
		Blobs:   blobs,
		Prefix:  "test",
		Objects: objects,
		Content: content,
		Model:   func(context.Context, string, string, string) (identity.Model, error) { return model, nil },
		Audit:   func(context.Context, string, string, string, string) error { return nil },
	}
	objects.Lookups = []ObjectLookup{repo.Lookup}
	svc := &catalog.Service{Repo: repo, Content: content}

	skill, err := svc.Create(ctx, "actor", space, "skill", "rules")
	if err != nil {
		t.Fatal(err)
	}
	skill, err = svc.Save(ctx, "actor", space, "skill", skill.ID, skill.Revision, catalog.Document{Markdown: "# Rules"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Publish(ctx, "actor", space, "skill", skill.ID, "v1", skill.Revision, false); err != nil {
		t.Fatal(err)
	}

	// Initially unused.
	usage, err := svc.SkillUsage(ctx, "actor", space, skill.ID)
	if err != nil || len(usage) != 0 {
		t.Fatalf("expected unused skill, got %+v (%v)", usage, err)
	}

	// Agent referencing the skill in draft and published version.
	alpha, err := svc.Create(ctx, "actor", space, "agent", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	cfg := catalog.Config{
		SchemaVersion: 1, Name: "alpha", Model: model.ID,
		Skills: []catalog.SkillRef{{ID: skill.ID, Version: "v1"}},
		Tools:  []string{"read_file"},
		Context: catalog.ContextConfig{
			MaxInputTokens: 20000, MaxTokens: 1024, Compression: "summary",
		},
	}
	cfg.Limits.MaxSteps, cfg.Limits.TimeoutSeconds = 5, 30
	rawCfg, _ := json.Marshal(cfg)
	alpha, err = svc.Save(ctx, "actor", space, "agent", alpha.ID, alpha.Revision, catalog.Document{Markdown: "Alpha", ConfigYAML: string(rawCfg)})
	if err != nil {
		t.Fatal(err)
	}

	// Unrelated agent should not appear in usage.
	if _, err := svc.Create(ctx, "actor", space, "agent", "beta"); err != nil {
		t.Fatal(err)
	}

	usage, err = svc.SkillUsage(ctx, "actor", space, skill.ID)
	if err != nil || len(usage) != 1 || usage[0].AgentID != alpha.ID || !usage[0].InDraft || len(usage[0].Versions) != 0 {
		t.Fatalf("expected draft usage for alpha, got %+v (%v)", usage, err)
	}

	if _, err := svc.Publish(ctx, "actor", space, "agent", alpha.ID, "v1", alpha.Revision, false); err != nil {
		t.Fatal(err)
	}
	usage, err = svc.SkillUsage(ctx, "actor", space, skill.ID)
	if err != nil || len(usage) != 1 || !usage[0].InDraft || !slices.Equal(usage[0].Versions, []string{"v1"}) {
		t.Fatalf("expected draft+v1 usage for alpha, got %+v (%v)", usage, err)
	}

	// Historical Agent bundles with retired ContextConfig fields (e.g. reserved_output_tokens)
	// must still be scanned cleanly without returning storage.ErrIntegrity.
	idx, err := repo.load(ctx, space)
	if err != nil {
		t.Fatal(err)
	}
	skillVerID := idx[skill.ID].Versions[0].ID
	legacyKey := Key("test", space, "agents", alpha.ID, "versions", "legacy.json")
	blobs.Data[legacyKey] = []byte(`{"format_version":1,"definition_id":"` + alpha.ID + `","kind":"agent","name":"alpha","revision":1,"document":{"markdown":"legacy"},"config":{"schema_version":1,"name":"alpha","context":{"max_input_tokens":20000,"reserved_output_tokens":512,"compression":"summary"}},"bindings":[{"skill_id":"` + skill.ID + `","version_id":"` + skillVerID + `","version":"v1","sha256":"` + idx[skill.ID].Versions[0].Object.SHA256 + `"}]}`)
	idx[alpha.ID].Versions = append(idx[alpha.ID].Versions, &version{
		ID: "00000000-0000-4000-8000-000000000099", Label: "v0", CreatedAt: time.Now().UTC(),
		Object: Ref{Object: storage.Object{ID: "00000000-0000-4000-8000-000000000098", Kind: "definition.version", MediaType: "application/json"}, Key: legacyKey},
	})
	rawIdx, _ := json.Marshal(idx)
	blobs.Data[Key("test", space, "catalog.json")] = rawIdx
	usage, err = svc.SkillUsage(ctx, "actor", space, skill.ID)
	if err != nil || len(usage) != 1 || !slices.Equal(usage[0].Versions, []string{"v0", "v1"}) {
		t.Fatalf("expected legacy bundle to be scanned cleanly, got %+v (%v)", usage, err)
	}

	// Deleting unknown kind is rejected; deleting both skills and agents removes them permanently.
	if err := repo.CatalogDelete(ctx, "actor", space, "invalid", alpha.ID); !errors.Is(err, catalog.ErrConfig) {
		t.Fatalf("expected ErrConfig deleting unknown kind, got %v", err)
	}
	if err := svc.DeleteSkill(ctx, "actor", space, skill.ID); err != nil {
		t.Fatalf("DeleteSkill failed: %v", err)
	}
	list, err := repo.CatalogList(ctx, "actor", space, "skill", identity.Query{Limit: 20})
	if err != nil || len(list) != 0 {
		t.Fatalf("deleted skill still listed: %+v (%v)", list, err)
	}
	if _, err := svc.Draft(ctx, "actor", space, "skill", skill.ID); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after skill delete, got %v", err)
	}
	if err := svc.DeleteAgent(ctx, "actor", space, alpha.ID); err != nil {
		t.Fatalf("DeleteAgent failed: %v", err)
	}
	agents, err := repo.CatalogList(ctx, "actor", space, "agent", identity.Query{Limit: 20})
	if err != nil || len(agents) != 1 || agents[0].Name != "beta" {
		t.Fatalf("unexpected remaining agents after delete: %+v (%v)", agents, err)
	}
	if _, err := svc.Draft(ctx, "actor", space, "agent", alpha.ID); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after agent delete, got %v", err)
	}
}
