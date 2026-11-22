package resource

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/imalimin/mass/internal/domain/run"
)

type stubWorkspaceDir struct {
	WorkspaceDir
	root *os.Root
}

func (s stubWorkspaceDir) Root() *os.Root { return s.root }

type stubCommandWorkspace struct{ dir string }

func (s stubCommandWorkspace) With(_ context.Context, _, _ string, fn func(WorkspaceDir) error) error {
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return err
	}
	defer root.Close()
	return fn(stubWorkspaceDir{root: root})
}

func TestCommandServiceInjectsAndCleansUpEnv(t *testing.T) {
	dir := t.TempDir()
	cleaned := false
	var gotEnv []string
	svc := &CommandService{
		Workspaces: stubCommandWorkspace{dir: dir},
		Env: func(s run.Snapshot) ([]string, func(), error) {
			return []string{"MASS_API_URL=http://127.0.0.1:8080", "MASS_SPACE_ID=" + s.SpaceID, "MASS_RUN_TOKEN=ephemeral-tok"}, func() {
				cleaned = true
			}, nil
		},
		ExecuteWithEnv: func(_ context.Context, _ string, _ string, _ []string, extraEnv []string, _ int) (CommandOutput, error) {
			gotEnv = slices.Clone(extraEnv)
			if cleaned {
				t.Fatal("cleanup ran before command finished")
			}
			code := 1
			return CommandOutput{Started: true, ExitCode: &code, Stderr: "boom"}, errors.New("exit status 1")
		},
	}
	snap := run.Snapshot{
		SpaceID:   "4lca4gankkuxip2p",
		SessionID: "sess123456789012",
		ActorID:   "actor-1",
		Definition: run.Definition{
			ToolPolicy: run.ToolPolicy{TimeoutMS: 5000, MaxLogBytes: 4096},
		},
	}
	res, err := svc.Run(context.Background(), snap, "my-cli", []string{"list-skills"})
	if err != nil {
		t.Fatalf("unexpected context error: %v", err)
	}
	if !cleaned {
		t.Fatal("cleanup was not invoked after command exit")
	}
	if res.Status != "failed" || res.Error != "command_exit" {
		t.Fatalf("unexpected tool result: %+v", res)
	}
	want := []string{"MASS_API_URL=http://127.0.0.1:8080", "MASS_SPACE_ID=4lca4gankkuxip2p", "MASS_RUN_TOKEN=ephemeral-tok"}
	if !slices.Equal(gotEnv, want) {
		t.Fatalf("extraEnv=%v want %v", gotEnv, want)
	}
}
