package mariadb

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/journal"
	"github.com/imalimin/mass/internal/transport/agui"
)

func (s *Store) LoadStream(ctx context.Context, actor, space, id string) (v agui.Checkpoint, err error) {
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		var raw []byte
		err := tx.QueryRowContext(ctx, "SELECT session_id,source_sequence,last_ui_sequence,protocol_profile,mapper_version,parser_version,closed,state FROM agui_streams WHERE space_id=? AND run_id=?", space, id).Scan(&v.SessionID, &v.Source, &v.Cursor, &v.Profile, &v.Mapper, &v.Parser, &v.Closed, &raw)
		if err != nil {
			return err
		}
		return json.Unmarshal(raw, &v.State)
	})
	return
}
func (s *Store) CommitStream(ctx context.Context, actor, space, id string, old, next agui.Checkpoint, frames []agui.Frame) error {
	return s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		var cursor, source int64
		var closed bool
		if err := tx.QueryRowContext(ctx, "SELECT last_sequence FROM sessions WHERE space_id=? AND id=? FOR UPDATE", space, old.SessionID).Scan(&source); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, "SELECT source_sequence,last_ui_sequence,closed FROM agui_streams WHERE space_id=? AND run_id=? FOR UPDATE", space, id).Scan(&source, &cursor, &closed); err != nil {
			return err
		}
		if source != old.Source || cursor != old.Cursor || closed {
			return run.ErrState
		}
		for _, frame := range frames {
			cursor++
			if frame.Sequence != cursor {
				return run.ErrCursor
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO agui_events(space_id,run_id,ui_sequence,source_event_id,source_ordinal,event_json) VALUES(?,?,?,?,?,?)", space, id, frame.Sequence, frame.SourceID, frame.Ordinal, frame.JSON); err != nil {
				return err
			}
		}
		raw, _ := json.Marshal(next.State)
		_, err := tx.ExecContext(ctx, "UPDATE agui_streams SET source_sequence=?,last_ui_sequence=?,state=?,closed=? WHERE space_id=? AND run_id=?", next.Source, next.Cursor, raw, next.Closed, space, id)
		return err
	})
}
func (s *Store) ReadFrames(ctx context.Context, actor, space, id string, after int64, limit int) (items []agui.Frame, closed bool, err error) {
	items = []agui.Frame{}
	if after < 0 || limit < 1 || limit > 100 {
		return nil, false, run.ErrCursor
	}
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		var last int64
		if e := tx.QueryRowContext(ctx, "SELECT last_ui_sequence,closed FROM agui_streams WHERE space_id=? AND run_id=?", space, id).Scan(&last, &closed); e != nil {
			return e
		}
		if after > last {
			return run.ErrCursor
		}
		rows, e := tx.QueryContext(ctx, "SELECT ui_sequence,source_event_id,source_ordinal,event_json FROM agui_events WHERE space_id=? AND run_id=? AND ui_sequence>? ORDER BY ui_sequence LIMIT ?", space, id, after, limit)
		if e != nil {
			return e
		}
		defer rows.Close()
		expected := after + 1
		for rows.Next() {
			var f agui.Frame
			if e = rows.Scan(&f.Sequence, &f.SourceID, &f.Ordinal, &f.JSON); e != nil {
				return e
			}
			if f.Sequence != expected {
				return run.ErrCursor
			}
			expected++
			items = append(items, f)
		}
		if len(items) == 0 && after < last {
			return run.ErrCursor
		}
		closed = closed && (expected-1 == last)
		return rows.Err()
	})
	return
}

type UIWork struct{ Actor, Space, Run string }

func (l *RuntimeLease) UIWork(ctx context.Context) (items []UIWork, err error) {
	if err = l.Check(ctx); err != nil {
		return nil, err
	}
	rows, err := l.store.DB.QueryContext(ctx, "SELECT r.actor_id,a.space_id,a.run_id FROM agui_streams a JOIN runs r ON r.space_id=a.space_id AND r.id=a.run_id JOIN sessions s ON s.space_id=a.space_id AND s.id=a.session_id WHERE a.closed=FALSE AND a.source_sequence<s.last_sequence ORDER BY r.created_at LIMIT 100")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item UIWork
		if err = rows.Scan(&item.Actor, &item.Space, &item.Run); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// A read/derived-view capability for the runtime projector, without fact writes.
type UIRepository struct{ store *Store }

func (l *RuntimeLease) UIRepository() *UIRepository {
	s := &Store{DB: l.store.DB}
	s.scopeOverride = func(ctx context.Context, tx *sql.Tx, actor, space string, owner bool) error {
		if !l.alive.Load() {
			return run.ErrUnavailable
		}
		var id string
		return tx.QueryRowContext(ctx, "SELECT id FROM spaces WHERE id=? FOR UPDATE", space).Scan(&id)
	}
	return &UIRepository{store: s}
}
func (r *UIRepository) LoadStream(ctx context.Context, a, s, id string) (agui.Checkpoint, error) {
	return r.store.LoadStream(ctx, a, s, id)
}
func (r *UIRepository) CommitStream(ctx context.Context, a, s, id string, old, next agui.Checkpoint, f []agui.Frame) error {
	return r.store.CommitStream(ctx, a, s, id, old, next, f)
}
func (r *UIRepository) ReadEvents(ctx context.Context, a, s, id string, after int64, through *int64, limit int) (journal.Page, error) {
	return r.store.ReadEvents(ctx, a, s, id, after, through, limit)
}
func (r *UIRepository) ReadFrames(ctx context.Context, a, s, id string, after int64, limit int) ([]agui.Frame, bool, error) {
	return r.store.ReadFrames(ctx, a, s, id, after, limit)
}
