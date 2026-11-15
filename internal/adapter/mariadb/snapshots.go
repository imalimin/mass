package mariadb

import (
	"context"
	"database/sql"

	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
)

func (s *Store) RunBaseSnapshot(ctx context.Context, snapshot run.Snapshot) (storage.Snapshot, error) {
	if snapshot.BaseSnapshotID == "" {
		return storage.Snapshot{}, nil
	}
	return s.Snapshot(ctx, snapshot.ActorID, snapshot.SpaceID, snapshot.BaseSnapshotID)
}

const snapshotColumns = "id,space_id,session_id,source_run_id,COALESCE(base_snapshot_id,''),object_id,manifest_ref,tos_uri,archive_sha256,format_version"

func snapshotInTx(ctx context.Context, tx *sql.Tx, space, id string) (v storage.Snapshot, err error) {
	err = tx.QueryRowContext(ctx, "SELECT "+snapshotColumns+" FROM workspace_snapshots WHERE space_id=? AND id=?", space, id).Scan(&v.ID, &v.SpaceID, &v.SessionID, &v.SourceRunID, &v.BaseSnapshotID, &v.ObjectID, &v.ManifestRef, &v.URI, &v.SHA256, &v.FormatVersion)
	return
}
func saveSnapshot(ctx context.Context, tx *sql.Tx, space, session string, e journal.Proposed, v storage.Snapshot, seq int64) error {
	if v.SpaceID != space || v.SessionID != session || v.SourceRunID != e.RunID {
		return storage.ErrInvalid
	}
	var latest sql.NullString
	if err := tx.QueryRowContext(ctx, "SELECT latest_workspace_snapshot_id FROM sessions WHERE space_id=? AND id=?", space, session).Scan(&latest); err != nil {
		return err
	}
	if v.BaseSnapshotID != latest.String {
		return storage.ErrConflict
	}
	if err := snapshotRun(ctx, tx, space, session, v.SourceRunID); err != nil {
		return err
	}
	archive, err := scanObject(tx.QueryRowContext(ctx, "SELECT "+objectColumns+" FROM content_objects WHERE space_id=? AND id=? AND state='ready' AND kind='workspace.archive'", space, v.ObjectID).Scan)
	if err != nil {
		return err
	}
	manifest, err := scanObject(tx.QueryRowContext(ctx, "SELECT "+objectColumns+" FROM content_objects WHERE space_id=? AND id=? AND state='ready' AND kind='workspace.manifest'", space, v.ManifestRef).Scan)
	if err != nil {
		return err
	}
	if archive.URI != v.URI || archive.SHA256 != v.SHA256 {
		return storage.ErrIntegrity
	}
	matched := 0
	for _, r := range e.Refs {
		if r.Ordinal == 0 && ((r.Role == "archive" && r.ObjectID == archive.ID && r.SHA256 == archive.SHA256) || (r.Role == "manifest" && r.ObjectID == manifest.ID && r.SHA256 == manifest.SHA256)) {
			matched++
		}
	}
	if matched != 2 || len(e.Refs) != 2 {
		return storage.ErrInvalid
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO workspace_snapshots(space_id,id,session_id,source_run_id,base_snapshot_id,object_id,manifest_ref,tos_uri,archive_sha256,format_version,source_event_id,sequence,occurred_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)", space, v.ID, session, e.RunID, nullable(v.BaseSnapshotID), v.ObjectID, v.ManifestRef, v.URI, v.SHA256, v.FormatVersion, e.EventID, seq, e.OccurredAt)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE sessions SET latest_workspace_snapshot_id=?,workspace_error='' WHERE space_id=? AND id=?", v.ID, space, session); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE runs SET workspace_snapshot_id=? WHERE space_id=? AND id=?", v.ID, space, e.RunID)
	return err
}

// A retry cannot save an old run's directory over a newer run or active owner.
func snapshotRun(ctx context.Context, tx *sql.Tx, space, session, run string) error {
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM runs r WHERE r.space_id=? AND r.session_id=? AND r.id=? AND NOT EXISTS(SELECT 1 FROM runs newer WHERE newer.space_id=r.space_id AND newer.session_id=r.session_id AND newer.applied_sequence>r.applied_sequence) AND NOT EXISTS(SELECT 1 FROM session_active_runs a WHERE a.space_id=r.space_id AND a.session_id=r.session_id AND a.run_id<>r.id)", space, session, run).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return storage.ErrConflict
	}
	return nil
}
func (s *Store) Snapshot(ctx context.Context, actor, space, id string) (v storage.Snapshot, err error) {
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error { var e error; v, e = snapshotInTx(ctx, tx, space, id); return e })
	return
}
func (s *Store) SnapshotBase(ctx context.Context, actor, space, session, run string) (base string, err error) {
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		var latest sql.NullString
		if e := tx.QueryRowContext(ctx, "SELECT latest_workspace_snapshot_id FROM sessions WHERE space_id=? AND id=? FOR UPDATE", space, session).Scan(&latest); e != nil {
			return e
		}
		base = latest.String
		if run != "" {
			return snapshotRun(ctx, tx, space, session, run)
		}
		var n int
		if e := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM session_active_runs WHERE space_id=? AND session_id=?", space, session).Scan(&n); e != nil {
			return e
		}
		if n > 0 {
			return storage.ErrActive
		}
		return nil
	})
	return
}
func (s *Store) SnapshotEvent(ctx context.Context, actor, space, session, eventID string) (v storage.Snapshot, found bool, err error) {
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		e := tx.QueryRowContext(ctx, "SELECT "+snapshotColumns+" FROM workspace_snapshots WHERE space_id=? AND session_id=? AND source_event_id=?", space, session, eventID).Scan(&v.ID, &v.SpaceID, &v.SessionID, &v.SourceRunID, &v.BaseSnapshotID, &v.ObjectID, &v.ManifestRef, &v.URI, &v.SHA256, &v.FormatVersion)
		found = e == nil
		if e == sql.ErrNoRows {
			return nil
		}
		return e
	})
	return
}
