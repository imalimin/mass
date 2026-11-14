// Package builtin implements tool semantics through constrained resource services.
package builtin

import (
	"context"
	"encoding/json"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/journal"
	"github.com/imalimin/mass/internal/resource"
)

type Files interface {
	Read(context.Context, run.Snapshot, resource.ReadFileInput) (run.ToolResult, error)
	Write(context.Context, run.Snapshot, resource.WriteFileInput) (run.ToolResult, error)
	Edit(context.Context, run.Snapshot, resource.EditInput) (run.ToolResult, error)
	Move(context.Context, run.Snapshot, resource.MoveInput) (run.ToolResult, error)
	Delete(context.Context, run.Snapshot, resource.ReadFileInput) (run.ToolResult, error)
	Query(context.Context, run.Snapshot, string, resource.PageInput) (run.ToolResult, error)
	Publish(context.Context, run.Snapshot, string, resource.PublishInput) (run.Artifact, run.ToolResult, error)
}
type CodeRunner interface {
	Run(context.Context, run.Snapshot, run.CodeInput) (run.ToolResult, error)
}
type Handler struct {
	Files   Files
	Sources *resource.Sources
	Code    CodeRunner
	Journal *journal.Journal
}

func (h *Handler) Execute(ctx context.Context, s run.Snapshot, call run.Call, args map[string]any) (result run.ToolResult, artifact *run.Artifact, err error) {
	text := func(key string) string { value, _ := args[key].(string); return value }
	number := func(key string) int { value, _ := args[key].(float64); return int(value) }
	var span *run.Range
	if m, ok := args["range"].(map[string]any); ok {
		span = &run.Range{}
		if n, ok := m["offset"].(float64); ok {
			v := int64(n)
			span.Offset = &v
		}
		if n, ok := m["length"].(float64); ok {
			v := int64(n)
			span.Length = &v
		}
		if n, ok := m["start_line"].(float64); ok {
			v := int(n)
			span.StartLine = &v
		}
		if n, ok := m["end_line"].(float64); ok {
			v := int(n)
			span.EndLine = &v
		}
	}
	switch call.Name {
	case "read_file":
		result, err = h.Files.Read(ctx, s, resource.ReadFileInput{Path: text("path"), ExpectedSHA256: text("expected_sha256"), Range: span})
	case "write_file":
		result, err = h.Files.Write(ctx, s, resource.WriteFileInput{Path: text("path"), Content: text("content"), MediaType: text("media_type"), ExpectedSHA256: text("expected_sha256")})
	case "edit_file":
		edits := []resource.Edit{}
		for _, raw := range args["edits"].([]any) {
			e := raw.(map[string]any)
			edits = append(edits, resource.Edit{Old: e["old_text"].(string), New: e["new_text"].(string)})
		}
		result, err = h.Files.Edit(ctx, s, resource.EditInput{Path: text("path"), ExpectedSHA256: text("expected_sha256"), Edits: edits})
	case "move_file":
		result, err = h.Files.Move(ctx, s, resource.MoveInput{Source: text("source"), Target: text("target"), ExpectedSHA256: text("expected_sha256")})
	case "delete_file":
		result, err = h.Files.Delete(ctx, s, resource.ReadFileInput{Path: text("path"), ExpectedSHA256: text("expected_sha256")})
	case "list_files", "glob_files", "search_files":
		result, err = h.Files.Query(ctx, s, call.Name, resource.PageInput{Directory: text("directory"), Pattern: text("pattern"), Mode: text("mode"), PathGlob: text("path_glob"), Cursor: text("cursor"), Limit: number("limit")})
	case "list_skill_resources":
		result, err = h.Sources.List(ctx, s, text("skill_binding_id"), text("prefix"), text("cursor"), number("limit"))
	case "read_skill_resource":
		result, err = h.Sources.ReadSkill(ctx, s, resource.SourceInput{Binding: text("skill_binding_id"), Path: text("path"), Range: span})
	case "read_attachment":
		result, err = h.Sources.ReadAttachment(ctx, s, resource.SourceInput{AttachmentID: text("attachment_id"), Format: text("format"), Range: span, RecordOffset: number("record_offset"), RecordLimit: number("record_limit")})
	case "update_plan":
		steps := []run.PlanStep{}
		for _, raw := range args["steps"].([]any) {
			step := raw.(map[string]any)
			steps = append(steps, run.PlanStep{ID: step["id"].(string), Title: step["title"].(string), Status: step["status"].(string)})
		}
		result, err = resource.PreparePlan(ctx, h.Journal, s, int64(number("expected_revision")), steps)
	case "run_code":
		if h.Code == nil {
			return result, nil, run.ErrTool
		}
		input, _ := json.Marshal(args["input"])
		result, err = h.Code.Run(ctx, s, run.CodeInput{Language: text("language"), Code: text("code"), Input: input})
	case "publish_artifact":
		var a run.Artifact
		a, result, err = h.Files.Publish(ctx, s, call.ID, resource.PublishInput{Path: text("path"), ExpectedSHA256: text("expected_sha256"), DisplayName: text("display_name"), MediaType: text("media_type")})
		if err == nil {
			artifact = &a
		}
	default:
		err = run.ErrTool
	}
	return
}
