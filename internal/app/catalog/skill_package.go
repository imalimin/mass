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
	var b Bundle
	o, f, err := s.Content.Read(ctx, actor, space, id)
	if err != nil {
		return b, err
	}
	defer f.Close()
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
		var data []byte
		switch {
		case replacement != nil && asset.Path == replacement.Path:
			data = replacementBytes
		case asset.Package:
			key := asset
			key.Path = ""
			path, ok := previousPaths[key]
			if !ok {
				return d, identity.ErrNotFound
			}
			data, err = resource.ReadSkillEntry(previous, path, MaxResource)
		default:
			return d, identity.ErrNotFound
		}
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
	id := uuid.NewString()
	if replacement != nil {
		id = replacement.ObjectID
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
