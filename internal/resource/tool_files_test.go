package resource_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/adapter/javascript"
	"github.com/imalimin/mass/internal/adapter/workspace"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/resource"
	"github.com/imalimin/mass/tests/fakes"
)

type objects map[string]storage.Object

func (o objects) BeginObject(_ context.Context, _ string, v storage.Object) (storage.Object, error) {
	o[v.ID] = v
	return v, nil
}
func (o objects) ReadyObject(_ context.Context, _ string, v storage.Object) error {
	v.State = "ready"
	o[v.ID] = v
	return nil
}
func (o objects) Object(_ context.Context, _, space, id string, _ bool) (storage.Object, error) {
	v, ok := o[id]
	if !ok || v.SpaceID != space {
		return v, storage.ErrIntegrity
	}
	return v, nil
}

type signer struct{}

func (signer) SealPage(scope, payload string) string {
	h := hmac.New(sha256.New, []byte("test-key"))
	h.Write([]byte(scope + payload))
	return base64.RawURLEncoding.EncodeToString(append([]byte(payload), h.Sum(nil)...))
}
func (signer) OpenPage(scope, token string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) < 32 {
		return "", run.ErrCursor
	}
	text := string(raw[:len(raw)-32])
	if (signer{}).SealPage(scope, text) != token {
		return "", run.ErrCursor
	}
	return text, nil
}
func fileFixture(t *testing.T) (*resource.FileService, run.Snapshot, string) {
	t.Helper()
	root := t.TempDir()
	limits := storage.Limits{MaxArchiveFileBytes: 1 << 20, MaxTotalBytes: 8 << 20}
	w, err := workspace.New(root, limits)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	content := &resource.ContentStore{Repo: objects{}, Blobs: fakes.NewBlobs(), Prefix: "test/files/", MaxBytes: 8 << 20}
	f := &resource.FileService{Workspaces: w, Content: content, Cursors: signer{}, MaxBytes: 1 << 20, Limits: limits, Check: func(context.Context, run.Snapshot) error { return nil }}
	s := run.Snapshot{ID: uuid.NewString(), ActorID: uuid.NewString(), SpaceID: storage.NewShortID(), SessionID: storage.NewShortID()}
	return f, s, filepath.Join(root, s.SpaceID+"_"+s.SessionID)
}
func toolData(t *testing.T, r run.ToolResult) map[string]any {
	t.Helper()
	var data map[string]any
	if err := json.Unmarshal(r.Data, &data); err != nil {
		t.Fatal(err)
	}
	return data
}
func digest(v string) string { sum := sha256.Sum256([]byte(v)); return hex.EncodeToString(sum[:]) }

func TestPublishArtifactUsesExpectedDigestWithoutObjectLimit(t *testing.T) {
	ctx := context.Background()
	f, s, root := fileFixture(t)
	f.Content.MaxBytes = 4
	f.ReadBudget = func(int64) error { return run.ErrOverflow }
	const contents = "artifact larger than the content limit"
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "export.txt"), []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	input := resource.PublishInput{Path: "export.txt", DisplayName: "export.txt", MediaType: "text/plain", ExpectedSHA256: digest("another version")}
	if _, _, err := f.Publish(ctx, s, uuid.NewString(), input); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("expected stale digest to be rejected, got %v", err)
	}
	if len(f.Content.Repo.(objects)) != 0 || len(f.Content.Blobs.(*fakes.Blobs).Data) != 0 {
		t.Fatal("digest mismatch reached object storage")
	}
	input.ExpectedSHA256 = digest(contents)
	artifact, result, err := f.Publish(ctx, s, uuid.NewString(), input)
	if err != nil || result.Status != "completed" {
		t.Fatalf("publish failed: %v, %+v", err, result)
	}
	object, file, err := f.Content.Read(ctx, s.ActorID, s.SpaceID, artifact.ObjectID)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	raw, err := io.ReadAll(file)
	if err != nil || string(raw) != contents || object.SHA256 != input.ExpectedSHA256 {
		t.Fatalf("published bytes or digest differ: %v", err)
	}
}

func TestMkdirReportsConfirmedEffectsWhenInterrupted(t *testing.T) {
	f, s, root := fileFixture(t)
	f.Check = func(context.Context, run.Snapshot) error {
		if _, err := os.Stat(filepath.Join(root, "parent")); err == nil {
			return context.DeadlineExceeded
		}
		return nil
	}
	result, err := f.Mkdir(context.Background(), s, "parent/child")
	if !errors.Is(err, context.DeadlineExceeded) || result.SideEffect != "committed" {
		t.Fatalf("expected committed parent before timeout, got effect=%q err=%v", result.SideEffect, err)
	}
	if _, err := os.Stat(filepath.Join(root, "parent", "child")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("interrupted child should be absent: %v", err)
	}
	result, err = f.Mkdir(context.Background(), s, "another")
	if !errors.Is(err, context.DeadlineExceeded) || result.SideEffect != "none" {
		t.Fatalf("expected no effects before first write, got effect=%q err=%v", result.SideEffect, err)
	}
}

func TestFileEditingAndMutationBoundaries(t *testing.T) {
	f, s, root := fileFixture(t)
	ctx := context.Background()
	write := func(p, c, sha string) {
		t.Helper()
		if _, err := f.Write(ctx, s, resource.WriteFileInput{Path: p, Content: c, MediaType: "text/plain", ExpectedSHA256: sha}); err != nil {
			t.Fatal(err)
		}
	}
	write("notes.txt", "first\r\nsecond\r\nfirst", "missing")
	if _, err := f.Edit(ctx, s, resource.EditInput{Path: "notes.txt", ExpectedSHA256: digest("first\r\nsecond\r\nfirst"), Edits: []resource.Edit{{Old: "first", New: "x"}}}); !errors.Is(err, storage.ErrConflict) {
		t.Fatal("ambiguous edit accepted", err)
	}
	write("ambiguous.txt", "aaa", "missing")
	if _, err := f.Edit(ctx, s, resource.EditInput{Path: "ambiguous.txt", ExpectedSHA256: digest("aaa"), Edits: []resource.Edit{{Old: "aa", New: "x"}}}); !errors.Is(err, storage.ErrConflict) {
		t.Fatal("overlapping occurrences treated as a unique match", err)
	}
	write("notes.txt", "abcdef\r\nend", digest("first\r\nsecond\r\nfirst"))
	if _, err := f.Edit(ctx, s, resource.EditInput{Path: "notes.txt", ExpectedSHA256: digest("abcdef\r\nend"), Edits: []resource.Edit{{Old: "abc", New: "x"}, {Old: "bc", New: "y"}}}); !errors.Is(err, storage.ErrConflict) {
		t.Fatal("overlapping edit accepted", err)
	}
	result, err := f.Edit(ctx, s, resource.EditInput{Path: "notes.txt", ExpectedSHA256: digest("abcdef\r\nend"), Edits: []resource.Edit{{Old: "abc", New: "ABC"}, {Old: "end", New: "END"}}})
	if err != nil || result.SideEffect != "committed" {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(root, "notes.txt"))
	if string(raw) != "ABCdef\r\nEND" {
		t.Fatal("newline or content changed", string(raw))
	}
	write("exists.txt", "kept", "missing")
	if _, err = f.Move(ctx, s, resource.MoveInput{Source: "notes.txt", Target: "exists.txt", ExpectedSHA256: digest(string(raw))}); !errors.Is(err, storage.ErrConflict) {
		t.Fatal("overwrote target", err)
	}
	if _, err = f.Move(ctx, s, resource.MoveInput{Source: "notes.txt", Target: "moved.txt", ExpectedSHA256: digest(string(raw))}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Delete(ctx, s, resource.ReadFileInput{Path: "moved.txt", ExpectedSHA256: digest("stale")}); !errors.Is(err, storage.ErrConflict) {
		t.Fatal("stale delete", err)
	}
	if _, err = f.Delete(ctx, s, resource.ReadFileInput{Path: "moved.txt", ExpectedSHA256: digest(string(raw))}); err != nil {
		t.Fatal(err)
	}
	if err = os.Link(filepath.Join(root, "exists.txt"), filepath.Join(root, "hard.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Read(ctx, s, resource.ReadFileInput{Path: "hard.txt"}); err == nil {
		t.Fatal("hard link read accepted")
	}
	if _, err = f.Delete(ctx, s, resource.ReadFileInput{Path: ".", ExpectedSHA256: digest("")}); err == nil {
		t.Fatal("directory delete accepted")
	}
}
func TestBoundedReadAndStableQueryPages(t *testing.T) {
	f, s, _ := fileFixture(t)
	f.EnableRunPagination()
	ctx := context.Background()
	for p, c := range map[string]string{"a.txt": "alpha\nbeta\n", "b.txt": "alpha two", "nested/c.csv": "x,y\n1,2\n"} {
		if _, err := f.Write(ctx, s, resource.WriteFileInput{Path: p, Content: c, MediaType: "text/plain", ExpectedSHA256: "missing"}); err != nil {
			t.Fatal(err)
		}
	}
	start, end := 2, 2
	result, err := f.Read(ctx, s, resource.ReadFileInput{Path: "a.txt", Range: &run.Range{StartLine: &start, EndLine: &end}})
	if err != nil || toolData(t, result)["content"] != "beta\n" || result.Complete {
		t.Fatal("line range", err, string(result.Data))
	}
	first, err := f.Query(ctx, s, "glob_files", resource.PageInput{Pattern: "**/*.txt", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	data := toolData(t, first)
	cursor := data["next_cursor"].(string)
	if _, err = f.Write(ctx, s, resource.WriteFileInput{Path: "c.txt", Content: "later", MediaType: "text/plain", ExpectedSHA256: "missing"}); err != nil {
		t.Fatal(err)
	}
	second, err := f.Query(ctx, s, "glob_files", resource.PageInput{Pattern: "**/*.txt", Limit: 1, Cursor: cursor})
	if err != nil {
		t.Fatal(err)
	}
	items := toolData(t, second)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["path"] != "b.txt" || toolData(t, second)["next_cursor"] != nil {
		t.Fatal("pagination changed after write", string(second.Data))
	}
	wrong := s
	wrong.SessionID = storage.NewShortID()
	if _, err = f.Query(ctx, wrong, "glob_files", resource.PageInput{Pattern: "**/*.txt", Cursor: cursor}); err == nil {
		t.Fatal("cursor crossed session")
	}
	if _, err = f.Query(ctx, s, "glob_files", resource.PageInput{Pattern: "**/*.csv", Cursor: cursor}); err == nil {
		t.Fatal("cursor crossed query")
	}
	matches, err := f.Query(ctx, s, "search_files", resource.PageInput{Pattern: "^alpha", Mode: "regex", PathGlob: "*.txt"})
	if err != nil || len(toolData(t, matches)["items"].([]any)) != 2 {
		t.Fatal(err, string(matches.Data))
	}
	f.Check = func(context.Context, run.Snapshot) error { return run.ErrPermission }
	if _, err = f.Query(ctx, s, "glob_files", resource.PageInput{Pattern: "**", Cursor: cursor}); !errors.Is(err, run.ErrPermission) {
		t.Fatal("revoked cursor read", err)
	}
}

func TestFileToolsReportSymlinksWithoutFollowing(t *testing.T) {
	ctx := context.Background()
	f, s, root := fileFixture(t)
	f.EnableRunPagination()
	const content = "searchable text"
	const original = "source/notes.txt"
	if _, err := f.Write(ctx, s, resource.WriteFileInput{Path: original, Content: content, MediaType: "text/plain", ExpectedSHA256: "missing"}); err != nil {
		t.Fatal(err)
	}
	links := []string{"alias.txt", "broken.txt", "configs"}
	for i, target := range []string{original, "missing.txt", "source"} {
		if err := os.Symlink(target, filepath.Join(root, links[i])); err != nil {
			t.Fatal(err)
		}
	}
	checkItems := func(data map[string]any, want []string) {
		t.Helper()
		paths := []string{}
		for _, raw := range data["items"].([]any) {
			item := raw.(map[string]any)
			name := item["path"].(string)
			paths = append(paths, name)
			if slices.Contains(links, name) && (item["type"] != "symlink" || len(item) != 2) {
				t.Fatalf("link should contain only path and type: %v", item)
			}
		}
		if !slices.Equal(paths, want) {
			t.Fatalf("paths = %v, want %v", paths, want)
		}
	}
	query := func(kind string, input resource.PageInput) map[string]any {
		t.Helper()
		result, err := f.Query(ctx, s, kind, input)
		if err != nil || result.Status != "completed" || result.SideEffect != "none" || !result.Complete {
			t.Fatalf("%s: result=%+v err=%v", kind, result, err)
		}
		return toolData(t, result)
	}
	checkItems(query("list_files", resource.PageInput{}), []string{"alias.txt", "broken.txt", "configs", "source"})
	first := query("glob_files", resource.PageInput{Pattern: "**", Limit: 2})
	checkItems(first, links[:2])
	checkItems(query("glob_files", resource.PageInput{Pattern: "**", Limit: 2, Cursor: first["next_cursor"].(string)}), []string{"configs", original})
	checkItems(query("glob_files", resource.PageInput{Pattern: "*.txt"}), links[:2])
	checkItems(query("search_files", resource.PageInput{Pattern: content}), []string{original})

	assertRejected := func(err error) {
		t.Helper()
		var failure *run.ToolError
		if !errors.As(err, &failure) || failure.Code != "workspace_symlink" || failure.Effect != "none" {
			t.Fatalf("expected a recoverable link error, got %v", err)
		}
	}
	_, err := f.Query(ctx, s, "list_files", resource.PageInput{Directory: "configs"})
	assertRejected(err)
	_, err = f.Read(ctx, s, resource.ReadFileInput{Path: "configs/notes.txt"})
	assertRejected(err)
	_, err = f.Write(ctx, s, resource.WriteFileInput{Path: "alias.txt", Content: "changed", MediaType: "text/plain", ExpectedSHA256: digest(content)})
	assertRejected(err)
	_, err = f.Delete(ctx, s, resource.ReadFileInput{Path: "alias.txt", ExpectedSHA256: digest(content)})
	assertRejected(err)
	if raw, err := os.ReadFile(filepath.Join(root, original)); err != nil || string(raw) != content {
		t.Fatalf("link target changed: %q (%v)", raw, err)
	}
	if target, err := os.Readlink(filepath.Join(root, "alias.txt")); err != nil || target != original {
		t.Fatalf("link changed: %q (%v)", target, err)
	}

	s.Definition.ToolPolicy = run.ToolPolicy{TimeoutMS: 5000, MaxCodeBytes: 1024, MaxInputBytes: 1024, MaxOutputBytes: 4096, MaxFileOperations: 1}
	code := &resource.CodeService{Files: f, Engine: &javascript.Runner{MaxConcurrent: 1}}
	result, err := code.Run(ctx, s, "call-list", run.CodeInput{Language: "javascript", Code: "return fs.list();", Input: json.RawMessage("null")})
	if err != nil || result.Status != "completed" || result.SideEffect != "none" {
		t.Fatalf("fs.list: result=%+v err=%v", result, err)
	}
	checkItems(toolData(t, result)["value"].(map[string]any), []string{"alias.txt", "broken.txt", "configs", "source"})
}
