package mariadb

import (
	"context"
	"database/sql"
	"errors"

	"github.com/imalimin/mass/internal/domain/storage"
)

const objectColumns = "space_id,id,kind,media_type,size,sha256,object_key,tos_uri,etag,version_id,state"

func scanObject(scan func(...any) error) (o storage.Object, err error) {
	err = scan(&o.SpaceID, &o.ID, &o.Kind, &o.MediaType, &o.Size, &o.SHA256, &o.Key, &o.URI, &o.ETag, &o.VersionID, &o.State)
	return
}
func (s *Store) BeginObject(ctx context.Context, actor string, o storage.Object) (result storage.Object, err error) {
	err = s.scoped(ctx, actor, o.SpaceID, false, func(tx *sql.Tx) error {
		existing, e := scanObject(tx.QueryRowContext(ctx, "SELECT "+objectColumns+" FROM content_objects WHERE space_id=? AND id=?", o.SpaceID, o.ID).Scan)
		if e == nil {
			if existing.Key != o.Key || existing.URI != o.URI || existing.Size != o.Size || existing.SHA256 != o.SHA256 || existing.MediaType != o.MediaType || existing.Kind != o.Kind {
				return storage.ErrConflict
			}
			result = existing
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		_, e = tx.ExecContext(ctx, "INSERT INTO content_objects(space_id,id,kind,media_type,size,sha256,object_key,tos_uri) VALUES(?,?,?,?,?,?,?,?)", o.SpaceID, o.ID, o.Kind, o.MediaType, o.Size, o.SHA256, o.Key, o.URI)
		result = o
		return e
	})
	return
}
func (s *Store) ReadyObject(ctx context.Context, actor string, o storage.Object) error {
	return s.scoped(ctx, actor, o.SpaceID, false, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE content_objects SET state='ready',etag=?,version_id=? WHERE space_id=? AND id=? AND sha256=? AND size=? AND state='uploading'", o.ETag, o.VersionID, o.SpaceID, o.ID, o.SHA256, o.Size)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			var state string
			if err = tx.QueryRowContext(ctx, "SELECT state FROM content_objects WHERE space_id=? AND id=? AND sha256=? AND size=?", o.SpaceID, o.ID, o.SHA256, o.Size).Scan(&state); err != nil {
				return err
			}
			if state != "ready" {
				return storage.ErrIntegrity
			}
		}
		return nil
	})
}
func (s *Store) Object(ctx context.Context, actor, space, id string, referenced bool) (o storage.Object, err error) {
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		query := "SELECT " + objectColumns + " FROM content_objects WHERE space_id=? AND id=? AND state='ready'"
		if referenced {
			query += ` AND (
 EXISTS(SELECT 1 FROM sessions s WHERE s.space_id=content_objects.space_id AND s.definition_ref=content_objects.id)
 OR EXISTS(SELECT 1 FROM messages m WHERE m.space_id=content_objects.space_id AND m.content_id=content_objects.id)
 OR EXISTS(SELECT 1 FROM attachments a JOIN messages m ON m.space_id=a.space_id AND m.session_id=a.session_id
    WHERE a.space_id=content_objects.space_id AND a.object_id=content_objects.id
    AND JSON_CONTAINS(m.attachments,JSON_OBJECT('id',a.id)))
 OR EXISTS(SELECT 1 FROM model_attempts m WHERE m.space_id=content_objects.space_id AND (m.content_id=content_objects.id OR m.summary_ref=content_objects.id))
 OR EXISTS(SELECT 1 FROM context_projections p WHERE p.space_id=content_objects.space_id AND p.content_id=content_objects.id)
 OR EXISTS(SELECT 1 FROM workspace_snapshots s WHERE s.space_id=content_objects.space_id AND (s.object_id=content_objects.id OR s.manifest_ref=content_objects.id))
 OR EXISTS(SELECT 1 FROM artifacts a WHERE a.space_id=content_objects.space_id AND a.object_id=content_objects.id)
 OR EXISTS(SELECT 1 FROM definition_versions v WHERE v.space_id=content_objects.space_id AND v.content_id=content_objects.id)
 OR EXISTS(SELECT 1 FROM skill_drafts d WHERE d.space_id=content_objects.space_id AND d.content_id=content_objects.id)
 OR EXISTS(SELECT 1 FROM skill_versions v WHERE v.space_id=content_objects.space_id AND v.content_id=content_objects.id))`
		}
		var e error
		o, e = scanObject(tx.QueryRowContext(ctx, query, space, id).Scan)
		return e
	})
	return
}
