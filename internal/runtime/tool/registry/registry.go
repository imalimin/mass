// Package registry resolves fixed platform implementations and wire schemas.
package registry

import (
	"bytes"
	"encoding/json"

	"github.com/imalimin/mass/internal/domain/run"
)

type Registry struct{}

var legacyTools = map[string]bool{
	"list_files":           true,
	"glob_files":           true,
	"search_files":         true,
	"move_file":            true,
	"delete_file":          true,
	"list_skill_resources": true,
	"run_code":             true,
	"run_code_js":          true,
	"run_code_go":          true,
}

func str() map[string]any { return map[string]any{"type": "string"} }
func integer(min, max int) map[string]any {
	return map[string]any{"type": "integer", "minimum": min, "maximum": max}
}
func object(properties map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "required": required}
}

func Specs() []run.ToolSpec {
	path := map[string]any{"type": "string", "minLength": 1, "maxLength": 1024}
	sha := map[string]any{"type": "string", "pattern": "^[a-f0-9]{64}$"}
	media := map[string]any{"type": "string", "minLength": 1, "maxLength": 128}
	ranges := map[string]any{"oneOf": []any{
		object(map[string]any{"offset": integer(0, 1<<30), "length": integer(1, 65536)}, "offset", "length"),
		object(map[string]any{"start_line": integer(1, 10000000), "end_line": integer(1, 10000000)}, "start_line", "end_line"),
	}}
	specs := []run.ToolSpec{}
	add := func(name, description string, props map[string]any, required ...string) {
		raw, _ := json.Marshal(object(props, required...))
		specs = append(specs, run.ToolSpec{Name: name, Version: "1", Description: description, Schema: raw})
	}
	add(run.CommandTool, "Run a command in the current session workspace (macOS Seatbelt sandbox). By default command is executed as a shell script via sh -c (supporting pipes, redirects, &&, globs, file management, CLI tools, Python/Node/Go, and network access). Optionally pass args to execute an installed binary from PATH directly without a shell.", map[string]any{
		"command": map[string]any{"type": "string", "minLength": 1, "maxLength": 65536},
		"args":    map[string]any{"type": "array", "items": str()},
	}, "command")
	add("read_file", "Read workspace file content and SHA-256 (bounded 64 KiB UTF-8 or base64 view). Optional byte or line range.", map[string]any{
		"path":            path,
		"expected_sha256": sha,
		"range":           ranges,
	}, "path")
	add("write_file", "Create or overwrite a UTF-8 workspace file (up to 1 MiB), creating parent directories automatically. Optional expected_sha256 ('missing' or current SHA-256).", map[string]any{
		"path":            path,
		"content":         map[string]any{"type": "string", "maxLength": 1048576},
		"expected_sha256": map[string]any{"type": "string", "pattern": "^(missing|[a-f0-9]{64})$"},
	}, "path", "content")
	add("edit_file", "Apply exact, unique, non-overlapping UTF-8 text replacements to an existing workspace file. Optional expected_sha256.", map[string]any{
		"path":            path,
		"expected_sha256": sha,
		"edits": map[string]any{
			"type":     "array",
			"minItems": 1,
			"maxItems": 100,
			"items": object(map[string]any{
				"old_text": map[string]any{"type": "string", "minLength": 1, "maxLength": 1048576},
				"new_text": map[string]any{"type": "string", "maxLength": 1048576},
			}, "old_text", "new_text"),
		},
	}, "path", "edits")
	add("read_skill_resource", "Read a resource file from a bound Skill (use the skill id and resource paths listed in <skill> system instructions). Optional byte or line range.", map[string]any{
		"skill_binding_id": str(),
		"path":             path,
		"range":            ranges,
	}, "skill_binding_id", "path")
	add("read_attachment", "Read an accepted session attachment by ID. Supports text (with optional byte/line range) or structured json/csv (with optional record_offset/record_limit).", map[string]any{
		"attachment_id": str(),
		"record_offset": integer(0, 10000),
		"record_limit":  integer(1, 1000),
		"format":        map[string]any{"type": "string", "enum": []string{"text", "json", "csv"}},
		"range":         ranges,
	}, "attachment_id")
	add("update_plan", "Replace the current Run's progress plan shown in the UI. Pass the full ordered steps list (at most one step with status 'in_progress').", map[string]any{
		"steps": map[string]any{
			"type":     "array",
			"maxItems": 100,
			"items": object(map[string]any{
				"id":     map[string]any{"type": "string", "pattern": "^[A-Za-z0-9._:-]{1,64}$"},
				"title":  map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
				"status": map[string]any{"type": "string", "enum": []string{"pending", "in_progress", "completed", "skipped"}},
			}, "title", "status"),
		},
	}, "steps")
	add("publish_artifact", "Publish an immutable copy of a workspace file as a downloadable artifact. Optional display_name and media_type default to the file basename and detected MIME type.", map[string]any{
		"path":            path,
		"expected_sha256": sha,
		"display_name":    map[string]any{"type": "string", "minLength": 1, "maxLength": 200},
		"media_type":      media,
	}, "path")
	return specs
}

func (r *Registry) Describe(selected []run.ToolSpec) ([]run.ToolSpec, error) {
	result := []run.ToolSpec{}
	seen := map[string]bool{}
	specs := Specs()
	for _, wanted := range selected {
		if seen[wanted.Name] {
			return nil, run.ErrTool
		}
		seen[wanted.Name] = true
		found := false
		for _, spec := range specs {
			if spec.Name == wanted.Name && spec.Version == wanted.Version {
				if len(wanted.Schema) > 0 && !bytes.Equal(bytes.TrimSpace(wanted.Schema), []byte("null")) {
					spec = wanted
				}
				result = append(result, spec)
				found = true
				break
			}
		}
		if found {
			continue
		}
		if legacyTools[wanted.Name] && wanted.Version == "1" {
			continue
		}
		return nil, run.ErrTool
	}
	return result, nil
}
