package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/app/catalog"
	"github.com/imalimin/mass/internal/app/identity"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/resource"
)

type Catalog struct {
	Blobs   storage.BlobStore
	Prefix  string
	Objects *Objects
	Content *resource.ContentStore
	Model   func(context.Context, string, string, string) (identity.Model, error)
	Audit   func(context.Context, string, string, string, string) error
	locks   sync.Map
}

type catalogIndex map[string]*definition
type definition struct {
	catalog.Definition
	Revision int64      `json:"revision"`
	Draft    Ref        `json:"draft"`
	Versions []*version `json:"versions"`
}
type version struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	Debug     bool      `json:"debug"`
	Archived  bool      `json:"archived"`
	CreatedAt time.Time `json:"created_at"`
	Object    Ref       `json:"object"`
}

func (v *version) metadata(definition string) catalog.Version {
	return catalog.Version{ID: v.ID, DefinitionID: definition, Label: v.Label, Debug: v.Debug, Archived: v.Archived,
		ContentID: v.Object.ID, SHA256: v.Object.SHA256, CreatedAt: v.CreatedAt}
}

func (c *Catalog) lock(space string) func() {
	value, _ := c.locks.LoadOrStore(space, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (c *Catalog) load(ctx context.Context, space string) (catalogIndex, error) {
	index := catalogIndex{}
	err := readJSON(ctx, c.Blobs, Key(c.Prefix, space, "catalog.json"), &index)
	if errors.Is(err, storage.ErrBlobNotFound) {
		err = nil
	}
	return index, err
}

func (c *Catalog) authorized(ctx context.Context, actor, space string) (catalogIndex, error) {
	if err := c.Objects.Authorize(ctx, actor, space, false); err != nil {
		return nil, err
	}
	return c.load(ctx, space)
}

func (c *Catalog) save(ctx context.Context, actor, space, action, target string, index catalogIndex) error {
	if err := c.Objects.Authorize(ctx, actor, space, false); err != nil {
		return err
	}
	raw, err := json.Marshal(index)
	if err != nil {
		return err
	}
	_, err = c.Blobs.Overwrite(ctx, Key(c.Prefix, space, "catalog.json"), bytes.NewReader(raw), int64(len(raw)), "application/json")
	if err != nil {
		return err
	}
	return c.Audit(ctx, actor, space, action, target)
}

func findDefinition(index catalogIndex, kind, id string) (*definition, error) {
	d := index[id]
	if d == nil || d.Kind != kind {
		return nil, identity.ErrNotFound
	}
	return d, nil
}

func (c *Catalog) Lookup(ctx context.Context, space, id string) (Ref, error) {
	index, err := c.load(ctx, space)
	if err != nil {
		return Ref{}, err
	}
	for _, d := range index {
		if d.Draft.ID == id {
			return d.Draft, nil
		}
		for _, v := range d.Versions {
			if v.Object.ID == id {
				return v.Object, nil
			}
		}
	}
	return Ref{}, identity.ErrNotFound
}

func (c *Catalog) putDraft(ctx context.Context, actor, space string, d catalog.Draft) (Ref, error) {
	if d.Kind == "skill" {
		o, err := c.Objects.Object(ctx, actor, space, d.ContentID, false)
		return Reference(o), err
	}
	raw, err := json.Marshal(d.Document)
	if err != nil {
		return Ref{}, err
	}
	id := uuid.NewString()
	o, err := c.Content.PutAt(ctx, actor, space, id, "definition.draft", "application/json", Key(c.Prefix, space, "agents", d.ID, "drafts", id+".json"), bytes.NewReader(raw))
	return Reference(o), err
}

func (c *Catalog) CatalogCreate(ctx context.Context, actor, space string, input catalog.Draft) error {
	defer c.lock(space)()
	index, err := c.authorized(ctx, actor, space)
	if err != nil {
		return err
	}
	for _, d := range index {
		if d.Kind == input.Kind && d.Name == input.Name {
			return identity.ErrConflict
		}
	}
	ref, err := c.putDraft(ctx, actor, space, input)
	if err != nil {
		return err
	}
	defer c.Objects.Forget(space, ref.ID)
	index[input.ID] = &definition{Definition: input.Definition, Revision: input.Revision, Draft: ref, Versions: []*version{}}
	return c.save(ctx, actor, space, "definition.created", input.ID, index)
}

func (c *Catalog) CatalogList(ctx context.Context, actor, space, kind string, q identity.Query) ([]catalog.Definition, error) {
	index, err := c.authorized(ctx, actor, space)
	if err != nil {
		return nil, err
	}
	items := []catalog.Definition{}
	for id, d := range index {
		if d.Kind == kind && id > q.After {
			items = append(items, d.Definition)
		}
	}
	return catalogPage(items, q.Limit, func(d catalog.Definition) string { return d.ID }), nil
}

func catalogPage[T any](items []T, limit int, id func(T) string) []T {
	slices.SortFunc(items, func(a, b T) int { return strings.Compare(id(a), id(b)) })
	return items[:min(limit, len(items))]
}

func (c *Catalog) draft(ctx context.Context, d *definition) (catalog.Draft, error) {
	result := catalog.Draft{Definition: d.Definition, Revision: d.Revision, ContentID: d.Draft.ID}
	if d.Kind == "agent" {
		err := readJSON(ctx, c.Blobs, d.Draft.Key, &result.Document)
		return result, err
	}
	return result, nil
}

func (c *Catalog) CatalogDraft(ctx context.Context, actor, space, kind, id string) (catalog.Draft, error) {
	index, err := c.authorized(ctx, actor, space)
	if err != nil {
		return catalog.Draft{}, err
	}
	d, err := findDefinition(index, kind, id)
	if err != nil {
		return catalog.Draft{}, err
	}
	return c.draft(ctx, d)
}

func (c *Catalog) CatalogSave(ctx context.Context, actor, space string, input catalog.Draft) error {
	defer c.lock(space)()
	index, err := c.authorized(ctx, actor, space)
	if err != nil {
		return err
	}
	d, err := findDefinition(index, input.Kind, input.ID)
	if err != nil {
		return err
	}
	if input.Revision != d.Revision {
		return catalog.ErrDraft
	}
	if d.Archived {
		return catalog.ErrConfig
	}
	ref, err := c.putDraft(ctx, actor, space, input)
	if err != nil {
		return err
	}
	defer c.Objects.Forget(space, ref.ID)
	d.Draft, d.Revision = ref, d.Revision+1
	return c.save(ctx, actor, space, "draft.saved", input.ID, index)
}

func (c *Catalog) prepare(ctx context.Context, actor, space string, d *definition, revision int64, index catalogIndex) (catalog.Bundle, error) {
	if d.Revision != revision {
		return catalog.Bundle{}, catalog.ErrDraft
	}
	if d.Archived {
		return catalog.Bundle{}, catalog.ErrConfig
	}
	draft, err := c.draft(ctx, d)
	if err != nil {
		return catalog.Bundle{}, err
	}
	cfg, err := catalog.ValidateDocument(d.Kind, d.Name, draft.Document, true)
	if err != nil {
		return catalog.Bundle{}, err
	}
	model, err := c.Model(ctx, actor, space, cfg.Model)
	if err != nil {
		return catalog.Bundle{}, err
	}
	if !model.Enabled || model.Provider != "ark" || cfg.Context.MaxInputTokens+cfg.Context.MaxTokens > model.ContextWindow ||
		cfg.Context.MaxTokens > model.MaxOutputTokens {
		return catalog.Bundle{}, identity.ErrCapability
	}
	b := catalog.Bundle{FormatVersion: 1, Kind: d.Kind, DefinitionID: d.ID, Name: d.Name, Revision: d.Revision, Document: draft.Document,
		Config: cfg, Model: &model, Bindings: []catalog.Binding{}}
	for _, ref := range cfg.Skills {
		skill, err := findDefinition(index, "skill", ref.ID)
		if err != nil {
			return catalog.Bundle{}, err
		}
		i := slices.IndexFunc(skill.Versions, func(v *version) bool { return v.Label == ref.Version && !v.Archived })
		if skill.Archived || i == -1 {
			return catalog.Bundle{}, catalog.ErrConfig
		}
		v := skill.Versions[i]
		b.Bindings = append(b.Bindings, catalog.Binding{SkillID: skill.ID, VersionID: v.ID, Version: v.Label, SHA256: v.Object.SHA256})
	}
	return b, nil
}

func (c *Catalog) CatalogPrepare(ctx context.Context, actor, space, id string, revision int64) (catalog.Bundle, error) {
	index, err := c.authorized(ctx, actor, space)
	if err != nil {
		return catalog.Bundle{}, err
	}
	d, err := findDefinition(index, "agent", id)
	if err != nil {
		return catalog.Bundle{}, err
	}
	return c.prepare(ctx, actor, space, d, revision, index)
}

func nextLabel(versions []*version) string {
	largest := new(big.Int)
	for _, v := range versions {
		suffix, ok := strings.CutPrefix(v.Label, "v")
		if !ok || suffix == "" || strings.Trim(suffix, "0123456789") != "" {
			continue
		}
		n, _ := new(big.Int).SetString(suffix, 10)
		if n.Cmp(largest) > 0 {
			largest = n
		}
	}
	return "v" + largest.Add(largest, big.NewInt(1)).String()
}

func (c *Catalog) CatalogPublish(ctx context.Context, actor, space string, input *catalog.Version) error {
	defer c.lock(space)()
	index, err := c.authorized(ctx, actor, space)
	if err != nil {
		return err
	}
	b := input.Bundle
	d, err := findDefinition(index, b.Kind, input.DefinitionID)
	if err != nil {
		return err
	}
	if d.Revision != b.Revision || d.Archived {
		return catalog.ErrDraft
	}
	ref := d.Draft
	if d.Kind == "skill" {
		if input.ContentID != ref.ID {
			return catalog.ErrDraft
		}
	} else {
		defer c.Objects.Forget(space, input.ContentID)
		current, err := c.prepare(ctx, actor, space, d, b.Revision, index)
		if err != nil {
			return err
		}
		if !catalog.Equal(current, b) {
			return catalog.ErrDraft
		}
		o, err := c.Objects.Object(ctx, actor, space, input.ContentID, false)
		if err != nil {
			return err
		}
		ref = Reference(o)
	}
	if input.SHA256 != ref.SHA256 {
		return storage.ErrIntegrity
	}
	if !input.Debug {
		if input.Label == "" {
			input.Label = nextLabel(d.Versions)
		}
		if !catalog.ValidLabel(input.Label) {
			return catalog.ErrConfig
		}
		if slices.ContainsFunc(d.Versions, func(v *version) bool { return v.Label == input.Label }) {
			return catalog.ErrVersion
		}
	}
	d.Versions = append(d.Versions, &version{ID: input.ID, Label: input.Label, Debug: input.Debug, CreatedAt: input.CreatedAt, Object: ref})
	if d.Kind == "agent" && !input.Debug && d.DefaultVersionID == "" {
		d.DefaultVersionID = input.ID
	}
	action := "definition.published"
	if input.Debug {
		action = "definition.snapshot.created"
	}
	return c.save(ctx, actor, space, action, input.ID, index)
}

func findVersion(d *definition, id string) (*version, error) {
	i := slices.IndexFunc(d.Versions, func(v *version) bool { return v.ID == id })
	if i == -1 {
		return nil, identity.ErrNotFound
	}
	return d.Versions[i], nil
}

func (c *Catalog) CatalogVersion(ctx context.Context, actor, space, kind, id, versionID string) (catalog.Version, error) {
	index, err := c.authorized(ctx, actor, space)
	if err != nil {
		return catalog.Version{}, err
	}
	d, err := findDefinition(index, kind, id)
	if err != nil {
		return catalog.Version{}, err
	}
	v, err := findVersion(d, versionID)
	if err != nil {
		return catalog.Version{}, err
	}
	return v.metadata(d.ID), nil
}

func (c *Catalog) CatalogVersions(ctx context.Context, actor, space, kind, id string, debug bool, q identity.Query) ([]catalog.Version, error) {
	index, err := c.authorized(ctx, actor, space)
	if err != nil {
		return nil, err
	}
	d, err := findDefinition(index, kind, id)
	if err != nil {
		return nil, err
	}
	items := []catalog.Version{}
	for _, v := range d.Versions {
		if v.Debug == debug && v.ID > q.After {
			items = append(items, v.metadata(d.ID))
		}
	}
	return catalogPage(items, q.Limit, func(v catalog.Version) string { return v.ID }), nil
}

func (c *Catalog) references(ctx context.Context, index catalogIndex, skillVersion string) ([]catalog.Reference, error) {
	result := []catalog.Reference{}
	for _, d := range index {
		if d.Kind != "agent" {
			continue
		}
		for _, v := range d.Versions {
			var b catalog.Bundle
			if err := readJSON(ctx, c.Blobs, v.Object.Key, &b); err != nil {
				return nil, err
			}
			if slices.ContainsFunc(b.Bindings, func(binding catalog.Binding) bool { return binding.VersionID == skillVersion }) {
				result = append(result, catalog.Reference{ID: v.ID, DefinitionID: d.ID, Name: d.Name, Label: v.Label, Debug: v.Debug})
			}
		}
	}
	return result, nil
}

func (c *Catalog) CatalogReferences(ctx context.Context, actor, space, kind, id, versionID string, q identity.Query) ([]catalog.Reference, error) {
	index, err := c.authorized(ctx, actor, space)
	if err != nil {
		return nil, err
	}
	d, err := findDefinition(index, kind, id)
	if err != nil {
		return nil, err
	}
	if _, err = findVersion(d, versionID); err != nil {
		return nil, err
	}
	refs, err := c.references(ctx, index, versionID)
	if err != nil {
		return nil, err
	}
	items := slices.DeleteFunc(refs, func(r catalog.Reference) bool { return r.ID <= q.After })
	return catalogPage(items, q.Limit, func(r catalog.Reference) string { return r.ID }), nil
}

func (c *Catalog) CatalogState(ctx context.Context, actor, space, kind, id, versionID, action string, archived bool) error {
	defer c.lock(space)()
	index, err := c.authorized(ctx, actor, space)
	if err != nil {
		return err
	}
	d, err := findDefinition(index, kind, id)
	if err != nil {
		return err
	}
	if action == "archive" {
		d.Archived = archived
	} else {
		v, err := findVersion(d, versionID)
		if err != nil {
			return err
		}
		switch action {
		case "default":
			if kind != "agent" || d.Archived || v.Archived || v.Debug {
				return catalog.ErrConfig
			}
			d.DefaultVersionID = v.ID
		case "version.archive":
			if archived && d.DefaultVersionID == v.ID {
				return catalog.ErrReferenced
			}
			v.Archived = archived
		case "delete":
			if d.DefaultVersionID == v.ID {
				return catalog.ErrReferenced
			}
			if kind == "skill" {
				refs, err := c.references(ctx, index, v.ID)
				if err != nil {
					return err
				}
				if len(refs) > 0 {
					return catalog.ErrReferenced
				}
			}
			return catalog.ErrConfig
		default:
			return catalog.ErrConfig
		}
	}
	return c.save(ctx, actor, space, "definition."+action, id, index)
}
