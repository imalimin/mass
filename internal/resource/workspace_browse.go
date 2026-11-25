package resource

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
)

const workspacePreviewLimit = 256 << 10

type existingWorkspaces interface {
	WithExisting(context.Context, string, string, func(WorkspaceDir) error) error
}

type missingWorkspaces interface {
	WithMissing(context.Context, string, string, func(WorkspaceDir) error) error
}

type WorkspaceEntry struct {
	Name      string `json:"name"`
	Directory bool   `json:"directory"`
}

type WorkspaceEntryView struct {
	Directory bool             `json:"directory"`
	Entries   []WorkspaceEntry `json:"entries"`
	Text      string           `json:"text"`
	Status    string           `json:"status,omitempty"`
}

func (w *RunWorkspaces) restoreMissing(ctx context.Context, actor string, session run.Session, snapshots *SnapshotStore) error {
	manager, ok := w.Workspaces.(missingWorkspaces)
	if !ok {
		return storage.ErrWorkspace
	}
	space, id := session.SpaceID, session.ID
	return manager.WithMissing(ctx, space, id, func(dir WorkspaceDir) error {
		ctx, cancel := context.WithTimeout(ctx, snapshots.Timeout)
		defer cancel()
		base, err := snapshots.Repo.SnapshotBase(ctx, actor, space, id, "")
		if err != nil {
			return err
		}
		if base == "" {
			return storage.ErrWorkspace
		}
		snapshot, err := snapshots.Repo.Snapshot(ctx, actor, space, base)
		if err != nil {
			return err
		}
		manifest, err := readWorkspaceManifest(ctx, snapshots.Content, actor, space, snapshot)
		if err != nil {
			return err
		}
		if err := restoreWorkspace(ctx, snapshots.Content, actor, space, dir, snapshot, manifest); err != nil {
			return err
		}
		w.Mark(space, id)
		return nil
	})
}

func (w *RunWorkspaces) Browse(ctx context.Context, actor string, session run.Session, path string, snapshots *SnapshotStore) (WorkspaceEntryView, error) {
	view := WorkspaceEntryView{Entries: []WorkspaceEntry{}}
	if path != "." && !filePath(path) {
		return view, storage.ErrInvalid
	}
	if session.LatestSnapshotID != "" {
		if err := w.restoreMissing(ctx, actor, session, snapshots); err != nil {
			if errors.Is(err, storage.ErrBlobNotFound) {
				return view, storage.ErrIntegrity
			}
			return view, err
		}
	}
	reader, ok := w.Workspaces.(existingWorkspaces)
	if !ok {
		return view, storage.ErrWorkspace
	}
	err := reader.WithExisting(ctx, session.SpaceID, session.ID, func(dir WorkspaceDir) error {
		if path != "." {
			if err := checkParents(dir.Root(), path); err != nil {
				return err
			}
		}
		info, err := dir.Root().Lstat(path)
		if err != nil {
			return err
		}
		view.Directory = info.IsDir()
		if !view.Directory {
			return readWorkspacePreview(dir, path, &view)
		}
		entries, err := fs.ReadDir(dir.Root().FS(), path)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if !filePath(entry.Name()) || !entry.IsDir() && !entry.Type().IsRegular() {
				continue
			}
			info, err := entry.Info()
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			if !hasMultipleLinks(info) && (info.IsDir() || info.Mode().IsRegular()) {
				view.Entries = append(view.Entries, WorkspaceEntry{Name: entry.Name(), Directory: info.IsDir()})
			}
		}
		slices.SortFunc(view.Entries, func(left, right WorkspaceEntry) int {
			if path == "." {
				leftInstructions := left.Name == "AGENTS.md" && !left.Directory
				rightInstructions := right.Name == "AGENTS.md" && !right.Directory
				if leftInstructions != rightInstructions {
					if leftInstructions {
						return -1
					}
					return 1
				}
			}
			if left.Directory != right.Directory {
				if left.Directory {
					return -1
				}
				return 1
			}
			return strings.Compare(left.Name, right.Name)
		})
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		if path == "." {
			view.Directory = true
			return view, nil
		}
		return view, storage.ErrBlobNotFound
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, storage.ErrInvalid) {
		return view, err
	}
	if err != nil {
		return view, storage.ErrWorkspace
	}
	return view, nil
}

// OpenFile opens a regular file in the session workspace for read-only
// streaming download without holding the workspace lock across network I/O.
func (w *RunWorkspaces) OpenFile(ctx context.Context, actor string, session run.Session, rel string, snapshots *SnapshotStore) (*os.File, fs.FileInfo, error) {
	if !filePath(rel) {
		return nil, nil, storage.ErrInvalid
	}
	if session.LatestSnapshotID != "" {
		if err := w.restoreMissing(ctx, actor, session, snapshots); err != nil {
			if errors.Is(err, storage.ErrBlobNotFound) {
				return nil, nil, storage.ErrIntegrity
			}
			return nil, nil, err
		}
	}
	reader, ok := w.Workspaces.(existingWorkspaces)
	if !ok {
		return nil, nil, storage.ErrWorkspace
	}
	var file *os.File
	var info fs.FileInfo
	err := reader.WithExisting(ctx, session.SpaceID, session.ID, func(dir WorkspaceDir) error {
		var openErr error
		file, info, openErr = openWorkspaceFile(dir, rel)
		return openErr
	})
	if err != nil {
		if file != nil {
			file.Close()
		}
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, storage.ErrBlobNotFound
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, storage.ErrInvalid) {
			return nil, nil, err
		}
		return nil, nil, storage.ErrWorkspace
	}
	return file, info, nil
}

func readWorkspacePreview(dir WorkspaceDir, path string, view *WorkspaceEntryView) error {
	file, info, err := openWorkspaceFile(dir, path)
	if err != nil {
		return err
	}
	defer file.Close()
	if info.Size() > workspacePreviewLimit {
		view.Status = "too_large"
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(file, workspacePreviewLimit+1))
	if err != nil {
		return err
	}
	if len(data) > workspacePreviewLimit {
		view.Status = "too_large"
	} else if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		view.Status = "binary"
	} else {
		view.Text = string(data)
	}
	return nil
}
