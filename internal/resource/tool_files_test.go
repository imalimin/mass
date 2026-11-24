package resource_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/adapter/workspace"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
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

type memoryJournal struct {
	events []journal.Event
}

func (m *memoryJournal) AppendEvents(_ context.Context, _ string, batch journal.Batch) ([]journal.Event, error) {
	out := make([]journal.Event, len(batch.Events))
	for i, e := range batch.Events {
		ev := journal.Event{Proposed: e, SpaceID: batch.SpaceID, SessionID: batch.SessionID, Sequence: int64(len(m.events) + 1)}
		m.events = append(m.events, ev)
		out[i] = ev
	}
	return out, nil
}
func (m *memoryJournal) ReadEvents(_ context.Context, _, space, session string, after int64, through *int64, limit int) (journal.Page, error) {
	items := []journal.Event{}
	var maxSeq int64
	for _, ev := range m.events {
		if ev.SpaceID != space || ev.SessionID != session {
			continue
		}
		if through != nil && ev.Sequence > *through {
			continue
		}
		maxSeq = ev.Sequence
		if ev.Sequence <= after {
			continue
		}
		items = append(items, ev)
		if limit > 0 && len(items) == limit {
			return journal.Page{Events: items, Through: int64(len(m.events)), Next: ev.Sequence, HasMore: true}, nil
		}
	}
	return journal.Page{Events: items, Through: maxSeq, Next: maxSeq}, nil
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
	f := &resource.FileService{Workspaces: w, Content: content, MaxBytes: 1 << 20, Limits: limits, Check: func(context.Context, run.Snapshot) error { return nil }}
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

func TestPublishArtifactDefaultsAndOptionalDigest(t *testing.T) {
	ctx := context.Background()
	f, s, root := fileFixture(t)
	f.Content.MaxBytes = 4
	f.ReadBudget = func(int64) error { return run.ErrOverflow }
	const contents = `{"ok":true,"note":"artifact larger than the content limit"}`
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "export.json"), []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	stale := resource.PublishInput{Path: "export.json", ExpectedSHA256: digest("another version")}
	if _, _, err := f.Publish(ctx, s, uuid.NewString(), stale); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("expected stale digest to be rejected, got %v", err)
	}
	if len(f.Content.Repo.(objects)) != 0 || len(f.Content.Blobs.(*fakes.Blobs).Data) != 0 {
		t.Fatal("digest mismatch reached object storage")
	}
	// Default display_name, media_type, and omitted expected_sha256.
	artifact, result, err := f.Publish(ctx, s, uuid.NewString(), resource.PublishInput{Path: "export.json"})
	if err != nil || result.Status != "completed" {
		t.Fatalf("publish with defaults failed: %v, %+v", err, result)
	}
	if artifact.Name != "export.json" || !strings.HasPrefix(artifact.MediaType, "application/json") || artifact.SHA256 != digest(contents) {
		t.Fatalf("unexpected artifact metadata: %+v", artifact)
	}
	object, file, err := f.Content.Read(ctx, s.ActorID, s.SpaceID, artifact.ObjectID)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	raw, err := io.ReadAll(file)
	if err != nil || string(raw) != contents || object.SHA256 != digest(contents) {
		t.Fatalf("published bytes or digest differ: %v", err)
	}
}

func TestFileEditingAndMutationBoundaries(t *testing.T) {
	f, s, root := fileFixture(t)
	ctx := context.Background()
	write := func(p, c, sha string) {
		t.Helper()
		if _, err := f.Write(ctx, s, resource.WriteFileInput{Path: p, Content: c, ExpectedSHA256: sha}); err != nil {
			t.Fatal(err)
		}
	}
	// Write without expected_sha256 creates and overwrites cleanly.
	write("notes.txt", "draft", "")
	write("notes.txt", "first\r\nsecond\r\nfirst", "")
	if _, err := f.Edit(ctx, s, resource.EditInput{Path: "notes.txt", Edits: []resource.Edit{{Old: "first", New: "x"}}}); !errors.Is(err, storage.ErrConflict) {
		t.Fatal("ambiguous edit accepted", err)
	}
	write("ambiguous.txt", "aaa", "missing")
	if _, err := f.Edit(ctx, s, resource.EditInput{Path: "ambiguous.txt", Edits: []resource.Edit{{Old: "aa", New: "x"}}}); !errors.Is(err, storage.ErrConflict) {
		t.Fatal("overlapping occurrences treated as a unique match", err)
	}
	write("notes.txt", "abcdef\r\nend", digest("first\r\nsecond\r\nfirst"))
	if _, err := f.Edit(ctx, s, resource.EditInput{Path: "notes.txt", Edits: []resource.Edit{{Old: "abc", New: "x"}, {Old: "bc", New: "y"}}}); !errors.Is(err, storage.ErrConflict) {
		t.Fatal("overlapping edit accepted", err)
	}
	if _, err := f.Edit(ctx, s, resource.EditInput{Path: "notes.txt", ExpectedSHA256: digest("stale"), Edits: []resource.Edit{{Old: "abc", New: "ABC"}}}); !errors.Is(err, storage.ErrConflict) {
		t.Fatal("stale expected_sha256 accepted", err)
	}
	result, err := f.Edit(ctx, s, resource.EditInput{Path: "notes.txt", Edits: []resource.Edit{{Old: "abc", New: "ABC"}, {Old: "end", New: "END"}}})
	if err != nil || result.SideEffect != "committed" {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(root, "notes.txt"))
	if string(raw) != "ABCdef\r\nEND" {
		t.Fatal("newline or content changed", string(raw))
	}
	write("exists.txt", "kept", "missing")
	if err = os.Link(filepath.Join(root, "exists.txt"), filepath.Join(root, "hard.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Read(ctx, s, resource.ReadFileInput{Path: "hard.txt"}); err == nil {
		t.Fatal("hard link read accepted")
	}
}

func TestBoundedReadAndSkillSources(t *testing.T) {
	f, s, root := fileFixture(t)
	ctx := context.Background()
	if _, err := f.Write(ctx, s, resource.WriteFileInput{Path: "source/a.txt", Content: "alpha\nbeta\n"}); err != nil {
		t.Fatal(err)
	}
	start, end := 2, 2
	result, err := f.Read(ctx, s, resource.ReadFileInput{Path: "source/a.txt", Range: &run.Range{StartLine: &start, EndLine: &end}})
	if err != nil || toolData(t, result)["content"] != "beta\n" || result.Complete {
		t.Fatal("line range", err, string(result.Data))
	}

	// Symlink traversal remains blocked even when expected_sha256 is omitted.
	if err := os.Symlink("source/a.txt", filepath.Join(root, "alias.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("source", filepath.Join(root, "configs")); err != nil {
		t.Fatal(err)
	}
	for _, opErr := range []error{
		func() error { _, e := f.Read(ctx, s, resource.ReadFileInput{Path: "configs/a.txt"}); return e }(),
		func() error {
			_, e := f.Write(ctx, s, resource.WriteFileInput{Path: "alias.txt", Content: "x"})
			return e
		}(),
	} {
		var failure *run.ToolError
		if !errors.As(opErr, &failure) || failure.Code != "workspace_symlink" || failure.Effect != "none" {
			t.Fatalf("expected workspace_symlink, got %v", opErr)
		}
	}

	// Bound Skill resource reading and permission checks.
	skillObj, err := f.Content.Put(ctx, s.ActorID, s.SpaceID, uuid.NewString(), "test", "text/plain", bytes.NewReader([]byte("rule=42")))
	if err != nil {
		t.Fatal(err)
	}
	skillVersionID := uuid.NewString()
	s.Definition.Skills = []run.Skill{{
		ID:        uuid.NewString(),
		VersionID: skillVersionID,
		Version:   "v1",
		SHA256:    skillObj.SHA256,
		Resources: []run.SkillResource{{Path: "assets/rules.txt", ObjectID: skillObj.ID, SHA256: skillObj.SHA256, Size: skillObj.Size, MediaType: "text/plain"}},
	}}
	mj := &memoryJournal{}
	j := &journal.Journal{Repo: mj}
	sources := &resource.Sources{Files: f, Journal: j}
	skillRes, err := sources.ReadSkill(ctx, s, resource.SourceInput{Binding: skillVersionID, Path: "assets/rules.txt"})
	if err != nil || toolData(t, skillRes)["content"] != "rule=42" {
		t.Fatalf("ReadSkill failed: %v, %s", err, skillRes.Data)
	}

	// Plan updates auto-increment revision, auto-assign step IDs, and allow title edits.
	p1, err := resource.PreparePlan(ctx, j, s, []run.PlanStep{{Title: "Draft summary", Status: "in_progress"}})
	if err != nil || p1.Plan == nil || p1.Plan.Revision != 1 || p1.Plan.Steps[0].ID != "step-1" {
		t.Fatalf("first PreparePlan failed: %v, %+v", err, p1.Plan)
	}
	ev := journal.New(s.ActorID, "plan.updated", s.ID, *p1.Plan)
	ev.OperationID = "call-1"
	if _, err := resource.AppendRun(ctx, j, s, ev); err != nil {
		t.Fatal(err)
	}
	p2, err := resource.PreparePlan(ctx, j, s, []run.PlanStep{{ID: "step-1", Title: "Revised summary title", Status: "completed"}})
	if err != nil || p2.Plan == nil || p2.Plan.Revision != 2 || p2.Plan.Steps[0].Title != "Revised summary title" {
		t.Fatalf("second PreparePlan failed: %v, %+v", err, p2.Plan)
	}

	f.Check = func(context.Context, run.Snapshot) error { return run.ErrPermission }
	if _, err = sources.ReadSkill(ctx, s, resource.SourceInput{Binding: skillVersionID, Path: "assets/rules.txt"}); !errors.Is(err, run.ErrPermission) {
		t.Fatal("revoked skill read accepted", err)
	}
}
