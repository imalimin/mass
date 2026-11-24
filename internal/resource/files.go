package resource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/diagnostic"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
)

type FileService struct {
	ReadBudget func(int64) error
	Workspaces Workspaces
	Content    *ContentStore
	Skills     *SkillCache
	MaxBytes   int64
	Limits     storage.Limits
	Check      func(context.Context, run.Snapshot) error
}
type ReadFileInput struct {
	Path           string     `json:"path"`
	ExpectedSHA256 string     `json:"expected_sha256,omitempty"`
	Range          *run.Range `json:"range,omitempty"`
}
type WriteFileInput struct {
	Path           string `json:"path"`
	Content        string `json:"content"`
	MediaType      string `json:"media_type,omitempty"`
	ExpectedSHA256 string `json:"expected_sha256,omitempty"`
}
type PublishInput struct {
	Path           string `json:"path"`
	ExpectedSHA256 string `json:"expected_sha256,omitempty"`
	DisplayName    string `json:"display_name,omitempty"`
	MediaType      string `json:"media_type,omitempty"`
}

var errWorkspaceSymlink = run.ToolFailure("workspace_symlink", "none")

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
		local := f.Limits.IsHostLocal(info)
		regular := info.Mode().IsRegular()
		if !filePath(name) || (!local && !info.IsDir() && !regular) || (regular && hasMultipleLinks(info)) {
			return storage.ErrWorkspace
		}
		folded := strings.ToLower(name)
		if entries[folded] != "" {
			return storage.ErrWorkspace
		}
		entries[folded] = name
		if regular && !local && name != target {
			if info.Size() > f.Limits.MaxTotalBytes-total {
				return diagnostic.Failure(ctx, "workspace.operation_failed", run.ErrOverflow, "stage=write limit=workspace_bytes accumulated_bytes=%d entry_bytes=%d max_bytes=%d", total, info.Size(), f.Limits.MaxTotalBytes)
			}
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		return err
	}
	if size <= f.Limits.MaxArchiveFileBytes && size > f.Limits.MaxTotalBytes-total {
		return diagnostic.Failure(ctx, "workspace.operation_failed", run.ErrOverflow, "stage=write limit=workspace_bytes accumulated_bytes=%d entry_bytes=%d max_bytes=%d", total, size, f.Limits.MaxTotalBytes)
	}
	parts := strings.Split(target, "/")
	for i := 1; i <= len(parts); i++ {
		name := strings.Join(parts[:i], "/")
		key := strings.ToLower(name)
		if old := entries[key]; old != "" && old != name {
			return storage.ErrConflict
		}
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
		if info.Mode()&os.ModeSymlink != 0 {
			return errWorkspaceSymlink
		}
		if hasMultipleLinks(info) || (!info.IsDir() && !info.Mode().IsRegular()) {
			return storage.ErrWorkspace
		}
		if i < len(parts) && !info.IsDir() {
			return storage.ErrWorkspace
		}
	}
	return nil
}
func hashBytes(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
func openWorkspaceFile(dir WorkspaceDir, p string) (*os.File, fs.FileInfo, error) {
	if err := checkParents(dir.Root(), p); err != nil {
		return nil, nil, err
	}
	file, err := dir.Root().Open(p)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, nil, storage.ErrWorkspace
	}
	return file, info, nil
}

func splitSkillPath(raw string) (skillName, rel string, ok bool) {
	var trimmed string
	switch {
	case strings.HasPrefix(raw, "$"+SkillsDirEnv+"/"):
		trimmed = strings.TrimPrefix(raw, "$"+SkillsDirEnv+"/")
	case strings.HasPrefix(raw, "${"+SkillsDirEnv+"}/"):
		trimmed = strings.TrimPrefix(raw, "${"+SkillsDirEnv+"}/")
	default:
		return "", "", false
	}
	name, rest, found := strings.Cut(trimmed, "/")
	if !found || name == "" || rest == "" {
		return "", "", false
	}
	return name, rest, true
}

func (f *FileService) readRoot(ctx context.Context, root *os.Root, p string) ([]byte, error) {
	if !filePath(p) {
		return nil, storage.ErrInvalid
	}
	if err := checkParents(root, p); err != nil {
		return nil, err
	}
	file, err := root.Open(p)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, storage.ErrWorkspace
	}
	if info.Size() > storage.MaxContentObjectBytes {
		return nil, run.ErrOverflow
	}
	if f.ReadBudget != nil {
		if err = f.ReadBudget(info.Size()); err != nil {
			return nil, err
		}
	}
	raw, err := io.ReadAll(io.LimitReader(storage.ContextReader{Context: ctx, Reader: file}, info.Size()+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) != info.Size() {
		return nil, storage.ErrConflict
	}
	after, err := file.Stat()
	if err != nil || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
		return nil, storage.ErrConflict
	}
	return raw, nil
}

func (f *FileService) read(ctx context.Context, dir WorkspaceDir, p string) ([]byte, error) {
	return f.readRoot(ctx, dir.Root(), p)
}

func (f *FileService) readSkillFile(ctx context.Context, s run.Snapshot, in ReadFileInput, skillName, rel string) (run.ToolResult, error) {
	if err := f.Check(ctx, s); err != nil {
		return run.ToolResult{}, err
	}
	if !filePath(rel) {
		return run.ToolResult{}, storage.ErrInvalid
	}
	if f.Skills == nil {
		return run.ToolResult{}, run.ToolFailure("skill_not_bound", "none")
	}
	mounts, err := f.Skills.Mounts(ctx, f.Content, s)
	if err != nil {
		return run.ToolResult{}, err
	}
	mountDir, ok := mounts[skillName]
	if !ok {
		return run.ToolResult{}, run.ToolFailure("skill_not_bound", "none")
	}
	root, err := os.OpenRoot(mountDir)
	if err != nil {
		return run.ToolResult{}, err
	}
	defer root.Close()
	raw, err := f.readRoot(ctx, root, rel)
	if err != nil {
		return run.ToolResult{}, err
	}
	sha := hashBytes(raw)
	if in.ExpectedSHA256 != "" && in.ExpectedSHA256 != sha {
		return run.ToolResult{}, storage.ErrConflict
	}
	view, err := rangeData(raw, in.Range)
	if err != nil {
		return run.ToolResult{}, err
	}
	view["path"] = in.Path
	result := dataResult(view, "none")
	result.Complete = view["complete"].(bool)
	result.Truncated = !result.Complete
	return result, nil
}

func (f *FileService) Read(ctx context.Context, s run.Snapshot, in ReadFileInput) (run.ToolResult, error) {
	if skillName, rel, ok := splitSkillPath(in.Path); ok {
		return f.readSkillFile(ctx, s, in, skillName, rel)
	}
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
		view, err := rangeData(raw, in.Range)
		if err != nil {
			return err
		}
		view["path"] = in.Path
		result = dataResult(view, "none")
		result.Complete = view["complete"].(bool)
		result.Truncated = !result.Complete
		return nil
	})
	return result, err
}
func (f *FileService) Write(ctx context.Context, s run.Snapshot, in WriteFileInput) (result run.ToolResult, err error) {
	if _, _, isSkill := splitSkillPath(in.Path); isSkill {
		return result, run.ToolFailure("skill_read_only", "none")
	}
	if !filePath(in.Path) || !utf8.ValidString(in.Content) || int64(len(in.Content)) > f.MaxBytes || (in.MediaType != "" && !validMediaType(in.MediaType)) {
		return result, storage.ErrInvalid
	}
	err = f.Workspaces.With(ctx, s.SpaceID, s.SessionID, func(dir WorkspaceDir) error { return f.write(ctx, s, dir, in, &result) })
	return
}
func (f *FileService) write(ctx context.Context, s run.Snapshot, dir WorkspaceDir, in WriteFileInput, result *run.ToolResult) error {
	if err := f.Check(ctx, s); err != nil {
		return err
	}
	if in.ExpectedSHA256 != "" {
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
	} else {
		if err := checkParents(dir.Root(), in.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if info, err := dir.Root().Lstat(in.Path); err == nil && info.IsDir() {
			return storage.ErrWorkspace
		}
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
	*result = run.ToolResult{Status: "completed", Data: raw, SideEffect: "committed", Complete: true, SourceRefs: []storage.Ref{}}
	return nil
}

func detectArtifactMediaType(filePath string, file *os.File) string {
	if byExt := mime.TypeByExtension(strings.ToLower(path.Ext(filePath))); validMediaType(byExt) {
		return byExt
	}
	var head [512]byte
	n, _ := io.ReadFull(file, head[:])
	_, _ = file.Seek(0, io.SeekStart)
	if n > 0 {
		if detected := http.DetectContentType(head[:n]); validMediaType(detected) {
			return detected
		}
	}
	return "application/octet-stream"
}

func (f *FileService) Publish(ctx context.Context, s run.Snapshot, callID string, in PublishInput) (artifact run.Artifact, result run.ToolResult, err error) {
	displayName := in.DisplayName
	if displayName == "" && filePath(in.Path) {
		displayName = path.Base(in.Path)
	}
	if !filePath(in.Path) || in.ExpectedSHA256 == "missing" || len(displayName) == 0 || len(displayName) > 200 || strings.ContainsAny(displayName, "\r\n\x00/\\") || (in.MediaType != "" && !validMediaType(in.MediaType)) {
		return artifact, result, storage.ErrInvalid
	}
	err = f.Workspaces.With(ctx, s.SpaceID, s.SessionID, func(dir WorkspaceDir) error {
		if err := f.Check(ctx, s); err != nil {
			return err
		}
		file, _, err := openWorkspaceFile(dir, in.Path)
		if err != nil {
			return err
		}
		defer file.Close()
		mediaType := in.MediaType
		if mediaType == "" {
			mediaType = detectArtifactMediaType(in.Path, file)
		}
		id := uuid.NewString()
		o, err := f.Content.putAt(ctx, s.ActorID, s.SpaceID, id, "artifact", "application/octet-stream", f.Content.objectKey(s.SpaceID, id), file, in.ExpectedSHA256)
		if err != nil {
			return err
		}
		artifact = run.Artifact{ID: uuid.NewString(), SpaceID: s.SpaceID, SessionID: s.SessionID, RunID: s.ID, CallID: callID, ObjectID: o.ID, SHA256: o.SHA256, Name: displayName, MediaType: mediaType, Path: in.Path}
		artifact.URL = "/api/v1/spaces/" + s.SpaceID + "/artifacts/" + artifact.ID + "/download"
		data, _ := json.Marshal(artifact)
		result = run.ToolResult{Status: "completed", Data: data, SideEffect: "committed", Complete: true, SourceRefs: []storage.Ref{{Role: "artifact", ObjectID: o.ID, SHA256: o.SHA256}}}
		return nil
	})
	return
}

func hasMultipleLinks(info fs.FileInfo) bool {
	v := reflect.ValueOf(info.Sys())
	if v.Kind() == reflect.Pointer && !v.IsNil() {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return false
	}
	n := v.FieldByName("Nlink")
	return n.IsValid() && n.CanUint() && n.Uint() > 1 && !info.IsDir()
}
