package mariadb

import (
	"context"
	"database/sql"
	"errors"

	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
)

// RelocateStorage operates only on an offline restored database. Immutable
// events retain their historical URIs; content_objects resolves current bytes.
// copyObject must verify both source and destination bytes before returning.
func (s *Store) RelocateStorage(ctx context.Context, expectedDatabase string, checkKey func(string, string, string, []byte) error, copyObject func(context.Context, storage.Object) (storage.Object, error)) (int, error) {
	var database string
	if err := s.DB.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&database); err != nil {
		return 0, err
	}
	if expectedDatabase == "" || database != expectedDatabase {
		return 0, errors.New("restore database does not match --expect-database")
	}
	if err := s.CheckSchema(ctx); err != nil {
		return 0, err
	}
	lease, err := s.OpenRuntime(ctx)
	if err != nil {
		return 0, errors.New("restore requires the target service to be stopped")
	}
	defer lease.Close()
	// Validate every credential, including disabled ones needed by old backups.
	rows, err := s.DB.QueryContext(ctx, "SELECT space_id,id,key_id,ciphertext FROM credentials")
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var space, id, key string
		var ciphertext []byte
		if err = rows.Scan(&space, &id, &key, &ciphertext); err == nil {
			err = checkKey(space, id, key, ciphertext)
		}
		if err != nil {
			rows.Close()
			return 0, errors.New("restore master key cannot decrypt all credentials")
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	var invalid int
	if err = s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM runs WHERE status IN ('queued','running')").Scan(&invalid); err != nil {
		return 0, err
	}
	if invalid != 0 {
		return 0, errors.New("restore requires a quiescent backup with no queued or running runs")
	}
	if err = s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM content_objects WHERE state<>'ready'").Scan(&invalid); err != nil {
		return 0, err
	}
	if invalid != 0 {
		return 0, errors.New("restore contains unfinished uploads; reconcile them before relocation")
	}
	if err = s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM event_objects e LEFT JOIN content_objects o ON o.space_id=e.space_id AND o.id=e.object_id WHERE o.id IS NULL OR o.state<>'ready'").Scan(&invalid); err != nil {
		return 0, err
	}
	if invalid != 0 {
		return 0, storage.ErrIntegrity
	}
	if err = s.checkRestoredJournal(ctx); err != nil {
		return 0, err
	}
	rows, err = s.DB.QueryContext(ctx, "SELECT "+objectColumns+" FROM content_objects ORDER BY space_id,id")
	if err != nil {
		return 0, err
	}
	var objects []storage.Object
	for rows.Next() {
		o, e := scanObject(rows.Scan)
		if e != nil {
			rows.Close()
			return 0, e
		}
		objects = append(objects, o)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	relocated := make([]storage.Object, 0, len(objects))
	for _, o := range objects {
		if err = lease.Check(ctx); err != nil {
			return 0, err
		}
		next, e := copyObject(ctx, o)
		if e != nil {
			return 0, e
		}
		if next.SpaceID != o.SpaceID || next.ID != o.ID || next.Size != o.Size || next.SHA256 != o.SHA256 {
			return 0, storage.ErrIntegrity
		}
		relocated = append(relocated, next)
	}
	if err = lease.Check(ctx); err != nil {
		return 0, err
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
	return len(relocated), err
}

// Verify event digests (including reconstructed object hashes) and contiguous
// session sequences before changing any location. Paging bounds event memory.
func (s *Store) checkRestoredJournal(ctx context.Context) error {
	type session struct {
		space, id string
		last      int64
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT space_id,id,last_sequence FROM sessions")
	if err != nil {
		return err
	}
	var sessions []session
	for rows.Next() {
		var v session
		if err = rows.Scan(&v.space, &v.id, &v.last); err != nil {
			rows.Close()
			return err
		}
		sessions = append(sessions, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range sessions {
		for after := int64(0); after < v.last; {
			var events []journal.Event
			err = s.transaction(ctx, func(tx *sql.Tx) error {
				var e error
				events, e = readEvents(ctx, tx, v.space, v.id, after, v.last, 1000)
				return e
			})
			if err != nil {
				return err
			}
			if len(events) == 0 {
				return storage.ErrIntegrity
			}
			for _, event := range events {
				if event.Sequence != after+1 {
					return storage.ErrIntegrity
				}
				if err = journal.Validate(event.Proposed); err != nil {
					return err
				}
				var digest string
				if err = s.DB.QueryRowContext(ctx, "SELECT event_digest FROM runtime_events WHERE space_id=? AND event_id=?", v.space, event.EventID).Scan(&digest); err != nil {
					return err
				}
				if digest != journal.Digest(event.Proposed) {
					return storage.ErrIntegrity
				}
				after = event.Sequence
			}
		}
	}
	return nil
}
