package resource

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime"
	"os"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
)

type FileService struct {
	Workspaces Workspaces
	Content    *ContentStore
	MaxBytes   int64
	Limits     storage.Limits
	Check      func(context.Context, run.Snapshot) error
}
type ReadFileInput struct {
	Path           string `json:"path"`
	ExpectedSHA256 string `json:"expected_sha256,omitempty"`
}
type WriteFileInput struct {
	Path           string `json:"path"`
	Content        string `json:"content"`
	MediaType      string `json:"media_type"`
	ExpectedSHA256 string `json:"expected_sha256"`
}
type PublishInput struct {
	Path           string `json:"path"`
	ExpectedSHA256 string `json:"expected_sha256"`
	DisplayName    string `json:"display_name"`
	MediaType      string `json:"media_type"`
}

func filePath(s string) bool {
	return fs.ValidPath(s) && s != "." && len(s) <= 1024 && !strings.ContainsAny(s, "\\:\x00\r\n")
}
func validMediaType(value string) bool {
	if len(value) == 0 || len(value) > 128 || strings.ContainsAny(value, "\r\n\x00") {
		return false
	}
	_, _, err := mime.ParseMediaType(value)
	return err == nil
}

// Called under the session directory lock, before any write effect.
func (f *FileService) checkWrite(ctx context.Context, root *os.Root, target string, size int64) error {
	entries := map[string]string{}
	var total int64
	err := fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		info, err := root.Lstat(name)
		if err != nil {
			return err
		}
		if !filePath(name) || (!info.IsDir() && !info.Mode().IsRegular()) {
			return storage.ErrWorkspace
		}
		folded := strings.ToLower(name)
		if entries[folded] != "" {
			return storage.ErrWorkspace
		}
		entries[folded] = name
		if len(entries) > f.Limits.MaxEntries {
			return run.ErrOverflow
		}
		if info.Mode().IsRegular() && name != target {
			if info.Size() > f.Limits.MaxFileBytes || info.Size() > f.Limits.MaxTotalBytes-total {
				return run.ErrOverflow
			}
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		return err
	}
	if size > f.Limits.MaxTotalBytes-total {
		return run.ErrOverflow
	}
	parts := strings.Split(target, "/")
	for i := 1; i <= len(parts); i++ {
		name := strings.Join(parts[:i], "/")
		key := strings.ToLower(name)
		if old := entries[key]; old != "" && old != name {
			return storage.ErrConflict
		}
		entries[key] = name
	}
	if len(entries) > f.Limits.MaxEntries {
		return run.ErrOverflow
	}
	return nil
}
func checkParents(root *os.Root, name string) error {
	parts := strings.Split(name, "/")
	for i := 1; i <= len(parts); i++ {
		p := strings.Join(parts[:i], "/")
		info, err := root.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return storage.ErrWorkspace
		}
		if i < len(parts) && !info.IsDir() {
			return storage.ErrWorkspace
		}
	}
	return nil
}
func hashBytes(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
func (f *FileService) read(ctx context.Context, dir WorkspaceDir, p string) ([]byte, error) {
	if !filePath(p) {
		return nil, storage.ErrInvalid
	}
	if err := checkParents(dir.Root(), p); err != nil {
		return nil, err
	}
	file, err := dir.Root().Open(p)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, storage.ErrWorkspace
	}
	raw, err := io.ReadAll(io.LimitReader(storage.ContextReader{Context: ctx, Reader: file}, f.MaxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > f.MaxBytes {
		return nil, run.ErrOverflow
	}
	after, err := file.Stat()
	if err != nil || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
		return nil, storage.ErrConflict
	}
	return raw, nil
}
func (f *FileService) Read(ctx context.Context, s run.Snapshot, in ReadFileInput) (run.ToolResult, error) {
	var result run.ToolResult
	err := f.Workspaces.With(ctx, s.SpaceID, s.SessionID, func(dir WorkspaceDir) error {
		if err := f.Check(ctx, s); err != nil {
			return err
		}
		raw, err := f.read(ctx, dir, in.Path)
		if err != nil {
			return err
		}
		sha := hashBytes(raw)
		if in.ExpectedSHA256 != "" && in.ExpectedSHA256 != sha {
			return storage.ErrConflict
		}
		if !utf8.Valid(raw) {
			return storage.ErrInvalid
		}
		o, err := f.Content.Put(ctx, s.ActorID, s.SpaceID, uuid.NewString(), "file.read", "application/octet-stream", bytes.NewReader(raw))
		if err != nil {
			return err
		}
		data, _ := json.Marshal(map[string]any{"path": in.Path, "content": string(raw), "sha256": sha, "size": len(raw)})
		result = run.ToolResult{Status: "completed", Data: data, SideEffect: "none", Complete: true, SourceRefs: []storage.Ref{{Role: "source", ObjectID: o.ID, SHA256: o.SHA256}}}
		return nil
	})
	return result, err
}
func (f *FileService) Write(ctx context.Context, s run.Snapshot, in WriteFileInput) (result run.ToolResult, err error) {
	if !filePath(in.Path) || !utf8.ValidString(in.Content) || int64(len(in.Content)) > f.MaxBytes || in.ExpectedSHA256 == "" || !validMediaType(in.MediaType) {
		return result, storage.ErrInvalid
	}
	err = f.Workspaces.With(ctx, s.SpaceID, s.SessionID, func(dir WorkspaceDir) error {
		if err := f.Check(ctx, s); err != nil {
			return err
		}
		existing, readErr := f.read(ctx, dir, in.Path)
		if errors.Is(readErr, fs.ErrNotExist) {
			if in.ExpectedSHA256 != "missing" {
				return storage.ErrConflict
			}
		} else if readErr != nil {
			return readErr
		} else if in.ExpectedSHA256 != hashBytes(existing) {
			return storage.ErrConflict
		}
		if err := f.checkWrite(ctx, dir.Root(), in.Path, int64(len(in.Content))); err != nil {
			return err
		}
		parent := path.Dir(in.Path)
		// New parent directories are created one segment at a time; no links are followed.
		if parent != "." {
			parts := strings.Split(parent, "/")
			for i := 1; i <= len(parts); i++ {
				p := strings.Join(parts[:i], "/")
				if err := dir.Root().Mkdir(p, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
					return err
				}
				if err := checkParents(dir.Root(), p); err != nil {
					return err
				}
			}
		}
		temp := path.Join(parent, ".mass-write-"+uuid.NewString())
		file, err := dir.Root().OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer dir.Root().Remove(temp)
		_, err = io.Copy(file, storage.ContextReader{Context: ctx, Reader: strings.NewReader(in.Content)})
		if err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if err = f.Check(ctx, s); err != nil {
			return err
		}
		if err = dir.Root().Rename(temp, in.Path); err != nil {
			return err
		}
		raw, _ := json.Marshal(map[string]any{"path": in.Path, "sha256": hashBytes([]byte(in.Content)), "size": len(in.Content)})
		result = run.ToolResult{Status: "completed", Data: raw, SideEffect: "committed", Complete: true, SourceRefs: []storage.Ref{}}
		return nil
	})
	return
}
func (f *FileService) Publish(ctx context.Context, s run.Snapshot, callID string, in PublishInput) (artifact run.Artifact, result run.ToolResult, err error) {
	if !filePath(in.Path) || in.ExpectedSHA256 == "" || in.ExpectedSHA256 == "missing" || len(in.DisplayName) == 0 || len(in.DisplayName) > 200 || strings.ContainsAny(in.DisplayName, "\r\n\x00/\\") || !validMediaType(in.MediaType) {
		return artifact, result, storage.ErrInvalid
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
		o, err := f.Content.Put(ctx, s.ActorID, s.SpaceID, uuid.NewString(), "artifact", "application/octet-stream", bytes.NewReader(raw))
		if err != nil {
			return err
		}
		artifact = run.Artifact{ID: uuid.NewString(), SpaceID: s.SpaceID, SessionID: s.SessionID, RunID: s.ID, CallID: callID, ObjectID: o.ID, SHA256: o.SHA256, Name: in.DisplayName, MediaType: in.MediaType, Path: in.Path}
		artifact.URL = "/api/v1/spaces/" + s.SpaceID + "/artifacts/" + artifact.ID + "/download"
		data, _ := json.Marshal(artifact)
		result = run.ToolResult{Status: "completed", Data: data, SideEffect: "committed", Complete: true, SourceRefs: []storage.Ref{{Role: "artifact", ObjectID: o.ID, SHA256: o.SHA256}}}
		return nil
	})
	return
}
