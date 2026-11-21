package catalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"time"

	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/app/identity"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/resource"
)

type Service struct {
	Repo    Repository
	Content *resource.ContentStore
}

func (s *Service) Draft(ctx context.Context, actor, space, kind, id string) (Draft, error) {
	d, err := s.Repo.CatalogDraft(ctx, actor, space, kind, id)
	if err != nil || kind != "skill" {
		return d, err
	}
	b, err := s.readSkillBundle(ctx, actor, space, d.ContentID, "")
	if err != nil {
		return d, err
	}
	if b.DefinitionID != d.ID || b.Revision != d.Revision {
		return d, storage.ErrIntegrity
	}
	d.Document = b.Document
	return d, nil
}

func (s *Service) createDraft(ctx context.Context, actor, space string, d Draft) (Draft, error) {
	if _, err := ValidateDocument(d.Kind, d.Name, d.Document, false); err != nil {
		return d, err
	}
	if d.Kind == "skill" {
		var err error
		d, err = s.packSkill(ctx, actor, space, d, d.Revision, nil, nil)
		if err != nil {
			return d, err
		}
	}
	return d, s.Repo.CatalogCreate(ctx, actor, space, d)
}

func (s *Service) Create(ctx context.Context, actor, space, kind, name string) (Draft, error) {
	d := Draft{Definition: newDefinition(kind, name), Revision: 1}
	if kind == "skill" {
		d.Document.Assets = []Asset{}
	}
	return s.createDraft(ctx, actor, space, d)
}

func (s *Service) Save(ctx context.Context, actor, space, kind, id string, revision int64, doc Document) (Draft, error) {
	d, err := s.Repo.CatalogDraft(ctx, actor, space, kind, id)
	if err != nil {
		return d, err
	}
	if d.Revision != revision {
		return d, ErrDraft
	}
	d.Document = doc
	return s.saveDraft(ctx, actor, space, d, nil, nil)
}

func (s *Service) saveDraft(ctx context.Context, actor, space string, d Draft, replacement *Asset, data []byte) (Draft, error) {
	if d.Archived {
		return d, ErrConfig
	}
	if d.Kind == "skill" && d.Document.Assets == nil {
		d.Document.Assets = []Asset{}
	}
	if _, err := ValidateDocument(d.Kind, d.Name, d.Document, false); err != nil {
		return d, err
	}
	if d.Kind == "skill" {
		var err error
		d, err = s.packSkill(ctx, actor, space, d, d.Revision+1, replacement, data)
		if err != nil {
			return d, err
		}
	}
	if err := s.Repo.CatalogSave(ctx, actor, space, d); err != nil {
		return d, err
	}
	d.Revision++
	return d, nil
}

func (s *Service) Upload(ctx context.Context, actor, space, kind, id, path, media string, revision int64, r io.Reader) (Draft, error) {
	if kind != "skill" {
		return Draft{}, ErrConfig
	}
	d, err := s.Draft(ctx, actor, space, kind, id)
	if err != nil {
		return d, err
	}
	if d.Revision != revision {
		return d, ErrDraft
	}
	if !resource.ValidSkillPath(path) {
		return d, ErrConfig
	}
	raw, err := io.ReadAll(io.LimitReader(storage.ContextReader{Context: ctx, Reader: r}, MaxResource+1))
	if err != nil {
		return d, err
	}
	if len(raw) > MaxResource {
		return d, ErrConfig
	}
	if _, _, err = mime.ParseMediaType(media); err != nil || len(media) > 128 {
		return d, ErrConfig
	}
	sum := sha256.Sum256(raw)
	// Reserve the id of the immutable package containing the new file.
	a := Asset{Path: path, ObjectID: uuid.NewString(), SHA256: hex.EncodeToString(sum[:]), Size: int64(len(raw)), MediaType: media}
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
	return s.saveDraft(ctx, actor, space, d, &a, raw)
}
func (s *Service) Publish(ctx context.Context, actor, space, kind, id, label string, revision int64, debug bool) (Version, error) {
	return s.publish(ctx, actor, space, kind, id, label, revision, debug, nil)
}

func (s *Service) publish(ctx context.Context, actor, space, kind, id, label string, revision int64, debug bool, base *string) (Version, error) {
	if (!debug && label != "" && !ValidLabel(label)) || (debug && kind != "agent") {
		return Version{}, ErrConfig
	}
	commit := func(v *Version) error {
		if base != nil {
			return s.Repo.(projectCatalog).CatalogPublishBasedOn(ctx, actor, space, v, *base)
		}
		return s.Repo.CatalogPublish(ctx, actor, space, v)
	}
	if kind == "skill" {
		d, err := s.Draft(ctx, actor, space, kind, id)
		if err != nil {
			return Version{}, err
		}
		if d.Revision != revision {
			return Version{}, ErrDraft
		}
		if d.Archived {
			return Version{}, ErrConfig
		}
		if _, err = ValidateDocument(kind, d.Name, d.Document, true); err != nil {
			return Version{}, err
		}
		o, err := s.Content.Repo.Object(ctx, actor, space, d.ContentID, true)
		if err != nil {
			return Version{}, err
		}
		v := Version{ID: uuid.NewString(), DefinitionID: id, Label: label, ContentID: o.ID, SHA256: o.SHA256, CreatedAt: time.Now().UTC().Truncate(time.Microsecond), Bundle: skillBundle(d)}
		err = commit(&v)
		return v, err
	}
	b, err := s.Repo.CatalogPrepare(ctx, actor, space, id, revision)
	if err != nil {
		return Version{}, err
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return Version{}, err
	}
	versionID := uuid.NewString()
	o, err := s.Content.PutAt(ctx, actor, space, versionID, "definition.bundle", "application/json",
		s.Content.CatalogObjectKey(space, kind, id, "versions/"+versionID+".json"), bytes.NewReader(raw))
	if err != nil {
		return Version{}, err
	}
	if debug {
		label = ""
	}
	v := Version{ID: versionID, DefinitionID: id, Label: label, Debug: debug, ContentID: o.ID, SHA256: o.SHA256, CreatedAt: time.Now().UTC().Truncate(time.Microsecond), Bundle: b}
	err = commit(&v)
	return v, err
}
func (s *Service) Version(ctx context.Context, actor, space, kind, id, version string) (Version, error) {
	v, err := s.Repo.CatalogVersion(ctx, actor, space, kind, id, version)
	if err != nil {
		return v, err
	}
	if err = s.loadVersion(ctx, actor, space, kind, &v); err != nil {
		return v, err
	}
	// Recheck current membership after remote reads and refresh mutable archive state.
	current, err := s.Repo.CatalogVersion(ctx, actor, space, kind, id, version)
	if err != nil {
		return v, err
	}
	current.Bundle = v.Bundle
	return current, nil
}

func (s *Service) Versions(ctx context.Context, actor, space, kind, id string, debug bool, q identity.Query) ([]Version, error) {
	items, err := s.Repo.CatalogVersions(ctx, actor, space, kind, id, debug, q)
	if err != nil {
		return items, err
	}
	for i := range items {
		if items[i].Bundle.FormatVersion != 0 {
			continue
		}
		if err := s.loadVersion(ctx, actor, space, kind, &items[i]); err != nil {
			return nil, err
		}
	}
	return items, nil
}

func (s *Service) loadVersion(ctx context.Context, actor, space, kind string, v *Version) error {
	if kind == "skill" {
		b, err := s.readSkillBundle(ctx, actor, space, v.ContentID, v.SHA256)
		if err != nil {
			return err
		}
		if b.DefinitionID != v.DefinitionID {
			return storage.ErrIntegrity
		}
		v.Bundle = b
		return nil
	}
	_, f, err := s.Content.Read(ctx, actor, space, v.ContentID)
	if err != nil {
		return err
	}
	defer f.Close()
	return decodeAgentVersion(f, v)
}

func decodeAgentVersion(f io.Reader, v *Version) error {
	raw, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != v.SHA256 {
		return storage.ErrIntegrity
	}
	if json.Unmarshal(raw, &v.Bundle) != nil {
		return storage.ErrIntegrity
	}
	// Historical Agent bundles keep their original bytes and digest in TOS.
	v.Bundle.Document.Assets = nil
	return nil
}
func (s *Service) Copy(ctx context.Context, actor, space, kind, id, name string) (Draft, error) {
	d, err := s.Draft(ctx, actor, space, kind, id)
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
	d.Definition = newDefinition(kind, name)
	d.Revision = 1
	return s.createDraft(ctx, actor, space, d)
}

func newDefinition(kind, name string) Definition {
	if kind == "agent" {
		return Definition{ID: storage.NewShortID(), Kind: kind, Name: name, Visibility: VisibilitySpace}
	}
	return Definition{ID: uuid.NewString(), Kind: kind, Name: name}
}
