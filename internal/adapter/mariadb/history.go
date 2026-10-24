package mariadb

import (
	"context"
	"database/sql"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
)

func (s *Store) ReadRunEvents(ctx context.Context, actor, space, session, runID string, after int64, through *int64, limit int) (page journal.Page, err error) {
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		var actual string
		if e := tx.QueryRowContext(ctx, "SELECT session_id FROM runs WHERE space_id=? AND id=?", space, runID).Scan(&actual); e != nil {
			return e
		}
		if actual != session {
			return storage.ErrInvalid
		}
		var last int64
		if e := tx.QueryRowContext(ctx, "SELECT last_sequence FROM sessions WHERE space_id=? AND id=?", space, session).Scan(&last); e != nil {
			return e
		}
		page.Through = last
		if through != nil {
			page.Through = *through
		}
		if after < 0 || page.Through < after || page.Through > last || limit < 1 || limit > 100 {
			return storage.ErrInvalid
		}
		items, e := readEvents(ctx, tx, space, session, after, page.Through, limit+1, runID)
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

func (c *CleanupRepository) ReadRunEvents(ctx context.Context, actor, space, session, runID string, after int64, through *int64, limit int) (journal.Page, error) {
	if space != c.snapshot.SpaceID || session != c.snapshot.SessionID || runID != c.snapshot.ID {
		return journal.Page{}, storage.ErrInvalid
	}
	return c.store.ReadRunEvents(ctx, actor, space, session, runID, after, through, limit)
}
