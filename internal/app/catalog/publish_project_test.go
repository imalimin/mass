package catalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/resource"
)

const reporterYAML = `schema_version: 1
name: reporter
model: 877406ec-a526-485e-a0b6-dcff188c191b
context: {max_input_tokens: 1000, max_tokens: 1000, compression: summary}
limits: {max_steps: 5, timeout_seconds: 30}
`

func TestPublishAgentConfigFileSelection(t *testing.T) {
	const newModel = "af84a2d7-412f-487c-8525-e00b719d975f"
	doc := Document{Markdown: "Generate reports", ConfigYAML: reporterYAML}
	config, err := ValidateDocument("agent", "reporter", doc, true)
	if err != nil {
		t.Fatal(err)
	}
	oldModel := config.Model
	config.Model = newModel
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, jsonFile, yamlFile, model string
		local, oversized                []string
		invalid                         bool
	}{
		{name: "legacy YAML", yamlFile: reporterYAML, model: oldModel},
		{name: "legacy JSON", yamlFile: string(raw), model: newModel},
		{name: "prefer JSON", jsonFile: string(raw), yamlFile: reporterYAML, model: newModel},
		{name: "ignore oversized legacy file", jsonFile: string(raw), oversized: []string{resource.LegacyAgentConfigPath}, model: newModel},
		{name: "require JSON syntax", jsonFile: reporterYAML, yamlFile: reporterYAML, invalid: true},
		{name: "reject duplicate JSON fields", jsonFile: `{"schema_version":1,` + string(raw[1:]), invalid: true},
		{name: "JSON only on host", yamlFile: reporterYAML, local: []string{resource.AgentConfigPath}, invalid: true},
		{name: "missing config", invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			files := resource.ProjectFiles{
				Files: map[string][]byte{"AGENTS.md": []byte(doc.Markdown)}, LocalFiles: test.local, Oversized: test.oversized,
			}
			if test.jsonFile != "" {
				files.Files[resource.AgentConfigPath] = []byte(test.jsonFile)
			}
			if test.yamlFile != "" {
				files.Files[resource.LegacyAgentConfigPath] = []byte(test.yamlFile)
			}
			ctx := context.Background()
			svc := &Service{}
			project, err := svc.PreparePublishAgent(ctx, "", "", run.Creativity{}, files, "")
			if test.invalid {
				var fileErr *resource.ProjectFileError
				if !errors.As(err, &fileErr) || fileErr.Path != resource.AgentConfigPath {
					t.Fatalf("expected agent.json error, got %v", err)
				}
				return
			}
			if err != nil || project.Manifest().ModelID != test.model {
				t.Fatalf("expected model %q, got %+v, %v", test.model, project.Manifest(), err)
			}
			if _, err := svc.ScanPublishSkills(ctx, "", "", files, project, nil); err != nil {
				t.Fatal(err)
			}
			if !json.Valid([]byte(project.agentDocument(nil).ConfigYAML)) {
				t.Fatal("published config is not JSON")
			}
		})
	}
}

func TestIterationRestoresAndPublishesLegacyAgentAsJSON(t *testing.T) {
	ctx := context.Background()
	legacy := Document{Markdown: "Generate reports", ConfigYAML: reporterYAML}
	config, err := ValidateDocument("agent", "reporter", legacy, true)
	if err != nil {
		t.Fatal(err)
	}
	repo := &iterationRepository{
		draft: Draft{Definition: Definition{ID: "agent", Name: "reporter", Kind: "agent"}, Revision: 1, Document: legacy},
	}
	content := &iterationVersionContent{}
	storeVersion := func(id string, document Document) {
		bundle := Bundle{FormatVersion: 1, Kind: "agent", Name: "reporter", Document: document, Config: config}
		content.raw, err = json.Marshal(bundle)
		if err != nil {
			t.Fatal(err)
		}
		repo.version = Version{ID: id, SHA256: fmt.Sprintf("%x", sha256.Sum256(content.raw))}
	}
	storeVersion("legacy", legacy)
	svc := &Service{Repo: repo, Content: &resource.ContentStore{Inline: content}}
	base := run.Creativity{TargetAgentID: "agent", TargetVersionID: "legacy", TargetDraftRevision: 1}
	files := resource.ProjectFiles{Files: make(map[string][]byte)}
	if err := svc.RestoreIteration(ctx, "", "", base, func(name string, data []byte) error {
		files.Files[name] = data
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, exists := files.Files[resource.LegacyAgentConfigPath]; exists || !json.Valid(files.Files[resource.AgentConfigPath]) {
		t.Fatalf("expected agent.json in restored workspace, got %v", files.Files)
	}
	project, err := svc.PreparePublishAgent(ctx, "", "", base, files, "")
	if err != nil {
		t.Fatal(err)
	}
	desired := project.agentDocument(nil)
	if !sameAgent(legacy, desired) {
		t.Fatal("Agent config changed during migration")
	}
	target := run.PublishTarget{ID: base.TargetAgentID, Version: base.TargetVersionID, Revision: base.TargetDraftRevision}
	matches := func(doc Document) bool { return sameAgent(doc, desired) }
	state, err := svc.inspectProjectTarget(ctx, repo, "", "", "agent", "reporter", target, matches, true)
	if err != nil || state.done.ID != "" {
		t.Fatalf("legacy YAML version was treated as already published: %+v, %v", state.done, err)
	}
	storeVersion("successor", desired)
	state, err = svc.inspectProjectTarget(ctx, repo, "", "", "agent", "reporter", target, matches, true)
	if err != nil || state.done.ID != "successor" {
		t.Fatalf("retry did not reuse the JSON version: %+v, %v", state.done, err)
	}
}

type iterationRepository struct {
	Repository
	projectCatalog
	draft   Draft
	version Version
}

func (r *iterationRepository) CatalogDraft(context.Context, string, string, string, string) (Draft, error) {
	return r.draft, nil
}

func (r *iterationRepository) CatalogVersion(context.Context, string, string, string, string, string) (Version, error) {
	return r.version, nil
}

func (r *iterationRepository) LatestFormalVersion(context.Context, string, string, string, string) (Version, bool, error) {
	return r.version, true, nil
}

type iterationVersionContent struct {
	resource.InlineContent
	raw []byte
}

func (c *iterationVersionContent) OpenInline(context.Context, string, string, string) (storage.Object, io.ReadCloser, error) {
	object := storage.Object{Size: int64(len(c.raw)), SHA256: fmt.Sprintf("%x", sha256.Sum256(c.raw))}
	return object, io.NopCloser(bytes.NewReader(c.raw)), nil
}

func TestPublishAgentModelBeforeSkills(t *testing.T) {
	const model = "877406ec-a526-485e-a0b6-dcff188c191b"
	files := resource.ProjectFiles{
		Files: map[string][]byte{
			"AGENTS.md":  []byte("Generate reports"),
			"agent.yaml": []byte("schema_version: 1\nname: reporter\ncontext: {max_input_tokens: 1000, max_tokens: 1000, compression: summary}\nlimits: {max_steps: 5, timeout_seconds: 30}"),
			"proj.yaml":  []byte("unexpected: true"),
		},
	}
	svc := &Service{}
	ctx := context.Background()
	if _, err := svc.PreparePublishAgent(ctx, "", "", run.Creativity{}, files, ""); !errors.Is(err, ErrPublishModelRequired) {
		t.Fatalf("expected a model prompt before validating skills, got %v", err)
	}
	project, err := svc.PreparePublishAgent(ctx, "", "", run.Creativity{}, files, model)
	if err != nil || project.Manifest().ModelID != model {
		t.Fatalf("expected selected model, got %+v, %v", project.Manifest(), err)
	}
	if _, err := svc.ScanPublishSkills(ctx, "", "", files, project, nil); err == nil || !strings.Contains(err.Error(), "proj.yaml") {
		t.Fatalf("expected Skill validation after selecting a model, got %v", err)
	}
}

func TestProjectSkillPath(t *testing.T) {
	const skill = "877406ec-a526-485e-a0b6-dcff188c191b"
	const version = "af84a2d7-412f-487c-8525-e00b719d975f"
	base := fmt.Sprintf("skills:\n  - skill_id: %s\n    version_id: %s\n", skill, version)
	tests := []struct {
		name, field, want string
		invalid           bool
	}{
		{name: "reference when omitted"},
		{name: "local directory", field: "skills/report-writer", want: "skills/report-writer"},
		{name: "empty string", field: `""`, invalid: true},
		{name: "empty value", field: " ", invalid: true},
		{name: "explicit null", field: "null", invalid: true},
		{name: "nested directory", field: "skills/report-writer/scripts", invalid: true},
		{name: "not a string", field: "[skills/report-writer]", invalid: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := base
			if test.field != "" {
				raw += "    path: " + test.field + "\n"
			}
			bindings, err := parseProjectSkills([]byte(raw))
			if test.invalid {
				if err == nil || !strings.Contains(err.Error(), "proj.yaml") || !strings.Contains(err.Error(), "skills[0].path") || !strings.Contains(err.Error(), skill) {
					t.Fatalf("expected a Skill path error, got %v", err)
				}
				return
			}
			if err != nil || len(bindings) != 1 || bindings[0].Path != test.want {
				t.Fatalf("got bindings %+v, error %v", bindings, err)
			}
		})
	}
	t.Run("duplicate directory", func(t *testing.T) {
		raw := base + "    path: skills/report-writer\n" +
			"  - skill_id: 63a7748b-a6ce-4b87-8cdc-bacafde92fc7\n    version_id: " + version + "\n    path: skills/report-writer\n"
		if _, err := parseProjectSkills([]byte(raw)); err == nil || !strings.Contains(err.Error(), "skills[1].path") {
			t.Fatalf("expected duplicate path error, got %v", err)
		}
	})
}
