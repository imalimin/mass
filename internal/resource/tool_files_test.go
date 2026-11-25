package resource_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
	skills := &resource.SkillCache{Root: filepath.Join(root, ".skill-cache")}
	f := &resource.FileService{Workspaces: w, Content: content, Skills: skills, MaxBytes: 1 << 20, Limits: limits, Check: func(context.Context, run.Snapshot) error { return nil }}
	s := run.Snapshot{ID: uuid.NewString(), ActorID: uuid.NewString(), SpaceID: storage.NewShortID(), SessionID: storage.NewShortID()}
	return f, s, filepath.Join(root, s.SpaceID+"_"+s.SessionID)
}
func putSkillPackage(t *testing.T, ctx context.Context, content *resource.ContentStore, s run.Snapshot, markdown string, files map[string]string) (storage.Object, []run.SkillResource) {
	t.Helper()
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	assets := make([]run.SkillResource, 0, len(files))
	for rel, body := range files {
		w, err := z.Create(rel)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
		assets = append(assets, run.SkillResource{Path: rel, SHA256: digest(body), Size: int64(len(body)), MediaType: "text/plain"})
	}
	w, err := z.Create(resource.SkillDocumentPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(markdown)); err != nil {
		t.Fatal(err)
	}
	manifest, err := json.Marshal(map[string]any{
		"document": map[string]any{"assets": assets},
	})
	if err != nil {
		t.Fatal(err)
	}
	w, err = z.Create(resource.SkillManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(manifest); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	obj, err := content.Put(ctx, s.ActorID, s.SpaceID, uuid.NewString(), resource.SkillPackageKind, "application/zip", bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	return obj, assets
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
	write := func(p, c string) {
		t.Helper()
		if _, err := f.Write(ctx, s, resource.WriteFileInput{Path: p, Content: c}); err != nil {
			t.Fatal(err)
		}
	}
	write("notes.txt", "draft")
	write("notes.txt", "first\r\nsecond\r\nfirst")
	if _, err := f.Edit(ctx, s, resource.EditInput{Path: "notes.txt", Edits: []resource.Edit{{Old: "first", New: "x"}}}); !errors.Is(err, storage.ErrConflict) {
		t.Fatal("ambiguous edit accepted", err)
	}
	write("ambiguous.txt", "aaa")
	if _, err := f.Edit(ctx, s, resource.EditInput{Path: "ambiguous.txt", Edits: []resource.Edit{{Old: "aa", New: "x"}}}); !errors.Is(err, storage.ErrConflict) {
		t.Fatal("overlapping occurrences treated as a unique match", err)
	}
	write("notes.txt", "abcdef\r\nend")
	if _, err := f.Edit(ctx, s, resource.EditInput{Path: "notes.txt", Edits: []resource.Edit{{Old: "abc", New: "x"}, {Old: "bc", New: "y"}}}); !errors.Is(err, storage.ErrConflict) {
		t.Fatal("overlapping edit accepted", err)
	}
	result, err := f.Edit(ctx, s, resource.EditInput{Path: "notes.txt", Edits: []resource.Edit{{Old: "abc", New: "ABC"}, {Old: "end", New: "END"}}})
	if err != nil || result.SideEffect != "committed" {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(root, "notes.txt"))
	if string(raw) != "ABCdef\r\nEND" {
		t.Fatal("newline or content changed", string(raw))
	}
	write("exists.txt", "kept")
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

	// Bound Skill unpacking, read_file via $MASS_SKILLS_DIR, read-only guards, and permission checks.
	skillDoc := "---\nname: rules\ndescription: Apply rules.\n---\nUse assets/rules.txt."
	skillObj, skillResources := putSkillPackage(t, ctx, f.Content, s, skillDoc, map[string]string{
		"assets/rules.txt": "rule=42",
		"scripts/run.sh":   "#!/bin/sh\necho ok\n",
	})
	s.Definition.Skills = []run.Skill{{
		ID:          uuid.NewString(),
		VersionID:   uuid.NewString(),
		Name:        "rules",
		Version:     "v1",
		Description: "Apply rules.",
		ObjectID:    skillObj.ID,
		SHA256:      skillObj.SHA256,
		Resources:   skillResources,
	}}
	docRes, err := f.Read(ctx, s, resource.ReadFileInput{Path: "$MASS_SKILLS_DIR/rules/SKILL.md"})
	if err != nil || toolData(t, docRes)["content"] != skillDoc {
		t.Fatalf("read_file SKILL.md failed: %v, %s", err, docRes.Data)
	}
	skillRes, err := f.Read(ctx, s, resource.ReadFileInput{Path: "${MASS_SKILLS_DIR}/rules/assets/rules.txt"})
	if err != nil || toolData(t, skillRes)["content"] != "rule=42" {
		t.Fatalf("read_file skill asset failed: %v, %s", err, skillRes.Data)
	}
	mounts, err := f.Skills.Mounts(ctx, f.Content, s)
	if err != nil || mounts["rules"] == "" {
		t.Fatalf("SkillCache.Mounts failed: %v, %+v", err, mounts)
	}
	if info, statErr := os.Stat(filepath.Join(mounts["rules"], "scripts", "run.sh")); statErr != nil || info.Mode()&0100 == 0 {
		t.Fatalf("expected script in skill cache to be executable: %v, %v", info, statErr)
	}
	if _, err = f.Read(ctx, s, resource.ReadFileInput{Path: "$MASS_SKILLS_DIR/unbound/SKILL.md"}); err == nil {
		t.Fatal("unbound skill read accepted")
	} else {
		var failure *run.ToolError
		if !errors.As(err, &failure) || failure.Code != "skill_not_bound" || failure.Effect != "none" {
			t.Fatalf("expected skill_not_bound, got %v", err)
		}
	}
	for _, mutErr := range []error{
		func() error {
			_, e := f.Write(ctx, s, resource.WriteFileInput{Path: "$MASS_SKILLS_DIR/rules/SKILL.md", Content: "tampered"})
			return e
		}(),
		func() error {
			_, e := f.Edit(ctx, s, resource.EditInput{Path: "$MASS_SKILLS_DIR/rules/SKILL.md", Edits: []resource.Edit{{Old: "rules", New: "bad"}}})
			return e
		}(),
	} {
		var failure *run.ToolError
		if !errors.As(mutErr, &failure) || failure.Code != "skill_read_only" || failure.Effect != "none" {
			t.Fatalf("expected skill_read_only, got %v", mutErr)
		}
	}

	mj := &memoryJournal{}
	j := &journal.Journal{Repo: mj}

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
	if _, err = f.Read(ctx, s, resource.ReadFileInput{Path: "$MASS_SKILLS_DIR/rules/assets/rules.txt"}); !errors.Is(err, run.ErrPermission) {
		t.Fatal("revoked skill read accepted", err)
	}
}
