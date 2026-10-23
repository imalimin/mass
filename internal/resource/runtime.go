package resource

import (
	"context"
	"encoding/json"
	"io"

	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
)

func ReadBytes(ctx context.Context, content *ContentStore, actor, space, id string, max int64) ([]byte, error) {
	_, f, err := content.Read(ctx, actor, space, id)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > max {
		return nil, run.ErrOverflow
	}
	return raw, nil
}
func ReadJSON(ctx context.Context, content *ContentStore, actor, space, id string, max int64, v any) error {
	raw, err := ReadBytes(ctx, content, actor, space, id, max)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(raw, v); err != nil {
		return storage.ErrIntegrity
	}
	return nil
}
func History(ctx context.Context, j *journal.Journal, actor, space, session string) ([]journal.Event, int64, error) {
	var events []journal.Event
	after := int64(0)
	var through *int64
	for {
		page, err := j.Read(ctx, actor, space, session, after, through, 100)
		if err != nil {
			return nil, 0, err
		}
		if through == nil {
			n := page.Through
			through = &n
		}
		events = append(events, page.Events...)
		if len(events) > 20000 {
			return nil, 0, run.ErrOverflow
		}
		after = page.Next
		if !page.HasMore {
			return events, *through, nil
		}
	}
}
func AppendRun(ctx context.Context, j *journal.Journal, s run.Snapshot, events ...journal.Proposed) ([]journal.Event, error) {
	if len(events) == 0 {
		return nil, storage.ErrInvalid
	}
	return j.Append(ctx, s.ActorID, journal.Request{SpaceID: s.SpaceID, SessionID: s.SessionID, AppendKey: events[0].EventID, Events: events})
}
