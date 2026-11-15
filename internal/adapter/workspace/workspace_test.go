package workspace

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/resource"
)

func sha(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func archive(t *testing.T, headers []*tar.Header, contents []string) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for i, h := range headers {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if i < len(contents) {
			if _, err := tw.Write([]byte(contents[i])); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	gz.Close()
	return b.Bytes()
}
func TestRestoreRejectsUnsafeOrIncompleteArchives(t *testing.T) {
	limits := storage.Limits{MaxFileBytes: 16, MaxTotalBytes: 32, MaxEntries: 10}
	m, err := New(t.TempDir(), limits)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	valid := storage.Manifest{FormatVersion: 1, Entries: []storage.Entry{{Path: "x", Type: "file", Size: 1, SHA256: sha("a")}}}
	reg := func(name string) *tar.Header {
		return &tar.Header{Name: name, Typeflag: tar.TypeReg, Size: 1, Mode: 0777}
	}
	good := archive(t, []*tar.Header{reg("x")}, []string{"a"})
	tests := []struct {
		name     string
		data     []byte
		manifest storage.Manifest
	}{
		{"traversal", archive(t, []*tar.Header{reg("../x")}, []string{"a"}), valid},
		{"absolute", archive(t, []*tar.Header{reg("/x")}, []string{"a"}), valid},
		{"duplicate", archive(t, []*tar.Header{reg("x"), reg("x")}, []string{"a", "a"}), valid},
		{"symlink", archive(t, []*tar.Header{{Name: "x", Typeflag: tar.TypeSymlink, Linkname: "/tmp"}}, nil), valid},
		{"hardlink", archive(t, []*tar.Header{{Name: "x", Typeflag: tar.TypeLink, Linkname: "../outside"}}, nil), valid},
		{"device", archive(t, []*tar.Header{{Name: "x", Typeflag: tar.TypeChar}}, nil), valid},
		{"digest", archive(t, []*tar.Header{reg("x")}, []string{"b"}), valid},
		{"missing", archive(t, nil, nil), valid},
		{"truncated_gzip", good[:len(good)-5], valid},
		{"wrong_version", good, storage.Manifest{FormatVersion: 2}},
		{"too_large", good, storage.Manifest{FormatVersion: 1, Entries: []storage.Entry{{Path: "x", Type: "file", Size: 100, SHA256: sha("a")}}}},
		{"invalid_manifest_path", good, storage.Manifest{FormatVersion: 1, Entries: []storage.Entry{{Path: "../x", Type: "file", Size: 1, SHA256: sha("a")}}}},
		{"case_collision", good, storage.Manifest{FormatVersion: 1, Entries: []storage.Entry{{Path: "x", Type: "file", Size: 1, SHA256: sha("a")}, {Path: "X", Type: "file", Size: 1, SHA256: sha("a")}}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := m.With(context.Background(), storage.NewShortID(), storage.NewShortID(), func(dir resource.WorkspaceDir) error {
				if err := dir.Restore(context.Background(), bytes.NewReader(tc.data), tc.manifest); err == nil {
					t.Fatal("unsafe archive accepted")
				}
				entries, err := fs.ReadDir(dir.Root().FS(), ".")
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 0 {
					t.Fatal("partial restore exposed")
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("valid_restricts_permissions", func(t *testing.T) {
		if err := m.With(context.Background(), storage.NewShortID(), storage.NewShortID(), func(dir resource.WorkspaceDir) error {
			if err := dir.Restore(context.Background(), bytes.NewReader(good), valid); err != nil {
				return err
			}
			info, err := dir.Root().Stat("x")
			if err != nil {
				return err
			}
			if info.Mode().Perm() != 0600 {
				t.Fatal("archive permissions retained")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}
func TestPackBoundariesAndLockCancellation(t *testing.T) {
	base := t.TempDir()
	m, err := New(base, storage.Limits{MaxFileBytes: 16, MaxTotalBytes: 32, MaxEntries: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	space, session := storage.NewShortID(), storage.NewShortID()
	if err = m.With(context.Background(), space, session, func(dir resource.WorkspaceDir) error {
		if err := dir.Root().Symlink("/etc/passwd", "link"); err != nil {
			return err
		}
		if f, _, err := dir.Pack(context.Background()); err == nil {
			f.Close()
			t.Fatal("symlink packed")
		}
		dir.Root().Remove("link")
		f, err := dir.Root().OpenFile("large", os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		f.Write(bytes.Repeat([]byte("a"), 17))
		f.Close()
		if f, _, err := dir.Pack(context.Background()); err == nil {
			f.Close()
			t.Fatal("oversize packed")
		}
		dir.Root().Remove("large")
		dir.Root().Mkdir("empty", 0700)
		packed, manifest, err := dir.Pack(context.Background())
		if err != nil {
			return err
		}
		defer packed.Close()
		if len(manifest.Entries) != 1 || manifest.Entries[0].Type != "directory" {
			t.Fatal("empty directory not packed")
		}
		if _, err = io.ReadAll(packed); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		if err = m.With(ctx, space, session, func(resource.WorkspaceDir) error { t.Fatal("lock acquired while held"); return nil }); err != context.DeadlineExceeded {
			t.Fatal("lock wait ignored cancellation", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	linked := storage.NewShortID()
	os.Symlink(filepath.Join(base, space+"_"+session), filepath.Join(base, space+"_"+linked))
	if err = m.With(context.Background(), space, linked, func(resource.WorkspaceDir) error { return nil }); err == nil {
		t.Fatal("session symlink accepted")
	}
}
