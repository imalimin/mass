package catalog

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/resource"
)

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
	if _, err := svc.ScanPublishSkills(ctx, "", "", files, project); err == nil || !strings.Contains(err.Error(), "proj.yaml") {
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
