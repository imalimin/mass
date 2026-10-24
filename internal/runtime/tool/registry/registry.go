// Package registry resolves fixed platform implementations and frozen wire schemas.
package registry

import (
	"bytes"
	"encoding/json"
	"github.com/imalimin/mass/internal/domain/run"
)

type Registry struct{}

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
	ranges := map[string]any{"oneOf": []any{object(map[string]any{"offset": integer(0, 1<<30), "length": integer(1, 65536)}, "offset", "length"), object(map[string]any{"start_line": integer(1, 10000000), "end_line": integer(1, 10000000)}, "start_line", "end_line")}}
	result := object(map[string]any{"call_id": str(), "status": map[string]any{"enum": []string{"completed", "failed"}}, "data": map[string]any{}, "error": str(), "side_effect": map[string]any{"enum": []string{"none", "committed", "unknown"}}, "complete": map[string]any{"type": "boolean"}, "truncated": map[string]any{"type": "boolean"}, "data_ref": str(), "source_refs": map[string]any{"type": "array", "items": object(map[string]any{"role": str(), "ordinal": integer(0, 10000), "object_id": map[string]any{"type": "string", "format": "uuid"}, "sha256": sha}, "role", "ordinal", "object_id", "sha256")}}, "call_id", "status", "data", "side_effect", "complete", "source_refs")
	resultRaw, _ := json.Marshal(result)
	specs := []run.ToolSpec{}
	add := func(name, description string, props map[string]any, required ...string) {
		raw, _ := json.Marshal(object(props, required...))
		var envelope map[string]any
		json.Unmarshal(resultRaw, &envelope)
		envelope["properties"].(map[string]any)["data"] = map[string]any{"type": "object"}
		dataProps := map[string]any{"path": str(), "sha256": sha, "size": integer(0, 1<<30), "content": str(), "encoding": map[string]any{"enum": []string{"utf-8", "base64"}}, "complete": map[string]any{"type": "boolean"}, "parser_version": str(), "items": map[string]any{"type": "array"}, "query_ref": str(), "attachment_id": str(), "skill_binding_id": str(), "source": str(), "revision": integer(1, 1000000), "steps": map[string]any{"type": "array"}, "value": map[string]any{}, "logs": map[string]any{"type": "array"}, "logs_complete": map[string]any{"type": "boolean"}, "io_count": integer(0, 10000), "io_complete": map[string]any{"type": "boolean"}, "engine_version": str(), "id": str(), "object_id": str(), "download_url": str()}
		fields := map[string][]string{
			"list_files": {"items", "query_ref", "complete"}, "glob_files": {"items", "query_ref", "complete"}, "search_files": {"items", "query_ref", "complete"}, "list_skill_resources": {"items", "query_ref", "complete"},
			"read_file": {"path", "sha256", "content", "encoding", "complete"}, "read_skill_resource": {"skill_binding_id", "path", "sha256", "content", "encoding", "complete"}, "read_attachment": {"attachment_id", "sha256", "parser_version", "complete"},
			"write_file": {"path", "sha256", "size"}, "edit_file": {"path", "sha256", "size"}, "move_file": {"source", "path", "sha256", "size"}, "delete_file": {"path", "sha256"},
			"run_code": {"value", "logs", "logs_complete", "io_count", "io_complete", "engine_version"}, "update_plan": {"revision", "steps"}, "publish_artifact": {"id", "object_id", "sha256", "download_url", "path"},
		}[name]
		selected := map[string]any{}
		for _, key := range fields {
			selected[key] = dataProps[key]
		}
		envelope["allOf"] = []any{map[string]any{"if": map[string]any{"properties": map[string]any{"status": map[string]any{"const": "completed"}}}, "then": map[string]any{"properties": map[string]any{"data": map[string]any{"type": "object", "properties": selected, "required": fields}}}}}
		resultSchema, _ := json.Marshal(envelope)
		specs = append(specs, run.ToolSpec{Name: name, Version: "1", Description: description, Schema: raw, ResultSchema: resultSchema})
	}
	page := func(props map[string]any) map[string]any {
		props["cursor"] = map[string]any{"type": "string", "maxLength": 4096}
		props["limit"] = integer(1, 100)
		return props
	}
	add("list_files", "List the current session directory. Pagination resumes an immutable result; complete=false declares scan limits.", page(map[string]any{"directory": path}))
	add("read_file", "Read current file bytes and SHA-256. Optional byte or line range; UTF-8 or base64 encoding, bounded 64 KiB view, immutable source.", map[string]any{"path": path, "expected_sha256": sha, "range": ranges}, "path")
	add("write_file", "Create/replace one UTF-8 file, at most 1 MiB. New file: expected_sha256='missing'; update: exact current digest.", map[string]any{"path": path, "content": map[string]any{"type": "string", "maxLength": 1048576}, "media_type": media, "expected_sha256": map[string]any{"type": "string", "pattern": "^(missing|[a-f0-9]{64})$"}}, "path", "content", "media_type", "expected_sha256")
	add("edit_file", "Apply unique non-overlapping UTF-8 exact replacements against the original file, only if all match.", map[string]any{"path": path, "expected_sha256": sha, "edits": map[string]any{"type": "array", "minItems": 1, "maxItems": 100, "items": object(map[string]any{"old_text": map[string]any{"type": "string", "minLength": 1, "maxLength": 1048576}, "new_text": map[string]any{"type": "string", "maxLength": 1048576}}, "old_text", "new_text")}}, "path", "expected_sha256", "edits")
	add("glob_files", "Match relative file paths with ** patterns. Cursor continues the saved result.", page(map[string]any{"pattern": map[string]any{"type": "string", "minLength": 1, "maxLength": 512}}), "pattern")
	add("search_files", "Search UTF-8 files with literal or RE2 regex. Results include original SHA-256 and line; scan coverage is explicit.", page(map[string]any{"pattern": map[string]any{"type": "string", "minLength": 1, "maxLength": 512}, "mode": map[string]any{"enum": []string{"literal", "regex"}}, "path_glob": path}), "pattern")
	add("move_file", "Rename a single current file to an existing parent directory. Exact source digest required; target must not exist.", map[string]any{"source": path, "target": path, "expected_sha256": sha}, "source", "target", "expected_sha256")
	add("delete_file", "Delete only the current single file after digest validation. Snapshots and published artifacts remain immutable.", map[string]any{"path": path, "expected_sha256": sha}, "path", "expected_sha256")
	add("list_skill_resources", "List resources of a fixed Skill binding; skill_binding_id is the SkillVersion ID in the system instructions.", page(map[string]any{"skill_binding_id": str(), "prefix": path}), "skill_binding_id")
	add("read_skill_resource", "Read a resource from an exact bound SkillVersion; never executes scripts. Optional byte/line range.", map[string]any{"skill_binding_id": str(), "path": path, "range": ranges}, "skill_binding_id", "path")
	add("read_attachment", "Read only attachments accepted by this session. text permits byte/line range; json/csv validates the whole document before a bounded structured view; record_offset/record_limit continues structured rows.", map[string]any{"attachment_id": str(), "record_offset": integer(0, 10000), "record_limit": integer(1, 1000), "format": map[string]any{"enum": []string{"text", "json", "csv"}}, "range": ranges}, "attachment_id")
	add("run_code", "Execute a synchronous JavaScript function body with JSON input. APIs: fs.list(directory='.', {cursor,limit}?); fs.readFile(path, {expected_sha256,range}?); fs.mkdir(path); fs.writeFile(path,text,{expected_sha256,media_type?}); fs.editFile(path,[{old_text,new_text}],{expected_sha256}); fs.rename(source,target,{expected_sha256}); fs.remove(path,{expected_sha256}). Create uses digest 'missing', updates require current SHA-256. Reads return content, encoding (utf-8/base64), sha256 and complete; list returns items/next_cursor. net.request({method?,url,headers?,body?}) returns status/headers/body/encoding/complete; only HTTP(S), no retries. CSV.parse(text)/stringify(string[][]), log(JSON), JSON and input are available. Return a bounded JSON value. Confirmed effects persist after errors. No shell, Node, Python, Promise result or OS sandbox.", map[string]any{"language": map[string]any{"const": "javascript", "type": "string"}, "code": map[string]any{"type": "string", "minLength": 1, "maxLength": 65536}, "input": map[string]any{}}, "language", "code", "input")
	add("update_plan", "Replace this Run's complete progress plan using expected_revision (initial 0). At most one in_progress; stable IDs retain titles. This is reported progress, not proof of success.", map[string]any{"expected_revision": integer(0, 1000000), "steps": map[string]any{"type": "array", "maxItems": 100, "items": object(map[string]any{"id": map[string]any{"type": "string", "pattern": "^[A-Za-z0-9._:-]{1,64}$"}, "title": map[string]any{"type": "string", "minLength": 1, "maxLength": 256}, "status": map[string]any{"enum": []string{"pending", "in_progress", "completed", "skipped"}}}, "id", "title", "status")}}, "expected_revision", "steps")
	add("publish_artifact", "Publish an immutable copy of the exact current file and return an authenticated download URL.", map[string]any{"path": path, "expected_sha256": sha, "display_name": map[string]any{"type": "string", "minLength": 1, "maxLength": 200}, "media_type": media}, "path", "expected_sha256", "display_name", "media_type")
	return specs
}
func (r *Registry) Describe(selected []run.ToolSpec) ([]run.ToolSpec, error) {
	result := []run.ToolSpec{}
	seen := map[string]bool{}
	for _, wanted := range selected {
		found := false
		for _, spec := range Specs() {
			if spec.Name == wanted.Name && spec.Version == wanted.Version {
				if seen[spec.Name] {
					return nil, run.ErrTool
				}
				seen[spec.Name] = true
				if len(wanted.Schema) > 0 && !bytes.Equal(bytes.TrimSpace(wanted.Schema), []byte("null")) {
					spec = wanted
				}
				result = append(result, spec)
				found = true
				break
			}
		}
		if !found {
			return nil, run.ErrTool
		}
	}
	return result, nil
}
