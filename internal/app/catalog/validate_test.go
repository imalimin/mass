package catalog

import (
	"strings"
	"testing"
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
	for _, path := range []string{"../secret", "/assets/a", "assets/../a", "assets/a\\b", "assets/a:b", "assets/", "assets/./a"} {
		if ValidPath(path) {
			t.Fatal("unsafe resource path", path)
		}
	}
	if !ValidPath("assets/templates/report.txt") {
		t.Fatal("valid path rejected")
	}
}
