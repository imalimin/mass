package catalog

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/imalimin/mass/internal/resource"
)

func TestStrictDefinitionValidation(t *testing.T) {
	if _, e := ValidateDocument("agent", "reporter", Document{Markdown: "hello", ConfigYAML: reporterJSON}, true); e != nil {
		t.Fatal(e)
	}
	for name, input := range map[string]string{
		"YAML":                   reporterYAML,
		"unknown field":          `{"unknown":true,` + reporterJSON[1:],
		"multiple objects":       reporterJSON + "{}",
		"duplicate field":        `{"name":"reporter",` + reporterJSON[1:],
		"duplicate nested field": strings.Replace(reporterJSON, `"max_steps": 5`, `"max_steps": 5, "max_steps": 5`, 1),
		"wrong key case":         strings.Replace(reporterJSON, `"name"`, `"Name"`, 1),
		"unknown tool":           `{"tools":["run_shell"],` + reporterJSON[1:],
		"retired tool":           `{"tools":["list_files"],` + reporterJSON[1:],
		"duplicate tool":         `{"tools":["read_file","read_file"],` + reporterJSON[1:],
		"invalid limit":          strings.Replace(reporterJSON, `"max_steps": 5`, `"max_steps": 0`, 1),
		"invalid schema version": strings.Replace(reporterJSON, `"schema_version": 1`, `"schema_version": 2`, 1),
		"moving skill":           `{"skills":[{"id":"00000000-0000-4000-8000-000000000002","version":"latest"}],` + reporterJSON[1:],
	} {
		if _, e := ValidateDocument("agent", "reporter", Document{Markdown: "hello", ConfigYAML: input}, true); e == nil {
			t.Errorf("accepted %s", name)
		}
	}
}

func TestContextMaxTokensDefaultAndRemovedKey(t *testing.T) {
	base := strings.Replace(reporterJSON, `, "max_tokens": 1000`, "%s", 1)
	for _, tc := range []struct {
		name   string
		suffix string
		want   int
	}{
		{"default", "", DefaultMaxTokens},
		{"explicit", `, "max_tokens": 8000`, 8000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ValidateDocument("agent", "reporter", Document{Markdown: "hello", ConfigYAML: fmt.Sprintf(base, tc.suffix)}, true)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Context.MaxTokens != tc.want {
				t.Fatal("max_tokens", cfg.Context.MaxTokens)
			}
		})
	}
	if _, err := ValidateDocument("agent", "reporter", Document{Markdown: "hello", ConfigYAML: fmt.Sprintf(base, `, "reserved_output_tokens": 512`)}, true); err == nil {
		t.Fatal("Agent configuration accepted the removed field")
	}
	var stored ContextConfig
	if err := json.Unmarshal([]byte(`{"max_input_tokens":2000,"compression":"summary"}`), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.MaxTokens != DefaultMaxTokens {
		t.Fatal("stored version", stored.MaxTokens)
	}
}

func TestResourcePaths(t *testing.T) {
	for _, path := range []string{".", "../secret", "/assets/a", "assets/../a", "assets/a\\b", "assets/a:b", "assets/", "assets/./a", "skill.md", "SKILL.md/child", "bundle.json", "Bundle.json/child"} {
		if resource.ValidSkillPath(path) {
			t.Errorf("Skill accepted invalid or reserved path %q", path)
		}
	}
	for _, path := range []string{"assets/templates/report.txt", "rules.txt", "scripts/python/example.py"} {
		if !resource.ValidSkillPath(path) {
			t.Errorf("Skill rejected valid path %q", path)
		}
	}
	if _, err := ValidateDocument("agent", "test-agent", Document{Assets: []Asset{}}, false); err == nil {
		t.Fatal("Agent accepted an assets field")
	}
}

func TestSkillDirectoryPaths(t *testing.T) {
	file := Asset{
		Path: "scripts/python/example.py", ObjectID: "00000000-0000-4000-8000-000000000001",
		SHA256: strings.Repeat("a", 64),
	}
	doc := Document{Markdown: "Skill", Assets: []Asset{file}, Directories: []string{"scripts/python", "scripts/python/empty", "notes", "assets"}}
	if _, err := ValidateDocument("skill", "test-skill", doc, true); err != nil {
		t.Fatal(err)
	}
	for _, directories := range [][]string{
		{"scripts/../outside"}, {"scripts/python", "scripts/python"},
		{"scripts/python/example.py"}, {"scripts/python/example.py/child"},
		{"Scripts/python/empty"}, {"Other/one", "other/two"}, {"SKILL.md/child"}, {"bundle.json"},
	} {
		doc.Directories = directories
		if _, err := ValidateDocument("skill", "test-skill", doc, true); err == nil {
			t.Errorf("accepted conflicting or invalid directories %v", directories)
		}
	}
}
