package catalog

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"

	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/app/identity"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/resource"
)

func skillBundle(d Draft) Bundle {
	return Bundle{FormatVersion: 1, DefinitionID: d.ID, Kind: d.Kind, Name: d.Name, Revision: d.Revision, Document: d.Document, Bindings: []Binding{}}
}

func (s *Service) readSkillBundle(ctx context.Context, actor, space, id, expectedSHA string) (Bundle, error) {
	o, f, err := s.Content.Read(ctx, actor, space, id)
	if err != nil {
		return Bundle{}, err
	}
	defer f.Close()
	return decodeSkillBundle(o, f, expectedSHA)
}

func decodeSkillBundle(o storage.Object, f *resource.File, expectedSHA string) (Bundle, error) {
	var b Bundle
	if expectedSHA != "" && o.SHA256 != expectedSHA {
		return b, storage.ErrIntegrity
	}
	z, err := resource.OpenSkillPackage(f, o)
	if err != nil {
		return b, err
	}
	raw, err := resource.ReadSkillEntry(z, resource.SkillManifestPath, 4<<20)
	if err != nil {
		return b, err
	}
	if json.Unmarshal(raw, &b) != nil || b.Kind != "skill" || b.FormatVersion != 1 {
		return b, storage.ErrIntegrity
	}
	raw, err = resource.ReadSkillEntry(z, resource.SkillDocumentPath, MaxDocument)
	if err != nil {
		return b, err
	}
	b.Document.Markdown = string(raw)
	for i := range b.Document.Assets {
		b.Document.Assets[i].ObjectID = o.ID
		b.Document.Assets[i].Package = true
	}
	return b, nil
}

// packSkill copies verified files from the previous snapshot and writes one
// immutable object. The caller switches the database reference after upload.
func (s *Service) packSkill(ctx context.Context, actor, space string, d Draft, revision int64, replacement *Asset, replacementBytes []byte) (Draft, error) {
	var previous *zip.Reader
	previousPaths := map[Asset]string{}
	if d.ContentID != "" {
		o, f, err := s.Content.Read(ctx, actor, space, d.ContentID)
		if err != nil {
			return d, err
		}
		defer f.Close()
		previous, err = resource.OpenSkillPackage(f, o)
		if err != nil {
			return d, err
		}
		raw, err := resource.ReadSkillEntry(previous, resource.SkillManifestPath, 4<<20)
		if err != nil {
			return d, err
		}
		var b Bundle
		if json.Unmarshal(raw, &b) != nil {
			return d, storage.ErrIntegrity
		}
		for _, asset := range b.Document.Assets {
			path := asset.Path
			asset.Path, asset.ObjectID, asset.Package = "", o.ID, true
			previousPaths[asset] = path
		}
	}

	id := uuid.NewString()
	if replacement != nil {
		id = replacement.ObjectID
	}
	return s.writeSkillPackage(ctx, actor, space, d, revision, id, func(asset Asset) ([]byte, error) {
		if replacement != nil && asset.Path == replacement.Path {
			return replacementBytes, nil
		}
		key := asset
		key.Path = ""
		path, ok := previousPaths[key]
		if !asset.Package || !ok {
			return nil, identity.ErrNotFound
		}
		return resource.ReadSkillEntry(previous, path, MaxResource)
	})
}

func (s *Service) writeSkillPackage(ctx context.Context, actor, space string, d Draft, revision int64, id string, readAsset func(Asset) ([]byte, error)) (Draft, error) {
	f, err := os.CreateTemp("", "mass-skill-package-*")
	if err != nil {
		return d, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	z := zip.NewWriter(f)
	write := func(path string, data []byte) error {
		w, err := z.Create(path)
		if err == nil {
			_, err = w.Write(data)
		}
		return err
	}

	b := skillBundle(d)
	b.Revision = revision
	b.Document.Markdown = ""
	b.Document.Assets = make([]Asset, len(d.Document.Assets))
	for i, asset := range d.Document.Assets {
		data, err := readAsset(asset)
		if err != nil {
			return d, err
		}
		sum := sha256.Sum256(data)
		if int64(len(data)) != asset.Size || hex.EncodeToString(sum[:]) != asset.SHA256 {
			return d, storage.ErrIntegrity
		}
		if err = write(asset.Path, data); err != nil {
			return d, err
		}
		asset.ObjectID = ""
		asset.Package = false
		b.Document.Assets[i] = asset
	}
	for _, dir := range d.Document.Directories {
		if _, err = z.Create(dir + "/"); err != nil {
			return d, err
		}
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return d, err
	}
	if err = write(resource.SkillManifestPath, raw); err != nil {
		return d, err
	}
	if err = write(resource.SkillDocumentPath, []byte(d.Document.Markdown)); err != nil {
		return d, err
	}
	if err = z.Close(); err != nil {
		return d, err
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return d, err
	}
	o, err := s.Content.PutAt(ctx, actor, space, id, resource.SkillPackageKind, "application/zip",
		s.Content.CatalogObjectKey(space, "skill", d.ID, "packages/"+id+".zip"), f)
	if err != nil {
		return d, err
	}
	d.ContentID = o.ID
	assets := make([]Asset, len(d.Document.Assets))
	for i, asset := range d.Document.Assets {
		asset.ObjectID = o.ID
		asset.Package = true
		assets[i] = asset
	}
	d.Document.Assets = assets
	return d, nil
}

type SkillPackageExport struct {
	SkillID   string
	Name      string
	VersionID string
	Label     string
	Size      int64
	File      *os.File
}

func (e *SkillPackageExport) Close() error {
	if e == nil || e.File == nil {
		return nil
	}
	name := e.File.Name()
	err := e.File.Close()
	_ = os.Remove(name)
	return err
}

// ExportSkillVersionPackage builds a clean, portable ZIP for one published Skill
// version containing SKILL.md, explicit directories, and verified assets, while
// omitting internal platform metadata such as bundle.json.
func (s *Service) ExportSkillVersionPackage(ctx context.Context, actor, space, id, versionID string) (*SkillPackageExport, error) {
	version, err := s.Repo.CatalogVersion(ctx, actor, space, "skill", id, versionID)
	if err != nil {
		return nil, err
	}
	if version.Debug {
		return nil, identity.ErrNotFound
	}
	object, file, err := s.Content.Read(ctx, actor, space, version.ContentID)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	bundle, err := decodeSkillBundle(object, file, version.SHA256)
	if err != nil {
		return nil, err
	}
	if bundle.DefinitionID != id || !ValidName(bundle.Name) {
		return nil, storage.ErrIntegrity
	}
	if _, err := ValidateDocument("skill", bundle.Name, bundle.Document, true); err != nil {
		return nil, err
	}
	source, err := resource.OpenSkillPackage(file, object)
	if err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp("", "mass-skill-export-*")
	if err != nil {
		return nil, err
	}
	cleanup := func() {
		name := tmp.Name()
		_ = tmp.Close()
		_ = os.Remove(name)
	}
	zw := zip.NewWriter(tmp)
	writeEntry := func(entryPath string, data []byte) error {
		w, err := zw.Create(entryPath)
		if err == nil {
			_, err = w.Write(data)
		}
		return err
	}
	if err := writeEntry(resource.SkillDocumentPath, []byte(bundle.Document.Markdown)); err != nil {
		cleanup()
		return nil, err
	}
	for _, dir := range bundle.Document.Directories {
		if _, err := zw.Create(dir + "/"); err != nil {
			cleanup()
			return nil, err
		}
	}
	for _, asset := range bundle.Document.Assets {
		data, err := resource.ReadSkillEntry(source, asset.Path, MaxResource)
		if err != nil {
			cleanup()
			return nil, err
		}
		sum := sha256.Sum256(data)
		if int64(len(data)) != asset.Size || hex.EncodeToString(sum[:]) != asset.SHA256 {
			cleanup()
			return nil, storage.ErrIntegrity
		}
		if err := writeEntry(asset.Path, data); err != nil {
			cleanup()
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		cleanup()
		return nil, err
	}
	info, err := tmp.Stat()
	if err != nil {
		cleanup()
		return nil, err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, err
	}
	// Recheck current membership after remote reads.
	current, err := s.Repo.CatalogVersion(ctx, actor, space, "skill", id, versionID)
	if err != nil {
		cleanup()
		return nil, err
	}
	if current.Debug {
		cleanup()
		return nil, identity.ErrNotFound
	}
	return &SkillPackageExport{
		SkillID:   id,
		Name:      bundle.Name,
		VersionID: current.ID,
		Label:     current.Label,
		Size:      info.Size(),
		File:      tmp,
	}, nil
}
