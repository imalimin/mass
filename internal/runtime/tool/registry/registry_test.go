package registry

import (
	"encoding/json"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"testing"
)

func TestAllToolSchemasAndFrozenDefinitions(t *testing.T) {
	specs := Specs()
	if len(specs) != 16 {
		t.Fatal(len(specs))
	}
	seen := map[string]bool{}
	for _, spec := range specs {
		if seen[spec.Name] {
			t.Fatal("duplicate", spec.Name)
		}
		seen[spec.Name] = true
		for i, raw := range []json.RawMessage{spec.Schema, spec.ResultSchema} {
			var doc any
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			c := jsonschema.NewCompiler()
			if err := c.AddResource("https://mass.local/schema", doc); err != nil {
				t.Fatal(err)
			}
			compiled, err := c.Compile("https://mass.local/schema")
			if err != nil {
				t.Fatal(spec.Name, err)
			}
			if language := run.CodeLanguage(spec.Name); i == 0 && language != "" {
				input := map[string]any{"language": language, "code": "return 1", "input": nil}
				if err := compiled.Validate(input); err != nil {
					t.Fatal(spec.Name, err)
				}
				input["language"] = "other"
				if compiled.Validate(input) == nil {
					t.Fatal("wrong language accepted", spec.Name)
				}
			}
			if i == 1 {
				value := map[string]any{"call_id": "call", "status": "completed", "data": map[string]any{}, "side_effect": "none", "complete": true, "source_refs": []any{}}
				if compiled.Validate(value) == nil {
					t.Fatal("empty completed result accepted", spec.Name)
				}
				value["status"] = "failed"
				if err := compiled.Validate(value); err != nil {
					t.Fatal("failed result rejected", spec.Name, err)
				}
			}
		}
	}
	r := &Registry{}
	frozen := specs[0]
	frozen.Description = "frozen description"
	actual, err := r.Describe([]run.ToolSpec{frozen})
	if err != nil || actual[0].Description != frozen.Description {
		t.Fatal("definition changed", err)
	}
	actual, err = r.Describe([]run.ToolSpec{{Name: "read_file", Version: "1", Schema: json.RawMessage(`null`)}})
	if err != nil || string(actual[0].Schema) == "null" {
		t.Fatal("legacy empty schema not resolved", err)
	}
	if _, err = r.Describe([]run.ToolSpec{{Name: "shell", Version: "1"}}); err == nil {
		t.Fatal("unknown tool accepted")
	}
}
