package mariadb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/go-sql-driver/mysql"
	"github.com/imalimin/mass/internal/app/catalog"
	"github.com/imalimin/mass/internal/app/identity"
	"github.com/imalimin/mass/internal/domain/storage"
)

const definitionColumns = "d.id,d.kind,d.name,d.archived,COALESCE(d.default_version_id,'')"

func scanDefinition(scan func(...any) error) (v catalog.Definition, err error) {
	err = scan(&v.ID, &v.Kind, &v.Name, &v.Archived, &v.DefaultVersionID)
	return
}
func catalogDefinition(ctx context.Context, tx *sql.Tx, space, kind, id string) (catalog.Definition, error) {
	return scanDefinition(tx.QueryRowContext(ctx, "SELECT "+definitionColumns+" FROM definitions d WHERE d.space_id=? AND d.kind=? AND d.id=?", space, kind, id).Scan)
}
func catalogDraft(ctx context.Context, tx *sql.Tx, space, kind, id string) (d catalog.Draft, err error) {
	d.Definition, err = catalogDefinition(ctx, tx, space, kind, id)
	if err != nil {
		return
	}
	var raw []byte
	err = tx.QueryRowContext(ctx, "SELECT revision,document FROM definition_drafts WHERE space_id=? AND definition_id=? FOR UPDATE", space, id).Scan(&d.Revision, &raw)
	if err == nil {
		err = json.Unmarshal(raw, &d.Document)
	}
	return
}
func assetObjects(ctx context.Context, tx *sql.Tx, space string, assets []catalog.Asset) error {
	for _, a := range assets {
		o, err := scanObject(tx.QueryRowContext(ctx, "SELECT "+objectColumns+" FROM content_objects WHERE space_id=? AND id=? AND state='ready' AND kind='definition.asset'", space, a.ObjectID).Scan)
		if err != nil {
			return err
		}
		if o.SHA256 != a.SHA256 || o.Size != a.Size || o.MediaType != a.MediaType {
			return catalog.ErrConfig
		}
	}
	return nil
}
func draftObjects(ctx context.Context, tx *sql.Tx, space string, d catalog.Draft) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM definition_draft_objects WHERE space_id=? AND definition_id=?", space, d.ID); err != nil {
		return err
	}
	for _, a := range d.Document.Assets {
		if _, err := tx.ExecContext(ctx, "INSERT INTO definition_draft_objects(space_id,definition_id,path,object_id) VALUES(?,?,?,?)", space, d.ID, a.Path, a.ObjectID); err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) CatalogCreate(ctx context.Context, actor, space string, d catalog.Draft) error {
	return s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		if _, err := catalog.ValidateDocument(d.Kind, d.Name, d.Document, false); err != nil {
			return err
		}
		if err := assetObjects(ctx, tx, space, d.Document.Assets); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO definitions(space_id,id,kind,name) VALUES(?,?,?,?)", space, d.ID, d.Kind, d.Name); err != nil {
			return err
		}
		raw, _ := json.Marshal(d.Document)
		if _, err := tx.ExecContext(ctx, "INSERT INTO definition_drafts(space_id,definition_id,document) VALUES(?,?,?)", space, d.ID, raw); err != nil {
			return err
		}
		if err := draftObjects(ctx, tx, space, d); err != nil {
			return err
		}
		return audit(ctx, tx, actor, space, "definition.created", d.ID)
	})
}
func (s *Store) CatalogList(ctx context.Context, actor, space, kind string, q identity.Query) (items []catalog.Definition, err error) {
	items = []catalog.Definition{}
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		rows, e := tx.QueryContext(ctx, "SELECT "+definitionColumns+" FROM definitions d WHERE d.space_id=? AND d.kind=? AND d.id>? ORDER BY d.id LIMIT ?", space, kind, q.After, q.Limit)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			v, e := scanDefinition(rows.Scan)
			if e != nil {
				return e
			}
			items = append(items, v)
		}
		return rows.Err()
	})
	return
}
func (s *Store) CatalogDraft(ctx context.Context, actor, space, kind, id string) (d catalog.Draft, err error) {
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error { var e error; d, e = catalogDraft(ctx, tx, space, kind, id); return e })
	return
}
func (s *Store) CatalogSave(ctx context.Context, actor, space string, d catalog.Draft) error {
	return s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		old, err := catalogDraft(ctx, tx, space, d.Kind, d.ID)
		if err != nil {
			return err
		}
		if old.Revision != d.Revision {
			return catalog.ErrDraft
		}
		if old.Archived {
			return catalog.ErrConfig
		}
		if _, err = catalog.ValidateDocument(d.Kind, old.Name, d.Document, false); err != nil {
			return err
		}
		if err = assetObjects(ctx, tx, space, d.Document.Assets); err != nil {
			return err
		}
		raw, _ := json.Marshal(d.Document)
		result, err := tx.ExecContext(ctx, "UPDATE definition_drafts SET document=?,revision=revision+1 WHERE space_id=? AND definition_id=? AND revision=?", raw, space, d.ID, d.Revision)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return catalog.ErrDraft
		}
		if err = draftObjects(ctx, tx, space, d); err != nil {
			return err
		}
		return audit(ctx, tx, actor, space, "draft.saved", d.ID)
	})
}
func prepareCatalog(ctx context.Context, tx *sql.Tx, space, kind, id string, revision int64) (b catalog.Bundle, err error) {
	d, err := catalogDraft(ctx, tx, space, kind, id)
	if err != nil {
		return b, err
	}
	if d.Revision != revision {
		return b, catalog.ErrDraft
	}
	if d.Archived {
		return b, catalog.ErrConfig
	}
	c, err := catalog.ValidateDocument(kind, d.Name, d.Document, true)
	if err != nil {
		return b, err
	}
	if err = assetObjects(ctx, tx, space, d.Document.Assets); err != nil {
		return b, err
	}
	b = catalog.Bundle{FormatVersion: 1, DefinitionID: id, Kind: kind, Name: d.Name, Revision: revision, Document: d.Document, Config: c, Bindings: []catalog.Binding{}}
	if c == nil {
		return b, nil
	}
	m, err := scanModel(tx.QueryRowContext(ctx, "SELECT "+modelColumns+" FROM model_configs m JOIN credentials c ON c.space_id=m.space_id AND c.id=m.credential_id WHERE m.space_id=? AND m.id=? AND m.enabled=TRUE AND c.enabled=TRUE", space, c.Model).Scan)
	if err != nil {
		return b, err
	}
	if c.Context.MaxInputTokens+c.Context.ReservedOutputTokens > m.ContextWindow || c.Context.ReservedOutputTokens > m.MaxOutputTokens || m.Provider != "ark" {
		return b, identity.ErrCapability
	}
	b.Model = &m
	for _, ref := range c.Skills {
		var binding catalog.Binding
		err = tx.QueryRowContext(ctx, "SELECT v.definition_id,v.id,v.label,v.sha256 FROM definition_versions v JOIN definitions d ON d.space_id=v.space_id AND d.id=v.definition_id WHERE v.space_id=? AND v.definition_id=? AND v.label=? AND v.debug=FALSE AND v.archived=FALSE AND d.archived=FALSE AND d.kind='skill'", space, ref.ID, ref.Version).Scan(&binding.SkillID, &binding.VersionID, &binding.Version, &binding.SHA256)
		if err != nil {
			return b, err
		}
		b.Bindings = append(b.Bindings, binding)
	}
	return b, nil
}
func (s *Store) CatalogPrepare(ctx context.Context, actor, space, kind, id string, revision int64) (b catalog.Bundle, err error) {
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		var e error
		b, e = prepareCatalog(ctx, tx, space, kind, id, revision)
		return e
	})
	return
}
func (s *Store) CatalogPublish(ctx context.Context, actor, space string, v catalog.Version) error {
	return s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		b, err := prepareCatalog(ctx, tx, space, v.Bundle.Kind, v.DefinitionID, v.Bundle.Revision)
		if err != nil {
			return err
		}
		if !catalog.Equal(b, v.Bundle) {
			return catalog.ErrDraft
		}
		o, err := scanObject(tx.QueryRowContext(ctx, "SELECT "+objectColumns+" FROM content_objects WHERE space_id=? AND id=? AND state='ready' AND kind='definition.bundle'", space, v.ContentID).Scan)
		if err != nil {
			return err
		}
		if o.SHA256 != v.SHA256 {
			return storage.ErrIntegrity
		}
		raw, _ := json.Marshal(b)
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != o.SHA256 {
			return storage.ErrIntegrity
		}
		var label any = v.Label
		if v.Debug {
			label = nil
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO definition_versions(space_id,id,definition_id,label,debug,content_id,sha256,bundle,created_at) VALUES(?,?,?,?,?,?,?,?,?)", space, v.ID, v.DefinitionID, label, v.Debug, v.ContentID, v.SHA256, raw, v.CreatedAt)
		var sqlerr *mysql.MySQLError
		if errors.As(err, &sqlerr) && sqlerr.Number == 1062 {
			return catalog.ErrVersion
		}
		if err != nil {
			return err
		}
		for i, binding := range b.Bindings {
			if _, err = tx.ExecContext(ctx, "INSERT INTO definition_dependencies(space_id,version_id,ordinal,skill_id,skill_version_id) VALUES(?,?,?,?,?)", space, v.ID, i, binding.SkillID, binding.VersionID); err != nil {
				return err
			}
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO definition_version_objects(space_id,version_id,path,object_id) VALUES(?,?,?,?)", space, v.ID, "bundle.json", v.ContentID); err != nil {
			return err
		}
		for _, a := range b.Document.Assets {
			if _, err = tx.ExecContext(ctx, "INSERT INTO definition_version_objects(space_id,version_id,path,object_id) VALUES(?,?,?,?)", space, v.ID, a.Path, a.ObjectID); err != nil {
				return err
			}
		}
		if !v.Debug && b.Kind == "agent" {
			if _, err = tx.ExecContext(ctx, "UPDATE definitions SET default_version_id=? WHERE space_id=? AND id=? AND default_version_id IS NULL", v.ID, space, v.DefinitionID); err != nil {
				return err
			}
		}
		action := "definition.published"
		if v.Debug {
			action = "definition.snapshot.created"
		}
		return audit(ctx, tx, actor, space, action, v.ID)
	})
}

const versionColumns = "v.id,v.definition_id,COALESCE(v.label,''),v.debug,v.archived,v.content_id,v.sha256,v.created_at,v.bundle"

func scanVersion(scan func(...any) error) (v catalog.Version, err error) {
	var raw []byte
	err = scan(&v.ID, &v.DefinitionID, &v.Label, &v.Debug, &v.Archived, &v.ContentID, &v.SHA256, &v.CreatedAt, &raw)
	if err == nil {
		err = json.Unmarshal(raw, &v.Bundle)
	}
	return
}
func (s *Store) CatalogVersions(ctx context.Context, actor, space, kind, id string, debug bool, q identity.Query) (items []catalog.Version, err error) {
	items = []catalog.Version{}
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		if _, e := catalogDefinition(ctx, tx, space, kind, id); e != nil {
			return e
		}
		rows, e := tx.QueryContext(ctx, "SELECT "+versionColumns+" FROM definition_versions v WHERE v.space_id=? AND v.definition_id=? AND v.debug=? AND v.id>? ORDER BY v.id LIMIT ?", space, id, debug, q.After, q.Limit)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			v, e := scanVersion(rows.Scan)
			if e != nil {
				return e
			}
			items = append(items, v)
		}
		return rows.Err()
	})
	return
}
func (s *Store) CatalogVersion(ctx context.Context, actor, space, kind, id, version string) (v catalog.Version, err error) {
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		if _, e := catalogDefinition(ctx, tx, space, kind, id); e != nil {
			return e
		}
		var e error
		v, e = scanVersion(tx.QueryRowContext(ctx, "SELECT "+versionColumns+" FROM definition_versions v WHERE v.space_id=? AND v.definition_id=? AND v.id=?", space, id, version).Scan)
		return e
	})
	return
}
func (s *Store) CatalogReferences(ctx context.Context, actor, space, kind, id, version string, q identity.Query) (items []catalog.Reference, err error) {
	items = []catalog.Reference{}
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		if _, e := catalogDefinition(ctx, tx, space, kind, id); e != nil {
			return e
		}
		var found string
		if e := tx.QueryRowContext(ctx, "SELECT id FROM definition_versions WHERE space_id=? AND definition_id=? AND id=?", space, id, version).Scan(&found); e != nil {
			return e
		}
		rows, e := tx.QueryContext(ctx, "SELECT v.id,d.id,d.name,COALESCE(v.label,''),v.debug FROM definition_dependencies dep JOIN definition_versions v ON v.space_id=dep.space_id AND v.id=dep.version_id JOIN definitions d ON d.space_id=v.space_id AND d.id=v.definition_id WHERE dep.space_id=? AND dep.skill_version_id=? AND v.id>? ORDER BY v.id LIMIT ?", space, version, q.After, q.Limit)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v catalog.Reference
			if e = rows.Scan(&v.ID, &v.DefinitionID, &v.Name, &v.Label, &v.Debug); e != nil {
				return e
			}
			items = append(items, v)
		}
		return rows.Err()
	})
	return
}
func (s *Store) CatalogState(ctx context.Context, actor, space, kind, id, version, action string, archived bool) error {
	return s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		d, err := catalogDefinition(ctx, tx, space, kind, id)
		if err != nil {
			return err
		}
		if action == "archive" {
			_, err = tx.ExecContext(ctx, "UPDATE definitions SET archived=? WHERE space_id=? AND id=?", archived, space, id)
		} else {
			var debug, oldArchive bool
			if err = tx.QueryRowContext(ctx, "SELECT debug,archived FROM definition_versions WHERE space_id=? AND definition_id=? AND id=?", space, id, version).Scan(&debug, &oldArchive); err != nil {
				return err
			}
			switch action {
			case "default":
				if kind != "agent" || d.Archived || debug || oldArchive {
					return catalog.ErrConfig
				}
				_, err = tx.ExecContext(ctx, "UPDATE definitions SET default_version_id=? WHERE space_id=? AND id=?", version, space, id)
			case "version.archive":
				if archived && d.DefaultVersionID == version {
					return catalog.ErrReferenced
				}
				_, err = tx.ExecContext(ctx, "UPDATE definition_versions SET archived=? WHERE space_id=? AND id=?", archived, space, version)
			case "delete":
				// Published content is retained; referenced versions have a specific conflict.
				var n int
				if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM definition_dependencies WHERE space_id=? AND skill_version_id=?", space, version).Scan(&n); err != nil {
					return err
				}
				if n > 0 || d.DefaultVersionID == version {
					return catalog.ErrReferenced
				}
				return catalog.ErrConfig
			default:
				return catalog.ErrConfig
			}
		}
		if err != nil {
			return err
		}
		return audit(ctx, tx, actor, space, "definition."+action, id)
	})
}
