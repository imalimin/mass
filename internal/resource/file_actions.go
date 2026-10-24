package resource

import (
	"context"
	"errors"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"io/fs"
	"path"
	"sort"
	"strings"
	"unicode/utf8"
)

type Edit struct {
	Old string `json:"old_text"`
	New string `json:"new_text"`
}
type EditInput struct {
	Path           string `json:"path"`
	ExpectedSHA256 string `json:"expected_sha256"`
	Edits          []Edit `json:"edits"`
}
type MoveInput struct {
	Source         string `json:"source"`
	Target         string `json:"target"`
	ExpectedSHA256 string `json:"expected_sha256"`
}

func (f *FileService) Edit(ctx context.Context, s run.Snapshot, in EditInput) (result run.ToolResult, err error) {
	if !filePath(in.Path) || len(in.Edits) < 1 || len(in.Edits) > 100 {
		return result, storage.ErrInvalid
	}
	err = f.Workspaces.With(ctx, s.SpaceID, s.SessionID, func(dir WorkspaceDir) error {
		if err := f.Check(ctx, s); err != nil {
			return err
		}
		raw, err := f.read(ctx, dir, in.Path)
		if err != nil {
			return err
		}
		if !utf8.Valid(raw) {
			return storage.ErrInvalid
		}
		if hashBytes(raw) != in.ExpectedSHA256 {
			return storage.ErrConflict
		}
		type interval struct {
			start, end  int
			replacement string
		}
		edits := []interval{}
		source := string(raw)
		for _, e := range in.Edits {
			if e.Old == "" || !utf8.ValidString(e.Old) || !utf8.ValidString(e.New) {
				return storage.ErrConflict
			}
			start := strings.Index(source, e.Old)
			if start < 0 || strings.Index(source[start+1:], e.Old) >= 0 {
				return storage.ErrConflict
			}
			edits = append(edits, interval{start, start + len(e.Old), e.New})
		}
		sort.Slice(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
		for i := 1; i < len(edits); i++ {
			if edits[i].start < edits[i-1].end {
				return storage.ErrConflict
			}
		}
		for i := len(edits) - 1; i >= 0; i-- {
			e := edits[i]
			source = source[:e.start] + e.replacement + source[e.end:]
			if int64(len(source)) > f.MaxBytes {
				return storage.ErrInvalid
			}
		}
		return f.write(ctx, s, dir, WriteFileInput{Path: in.Path, ExpectedSHA256: in.ExpectedSHA256, Content: source, MediaType: "text/plain"}, &result)
	})
	return
}
func (f *FileService) Move(ctx context.Context, s run.Snapshot, in MoveInput) (result run.ToolResult, err error) {
	if !filePath(in.Source) || !filePath(in.Target) || strings.EqualFold(in.Source, in.Target) {
		return result, storage.ErrInvalid
	}
	err = f.Workspaces.With(ctx, s.SpaceID, s.SessionID, func(dir WorkspaceDir) error {
		if err := f.Check(ctx, s); err != nil {
			return err
		}
		raw, err := f.read(ctx, dir, in.Source)
		if err != nil {
			return err
		}
		if hashBytes(raw) != in.ExpectedSHA256 {
			return storage.ErrConflict
		}
		if err = checkParents(dir.Root(), in.Target); err == nil {
			return storage.ErrConflict
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		parent := path.Dir(in.Target)
		if parent != "." {
			if err = checkParents(dir.Root(), parent); err != nil {
				return err
			}
		}
		if _, err = dir.Root().Lstat(in.Target); !errors.Is(err, fs.ErrNotExist) {
			return storage.ErrConflict
		}
		if err = f.Check(ctx, s); err != nil {
			return err
		}
		if err = dir.Root().Rename(in.Source, in.Target); err != nil {
			return err
		}
		result = dataResult(map[string]any{"source": in.Source, "path": in.Target, "sha256": hashBytes(raw), "size": len(raw)}, "committed")
		return nil
	})
	return
}
func (f *FileService) Delete(ctx context.Context, s run.Snapshot, in ReadFileInput) (result run.ToolResult, err error) {
	if !filePath(in.Path) || in.ExpectedSHA256 == "" {
		return result, storage.ErrInvalid
	}
	err = f.Workspaces.With(ctx, s.SpaceID, s.SessionID, func(dir WorkspaceDir) error {
		if err := f.Check(ctx, s); err != nil {
			return err
		}
		raw, err := f.read(ctx, dir, in.Path)
		if err != nil {
			return err
		}
		if hashBytes(raw) != in.ExpectedSHA256 {
			return storage.ErrConflict
		}
		if err = f.Check(ctx, s); err != nil {
			return err
		}
		if err = dir.Root().Remove(in.Path); err != nil {
			return err
		}
		result = dataResult(map[string]any{"path": in.Path, "sha256": hashBytes(raw)}, "committed")
		return nil
	})
	return
}
func (f *FileService) Mkdir(ctx context.Context, s run.Snapshot, directory string) (result run.ToolResult, err error) {
	if !filePath(directory) {
		return result, storage.ErrInvalid
	}
	err = f.Workspaces.With(ctx, s.SpaceID, s.SessionID, func(dir WorkspaceDir) error {
		if err := f.Check(ctx, s); err != nil {
			return err
		}
		if err := f.checkWrite(ctx, dir.Root(), directory, 0); err != nil {
			return err
		}
		parts := strings.Split(directory, "/")
		effect := "none"
		for i := 1; i <= len(parts); i++ {
			p := strings.Join(parts[:i], "/")
			if err := f.Check(ctx, s); err != nil {
				return err
			}
			if err := dir.Root().Mkdir(p, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			} else if err == nil {
				effect = "committed"
			}
			if err := checkParents(dir.Root(), p); err != nil {
				return err
			}
			info, err := dir.Root().Stat(p)
			if err != nil || !info.IsDir() {
				return storage.ErrConflict
			}
		}
		result = dataResult(map[string]any{"directory": directory}, effect)
		return nil
	})
	return
}
