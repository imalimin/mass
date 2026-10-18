package catalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/resource"
)

type Service struct {
	Repo    Repository
	Content *resource.ContentStore
}

func (s *Service) Create(ctx context.Context, actor, space, kind, name string) (Draft, error) {
	d := Draft{Definition: Definition{ID: uuid.NewString(), Kind: kind, Name: name}, Revision: 1, Document: Document{Assets: []Asset{}}}
	if _, err := ValidateDocument(kind, name, d.Document, false); err != nil {
		return d, err
	}
	return d, s.Repo.CatalogCreate(ctx, actor, space, d)
}
func (s *Service) Save(ctx context.Context, actor, space, kind, id string, revision int64, doc Document) (Draft, error) {
	d, err := s.Repo.CatalogDraft(ctx, actor, space, kind, id)
	if err != nil {
		return d, err
	}
	if d.Revision != revision {
		return d, ErrDraft
	}
	if doc.Assets == nil {
		doc.Assets = []Asset{}
	}
	if _, err = ValidateDocument(kind, d.Name, doc, false); err != nil {
		return d, err
	}
	d.Document = doc
	if err = s.Repo.CatalogSave(ctx, actor, space, d); err != nil {
		return d, err
	}
	d.Revision++
	return d, nil
}
func (s *Service) Upload(ctx context.Context, actor, space, kind, id, path, media string, revision int64, r io.Reader) (Draft, error) {
	d, err := s.Repo.CatalogDraft(ctx, actor, space, kind, id)
	if err != nil {
		return d, err
	}
	if d.Revision != revision {
		return d, ErrDraft
	}
	if !ValidPath(path) {
		return d, ErrConfig
	}
	raw, err := io.ReadAll(io.LimitReader(storage.ContextReader{Context: ctx, Reader: r}, MaxResource+1))
	if err != nil {
		return d, err
	}
	if len(raw) > MaxResource {
		return d, ErrConfig
	}
	o, err := s.Content.Put(ctx, actor, space, uuid.NewString(), "definition.asset", media, bytes.NewReader(raw))
	if err != nil {
		return d, err
	}
	a := Asset{Path: path, ObjectID: o.ID, SHA256: o.SHA256, Size: o.Size, MediaType: o.MediaType}
	replaced := false
	for i, old := range d.Document.Assets {
		if old.Path == path {
			d.Document.Assets[i] = a
			replaced = true
		}
	}
	if !replaced {
		d.Document.Assets = append(d.Document.Assets, a)
	}
	return s.Save(ctx, actor, space, kind, id, revision, d.Document)
}
func (s *Service) Publish(ctx context.Context, actor, space, kind, id, label string, revision int64, debug bool) (Version, error) {
	if (!debug && !ValidLabel(label)) || (debug && kind != "agent") {
		return Version{}, ErrConfig
	}
	b, err := s.Repo.CatalogPrepare(ctx, actor, space, kind, id, revision)
	if err != nil {
		return Version{}, err
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return Version{}, err
	}
	o, err := s.Content.Put(ctx, actor, space, uuid.NewString(), "definition.bundle", "application/json", bytes.NewReader(raw))
	if err != nil {
		return Version{}, err
	}
	if debug {
		label = ""
	}
	v := Version{ID: uuid.NewString(), DefinitionID: id, Label: label, Debug: debug, ContentID: o.ID, SHA256: o.SHA256, CreatedAt: time.Now().UTC().Truncate(time.Microsecond), Bundle: b}
	return v, s.Repo.CatalogPublish(ctx, actor, space, v)
}
func (s *Service) Version(ctx context.Context, actor, space, kind, id, version string) (Version, error) {
	v, err := s.Repo.CatalogVersion(ctx, actor, space, kind, id, version)
	if err != nil {
		return v, err
	}
	_, f, err := s.Content.Read(ctx, actor, space, v.ContentID)
	if err != nil {
		return v, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return v, err
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != v.SHA256 {
		return v, storage.ErrIntegrity
	}
	if err = json.Unmarshal(raw, &v.Bundle); err != nil {
		return v, storage.ErrIntegrity
	}
	// Recheck current membership after remote reads and refresh mutable archive state.
	current, err := s.Repo.CatalogVersion(ctx, actor, space, kind, id, version)
	if err != nil {
		return v, err
	}
	current.Bundle = v.Bundle
	return current, nil
}
func (s *Service) Copy(ctx context.Context, actor, space, kind, id, name string) (Draft, error) {
	d, err := s.Repo.CatalogDraft(ctx, actor, space, kind, id)
	if err != nil {
		return d, err
	}
	if !ValidName(name) {
		return d, ErrConfig
	}
	if kind == "agent" && d.Document.ConfigYAML != "" {
		c, err := ValidateDocument(kind, d.Name, d.Document, false)
		if err != nil {
			return d, err
		}
		if c != nil {
			c.Name = name
			raw, _ := json.Marshal(c)
			d.Document.ConfigYAML = string(raw)
		}
	}
	d.Definition = Definition{ID: uuid.NewString(), Kind: kind, Name: name}
	d.Revision = 1
	return d, s.Repo.CatalogCreate(ctx, actor, space, d)
}
