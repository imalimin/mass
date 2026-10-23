package resource

import (
	"context"
	"io"
	"sync"

	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
)

type SessionReader interface {
	RuntimeSession(context.Context, string, string, string) (run.Session, error)
}
type workspaceExistence interface{ Exists(string, string) bool }
type RunWorkspaces struct {
	Workspaces Workspaces
	Repo       SessionReader
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
func (w *RunWorkspaces) Ensure(ctx context.Context, s run.Snapshot) error {
	if w.Available(s.SpaceID, s.SessionID) {
		return nil
	}
	session, err := w.Repo.RuntimeSession(ctx, s.ActorID, s.SpaceID, s.SessionID)
	if err != nil {
		return err
	}
	err = w.Workspaces.With(ctx, s.SpaceID, s.SessionID, func(dir WorkspaceDir) error {
		if err := dir.Clear(); err != nil {
			return err
		}
		manifest := storage.Manifest{FormatVersion: 1, Entries: []storage.Entry{}}
		var reader io.Reader
		var closers []io.Closer
		defer func() {
			for _, c := range closers {
				c.Close()
			}
		}()
		if session.LatestSnapshotID != "" {
			// Snapshot metadata is already authorized by the owning session pointer.
			repo, ok := w.Repo.(SnapshotRepository)
			if !ok {
				return storage.ErrWorkspace
			}
			snapshot, err := repo.Snapshot(ctx, s.ActorID, s.SpaceID, session.LatestSnapshotID)
			if err != nil {
				return err
			}
			if err = ReadJSON(ctx, w.Content, s.ActorID, s.SpaceID, snapshot.ManifestRef, 4<<20, &manifest); err != nil {
				return err
			}
			_, file, err := w.Content.Read(ctx, s.ActorID, s.SpaceID, snapshot.ObjectID)
			if err != nil {
				return err
			}
			closers = append(closers, file)
			reader = file
		} else {
			archive, _, err := dir.Pack(ctx)
			if err != nil {
				return err
			}
			closers = append(closers, archive)
			reader = archive
		}
		if err := dir.Restore(ctx, reader, manifest); err != nil {
			return err
		}
		e := journal.New(s.ActorID, "workspace.restored", s.ID, journal.RestoreData{SnapshotID: session.LatestSnapshotID, Reason: "service_start_or_directory_missing"})
		if _, err := AppendRun(ctx, w.Journal, s, e); err != nil {
			dir.Clear()
			return err
		}
		return nil
	})
	if err == nil {
		w.Mark(s.SpaceID, s.SessionID)
	}
	return err
}
func (w *RunWorkspaces) RestoreIdle(ctx context.Context, actor, space, session string, snapshots *SnapshotStore) error {
	w.Forget(space, session)
	if err := w.Workspaces.With(ctx, space, session, func(dir WorkspaceDir) error { return dir.Clear() }); err != nil {
		return err
	}
	if err := snapshots.Restore(ctx, actor, space, session); err != nil {
		return err
	}
	w.Mark(space, session)
	return nil
}
