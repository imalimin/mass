package resource

import (
	"context"
	"encoding/json"
	"io"

	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
)

// WalkHistory validates a fixed journal boundary one page at a time. Returning
// io.EOF from visit ends an intentional prefix lookup, not a truncated history.
func WalkHistory(ctx context.Context, j *journal.Journal, actor, space, session string, visit func(journal.Event) error) (int64, error) {
	return walkHistory(ctx, j, actor, space, session, "", visit)
}

func walkHistory(ctx context.Context, j *journal.Journal, actor, space, session, runID string, visit func(journal.Event) error) (int64, error) {
	var through *int64
	var after int64
	for {
		var page journal.Page
		var err error
		if runID == "" {
			page, err = j.Read(ctx, actor, space, session, after, through, 100)
		} else {
			page, err = j.ReadRun(ctx, actor, space, session, runID, after, through, 100)
		}
		if err != nil {
			return 0, err
		}
		if through == nil {
			n := page.Through
			through = &n
		}
		if page.Through != *through || page.Next < after || page.Next > *through || page.HasMore && page.Next <= after {
			return 0, storage.ErrIntegrity
		}
		previous := after
		for _, e := range page.Events {
			if e.SpaceID != space || e.SessionID != session || e.Sequence <= previous || e.Sequence > *through || runID == "" && e.Sequence != previous+1 || runID != "" && e.RunID != runID {
				return 0, storage.ErrIntegrity
			}
			previous = e.Sequence
			if err = visit(e); err == io.EOF {
				return *through, nil
			} else if err != nil {
				return 0, err
			}
		}
		if len(page.Events) > 0 && previous > page.Next || runID == "" && previous != page.Next {
			return 0, storage.ErrIntegrity
		}
		after = page.Next
		if !page.HasMore {
			if runID == "" && after != *through {
				return 0, storage.ErrIntegrity
			}
			return *through, nil
		}
	}
}

// RunHistory excludes transport chunks and other runs before retaining facts.
// Its consumers request only the operation types needed by their own reducer.
func RunHistory(ctx context.Context, j *journal.Journal, actor, space, session, runID string, kinds ...string) ([]journal.Event, error) {
	allowed := map[string]bool{}
	for _, k := range kinds {
		allowed[k] = true
	}
	items := []journal.Event{}
	_, err := walkHistory(ctx, j, actor, space, session, runID, func(e journal.Event) error {
		if allowed[e.Type] {
			items = append(items, e)
		}
		return nil
	})
	return items, err
}

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

// History is a bounded compatibility helper for small full replays. Production
// execution and cleanup use paged scans or run-scoped reads instead.
func History(ctx context.Context, j *journal.Journal, actor, space, session string) ([]journal.Event, int64, error) {
	events := []journal.Event{}
	bytes := 0
	through, err := WalkHistory(ctx, j, actor, space, session, func(e journal.Event) error {
		raw, _ := json.Marshal(e)
		bytes += len(raw)
		if bytes > 32<<20 {
			return run.ErrOverflow
		}
		events = append(events, e)
		return nil
	})
	return events, through, err
}
func AppendRun(ctx context.Context, j *journal.Journal, s run.Snapshot, events ...journal.Proposed) ([]journal.Event, error) {
	if len(events) == 0 {
		return nil, storage.ErrInvalid
	}
	return j.Append(ctx, s.ActorID, journal.Request{SpaceID: s.SpaceID, SessionID: s.SessionID, AppendKey: events[0].EventID, Events: events})
}
