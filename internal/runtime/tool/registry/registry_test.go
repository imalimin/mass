package registry

import (
	"encoding/json"
	"testing"

	jschema "github.com/eino-contrib/jsonschema"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/schemacache"
)

func TestAllToolSchemasAndLegacyCompatibility(t *testing.T) {
	specs := Specs()
	if len(specs) != 5 {
		t.Fatalf("expected 5 core tools, got %d", len(specs))
	}
	validSamples := map[string][]map[string]any{
		run.CommandTool: {
			{"command": "ls -la | head -n 5"},
			{"command": "yt-dlp", "args": []any{"--version"}},
		},
		"read_file": {
			{"path": "notes.txt"},
			{"path": "$MASS_SKILLS_DIR/rules/SKILL.md"},
			{"path": "notes.txt", "range": map[string]any{"start_line": 1, "end_line": 10}},
		},
		"write_file": {
			{"path": "notes.txt", "content": "hello"},
		},
		"edit_file": {
			{"path": "notes.txt", "edits": []any{map[string]any{"old_text": "a", "new_text": "b"}}},
		},
		"update_plan": {
			{"steps": []any{map[string]any{"title": "Inspect workspace", "status": "in_progress"}}},
			{"steps": []any{map[string]any{"id": "step-a", "title": "Inspect workspace", "status": "completed"}}},
		},
	}
	seen := map[string]bool{}
	for _, spec := range specs {
		if seen[spec.Name] {
			t.Fatal("duplicate", spec.Name)
		}
		seen[spec.Name] = true
		var wireSchema jschema.Schema
		if err := json.Unmarshal(spec.Schema, &wireSchema); err != nil {
			t.Fatalf("%s failed eino jsonschema unmarshal: %v", spec.Name, err)
		}
		compiled, err := schemacache.Compile(spec.Schema)
		if err != nil {
			t.Fatal(spec.Name, err)
		}
		for _, sample := range validSamples[spec.Name] {
			if err := compiled.Validate(sample); err != nil {
				t.Fatalf("%s rejected valid sample %v: %v", spec.Name, sample, err)
			}
		}
		if compiled.Validate(map[string]any{"unexpected_extra_field": true}) == nil {
			t.Fatalf("%s accepted unknown property", spec.Name)
		}
	}

	r := &Registry{}
	frozen := specs[0]
	frozen.Description = "frozen description"
	actual, err := r.Describe([]run.ToolSpec{frozen})
	if err != nil || actual[0].Description != frozen.Description {
		t.Fatal("definition changed", err)
	}
	actual, err = r.Describe([]run.ToolSpec{
		{Name: "list_files", Version: "1"},
		{Name: "glob_files", Version: "1"},
		{Name: "search_files", Version: "1"},
		{Name: "move_file", Version: "1"},
		{Name: "delete_file", Version: "1"},
		{Name: "list_skill_resources", Version: "1"},
		{Name: "read_skill_resource", Version: "1"},
		{Name: "read_attachment", Version: "1"},
		{Name: "publish_artifact", Version: "1"},
		{Name: "run_code", Version: "1"},
		{Name: "run_code_js", Version: "1"},
		{Name: "run_code_go", Version: "1"},
		{Name: "read_file", Version: "1", Schema: json.RawMessage(`null`)},
	})
	if err != nil || len(actual) != 1 || actual[0].Name != "read_file" || string(actual[0].Schema) == "null" {
		t.Fatalf("legacy tool filtering or empty schema resolution failed: %v, %+v", err, actual)
	}
	if _, err = r.Describe([]run.ToolSpec{{Name: "shell", Version: "1"}}); err == nil {
		t.Fatal("unknown tool accepted")
	}
	if _, err = r.Describe([]run.ToolSpec{{Name: "read_file", Version: "1"}, {Name: "read_file", Version: "1"}}); err == nil {
		t.Fatal("duplicate tool accepted")
	}
}
