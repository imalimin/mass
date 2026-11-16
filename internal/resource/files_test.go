package resource_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/imalimin/mass/internal/adapter/workspace"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/resource"
)

func TestWriteCASBoundariesAndQuotas(t *testing.T) {
	limits := storage.Limits{MaxArchiveFileBytes: 16, MaxTotalBytes: 20}
	manager, err := workspace.New(t.TempDir(), limits)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	service := &resource.FileService{Workspaces: manager, MaxBytes: 16, Limits: limits, Check: func(context.Context, run.Snapshot) error { return nil }}
	snapshot := run.Snapshot{SpaceID: storage.NewShortID(), SessionID: storage.NewShortID()}
	ctx := context.Background()
	write := func(path, text, sha string) error {
		_, err := service.Write(ctx, snapshot, resource.WriteFileInput{Path: path, Content: text, ExpectedSHA256: sha, MediaType: "text/plain"})
		return err
	}
	if err := write("a.txt", "123456789012345", "missing"); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(write("a.txt", "changed", "missing"), storage.ErrConflict) {
		t.Fatal("stale write accepted")
	}
	if !errors.Is(write("b.txt", "123456", "missing"), run.ErrOverflow) {
		t.Fatal("workspace quota exceeded")
	}
	for _, p := range []string{"../escape", "/absolute", "a/../../escape", "bad\\name"} {
		if write(p, "x", "missing") == nil {
			t.Fatal("unsafe path", p)
		}
	}
	if err := write("dir/b.txt", "12345", "missing"); err != nil {
		t.Fatal(err)
	}
	if err := write("extra.txt", "", "missing"); err != nil {
		t.Fatal("empty file rejected at the byte limit", err)
	}
	if err := manager.With(ctx, snapshot.SpaceID, snapshot.SessionID, func(dir resource.WorkspaceDir) error { return dir.Root().Symlink(t.TempDir(), "link") }); err != nil {
		t.Fatal(err)
	}
	if err := write("beside-link.txt", "", "missing"); err != nil {
		t.Fatal("unrelated symlink blocked a workspace write", err)
	}
	if write("link/outside", "x", "missing") == nil {
		t.Fatal("symlink accepted")
	}
	manager.With(ctx, snapshot.SpaceID, snapshot.SessionID, func(dir resource.WorkspaceDir) error {
		raw, err := os.ReadFile(dir.Root().Name() + "/a.txt")
		if err != nil || string(raw) != "123456789012345" {
			t.Fatal("rejected write changed file", err)
		}
		return nil
	})
}
