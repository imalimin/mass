package resource

import (
	"archive/zip"
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

// WorkspaceArchive holds a temporary ZIP snapshot of a session workspace and
// removes the temporary file when closed.
type WorkspaceArchive struct {
	*os.File
	Size int64
}

func (a *WorkspaceArchive) Close() error {
	name := a.File.Name()
	err := a.File.Close()
	return errors.Join(err, os.Remove(name))
}

// Archive packs the entire session workspace into a temporary ZIP archive for
// streaming download without holding the workspace lock across network I/O.
func (w *RunWorkspaces) Archive(ctx context.Context, actor string, session run.Session, snapshots *SnapshotStore) (*WorkspaceArchive, error) {
	if session.LatestSnapshotID != "" {
		if err := w.restoreMissing(ctx, actor, session, snapshots); err != nil {
			if errors.Is(err, storage.ErrBlobNotFound) {
				return nil, storage.ErrIntegrity
			}
			return nil, err
		}
	}
	reader, ok := w.Workspaces.(existingWorkspaces)
	if !ok {
		return nil, storage.ErrWorkspace
	}
	tmp, err := os.CreateTemp("", "mass-workspace-download-*.zip")
	if err != nil {
		return nil, err
	}
	out := &WorkspaceArchive{File: tmp}
	done := false
	defer func() {
		if !done {
			out.Close()
		}
	}()
	zw := zip.NewWriter(tmp)
	err = reader.WithExisting(ctx, session.SpaceID, session.ID, func(dir WorkspaceDir) error {
		return fs.WalkDir(dir.Root().FS(), ".", func(name string, _ fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if name == "." {
				return nil
			}
			info, err := dir.Root().Lstat(name)
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			if !filePath(name) || info.Mode()&os.ModeSymlink != 0 || hasMultipleLinks(info) {
				if info.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if info.IsDir() {
				hdr := &zip.FileHeader{Name: name + "/", Method: zip.Store, Modified: info.ModTime()}
				hdr.SetMode(info.Mode())
				_, err := zw.CreateHeader(hdr)
				return err
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			f, err := dir.Root().Open(name)
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			defer f.Close()
			opened, err := f.Stat()
			if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
				return nil
			}
			hdr := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: info.ModTime()}
			hdr.SetMode(opened.Mode().Perm())
			entryWriter, err := zw.CreateHeader(hdr)
			if err != nil {
				return err
			}
			_, err = io.Copy(entryWriter, storage.ContextReader{Context: ctx, Reader: f})
			return err
		})
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, storage.ErrInvalid) {
			return nil, err
		}
		return nil, storage.ErrWorkspace
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	info, err := tmp.Stat()
	if err != nil {
		return nil, err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	out.Size = info.Size()
	done = true
	return out, nil
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
