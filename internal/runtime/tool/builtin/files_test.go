package builtin

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/resource"
)

type stubDir struct {
	resource.WorkspaceDir
	root *os.Root
}

func (s stubDir) Root() *os.Root { return s.root }

type stubCommandWorkspace struct{ dir string }

func (s stubCommandWorkspace) With(_ context.Context, _, _ string, fn func(resource.WorkspaceDir) error) error {
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return err
	}
	defer root.Close()
	return fn(stubDir{root: root})
}

func TestRunCommandShellDefaultAndExplicitArgs(t *testing.T) {
	var gotName string
	var gotArgs []string
	svc := &resource.CommandService{
		Workspaces: stubCommandWorkspace{dir: t.TempDir()},
		ExecuteWithEnv: func(_ context.Context, _ string, name string, args []string, _ []string, _ int) (resource.CommandOutput, error) {
			gotName = name
			gotArgs = slices.Clone(args)
			code := 0
			return resource.CommandOutput{Started: true, ExitCode: &code, Stdout: "ok"}, nil
		},
	}
	h := &Handler{Command: svc}
	snap := run.Snapshot{Definition: run.Definition{ToolPolicy: run.ToolPolicy{TimeoutMS: 5000, MaxLogBytes: 4096}}}

	// 1. Default mode executes via sh -c.
	res, err := h.Execute(context.Background(), snap, run.Call{ID: "c1", Name: run.CommandTool}, map[string]any{
		"command": "echo hi | tr a-z A-Z",
	})
	if err != nil || res.Status != "completed" || gotName != "sh" || !slices.Equal(gotArgs, []string{"-c", "echo hi | tr a-z A-Z"}) {
		t.Fatalf("shell mode mismatch: err=%v status=%s got=%s %v", err, res.Status, gotName, gotArgs)
	}

	// 2. Explicit args mode invokes executable directly from PATH.
	res, err = h.Execute(context.Background(), snap, run.Call{ID: "c2", Name: run.CommandTool}, map[string]any{
		"command": "my-cli",
		"args":    []any{"--version"},
	})
	if err != nil || res.Status != "completed" || gotName != "my-cli" || !slices.Equal(gotArgs, []string{"--version"}) {
		t.Fatalf("explicit args mode mismatch: err=%v status=%s got=%s %v", err, res.Status, gotName, gotArgs)
	}

	// 3. Explicit args mode rejects slash paths.
	if _, err = h.Execute(context.Background(), snap, run.Call{ID: "c3", Name: run.CommandTool}, map[string]any{
		"command": "./outside",
		"args":    []any{},
	}); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("expected ErrInvalid for slash command with args, got %v", err)
	}
}
