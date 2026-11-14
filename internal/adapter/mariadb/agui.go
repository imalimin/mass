package mariadb

import (
	"context"
	"database/sql"

	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/resource"
	"github.com/imalimin/mass/internal/transport/agui"
)

func (s *Store) LoadStream(ctx context.Context, actor, space, id string) (agui.Checkpoint, error) {
	var sessionID string
	err := s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT session_id FROM runs WHERE space_id=? AND id=? AND snapshot IS NOT NULL", space, id).Scan(&sessionID)
	})
	if err != nil {
		return agui.Checkpoint{}, err
	}
	cp := s.LiveUI.Checkpoint(space + "/" + id)
	if cp.SessionID == "" {
		cp.SessionID, cp.Profile, cp.Mapper, cp.Parser = sessionID, agui.Profile, 1, 1
	}
	return cp, nil
}

func (s *Store) CommitStream(_ context.Context, _, space, id string, old, next agui.Checkpoint, frames []agui.Frame) error {
	if !s.LiveUI.Commit(space+"/"+id, old, next, frames) {
		return run.ErrState
	}
	return nil
}

func (s *Store) ReadFrames(ctx context.Context, actor, space, id string, after int64, limit int) ([]agui.Frame, bool, error) {
	if after < 0 || limit < 1 || limit > 100 {
		return nil, false, run.ErrCursor
	}
	ui := &agui.Projector{Repo: s, Live: s.LiveUI, ResponseText: s.UIResponseText, ToolState: s.UIToolState}
	if _, err := ui.CatchUp(ctx, actor, space, id); err != nil {
		return nil, false, err
	}
	frames, closed, ok := s.LiveUI.Frames(space+"/"+id, after, limit)
	if !ok {
		return nil, false, run.ErrCursor
	}
	return frames, closed, nil
}

func (s *Store) UIResponseText(ctx context.Context, actor, space, contentID string) (string, error) {
	if s.UIContent == nil {
		return "", nil
	}
	var reply run.Reply
	err := resource.ReadJSON(ctx, s.UIContent, actor, space, contentID, 12<<20, &reply)
	return reply.Message.Content, err
}
