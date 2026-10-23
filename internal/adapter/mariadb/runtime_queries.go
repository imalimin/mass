package mariadb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/imalimin/mass/internal/app/identity"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
)

func findRuntimeRequest(ctx context.Context, tx *sql.Tx, actor, space, operation, key, digest string) (session, id string, found bool, err error) {
	var hash string
	err = tx.QueryRowContext(ctx, "SELECT session_id,COALESCE(run_id,''),request_digest FROM runtime_requests WHERE space_id=? AND actor_id=? AND operation=? AND request_key=?", space, actor, operation, key).Scan(&session, &id, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	if hash != digest {
		return "", "", false, storage.ErrConflict
	}
	return session, id, true, nil
}
func (s *Store) FindRuntimeRequest(ctx context.Context, actor, space, operation, key, digest string) (session, id string, found bool, err error) {
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		var e error
		session, id, found, e = findRuntimeRequest(ctx, tx, actor, space, operation, key, digest)
		return e
	})
	return
}
func (s *Store) AcceptRuntimeRequest(ctx context.Context, actor, space, operation, key, digest string, b journal.Batch) (session, id string, err error) {
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		var found bool
		var e error
		session, id, found, e = findRuntimeRequest(ctx, tx, actor, space, operation, key, digest)
		if e != nil || found {
			return e
		}
		if b.SpaceID != space {
			return storage.ErrInvalid
		}
		if _, e = appendInTx(ctx, tx, actor, b); e != nil {
			return e
		}
		session = b.SessionID
		for _, event := range b.Events {
			if event.Type == "run.created" {
				id = event.RunID
			}
		}
		_, e = tx.ExecContext(ctx, "INSERT INTO runtime_requests(space_id,actor_id,operation,request_key,request_digest,session_id,run_id) VALUES(?,?,?,?,?,?,?)", space, actor, operation, key, digest, session, nullable(id))
		return e
	})
	return
}

const sessionColumns = "s.id,s.space_id,s.actor_id,s.title,COALESCE(s.definition_id,''),COALESCE(s.version_id,''),COALESCE(s.definition_ref,''),COALESCE(o.sha256,''),COALESCE(s.latest_workspace_snapshot_id,''),s.workspace_error,s.last_sequence,s.created_at"

func scanSession(scan func(...any) error) (v run.Session, err error) {
	err = scan(&v.ID, &v.SpaceID, &v.ActorID, &v.Title, &v.DefinitionID, &v.VersionID, &v.DefinitionRef.ObjectID, &v.DefinitionRef.SHA256, &v.LatestSnapshotID, &v.WorkspaceError, &v.LastSequence, &v.CreatedAt)
	v.DefinitionRef.Role = "definition"
	return
}
func (s *Store) RuntimeSession(ctx context.Context, actor, space, id string) (v run.Session, err error) {
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		var e error
		v, e = scanSession(tx.QueryRowContext(ctx, "SELECT "+sessionColumns+" FROM sessions s LEFT JOIN content_objects o ON o.space_id=s.space_id AND o.id=s.definition_ref WHERE s.space_id=? AND s.id=? AND s.definition_ref IS NOT NULL", space, id).Scan)
		return e
	})
	return
}
func (s *Store) RuntimeSessions(ctx context.Context, actor, space string, q identity.Query) (items []run.Session, err error) {
	items = []run.Session{}
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		rows, e := tx.QueryContext(ctx, "SELECT "+sessionColumns+" FROM sessions s JOIN content_objects o ON o.space_id=s.space_id AND o.id=s.definition_ref WHERE s.space_id=? AND s.id>? ORDER BY s.id LIMIT ?", space, q.After, q.Limit)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			v, e := scanSession(rows.Scan)
			if e != nil {
				return e
			}
			items = append(items, v)
		}
		return rows.Err()
	})
	return
}

const runColumns = "snapshot,status,cancel_requested,COALESCE(final_response_event_id,''),error_code,snapshot_error,COALESCE(workspace_snapshot_id,''),started_at,finished_at,heartbeat_at,COALESCE(owner_id,'')"

func runInTx(ctx context.Context, tx *sql.Tx, space, id string) (v run.View, err error) {
	var raw []byte
	err = tx.QueryRowContext(ctx, "SELECT "+runColumns+" FROM runs WHERE space_id=? AND id=? AND snapshot IS NOT NULL", space, id).Scan(&raw, &v.Status, &v.CancelRequested, &v.FinalResponseEventID, &v.Error, &v.SnapshotError, &v.WorkspaceSnapshotID, &v.StartedAt, &v.FinishedAt, &v.HeartbeatAt, &v.OwnerID)
	if err != nil {
		return v, err
	}
	err = json.Unmarshal(raw, &v.Snapshot)
	if err != nil {
		return v, err
	}
	var input, output, cacheRead, cacheWrite sql.NullInt64
	err = tx.QueryRowContext(ctx, "SELECT IF(COUNT(*)=COUNT(input_tokens),SUM(input_tokens),NULL),IF(COUNT(*)=COUNT(output_tokens),SUM(output_tokens),NULL),IF(COUNT(*)=COUNT(cache_read_tokens),SUM(cache_read_tokens),NULL),IF(COUNT(*)=COUNT(cache_write_tokens),SUM(cache_write_tokens),NULL) FROM model_attempts WHERE space_id=? AND run_id=?", space, id).Scan(&input, &output, &cacheRead, &cacheWrite)
	for _, pair := range []struct {
		n sql.NullInt64
		p **int64
	}{{input, &v.Usage.Input}, {output, &v.Usage.Output}, {cacheRead, &v.Usage.CacheRead}, {cacheWrite, &v.Usage.CacheWrite}} {
		if pair.n.Valid {
			n := pair.n.Int64
			*pair.p = &n
		}
	}
	return
}
func (s *Store) RuntimeRun(ctx context.Context, actor, space, id string) (v run.View, err error) {
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error { var e error; v, e = runInTx(ctx, tx, space, id); return e })
	return
}
func (s *Store) RuntimeMessages(ctx context.Context, actor, space, session string, after int64, limit int) (items []run.MessageView, err error) {
	items = []run.MessageView{}
	if after < 0 || limit < 1 || limit > 100 {
		return items, storage.ErrInvalid
	}
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		var exists string
		if e := tx.QueryRowContext(ctx, "SELECT id FROM sessions WHERE space_id=? AND id=?", space, session).Scan(&exists); e != nil {
			return e
		}
		rows, e := tx.QueryContext(ctx, "SELECT id,COALESCE(run_id,''),role,content_id,source_event_id,sequence,complete FROM messages WHERE space_id=? AND session_id=? AND sequence>? ORDER BY sequence LIMIT ?", space, session, after, limit)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var m run.MessageView
			if e = rows.Scan(&m.ID, &m.RunID, &m.Role, &m.ContentID, &m.SourceEventID, &m.Sequence, &m.Complete); e != nil {
				return e
			}
			items = append(items, m)
		}
		return rows.Err()
	})
	return
}
func (s *Store) CancelRuntimeRun(ctx context.Context, actor, space, id string) (v run.View, err error) {
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		var session string
		if e := tx.QueryRowContext(ctx, "SELECT session_id FROM runs WHERE space_id=? AND id=?", space, id).Scan(&session); e != nil {
			return e
		}
		var last int64
		if e := tx.QueryRowContext(ctx, "SELECT last_sequence FROM sessions WHERE space_id=? AND id=? FOR UPDATE", space, session).Scan(&last); e != nil {
			return e
		}
		var e error
		v, e = runInTx(ctx, tx, space, id)
		if e != nil {
			return e
		}
		if run.Terminal(v.Status) || v.CancelRequested {
			return nil
		}
		events := []journal.Proposed{journal.New(actor, "run.cancel.requested", id, struct{}{})}
		if v.Status == "queued" {
			events = append(events, journal.New(actor, "run.finished", id, journal.FinishData{Status: "cancelled", Error: "run_cancelled"}))
		}
		b, e := journal.Prepare(actor, journal.Request{SpaceID: space, SessionID: session, AppendKey: events[0].EventID, Events: events})
		if e != nil {
			return e
		}
		if _, e = appendInTx(ctx, tx, actor, b); e != nil {
			return e
		}
		v, e = runInTx(ctx, tx, space, id)
		return e
	})
	return
}
func (s *Store) RuntimeArtifacts(ctx context.Context, actor, space, id string) (items []run.Artifact, err error) {
	items = []run.Artifact{}
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		if _, e := runInTx(ctx, tx, space, id); e != nil {
			return e
		}
		rows, e := tx.QueryContext(ctx, "SELECT metadata FROM artifacts WHERE space_id=? AND run_id=? ORDER BY id", space, id)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			var v run.Artifact
			if e = rows.Scan(&raw); e != nil {
				return e
			}
			if e = json.Unmarshal(raw, &v); e != nil {
				return e
			}
			items = append(items, v)
		}
		return rows.Err()
	})
	return
}
func (s *Store) RuntimeArtifact(ctx context.Context, actor, space, id string) (v run.Artifact, err error) {
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		var raw []byte
		if e := tx.QueryRowContext(ctx, "SELECT metadata FROM artifacts WHERE space_id=? AND id=?", space, id).Scan(&raw); e != nil {
			return e
		}
		return json.Unmarshal(raw, &v)
	})
	return
}

func (s *Store) RuntimeRuns(ctx context.Context, actor, space, session string, q identity.Query) (items []run.View, err error) {
	items = []run.View{}
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		var exists string
		if e := tx.QueryRowContext(ctx, "SELECT id FROM sessions WHERE space_id=? AND id=?", space, session).Scan(&exists); e != nil {
			return e
		}
		rows, e := tx.QueryContext(ctx, "SELECT id FROM runs WHERE space_id=? AND session_id=? AND snapshot IS NOT NULL AND id>? ORDER BY id LIMIT ?", space, session, q.After, q.Limit)
		if e != nil {
			return e
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return e
			}
			ids = append(ids, id)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for _, id := range ids {
			v, e := runInTx(ctx, tx, space, id)
			if e != nil {
				return e
			}
			items = append(items, v)
		}
		return nil
	})
	return
}
func (s *Store) RuntimeTrace(ctx context.Context, actor, space, id string, after int64, through *int64, limit int) (page journal.Page, err error) {
	if after < 0 || limit < 1 || limit > 100 {
		return page, run.ErrCursor
	}
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		v, e := runInTx(ctx, tx, space, id)
		if e != nil {
			return e
		}
		var last int64
		if e = tx.QueryRowContext(ctx, "SELECT last_sequence FROM sessions WHERE space_id=? AND id=?", space, v.SessionID).Scan(&last); e != nil {
			return e
		}
		page.Through = last
		if through != nil {
			page.Through = *through
		}
		if page.Through > last || page.Through < after {
			return run.ErrCursor
		}
		items, e := readEvents(ctx, tx, space, v.SessionID, after, page.Through, limit+1, id)
		if e != nil {
			return e
		}
		page.HasMore = len(items) > limit
		if page.HasMore {
			items = items[:limit]
		}
		page.Events = items
		page.Next = after
		if len(items) > 0 {
			page.Next = items[len(items)-1].Sequence
		}
		return nil
	})
	return
}
func (s *Store) RuntimeCleanupPlan(ctx context.Context, actor, space, id string) (plan run.CleanupPlan, err error) {
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		var raw []byte
		if e := tx.QueryRowContext(ctx, "SELECT cleanup_plan FROM runs WHERE space_id=? AND id=?", space, id).Scan(&raw); e != nil {
			return e
		}
		if len(raw) == 0 {
			return storage.ErrWorkspace
		}
		return json.Unmarshal(raw, &plan)
	})
	return
}
func (s *Store) RuntimeSnapshots(ctx context.Context, actor, space, session string) (items []storage.Snapshot, err error) {
	items = []storage.Snapshot{}
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		var exists string
		if e := tx.QueryRowContext(ctx, "SELECT id FROM sessions WHERE space_id=? AND id=?", space, session).Scan(&exists); e != nil {
			return e
		}
		rows, e := tx.QueryContext(ctx, "SELECT "+snapshotColumns+" FROM workspace_snapshots WHERE space_id=? AND session_id=? ORDER BY created_at DESC LIMIT 100", space, session)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v storage.Snapshot
			if e = rows.Scan(&v.ID, &v.SpaceID, &v.SessionID, &v.SourceRunID, &v.BaseSnapshotID, &v.ObjectID, &v.ManifestRef, &v.URI, &v.SHA256, &v.FormatVersion); e != nil {
				return e
			}
			items = append(items, v)
		}
		return rows.Err()
	})
	return
}

func (s *Store) BeginWorkspaceMaintenance(ctx context.Context, actor, space, session string) error {
	return s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		var maintenance bool
		if e := tx.QueryRowContext(ctx, "SELECT workspace_maintenance FROM sessions WHERE space_id=? AND id=? FOR UPDATE", space, session).Scan(&maintenance); e != nil {
			return e
		}
		var n int
		if e := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM session_active_runs WHERE space_id=? AND session_id=?", space, session).Scan(&n); e != nil {
			return e
		}
		if n > 0 || maintenance {
			return storage.ErrActive
		}
		_, err := tx.ExecContext(ctx, "UPDATE sessions SET workspace_maintenance=TRUE,workspace_maintenance_actor=? WHERE space_id=? AND id=?", actor, space, session)
		return err
	})
}
func (s *Store) EndWorkspaceMaintenance(ctx context.Context, actor, space, session string) error {
	// Releasing the caller's coordination flag remains possible after revocation.
	return s.transaction(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE sessions SET workspace_maintenance=FALSE,workspace_maintenance_actor=NULL WHERE space_id=? AND id=? AND workspace_maintenance_actor=?", space, session, actor)
		return err
	})
}
func (s *Store) RuntimeLastRun(ctx context.Context, actor, space, session string) (v run.View, err error) {
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		var id string
		if err := tx.QueryRowContext(ctx, "SELECT id FROM runs WHERE space_id=? AND session_id=? AND snapshot IS NOT NULL ORDER BY created_at DESC,id DESC LIMIT 1", space, session).Scan(&id); err != nil {
			return err
		}
		var e error
		v, e = runInTx(ctx, tx, space, id)
		return e
	})
	return
}
