package mariadb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

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
	err = s.scoped(ctx, actor, b.SpaceID, false, func(tx *sql.Tx) error {
		var last int64
		e := tx.QueryRowContext(ctx, "SELECT last_sequence FROM sessions WHERE space_id=? AND id=? FOR UPDATE", b.SpaceID, b.SessionID).Scan(&last)
		if errors.Is(e, sql.ErrNoRows) {
			if b.Events[0].Type != "session.created" {
				return storage.ErrInvalid
			}
			_, e = tx.ExecContext(ctx, "INSERT INTO sessions(space_id,id,actor_id) VALUES(?,?,?)", b.SpaceID, b.SessionID, actor)
		}
		if e != nil {
			return e
		}
		var digest string
		var first int64
		var count int
		e = tx.QueryRowContext(ctx, "SELECT digest,first_sequence,event_count FROM journal_appends WHERE space_id=? AND session_id=? AND append_key=?", b.SpaceID, b.SessionID, b.AppendKey).Scan(&digest, &first, &count)
		if e == nil {
			if digest != b.Digest {
				return storage.ErrConflict
			}
			result, e = readEvents(ctx, tx, b.SpaceID, b.SessionID, first-1, first+int64(count)-1, count)
			return e
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		existing := 0
		for i, event := range b.Events {
			var stored, session string
			var seq int64
			e = tx.QueryRowContext(ctx, "SELECT event_digest,session_id,sequence FROM runtime_events WHERE space_id=? AND event_id=?", b.SpaceID, event.EventID).Scan(&stored, &session, &seq)
			if errors.Is(e, sql.ErrNoRows) {
				continue
			}
			if e != nil {
				return e
			}
			if stored != b.Digests[i] || session != b.SessionID {
				return storage.ErrConflict
			}
			if i == 0 {
				first = seq
			}
			if seq != first+int64(i) {
				return storage.ErrConflict
			}
			existing++
		}
		if existing > 0 {
			if existing != len(b.Events) {
				return storage.ErrConflict
			}
			result, e = readEvents(ctx, tx, b.SpaceID, b.SessionID, first-1, first+int64(existing)-1, existing)
			if e != nil {
				return e
			}
		} else {
			if b.ExpectedSequence != nil && *b.ExpectedSequence != last {
				return storage.ErrConflict
			}
			if last > math.MaxInt64-int64(len(b.Events)) {
				return storage.ErrConflict
			}
			first = last + 1
			for i, event := range b.Events {
				seq := first + int64(i)
				if event.Type == "session.created" && seq != 1 {
					return storage.ErrConflict
				}
				if event.CausationID != "" {
					var n int64
					if e = tx.QueryRowContext(ctx, "SELECT sequence FROM runtime_events WHERE space_id=? AND session_id=? AND event_id=?", b.SpaceID, b.SessionID, event.CausationID).Scan(&n); e != nil {
						return e
					}
					if n >= seq {
						return storage.ErrInvalid
					}
				}
				if event.Type == "run.created" {
					if e = createRun(ctx, tx, actor, b.SpaceID, b.SessionID, event, seq); e != nil {
						return e
					}
				} else if event.RunID != "" {
					var id string
					if e = tx.QueryRowContext(ctx, "SELECT id FROM runs WHERE space_id=? AND session_id=? AND id=? FOR UPDATE", b.SpaceID, b.SessionID, event.RunID).Scan(&id); e != nil {
						return e
					}
				}
				for _, ref := range event.Refs {
					var hash string
					if e = tx.QueryRowContext(ctx, "SELECT sha256 FROM content_objects WHERE space_id=? AND id=? AND state='ready'", b.SpaceID, ref.ObjectID).Scan(&hash); e != nil {
						return e
					}
					if hash != ref.SHA256 {
						return storage.ErrIntegrity
					}
				}
				recorded := time.Now().UTC().Truncate(time.Microsecond)
				_, e = tx.ExecContext(ctx, "INSERT INTO runtime_events(space_id,session_id,event_id,sequence,run_id,event_type,schema_version,operation_id,causation_event_id,correlation_id,actor_kind,actor_id,occurred_at,recorded_at,data,event_digest) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)", b.SpaceID, b.SessionID, event.EventID, seq, nullable(event.RunID), event.Type, event.SchemaVersion, nullable(event.OperationID), nullable(event.CausationID), nullable(event.CorrelationID), event.Actor.Kind, event.Actor.ID, event.OccurredAt, recorded, string(event.Data), b.Digests[i])
				if e != nil {
					return e
				}
				for _, ref := range event.Refs {
					if _, e = tx.ExecContext(ctx, "INSERT INTO event_objects(space_id,event_id,object_id,role,ordinal) VALUES(?,?,?,?,?)", b.SpaceID, event.EventID, ref.ObjectID, ref.Role, ref.Ordinal); e != nil {
						return e
					}
				}
				if e = reduce(ctx, tx, b.SpaceID, b.SessionID, event, seq); e != nil {
					return e
				}
				result = append(result, journal.Event{Proposed: event, SpaceID: b.SpaceID, SessionID: b.SessionID, Sequence: seq, RecordedAt: recorded})
			}
			if _, e = tx.ExecContext(ctx, "UPDATE sessions SET last_sequence=? WHERE space_id=? AND id=?", first+int64(len(b.Events))-1, b.SpaceID, b.SessionID); e != nil {
				return e
			}
		}
		_, e = tx.ExecContext(ctx, "INSERT INTO journal_appends(space_id,session_id,append_key,digest,first_sequence,event_count) VALUES(?,?,?,?,?,?)", b.SpaceID, b.SessionID, b.AppendKey, b.Digest, first, len(b.Events))
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
	if workspaceError != "" {
		return storage.ErrWorkspace
	}
	var active int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM session_active_runs WHERE space_id=? AND session_id=?", space, session).Scan(&active); err != nil {
		return err
	}
	if active > 0 {
		return storage.ErrActive
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO runs(space_id,session_id,id,actor_id,status,source_event_id,applied_sequence) VALUES(?,?,?,?,'queued',?,?)", space, session, e.RunID, actor, e.EventID, seq)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO session_active_runs(space_id,session_id,run_id) VALUES(?,?,?)", space, session, e.RunID)
	return err
}
func reduce(ctx context.Context, tx *sql.Tx, space, session string, e journal.Proposed, seq int64) error {
	switch e.Type {
	case "session.created":
		var d journal.SessionData
		json.Unmarshal(e.Data, &d)
		if d.SourceSnapshotID != "" {
			if _, err := snapshotInTx(ctx, tx, space, d.SourceSnapshotID); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, "UPDATE sessions SET latest_workspace_snapshot_id=? WHERE space_id=? AND id=?", d.SourceSnapshotID, space, session)
			return err
		}
	case "run.started":
		result, err := tx.ExecContext(ctx, "UPDATE runs SET status='running',source_event_id=?,applied_sequence=? WHERE space_id=? AND session_id=? AND id=? AND status='queued'", e.EventID, seq, space, session, e.RunID)
		if err != nil {
			return err
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			return storage.ErrConflict
		}
	case "run.finished":
		var d journal.FinishData
		json.Unmarshal(e.Data, &d)
		var status string
		var snapshot sql.NullString
		if err := tx.QueryRowContext(ctx, "SELECT status,workspace_snapshot_id FROM runs WHERE space_id=? AND id=?", space, e.RunID).Scan(&status, &snapshot); err != nil {
			return err
		}
		if status != "queued" && status != "running" {
			return storage.ErrConflict
		}
		if d.Status == "succeeded" && (status != "running" || !snapshot.Valid) {
			return storage.ErrWorkspace
		}
		if status == "running" && !snapshot.Valid {
			if _, err := tx.ExecContext(ctx, "UPDATE sessions SET workspace_error='workspace_snapshot_failed' WHERE space_id=? AND id=?", space, session); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "UPDATE runs SET status=?,source_event_id=?,applied_sequence=? WHERE space_id=? AND id=?", d.Status, e.EventID, seq, space, e.RunID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM session_active_runs WHERE space_id=? AND session_id=? AND run_id=?", space, session, e.RunID)
		return err
	case "workspace.snapshot.created":
		var d storage.Snapshot
		json.Unmarshal(e.Data, &d)
		return saveSnapshot(ctx, tx, space, session, e, d)
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
		if active > 0 {
			return storage.ErrActive
		}
		_, err := tx.ExecContext(ctx, "UPDATE sessions SET workspace_error='' WHERE space_id=? AND id=?", space, session)
		return err
	}
	return nil
}
func readEvents(ctx context.Context, tx *sql.Tx, space, session string, after, through int64, limit int) ([]journal.Event, error) {
	rows, err := tx.QueryContext(ctx, "SELECT event_id,COALESCE(run_id,''),sequence,event_type,schema_version,COALESCE(operation_id,''),COALESCE(causation_event_id,''),COALESCE(correlation_id,''),actor_kind,actor_id,occurred_at,recorded_at,data FROM runtime_events WHERE space_id=? AND session_id=? AND sequence>? AND sequence<=? ORDER BY sequence LIMIT ?", space, session, after, through, limit)
	if err != nil {
		return nil, err
	}
	events := []journal.Event{}
	for rows.Next() {
		e := journal.Event{SpaceID: space, SessionID: session}
		if err = rows.Scan(&e.EventID, &e.RunID, &e.Sequence, &e.Type, &e.SchemaVersion, &e.OperationID, &e.CausationID, &e.CorrelationID, &e.Actor.Kind, &e.Actor.ID, &e.OccurredAt, &e.RecordedAt, &e.Data); err != nil {
			rows.Close()
			return nil, err
		}
		events = append(events, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range events {
		refs, e := tx.QueryContext(ctx, "SELECT e.role,e.ordinal,e.object_id,o.sha256 FROM event_objects e JOIN content_objects o ON o.space_id=e.space_id AND o.id=e.object_id WHERE e.space_id=? AND e.event_id=? ORDER BY e.role,e.ordinal", space, events[i].EventID)
		if e != nil {
			return nil, e
		}
		events[i].Refs = []storage.Ref{}
		for refs.Next() {
			var r storage.Ref
			if e = refs.Scan(&r.Role, &r.Ordinal, &r.ObjectID, &r.SHA256); e != nil {
				refs.Close()
				return nil, e
			}
			events[i].Refs = append(events[i].Refs, r)
		}
		e = refs.Err()
		refs.Close()
		if e != nil {
			return nil, e
		}
	}
	return events, nil
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
