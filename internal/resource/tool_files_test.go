package resource_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/adapter/workspace"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/resource"
	"github.com/imalimin/mass/tests/fakes"
	"os"
	"path/filepath"
	"testing"
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
	limits := storage.Limits{MaxFileBytes: 1 << 20, MaxTotalBytes: 8 << 20, MaxEntries: 100}
	w, err := workspace.New(root, limits)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	content := &resource.ContentStore{Repo: objects{}, Blobs: fakes.NewBlobs(), Prefix: "test/files/", MaxBytes: 8 << 20}
	f := &resource.FileService{Workspaces: w, Content: content, Cursors: signer{}, MaxBytes: 1 << 20, Limits: limits, Check: func(context.Context, run.Snapshot) error { return nil }}
	s := run.Snapshot{ID: uuid.NewString(), ActorID: uuid.NewString(), SpaceID: uuid.NewString(), SessionID: uuid.NewString()}
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
	wrong.SessionID = uuid.NewString()
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
	f.Limits.MaxEntries = 2
	limited, err := f.Query(ctx, s, "list_files", resource.PageInput{})
	if err != nil || limited.Complete {
		t.Fatal("scan limit hidden", err)
	}
	f.Check = func(context.Context, run.Snapshot) error { return run.ErrPermission }
	if _, err = f.Query(ctx, s, "glob_files", resource.PageInput{Pattern: "**", Cursor: cursor}); !errors.Is(err, run.ErrPermission) {
		t.Fatal("revoked cursor read", err)
	}
}
