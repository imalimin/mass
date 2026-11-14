package mariadb

import (
	"context"
	"database/sql"
	"errors"

	"github.com/imalimin/mass/internal/domain/storage"
)

// RelocateStorage operates only on an offline restored database. Business rows reference objects by ID; content_objects resolves current bytes.
// copyObject must verify both source and destination bytes before returning.
func (s *Store) RelocateStorage(ctx context.Context, expectedDatabase string, checkKey func(string, string, string, []byte) error, copyObject func(context.Context, storage.Object) (storage.Object, error), copyTrace func(context.Context, string, string) error) (result storage.RestoreResult, err error) {
	var database string
	if err := s.DB.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&database); err != nil {
		return result, err
	}
	if expectedDatabase == "" || database != expectedDatabase {
		return result, errors.New("restore database does not match --expect-database")
	}
	if err := s.CheckSchema(ctx); err != nil {
		return result, err
	}
	lease, err := s.OpenRuntime(ctx)
	if err != nil {
		return result, errors.New("restore requires the target service to be stopped")
	}
	defer lease.Close()
	// Validate every model key, including disabled models needed by old backups.
	rows, err := s.DB.QueryContext(ctx, "SELECT space_id,secret_binding_id,key_id,ciphertext FROM model_configs")
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var space, id, key string
		var ciphertext []byte
		if err = rows.Scan(&space, &id, &key, &ciphertext); err == nil {
			err = checkKey(space, id, key, ciphertext)
		}
		if err != nil {
			rows.Close()
			return result, errors.New("restore master key cannot decrypt all model keys")
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	var invalid int
	if err = s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM runs WHERE status IN ('queued','running')").Scan(&invalid); err != nil {
		return result, err
	}
	if invalid != 0 {
		return result, errors.New("restore requires a quiescent backup with no queued or running runs")
	}
	// Every committed reference counts, including drafts and attachments that
	// have not yet been accepted by a Run. An unreferenced upload intent is not confirmed content; retain its
	// original metadata without reading, copying, deleting or promoting bytes.
	if err = s.DB.QueryRowContext(ctx, restoredInvalidObjects).Scan(&invalid); err != nil {
		return result, err
	}
	if invalid != 0 {
		return result, errors.Join(storage.ErrIntegrity, errors.New("restore contains a missing or unfinished referenced object"))
	}

	var unreferencedUploads int
	if err = s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM content_objects WHERE state='uploading'").Scan(&unreferencedUploads); err != nil {
		return result, err
	}
	rows, err = s.DB.QueryContext(ctx, "SELECT "+objectColumns+" FROM content_objects WHERE state='ready' ORDER BY space_id,id")
	if err != nil {
		return result, err
	}
	var objects []storage.Object
	for rows.Next() {
		o, e := scanObject(rows.Scan)
		if e != nil {
			rows.Close()
			return result, e
		}
		objects = append(objects, o)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	relocated := make([]storage.Object, 0, len(objects))
	for _, o := range objects {
		if err = lease.Check(ctx); err != nil {
			return result, err
		}
		next, e := copyObject(ctx, o)
		if e != nil {
			return result, e
		}
		if next.SpaceID != o.SpaceID || next.ID != o.ID || next.Size != o.Size || next.SHA256 != o.SHA256 {
			return result, storage.ErrIntegrity
		}
		relocated = append(relocated, next)
	}
	if copyTrace != nil {
		if err = s.relocateTraces(ctx, copyTrace); err != nil {
			return result, err
		}
	}

	if err = lease.Check(ctx); err != nil {
		return result, err
	}
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		for i, o := range objects {
			next := relocated[i]
			// Lock and compare even when resuming a previously committed relocation.
			current, e := scanObject(tx.QueryRowContext(ctx, "SELECT "+objectColumns+" FROM content_objects WHERE space_id=? AND id=? FOR UPDATE", o.SpaceID, o.ID).Scan)
			if e != nil {
				return e
			}
			if current != o {
				return storage.ErrConflict
			}
			if _, e = tx.ExecContext(ctx, "UPDATE content_objects SET object_key=?,tos_uri=?,etag=?,version_id=? WHERE space_id=? AND id=?", next.Key, next.URI, next.ETag, next.VersionID, o.SpaceID, o.ID); e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, "UPDATE workspace_snapshots SET tos_uri=? WHERE space_id=? AND object_id=?", next.URI, o.SpaceID, o.ID); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	return storage.RestoreResult{Relocated: len(relocated), UnreferencedUploads: unreferencedUploads}, nil
}

func (s *Store) relocateTraces(ctx context.Context, copyTrace func(context.Context, string, string) error) error {
	rows, err := s.DB.QueryContext(ctx, "SELECT DISTINCT space_id,session_id FROM runs WHERE trace_uploaded_at IS NOT NULL ORDER BY space_id,session_id")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var space, session string
		if err := rows.Scan(&space, &session); err != nil {
			return err
		}
		if err := copyTrace(ctx, space, session); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Keep all content_objects foreign-key columns represented here. The schema
// contract test checks this list against the embedded migrations so a new
// reference cannot silently make an unfinished upload eligible for skipping.
const restoredInvalidObjects = `SELECT COUNT(*) FROM (
 SELECT space_id,object_id FROM workspace_snapshots
 UNION ALL SELECT space_id,manifest_ref FROM workspace_snapshots
 UNION ALL SELECT space_id,content_id FROM definition_versions
 UNION ALL SELECT space_id,content_id FROM skill_drafts
 UNION ALL SELECT space_id,content_id FROM skill_versions
 UNION ALL SELECT space_id,definition_ref FROM sessions WHERE definition_ref IS NOT NULL
 UNION ALL SELECT space_id,content_id FROM messages
 UNION ALL SELECT space_id,content_id FROM model_attempts WHERE content_id IS NOT NULL
 UNION ALL SELECT space_id,summary_ref FROM model_attempts WHERE summary_ref IS NOT NULL
 UNION ALL SELECT space_id,object_id FROM artifacts
 UNION ALL SELECT space_id,object_id FROM attachments
 UNION ALL SELECT space_id,content_id FROM context_projections
) refs LEFT JOIN content_objects o ON o.space_id=refs.space_id AND o.id=refs.object_id
WHERE o.id IS NULL OR o.state<>'ready'`
