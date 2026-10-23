// Package builtin implements tool semantics through constrained resource services.
package builtin

import (
	"context"

	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/resource"
)

type Files interface {
	Read(context.Context, run.Snapshot, resource.ReadFileInput) (run.ToolResult, error)
	Write(context.Context, run.Snapshot, resource.WriteFileInput) (run.ToolResult, error)
	Publish(context.Context, run.Snapshot, string, resource.PublishInput) (run.Artifact, run.ToolResult, error)
}
type Handler struct{ Files Files }

func (h *Handler) Execute(ctx context.Context, s run.Snapshot, call run.Call, args map[string]any) (result run.ToolResult, artifact *run.Artifact, err error) {
	text := func(key string) string { value, _ := args[key].(string); return value }
	switch call.Name {
	case "read_file":
		result, err = h.Files.Read(ctx, s, resource.ReadFileInput{Path: text("path"), ExpectedSHA256: text("expected_sha256")})
	case "write_file":
		result, err = h.Files.Write(ctx, s, resource.WriteFileInput{Path: text("path"), Content: text("content"), MediaType: text("media_type"), ExpectedSHA256: text("expected_sha256")})
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
