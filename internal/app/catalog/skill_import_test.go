package catalog

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/resource"
)

func TestImportSkillIgnoresMacOSXEntries(t *testing.T) {
	ctx := context.Background()
	spaceID := storage.NewShortID()
	actorID := uuid.NewString()

	blobs := &publishAttemptBlobs{data: map[string][]byte{}}
	objRepo := &publishAttemptContentRepo{objects: map[string]storage.Object{}}
	repo := &publishAttemptRepo{drafts: map[string]Draft{}}
	svc := &Service{
		Repo: repo,
		Content: &resource.ContentStore{
			Repo:     objRepo,
			Blobs:    blobs,
			MaxBytes: 8 << 20,
		},
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	addDir := func(name string) {
		t.Helper()
		hdr := &zip.FileHeader{Name: name}
		hdr.SetMode(fs.ModeDir | 0o755)
		if _, err := zw.CreateHeader(hdr); err != nil {
			t.Fatal(err)
		}
	}
	addFile := func(name, content string, mode fs.FileMode) {
		t.Helper()
		hdr := &zip.FileHeader{Name: name}
		hdr.SetMode(mode)
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}

	addFile("SKILL.md", "---\nname: demo-skill\ndescription: demo\n---\n# Demo\n", 0o644)
	addDir("scripts/")
	addFile("scripts/run.py", "print('ok')\n", 0o644)
	addFile("__MACOSX_notes/readme.txt", "keep me\n", 0o644)

	// macOS Finder archive metadata that must be filtered out.
	addDir("__MACOSX/")
	addFile("__MACOSX/._SKILL.md", "appledouble", 0o644)
	addDir("./__MACOSX/scripts/")
	addFile("./__MACOSX/scripts/._run.py", "appledouble", 0o644)
	addDir("scripts/__MACOSX/")
	addFile("scripts/__MACOSX/._nested", "appledouble", 0o644)
	addFile("__MACOSX/._link", "target", fs.ModeSymlink|0o777)

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	draft, err := svc.ImportSkill(ctx, actorID, spaceID, "demo-skill.zip", bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("ImportSkill failed: %v", err)
	}

	gotAssets := make([]string, 0, len(draft.Document.Assets))
	for _, a := range draft.Document.Assets {
		gotAssets = append(gotAssets, a.Path)
	}
	slices.Sort(gotAssets)
	wantAssets := []string{"__MACOSX_notes/readme.txt", "scripts/run.py"}
	if !slices.Equal(gotAssets, wantAssets) {
		t.Fatalf("assets = %v, want %v", gotAssets, wantAssets)
	}

	if !slices.Equal(draft.Document.Directories, []string{"scripts"}) {
		t.Fatalf("directories = %v, want [scripts]", draft.Document.Directories)
	}

	obj, ok := objRepo.objects[draft.ContentID]
	if !ok {
		t.Fatalf("stored package object %q not found", draft.ContentID)
	}
	pkgBytes := blobs.data[obj.Key]
	zr, err := zip.NewReader(bytes.NewReader(pkgBytes), int64(len(pkgBytes)))
	if err != nil {
		t.Fatalf("open stored package: %v", err)
	}
	for _, entry := range zr.File {
		if strings.Contains(entry.Name, "__MACOSX/") || entry.Name == "__MACOSX" {
			t.Fatalf("stored package unexpectedly contains macOS metadata entry %q", entry.Name)
		}
	}
}

func TestImportSkillIgnoresMacOSXSkillDocument(t *testing.T) {
	ctx := context.Background()
	svc := &Service{
		Repo: &publishAttemptRepo{drafts: map[string]Draft{}},
		Content: &resource.ContentStore{
			Repo:     &publishAttemptContentRepo{objects: map[string]storage.Object{}},
			Blobs:    &publishAttemptBlobs{data: map[string][]byte{}},
			MaxBytes: 8 << 20,
		},
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("__MACOSX/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("# fake")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = svc.ImportSkill(ctx, uuid.NewString(), storage.NewShortID(), "bad.zip", bytes.NewReader(buf.Bytes()))
	if !errors.Is(err, ErrSkillDocumentMissing) {
		t.Fatalf("err = %v, want ErrSkillDocumentMissing", err)
	}
}
