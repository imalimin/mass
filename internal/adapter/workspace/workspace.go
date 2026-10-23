// Package workspace manages temporary session directories and bounded archives.
// It is an application boundary, not an OS sandbox against the host's owner.
package workspace

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/resource"
)

type gate struct {
	token chan struct{}
	refs  int
}
type Manager struct {
	root   *os.Root
	limits storage.Limits
	mu     sync.Mutex
	gates  map[string]*gate
}
type directory struct {
	manager *Manager
	name    string
	root    *os.Root
}
type archiveFile struct{ *os.File }

func (f *archiveFile) Close() error {
	name := f.Name()
	err := f.File.Close()
	return errors.Join(err, os.Remove(name))
}
func New(base string, limits storage.Limits) (*Manager, error) {
	if !filepath.IsAbs(base) || limits.MaxFileBytes < 1 || limits.MaxTotalBytes < limits.MaxFileBytes || limits.MaxEntries < 1 {
		return nil, storage.ErrInvalid
	}
	if err := os.MkdirAll(base, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(base)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, storage.ErrWorkspace
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	return &Manager{root: root, limits: limits, gates: map[string]*gate{}}, nil
}
func (m *Manager) Close() error { return m.root.Close() }
func (m *Manager) With(ctx context.Context, space, session string, fn func(resource.WorkspaceDir) error) error {
	if !storage.ValidID(space) || !storage.ValidID(session) {
		return storage.ErrInvalid
	}
	name := space + "_" + session
	m.mu.Lock()
	g := m.gates[name]
	if g == nil {
		g = &gate{token: make(chan struct{}, 1)}
		g.token <- struct{}{}
		m.gates[name] = g
	}
	g.refs++
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		g.refs--
		if g.refs == 0 {
			delete(m.gates, name)
		}
		m.mu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-g.token:
	}
	defer func() { g.token <- struct{}{} }()
	if err := m.root.Mkdir(name, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	info, err := m.root.Lstat(name)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return storage.ErrWorkspace
	}
	root, err := m.root.OpenRoot(name)
	if err != nil {
		return err
	}
	dir := &directory{m, name, root}
	defer func() {
		if dir.root != nil {
			dir.root.Close()
		}
	}()
	return fn(dir)
}
func (d *directory) Root() *os.Root { return d.root }
func validPath(p string) bool {
	return p != "" && len(p) <= 1024 && utf8.ValidString(p) && fs.ValidPath(p) && p != "." && path.Clean(p) == p && !strings.ContainsAny(p, "\\:\x00") && !strings.HasPrefix(p, "/")
}
func (d *directory) Pack(ctx context.Context) (io.ReadCloser, storage.Manifest, error) {
	manifest := storage.Manifest{FormatVersion: 1, Entries: []storage.Entry{}}
	file, err := os.CreateTemp("", "mass-archive-*")
	if err != nil {
		return nil, manifest, err
	}
	out := &archiveFile{file}
	ok := false
	defer func() {
		if !ok {
			out.Close()
		}
	}()
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	var total int64
	seenNames := map[string]bool{}
	err = fs.WalkDir(d.root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		if seenNames[strings.ToLower(name)] {
			return storage.ErrWorkspace
		}
		seenNames[strings.ToLower(name)] = true
		if !validPath(name) || len(manifest.Entries) >= d.manager.limits.MaxEntries {
			return storage.ErrWorkspace
		}
		info, err := d.root.Lstat(name)
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return storage.ErrWorkspace
		}
		record := storage.Entry{Path: name, Type: "directory"}
		header := &tar.Header{Name: name, Mode: 0700, Typeflag: tar.TypeDir, Format: tar.FormatPAX}
		if info.Mode().IsRegular() {
			if info.Size() > d.manager.limits.MaxFileBytes || info.Size() > d.manager.limits.MaxTotalBytes-total {
				return storage.ErrWorkspace
			}
			record.Type = "file"
			record.Size = info.Size()
			header.Typeflag = tar.TypeReg
			header.Mode = 0600
			header.Size = info.Size()
		}
		if err = tw.WriteHeader(header); err != nil {
			return err
		}
		if record.Type == "file" {
			f, err := d.root.Open(name)
			if err != nil {
				return err
			}
			defer f.Close()
			opened, err := f.Stat()
			if err != nil || !os.SameFile(info, opened) {
				return storage.ErrWorkspace
			}
			hash := sha256.New()
			n, err := io.Copy(io.MultiWriter(tw, hash), io.LimitReader(storage.ContextReader{Context: ctx, Reader: f}, info.Size()+1))
			if err != nil || n != info.Size() {
				return storage.ErrWorkspace
			}
			after, err := d.root.Lstat(name)
			if err != nil || !after.Mode().IsRegular() || !os.SameFile(info, after) || after.Size() != info.Size() || after.ModTime() != info.ModTime() {
				return storage.ErrWorkspace
			}
			record.SHA256 = hex.EncodeToString(hash.Sum(nil))
			total += n
		}
		manifest.Entries = append(manifest.Entries, record)
		return nil
	})
	if err == nil {
		err = tw.Close()
	}
	if err == nil {
		err = gz.Close()
	}
	if err != nil {
		return nil, manifest, err
	}
	if _, err = file.Seek(0, 0); err != nil {
		return nil, manifest, err
	}
	ok = true
	return out, manifest, nil
}
func (d *directory) Restore(ctx context.Context, source io.Reader, manifest storage.Manifest) error {
	if manifest.FormatVersion != 1 || len(manifest.Entries) > d.manager.limits.MaxEntries {
		return storage.ErrWorkspace
	}
	expected := map[string]storage.Entry{}
	folded := map[string]bool{}
	var total int64
	for _, entry := range manifest.Entries {
		lower := strings.ToLower(entry.Path)
		if !validPath(entry.Path) || folded[lower] || entry.Size < 0 || entry.Size > d.manager.limits.MaxFileBytes || entry.Size > d.manager.limits.MaxTotalBytes-total {
			return storage.ErrWorkspace
		}
		if entry.Type != "file" && entry.Type != "directory" {
			return storage.ErrWorkspace
		}
		if entry.Type == "directory" && (entry.Size != 0 || entry.SHA256 != "") {
			return storage.ErrWorkspace
		}
		if entry.Type == "file" {
			hash, err := hex.DecodeString(entry.SHA256)
			if err != nil || len(hash) != 32 || hex.EncodeToString(hash) != entry.SHA256 {
				return storage.ErrWorkspace
			}
		}
		total += entry.Size
		folded[lower] = true
		expected[entry.Path] = entry
	}
	// Require explicit parent entries: no hidden extra directories in the copy.
	for name := range expected {
		parent := path.Dir(name)
		if parent != "." {
			e, ok := expected[parent]
			if !ok || e.Type != "directory" {
				return storage.ErrWorkspace
			}
		}
	}
	entries, err := fs.ReadDir(d.root.FS(), ".")
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return storage.ErrWorkspace
	}
	staging := "restore_" + uuid.NewString()
	if err = d.manager.root.Mkdir(staging, 0700); err != nil {
		return err
	}
	defer d.manager.root.RemoveAll(staging)
	target, err := d.manager.root.OpenRoot(staging)
	if err != nil {
		return err
	}
	defer target.Close()
	gz, err := gzip.NewReader(storage.ContextReader{Context: ctx, Reader: source})
	if err != nil {
		return storage.ErrWorkspace
	}
	defer gz.Close()
	// Limit decompression including archive padding and extended headers.
	maxExpanded := d.manager.limits.MaxTotalBytes + int64(d.manager.limits.MaxEntries)*4096 + 10240
	expanded := &io.LimitedReader{R: storage.ContextReader{Context: ctx, Reader: gz}, N: maxExpanded + 1}
	tr := tar.NewReader(expanded)
	seen := map[string]bool{}
	for {
		header, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return storage.ErrWorkspace
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		name := header.Name
		entry, ok := expected[name]
		if !ok || !validPath(name) || seen[name] || header.Linkname != "" || header.Size != entry.Size {
			return storage.ErrWorkspace
		}
		if header.Typeflag == tar.TypeDir && entry.Type == "directory" {
			if e = target.Mkdir(name, 0700); e != nil {
				return storage.ErrWorkspace
			}
		} else if header.Typeflag == tar.TypeReg && entry.Type == "file" {
			f, e := target.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if e != nil {
				return storage.ErrWorkspace
			}
			hash := sha256.New()
			n, e := io.Copy(io.MultiWriter(f, hash), storage.ContextReader{Context: ctx, Reader: tr})
			closeErr := f.Close()
			if e != nil || closeErr != nil || n != entry.Size || hex.EncodeToString(hash.Sum(nil)) != entry.SHA256 {
				return storage.ErrIntegrity
			}
		} else {
			return storage.ErrWorkspace
		}
		seen[name] = true
	}
	// Drain gzip to validate its checksum/trailer; reject non-padding extra data.
	buffer := make([]byte, 32<<10)
	for {
		n, e := expanded.Read(buffer)
		for _, b := range buffer[:n] {
			if b != 0 {
				return storage.ErrWorkspace
			}
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return storage.ErrWorkspace
		}
	}
	if expanded.N <= 0 {
		return storage.ErrWorkspace
	}
	if len(seen) != len(expected) {
		return storage.ErrIntegrity
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = d.root.Close(); err != nil {
		return err
	}
	if err = d.manager.root.Remove(d.name); err != nil {
		return err
	}
	if err = d.manager.root.Rename(staging, d.name); err != nil {
		return err
	}
	d.root, err = d.manager.root.OpenRoot(d.name)
	return err
}

// Clear discards only a newly restored, not-yet-confirmed copy under its lease.
func (d *directory) Clear() error {
	entries, err := fs.ReadDir(d.root.FS(), ".")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err = d.root.RemoveAll(entry.Name()); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) Exists(space, session string) bool {
	if !storage.ValidID(space) || !storage.ValidID(session) {
		return false
	}
	info, err := m.root.Lstat(space + "_" + session)
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0
}
