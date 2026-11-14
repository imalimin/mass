package catalog

import (
	"strings"
	"testing"

	"github.com/imalimin/mass/internal/resource"
)

func TestStrictDefinitionValidation(t *testing.T) {
	good := `schema_version: 1
name: test-agent
model: 00000000-0000-4000-8000-000000000001
skills: []
tools: [read_file]
context: {max_input_tokens: 2000, reserved_output_tokens: 100, compression: summary}
limits: {max_steps: 30, timeout_seconds: 300}
`
	if _, e := ValidateDocument("agent", "test-agent", Document{Markdown: "hello", ConfigYAML: good}, true); e != nil {
		t.Fatal(e)
	}
	for _, input := range []string{good + "unknown: true\n", good + "---\nname: other\n", strings.Replace(good, "name: test-agent", "name: test-agent\nname: other", 1), strings.Replace(good, "[read_file]", "[run_shell]", 1), strings.Replace(good, "[read_file]", "[read_file, read_file]", 1), strings.Replace(good, "max_steps: 30", "max_steps: 0", 1), strings.Replace(good, "skills: []", "skills: &a []\ncopy: *a", 1), strings.Replace(good, "schema_version: 1", "schema_version: 2", 1), strings.Replace(good, "skills: []", "skills: [{id: 00000000-0000-4000-8000-000000000002, version: latest}]", 1)} {
		if _, e := ValidateDocument("agent", "test-agent", Document{Markdown: "hello", ConfigYAML: input}, true); e == nil {
			t.Fatal("invalid configuration accepted")
		}
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
