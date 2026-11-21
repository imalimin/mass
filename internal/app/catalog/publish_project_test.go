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

const reporterJSON = `{
  "schema_version": 1,
  "name": "reporter",
  "model": "877406ec-a526-485e-a0b6-dcff188c191b",
  "context": {"max_input_tokens": 1000, "max_tokens": 1000, "compression": "summary"},
  "limits": {"max_steps": 5, "timeout_seconds": 30}
}`

func TestPublishAgentConfigFileSelection(t *testing.T) {
	doc := Document{Markdown: "Generate reports", ConfigYAML: reporterJSON}
	config, err := ValidateDocument("agent", "reporter", doc, true)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, jsonFile, yamlFile string
		local, oversized         []string
		invalid                  bool
	}{
		{name: "JSON", jsonFile: string(raw)},
		{name: "reject legacy filename even with JSON content", yamlFile: string(raw), invalid: true},
		{name: "require JSON syntax", jsonFile: reporterYAML, invalid: true},
		{name: "reject duplicate JSON fields", jsonFile: `{"schema_version":1,` + string(raw[1:]), invalid: true},
		{name: "JSON only on host", local: []string{resource.AgentConfigPath}, invalid: true},
		{name: "oversized JSON", oversized: []string{resource.AgentConfigPath}, invalid: true},
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
				files.Files["agent.yaml"] = []byte(test.yamlFile)
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
			if err != nil || project.Manifest().ModelID != config.Model {
				t.Fatalf("expected model %q, got %+v, %v", config.Model, project.Manifest(), err)
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

func TestIterationRequiresJSONAgentConfig(t *testing.T) {
	ctx := context.Background()
	legacy := Document{Markdown: "Generate reports", ConfigYAML: reporterYAML}
	current := Document{Markdown: legacy.Markdown, ConfigYAML: reporterJSON}
	config, err := ValidateDocument("agent", "reporter", current, true)
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
	write := func(name string, data []byte) error {
		files.Files[name] = data
		return nil
	}
	if _, err := svc.ResolveIteration(ctx, "", "", base.TargetAgentID); !errors.Is(err, ErrConfig) {
		t.Fatalf("expected YAML target to be rejected before iteration, got %v", err)
	}
	if err := svc.RestoreIteration(ctx, "", "", base, write); !errors.Is(err, ErrConfig) || len(files.Files) != 0 {
		t.Fatalf("expected YAML restore to fail before writing files, got %v and %v", err, files.Files)
	}
	draftBase := base
	draftBase.TargetVersionID = ""
	if err := svc.RestoreIteration(ctx, "", "", draftBase, write); !errors.Is(err, ErrConfig) || len(files.Files) != 0 {
		t.Fatalf("expected YAML draft restore to fail before writing files, got %v and %v", err, files.Files)
	}
	if sameAgent(legacy, current) {
		t.Fatal("YAML configuration matched a JSON version")
	}
	storeVersion("current", current)
	base.TargetVersionID = "current"
	if err := svc.RestoreIteration(ctx, "", "", base, write); err != nil {
		t.Fatal(err)
	}
	if _, exists := files.Files["agent.yaml"]; exists || string(files.Files[resource.AgentConfigPath]) != reporterJSON {
		t.Fatalf("expected agent.json in restored workspace, got %v", files.Files)
	}
	if _, exists := files.Files["proj.yaml"]; exists || !json.Valid(files.Files[resource.ProjectBindingsPath]) {
		t.Fatalf("expected proj.json in restored workspace, got %v", files.Files)
	}
	project, err := svc.PreparePublishAgent(ctx, "", "", base, files, "")
	if err != nil {
		t.Fatal(err)
	}
	if !sameAgent(current, project.agentDocument(nil)) {
		t.Fatal("restoring the project changed the Agent configuration")
	}
}

type iterationRepository struct {
	Repository
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
			"AGENTS.md": []byte("Generate reports"),
			resource.AgentConfigPath: []byte(`{
				"schema_version": 1,
				"name": "reporter",
				"context": {"max_input_tokens": 1000, "max_tokens": 1000, "compression": "summary"},
				"limits": {"max_steps": 5, "timeout_seconds": 30}
			}`),
			resource.ProjectBindingsPath: []byte(`{"unexpected": true}`),
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
	if _, err := svc.ScanPublishSkills(ctx, "", "", files, project, nil); err == nil || !strings.Contains(err.Error(), resource.ProjectBindingsPath) {
		t.Fatalf("expected Skill validation after selecting a model, got %v", err)
	}
}

func TestProjectBindingsFileSelection(t *testing.T) {
	const current = resource.ProjectBindingsPath
	for _, test := range []struct {
		name, jsonFile                string
		local, oversized, directories []string
		invalid                       bool
	}{
		{name: "optional"},
		{name: "JSON", jsonFile: `{"skills":[]}`},
		{name: "reject YAML in JSON file", jsonFile: "skills: []", invalid: true},
		{name: "reject duplicate JSON fields", jsonFile: `{"skills":[],"skills":[]}`, invalid: true},
		{name: "reject JSON only on host", local: []string{current}, invalid: true},
		{name: "reject oversized JSON", oversized: []string{current}, invalid: true},
		{name: "reject JSON directory", directories: []string{current}, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			files := resource.ProjectFiles{
				Files: map[string][]byte{}, LocalFiles: test.local, Oversized: test.oversized, Directories: test.directories,
			}
			if test.jsonFile != "" {
				files.Files[current] = []byte(test.jsonFile)
			}
			_, err := (&Service{}).ScanPublishSkills(context.Background(), "", "", files, PublishProject{}, nil)
			if !test.invalid && err == nil {
				return
			}
			var fileErr *resource.ProjectFileError
			if !test.invalid || !errors.As(err, &fileErr) || fileErr.Path != current {
				t.Fatalf("invalid=%t, got %v", test.invalid, err)
			}
		})
	}
}

func TestProjectSkillPath(t *testing.T) {
	const skill = "877406ec-a526-485e-a0b6-dcff188c191b"
	const version = "af84a2d7-412f-487c-8525-e00b719d975f"
	base := fmt.Sprintf(`"skill_id": %q, "version_id": %q`, skill, version)
	tests := []struct {
		name, field, want string
		invalid           bool
	}{
		{name: "reference when omitted"},
		{name: "local directory", field: `"skills/report-writer"`, want: "skills/report-writer"},
		{name: "empty string", field: `""`, invalid: true},
		{name: "explicit null", field: "null", invalid: true},
		{name: "nested directory", field: `"skills/report-writer/scripts"`, invalid: true},
		{name: "not a string", field: `["skills/report-writer"]`, invalid: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := base
			if test.field != "" {
				raw += `, "path": ` + test.field
			}
			bindings, err := parseProjectSkills([]byte(`{"skills":[{` + raw + `}]}`))
			if test.invalid {
				if err == nil || !strings.Contains(err.Error(), resource.ProjectBindingsPath) || !strings.Contains(err.Error(), "skills[0].path") || !strings.Contains(err.Error(), skill) {
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
		raw := fmt.Sprintf(`{"skills":[{%s,"path":"skills/report-writer"},{"skill_id":"63a7748b-a6ce-4b87-8cdc-bacafde92fc7","version_id":%q,"path":"skills/report-writer"}]}`, base, version)
		if _, err := parseProjectSkills([]byte(raw)); err == nil || !strings.Contains(err.Error(), "skills[1].path") {
			t.Fatalf("expected duplicate path error, got %v", err)
		}
	})
}
