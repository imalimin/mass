package resource

import (
	"context"
	"errors"
	"io/fs"
	"path"
	"sync"
	"syscall"

	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
)

type workspaceExistence interface{ Exists(string, string) bool }
type runBaseSnapshotReader interface {
	RunBaseSnapshot(context.Context, run.Snapshot) (storage.Snapshot, error)
}
type RunWorkspaces struct {
	Workspaces Workspaces
	Repo       runBaseSnapshotReader
	Content    *ContentStore
	Journal    *journal.Journal
	mu         sync.Mutex
	ready      map[string]bool
}

func (w *RunWorkspaces) key(space, session string) string { return space + ":" + session }
func (w *RunWorkspaces) Available(space, session string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ready[w.key(space, session)] && w.exists(space, session)
}
func (w *RunWorkspaces) exists(space, session string) bool {
	if e, ok := w.Workspaces.(workspaceExistence); ok {
		return e.Exists(space, session)
	}
	return false
}
func (w *RunWorkspaces) Forget(space, session string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.ready, w.key(space, session))
}
func (w *RunWorkspaces) Mark(space, session string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ready == nil {
		w.ready = map[string]bool{}
	}
	w.ready[w.key(space, session)] = true
}
func (w *RunWorkspaces) Ensure(ctx context.Context, s run.Snapshot) ([]string, error) {
	ready := w.Available(s.SpaceID, s.SessionID)
	snapshot, err := w.Repo.RunBaseSnapshot(ctx, s)
	if err != nil {
		return nil, err
	}
	manifest, err := readWorkspaceManifest(ctx, w.Content, s.ActorID, s.SpaceID, snapshot)
	if err != nil {
		return nil, err
	}
	if ready && len(manifest.LocalFiles) == 0 {
		return nil, nil
	}
	var missing []string
	err = w.Workspaces.With(ctx, s.SpaceID, s.SessionID, func(dir WorkspaceDir) error {
		if !ready {
			if err := restoreWorkspace(ctx, w.Content, s.ActorID, s.SpaceID, dir, snapshot, manifest); err != nil {
				return err
			}
			e := journal.New(s.ActorID, "workspace.restored", s.ID, journal.RestoreData{SnapshotID: snapshot.ID, Reason: "service_start_or_directory_missing"})
			if _, err := AppendRun(ctx, w.Journal, s, e); err != nil {
				return errors.Join(err, dir.Clear(ctx, manifest.LocalFiles))
			}
		}
		for _, name := range manifest.LocalFiles {
			if err := ctx.Err(); err != nil {
				return err
			}
			if parent := path.Dir(name); parent != "." {
				if err := checkParents(dir.Root(), parent); errors.Is(err, fs.ErrNotExist) || errors.Is(err, storage.ErrWorkspace) {
					missing = append(missing, name)
					continue
				} else if err != nil {
					return err
				}
			}
			info, err := dir.Root().Lstat(name)
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
				missing = append(missing, name)
			} else if err != nil {
				return err
			} else if !info.Mode().IsRegular() && info.Mode()&fs.ModeSymlink == 0 {
				missing = append(missing, name)
			}
		}
		return nil
	})
	if err == nil {
		w.Mark(s.SpaceID, s.SessionID)
	}
	return missing, err
}
func (w *RunWorkspaces) RestoreIdle(ctx context.Context, actor, space, session string, snapshots *SnapshotStore) error {
	w.Forget(space, session)
	if err := snapshots.Restore(ctx, actor, space, session); err != nil {
		return err
	}
	w.Mark(space, session)
	return nil
}
