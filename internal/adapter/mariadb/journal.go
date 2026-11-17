package mariadb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"

	"github.com/imalimin/mass/internal/app/identity"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
)

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func (s *Store) AppendEvents(ctx context.Context, actor string, b journal.Batch) (result []journal.Event, err error) {
	if !s.systemScope {
		for _, e := range b.Events {
			if e.Actor.Kind != "user" {
				return nil, identity.ErrForbidden
			}
		}
	}
	err = s.scoped(ctx, actor, b.SpaceID, false, func(tx *sql.Tx) error {
		var e error
		result, e = appendInTx(ctx, tx, actor, b)
		return e
	})
	if err != nil {
		return nil, err
	}
	return
}
func createRun(ctx context.Context, tx *sql.Tx, actor, space, session string, e journal.Proposed, seq int64) error {
	var workspaceError string
	if err := tx.QueryRowContext(ctx, "SELECT workspace_error FROM sessions WHERE space_id=? AND id=?", space, session).Scan(&workspaceError); err != nil {
		return err
	}
	var data journal.RunData
	json.Unmarshal(e.Data, &data)
	if err := run.CheckWorkspaceSubmission(workspaceError, data.Snapshot != nil && data.Snapshot.WorkspaceCleanup); err != nil {
		return err
	}
	var active int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM session_active_runs WHERE space_id=? AND session_id=?", space, session).Scan(&active); err != nil {
		return err
	}
	if active > 0 {
		return storage.ErrActive
	}

	var raw any
	if data.Snapshot == nil {
		if _, err := tx.ExecContext(ctx, "INSERT INTO runs(space_id,session_id,id,actor_id,status,source_event_id,created_event_id,created_sequence,applied_sequence) VALUES(?,?,?,?,'queued',?,?,?,?)", space, session, e.RunID, actor, e.EventID, e.EventID, seq, seq); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO session_active_runs(space_id,session_id,run_id) VALUES(?,?,?)", space, session, e.RunID)
		return err
	}
	if data.Snapshot != nil {
		v := data.Snapshot
		var maintenance bool
		if err := tx.QueryRowContext(ctx, "SELECT workspace_maintenance FROM sessions WHERE space_id=? AND id=?", space, session).Scan(&maintenance); err != nil {
			return err
		}
		if maintenance {
			return storage.ErrActive
		}
		var revision int64
		var enabled bool
		if err := tx.QueryRowContext(ctx, "SELECT m.revision,m.enabled FROM model_configs m WHERE m.space_id=? AND m.id=?", space, v.Definition.Model.ID).Scan(&revision, &enabled); err != nil {
			return err
		}
		if revision != v.Definition.Model.Revision || !enabled {
			return identity.ErrConflict
		}
		var def, base sql.NullString
		if err := tx.QueryRowContext(ctx, "SELECT definition_ref,latest_workspace_snapshot_id FROM sessions WHERE space_id=? AND id=?", space, session).Scan(&def, &base); err != nil {
			return err
		}
		if v.SpaceID != space || v.SessionID != session || v.ActorID != actor || v.ID != e.RunID || v.DefinitionRef.ObjectID != def.String || v.BaseSnapshotID != base.String {
			return storage.ErrInvalid
		}
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages m WHERE m.space_id=? AND m.session_id=? AND m.source_event_id=? AND m.role='user' AND m.run_id IS NULL AND m.actor_id=?", space, session, v.UserEventID, actor).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return storage.ErrInvalid
		}
		var queued int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM runs WHERE snapshot IS NOT NULL AND status='queued'").Scan(&queued); err != nil {
			return err
		}
		if queued >= 100 {
			return run.ErrUnavailable
		}
		r, _ := json.Marshal(v)
		raw = r
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO runs(space_id,session_id,id,actor_id,status,source_event_id,created_event_id,created_sequence,applied_sequence,snapshot,created_at) VALUES(?,?,?,?,'queued',?,?,?,?,?,?)", space, session, e.RunID, actor, e.EventID, e.EventID, seq, seq, raw, e.OccurredAt)
	if err != nil {
		return err
	}
	if data.Snapshot != nil {
		if _, err = tx.ExecContext(ctx, "UPDATE messages SET run_id=? WHERE space_id=? AND source_event_id=?", e.RunID, space, data.Snapshot.UserEventID); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO session_active_runs(space_id,session_id,run_id) VALUES(?,?,?)", space, session, e.RunID)
	return err
}
func reduce(ctx context.Context, tx *sql.Tx, space, session string, e journal.Proposed, seq int64) error {
	switch e.Type {
	case "run.created":
		return createRun(ctx, tx, e.Actor.ID, space, session, e, seq)
	case "session.created":
		var d journal.SessionData
		json.Unmarshal(e.Data, &d)
		if d.DefinitionRef != nil {
			if !hasRef(e, "definition", d.DefinitionRef.ObjectID) {
				return storage.ErrInvalid
			}
			var kind string
			var archived, debug, defArchived bool
			if err := tx.QueryRowContext(ctx, "SELECT d.kind,v.archived,v.debug,d.archived FROM definition_versions v JOIN definitions d ON d.space_id=v.space_id AND d.id=v.definition_id WHERE v.space_id=? AND v.id=? AND v.definition_id=?", space, d.VersionID, d.DefinitionID).Scan(&kind, &archived, &debug, &defArchived); err != nil {
				return err
			}
			if kind != "agent" || archived || defArchived {
				return storage.ErrInvalid
			}
			if _, err := tx.ExecContext(ctx, "UPDATE sessions SET title=?,definition_id=?,version_id=?,definition_ref=?,created_at=? WHERE space_id=? AND id=?", d.Title, d.DefinitionID, d.VersionID, d.DefinitionRef.ObjectID, e.OccurredAt, space, session); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "UPDATE sessions SET created_event_id=?,created_at=?,source_snapshot_id=? WHERE space_id=? AND id=?", e.EventID, e.OccurredAt, nullable(d.SourceSnapshotID), space, session); err != nil {
			return err
		}
		if d.SourceSnapshotID != "" {
			if _, err := snapshotInTx(ctx, tx, space, d.SourceSnapshotID); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, "UPDATE sessions SET latest_workspace_snapshot_id=? WHERE space_id=? AND id=?", d.SourceSnapshotID, space, session)
			return err
		}
		return nil
	case "run.started":
		var d journal.StartData
		json.Unmarshal(e.Data, &d)
		var status string
		var cancelled bool
		if err := tx.QueryRowContext(ctx, "SELECT status,cancel_requested FROM runs WHERE space_id=? AND id=?", space, e.RunID).Scan(&status, &cancelled); err != nil {
			return err
		}
		if err := run.CheckTransition(status, cancelled, e.Type, "", false); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE runs SET status='running',owner_id=?,started_at=?,heartbeat_at=?,started_event_id=?,started_sequence=?,source_event_id=?,applied_sequence=? WHERE space_id=? AND id=?", nullable(d.OwnerID), e.OccurredAt, e.OccurredAt, e.EventID, seq, e.EventID, seq, space, e.RunID)
		return err
	case "run.finished":
		var d journal.FinishData
		json.Unmarshal(e.Data, &d)
		var status string
		var snapshot sql.NullString
		var cancelled bool
		var runtime sql.NullString
		if err := tx.QueryRowContext(ctx, "SELECT status,workspace_snapshot_id,cancel_requested,snapshot FROM runs WHERE space_id=? AND id=?", space, e.RunID).Scan(&status, &snapshot, &cancelled, &runtime); err != nil {
			return err
		}
		if d.Status == "succeeded" && cancelled {
			return run.ErrCancelled
		}
		if d.Status == "succeeded" && !snapshot.Valid {
			return storage.ErrWorkspace
		}
		if err := run.CheckTransition(status, cancelled, e.Type, d.Status, snapshot.Valid); err != nil {
			return err
		}
		if runtime.Valid && d.Status == "succeeded" {
			var purpose string
			if err := tx.QueryRowContext(ctx, "SELECT purpose FROM model_attempts WHERE space_id=? AND run_id=? AND response_event_id=?", space, e.RunID, d.FinalResponseEventID).Scan(&purpose); err != nil {
				return err
			}
			if purpose != "task" {
				return storage.ErrInvalid
			}
			var calls int
			if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM model_calls WHERE space_id=? AND run_id=? AND response_event_id=?", space, e.RunID, d.FinalResponseEventID).Scan(&calls); err != nil {
				return err
			}
			if calls > 0 {
				return storage.ErrInvalid
			}
		}
		if status == "running" && !snapshot.Valid {
			if _, err := tx.ExecContext(ctx, "UPDATE sessions SET workspace_error='workspace_snapshot_failed' WHERE space_id=? AND id=?", space, session); err != nil {
				return err
			}
		}
		var detail any
		if d.FailureDetail != nil {
			detail, _ = json.Marshal(d.FailureDetail)
		}
		if _, err := tx.ExecContext(ctx, "UPDATE runs SET status=?,source_event_id=?,applied_sequence=?,finished_at=?,error_code=?,snapshot_error=?,final_response_event_id=?,failure_detail=? WHERE space_id=? AND id=?", d.Status, e.EventID, seq, e.OccurredAt, d.Error, d.SnapshotError, nullable(d.FinalResponseEventID), detail, space, e.RunID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM session_active_runs WHERE space_id=? AND session_id=? AND run_id=?", space, session, e.RunID)
		return err
	case "workspace.snapshot.created":
		var d storage.Snapshot
		json.Unmarshal(e.Data, &d)
		return saveSnapshot(ctx, tx, space, session, e, d, seq)
	case "workspace.snapshot.failed":
		_, err := tx.ExecContext(ctx, "UPDATE sessions SET workspace_error='workspace_snapshot_failed' WHERE space_id=? AND id=?", space, session)
		return err
	case "workspace.restored":
		var d journal.RestoreData
		json.Unmarshal(e.Data, &d)
		var latest sql.NullString
		if err := tx.QueryRowContext(ctx, "SELECT latest_workspace_snapshot_id FROM sessions WHERE space_id=? AND id=?", space, session).Scan(&latest); err != nil {
			return err
		}
		if latest.String != d.SnapshotID {
			return storage.ErrConflict
		}
		var active int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM session_active_runs WHERE space_id=? AND session_id=?", space, session).Scan(&active); err != nil {
			return err
		}
		if active > 0 && e.RunID == "" {
			return storage.ErrActive
		}
		if e.RunID != "" {
			var id string
			if err := tx.QueryRowContext(ctx, "SELECT run_id FROM session_active_runs WHERE space_id=? AND session_id=?", space, session).Scan(&id); err != nil {
				return err
			}
			if id != e.RunID {
				return storage.ErrActive
			}
		}
		_, err := tx.ExecContext(ctx, "UPDATE sessions SET workspace_error='',restore_event_id=?,restore_sequence=?,restored_at=?,restored_by=?,restore_run_id=?,restore_snapshot_id=?,restore_reason=? WHERE space_id=? AND id=?", e.EventID, seq, e.OccurredAt, e.Actor.ID, nullable(e.RunID), nullable(d.SnapshotID), d.Reason, space, session)
		return err
	}
	return reduceRuntime(ctx, tx, space, session, e, seq)
}
func (s *Store) ReadEvents(ctx context.Context, actor, space, session string, after int64, through *int64, limit int) (page journal.Page, err error) {
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		var last int64
		if e := tx.QueryRowContext(ctx, "SELECT last_sequence FROM sessions WHERE space_id=? AND id=?", space, session).Scan(&last); e != nil {
			return e
		}
		page.Through = last
		if through != nil {
			if *through > last {
				return storage.ErrInvalid
			}
			page.Through = *through
		}
		if after > page.Through {
			return storage.ErrInvalid
		}
		events, e := readEvents(ctx, tx, space, session, after, page.Through, limit+1)
		if e != nil {
			return e
		}
		page.HasMore = len(events) > limit
		if page.HasMore {
			events = events[:limit]
		}
		page.Events = events
		page.Next = after
		if len(events) > 0 {
			page.Next = events[len(events)-1].Sequence
		}
		return nil
	})
	return
}

func appendInTx(ctx context.Context, tx *sql.Tx, actor string, b journal.Batch) (result []journal.Event, err error) {
	var last int64
	e := tx.QueryRowContext(ctx, "SELECT last_sequence FROM sessions WHERE space_id=? AND id=? FOR UPDATE", b.SpaceID, b.SessionID).Scan(&last)
	if errors.Is(e, sql.ErrNoRows) {
		if b.Events[0].Type != "session.created" {
			return nil, storage.ErrInvalid
		}
		_, e = tx.ExecContext(ctx, "INSERT INTO sessions(space_id,id,actor_id) VALUES(?,?,?)", b.SpaceID, b.SessionID, actor)
	}
	if e != nil {
		return nil, e
	}
	var digest, session string
	var first int64
	var count int
	e = tx.QueryRowContext(ctx, "SELECT digest,session_id,first_sequence,event_count FROM journal_appends WHERE space_id=? AND ((session_id=? AND append_key=?) OR first_event_id=?) ORDER BY (session_id=? AND append_key=?) DESC LIMIT 1", b.SpaceID, b.SessionID, b.AppendKey, b.Events[0].EventID, b.SessionID, b.AppendKey).Scan(&digest, &session, &first, &count)
	if e == nil {
		if digest != b.Digest || session != b.SessionID || count != len(b.Events) {
			return nil, storage.ErrConflict
		}
		return appliedEvents(b, first), nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return nil, e
	}
	if b.ExpectedSequence != nil && *b.ExpectedSequence != last || last > math.MaxInt64-int64(len(b.Events)) {
		return nil, storage.ErrConflict
	}
	first = last + 1
	for i, event := range b.Events {
		seq := first + int64(i)
		if event.Type == "session.created" && seq != 1 {
			return nil, storage.ErrConflict
		}
		if event.Type != "run.created" && event.RunID != "" {
			var id string
			if e = tx.QueryRowContext(ctx, "SELECT id FROM runs WHERE space_id=? AND session_id=? AND id=? FOR UPDATE", b.SpaceID, b.SessionID, event.RunID).Scan(&id); e != nil {
				return nil, e
			}
		}
		for _, ref := range event.Refs {
			var hash string
			if e = tx.QueryRowContext(ctx, "SELECT sha256 FROM content_objects WHERE space_id=? AND id=? AND state='ready'", b.SpaceID, ref.ObjectID).Scan(&hash); e != nil {
				return nil, e
			}
			if hash != ref.SHA256 {
				return nil, storage.ErrIntegrity
			}
		}
		if e = reduce(ctx, tx, b.SpaceID, b.SessionID, event, seq); e != nil {
			return nil, e
		}
	}
	if _, e = tx.ExecContext(ctx, "UPDATE sessions SET last_sequence=? WHERE space_id=? AND id=?", first+int64(len(b.Events))-1, b.SpaceID, b.SessionID); e != nil {
		return nil, e
	}
	_, e = tx.ExecContext(ctx, "INSERT INTO journal_appends(space_id,session_id,append_key,digest,first_sequence,event_count,first_event_id) VALUES(?,?,?,?,?,?,?)", b.SpaceID, b.SessionID, b.AppendKey, b.Digest, first, len(b.Events), b.Events[0].EventID)
	return appliedEvents(b, first), e
}

func appliedEvents(b journal.Batch, first int64) []journal.Event {
	result := make([]journal.Event, len(b.Events))
	for i, event := range b.Events {
		result[i] = journal.Event{Proposed: event, SpaceID: b.SpaceID, SessionID: b.SessionID, Sequence: first + int64(i), RecordedAt: event.OccurredAt}
	}
	return result
}
