package mariadb

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
	"github.com/imalimin/mass/internal/transport/agui"
)

// RebuildViews is an offline operation. It holds the same connection lock as
// execution, validates facts/objects first, and invokes no model or tool code.
// Run identity rows remain because immutable events and idempotency keys refer
// to them; all mutable run/message/attempt/tool/artifact/UI columns are replayed.
func (s *Store) RebuildViews(ctx context.Context, verify func(context.Context, storage.Object) error) error {
	if verify == nil {
		return storage.ErrIntegrity
	}
	lease, err := s.OpenRuntime(ctx)
	if err != nil {
		return err
	}
	defer lease.Close()
	type sessionFacts struct {
		space, id, actor string
		events           []journal.Event
	}
	facts := []sessionFacts{}
	rows, err := s.DB.QueryContext(ctx, "SELECT space_id,id,actor_id FROM sessions ORDER BY space_id,id")
	if err != nil {
		return err
	}
	for rows.Next() {
		var item sessionFacts
		if err = rows.Scan(&item.space, &item.id, &item.actor); err != nil {
			rows.Close()
			return err
		}
		facts = append(facts, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	seenObjects := map[string]bool{}
	for i := range facts {
		item := &facts[i]
		if err = s.transaction(ctx, func(tx *sql.Tx) error {
			var last int64
			if err := tx.QueryRowContext(ctx, "SELECT last_sequence FROM sessions WHERE space_id=? AND id=?", item.space, item.id).Scan(&last); err != nil {
				return err
			}
			var e error
			item.events, e = readEvents(ctx, tx, item.space, item.id, 0, last, 1000000)
			if e != nil {
				return e
			}
			if len(item.events) != int(last) {
				return storage.ErrConflict
			}
			return nil
		}); err != nil {
			return err
		}
		for _, e := range item.events {
			if err = journal.Validate(e.Proposed); err != nil {
				return err
			}
			var digest string
			if err = s.DB.QueryRowContext(ctx, "SELECT event_digest FROM runtime_events WHERE space_id=? AND event_id=?", item.space, e.EventID).Scan(&digest); err != nil {
				return err
			}
			if digest != journal.Digest(e.Proposed) {
				return storage.ErrIntegrity
			}
			for _, ref := range e.Refs {
				key := item.space + ":" + ref.ObjectID
				if seenObjects[key] {
					continue
				}
				o, err := scanObject(s.DB.QueryRowContext(ctx, "SELECT "+objectColumns+" FROM content_objects WHERE space_id=? AND id=? AND state='ready'", item.space, ref.ObjectID).Scan)
				if err != nil {
					return err
				}
				if o.SHA256 != ref.SHA256 {
					return storage.ErrIntegrity
				}
				if err = verify(ctx, o); err != nil {
					return err
				}
				seenObjects[key] = true
			}
		}
	}
	if err = lease.Check(ctx); err != nil {
		return err
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		// The runtime lock is already held; no application service runs concurrently.
		for _, table := range []string{"agui_events", "agui_streams", "run_plans", "artifacts", "tool_executions", "model_calls", "model_attempts", "messages", "session_active_runs"} {
			if _, err := tx.ExecContext(ctx, "DELETE FROM "+table); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "UPDATE runs SET status='queued',cancel_requested=FALSE,owner_id=NULL,started_at=NULL,heartbeat_at=NULL,finished_at=NULL,final_response_event_id=NULL,error_code='',snapshot_error='',workspace_snapshot_id=NULL"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE sessions SET workspace_error='',workspace_maintenance=FALSE,workspace_maintenance_actor=NULL"); err != nil {
			return err
		}
		for _, item := range facts {
			for _, event := range item.events {
				e, seq := event.Proposed, event.Sequence
				switch e.Type {
				case "session.created":
					var d journal.SessionData
					journal.Decode(e.Data, &d)
					_, err = tx.ExecContext(ctx, "UPDATE sessions SET latest_workspace_snapshot_id=?,workspace_error='' WHERE space_id=? AND id=?", nullable(d.SourceSnapshotID), item.space, item.id)
				case "run.created":
					var d journal.RunData
					journal.Decode(e.Data, &d)
					var snapshot any
					if d.Snapshot != nil {
						snapshot, _ = json.Marshal(d.Snapshot)
					}
					_, err = tx.ExecContext(ctx, "UPDATE runs SET status='queued',snapshot=?,created_at=?,source_event_id=?,applied_sequence=? WHERE space_id=? AND id=?", snapshot, e.OccurredAt, e.EventID, seq, item.space, e.RunID)
					if err != nil {
						return err
					}
					_, err = tx.ExecContext(ctx, "INSERT INTO session_active_runs(space_id,session_id,run_id) VALUES(?,?,?)", item.space, item.id, e.RunID)
					if err != nil {
						return err
					}
					if d.Snapshot != nil {
						_, err = tx.ExecContext(ctx, "UPDATE messages SET run_id=? WHERE space_id=? AND source_event_id=?", e.RunID, item.space, d.Snapshot.UserEventID)
						if err != nil {
							return err
						}
						_, err = tx.ExecContext(ctx, "INSERT INTO agui_streams(space_id,session_id,run_id,protocol_profile,mapper_version,parser_version,state) VALUES(?,?,?,'mass-agui-v1',1,1,'{}')", item.space, item.id, e.RunID)
					}
				case "workspace.snapshot.created":
					var d storage.Snapshot
					journal.Decode(e.Data, &d)
					// Immutable snapshot index rows retain FKs from other sessions. Rebuild its pointers.
					var source string
					if err = tx.QueryRowContext(ctx, "SELECT source_event_id FROM workspace_snapshots WHERE space_id=? AND id=?", item.space, d.ID).Scan(&source); err != nil {
						return err
					}
					if source != e.EventID {
						return storage.ErrIntegrity
					}
					_, err = tx.ExecContext(ctx, "UPDATE runs SET workspace_snapshot_id=? WHERE space_id=? AND id=?", d.ID, item.space, e.RunID)
					if err != nil {
						return err
					}
					_, err = tx.ExecContext(ctx, "UPDATE sessions SET latest_workspace_snapshot_id=?,workspace_error='' WHERE space_id=? AND id=?", d.ID, item.space, item.id)
				case "workspace.restored":
					_, err = tx.ExecContext(ctx, "UPDATE sessions SET workspace_error='' WHERE space_id=? AND id=?", item.space, item.id)
				default:
					err = reduce(ctx, tx, item.space, item.id, e, seq)
				}
				if err != nil {
					return err
				}
			}
			runtimeRuns := map[string]bool{}
			for _, e := range item.events {
				if e.Type == "run.created" {
					var d journal.RunData
					journal.Decode(e.Data, &d)
					if d.Snapshot != nil {
						runtimeRuns[e.RunID] = true
					}
				}
			}
			for id := range runtimeRuns {
				cp := agui.Checkpoint{SessionID: item.id, Profile: agui.Profile, Mapper: 1, Parser: 1}
				next, frames, err := agui.Map(cp, id, item.events)
				if err != nil {
					return err
				}
				raw, _ := json.Marshal(next.State)
				for _, frame := range frames {
					if _, err = tx.ExecContext(ctx, "INSERT INTO agui_events(space_id,run_id,ui_sequence,source_event_id,source_ordinal,event_json) VALUES(?,?,?,?,?,?)", item.space, id, frame.Sequence, frame.SourceID, frame.Ordinal, frame.JSON); err != nil {
						return err
					}
				}
				if _, err = tx.ExecContext(ctx, "UPDATE agui_streams SET source_sequence=?,last_ui_sequence=?,state=?,closed=? WHERE space_id=? AND run_id=?", next.Source, next.Cursor, raw, next.Closed, item.space, id); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
