// Package builtin implements tool semantics through constrained resource services.
package builtin

import (
	"context"
	"strings"

	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
	"github.com/imalimin/mass/internal/resource"
)

type Files interface {
	Read(context.Context, run.Snapshot, resource.ReadFileInput) (run.ToolResult, error)
	Write(context.Context, run.Snapshot, resource.WriteFileInput) (run.ToolResult, error)
	Edit(context.Context, run.Snapshot, resource.EditInput) (run.ToolResult, error)
}

type Handler struct {
	Files   Files
	Command *resource.CommandService
	Journal *journal.Journal
}

func (h *Handler) Execute(ctx context.Context, s run.Snapshot, call run.Call, args map[string]any) (result run.ToolResult, err error) {
	text := func(key string) string { value, _ := args[key].(string); return value }
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
	case run.CommandTool:
		cmd := text("command")
		if rawArgs, hasArgs := args["args"]; hasArgs && rawArgs != nil {
			if strings.Contains(cmd, "/") {
				return result, storage.ErrInvalid
			}
			values := rawArgs.([]any)
			argv := make([]string, len(values))
			for i, value := range values {
				argv[i] = value.(string)
			}
			result, err = h.Command.Run(ctx, s, cmd, argv)
		} else {
			result, err = h.Command.Run(ctx, s, "sh", []string{"-c", cmd})
		}
	case "read_file":
		result, err = h.Files.Read(ctx, s, resource.ReadFileInput{Path: text("path"), Range: span})
	case "write_file":
		result, err = h.Files.Write(ctx, s, resource.WriteFileInput{Path: text("path"), Content: text("content")})
	case "edit_file":
		edits := []resource.Edit{}
		for _, raw := range args["edits"].([]any) {
			e := raw.(map[string]any)
			edits = append(edits, resource.Edit{Old: e["old_text"].(string), New: e["new_text"].(string)})
		}
		result, err = h.Files.Edit(ctx, s, resource.EditInput{Path: text("path"), Edits: edits})
	case "update_plan":
		steps := []run.PlanStep{}
		for _, raw := range args["steps"].([]any) {
			step := raw.(map[string]any)
			id, _ := step["id"].(string)
			steps = append(steps, run.PlanStep{ID: id, Title: step["title"].(string), Status: step["status"].(string)})
		}
		result, err = resource.PreparePlan(ctx, h.Journal, s, steps)
	default:
		err = run.ErrTool
	}
	return
}
