package projector

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
	"github.com/imalimin/mass/internal/resource"
)

const MaxHistoryBytes = 16 << 20

func fold(previous string, e journal.Event) string {
	return journal.Digest([]string{previous, journal.Digest(e.Proposed)})
}

// ReadHistory validates every source event at a fixed boundary, retaining only
// semantic facts. Capacity belongs to one projection, never the cleanup path.
type selection struct {
	snapshot run.Snapshot
	items    []journal.Event
	ranges   []run.ContextRange
	bytes    int
	unsaved  bool
}

func (c *selection) add(e journal.Event) error {
	keep := false
	switch e.Type {
	case "session.created", "user.message.accepted", "context.projected", "context.summary.created", "model.attempt.started", "model.response.completed", "tool.execution.completed", "run.finished", "workspace.snapshot.created":
		keep = true
	case "run.created":
		keep = e.RunID == c.snapshot.ID
	case "workspace.restored":
		keep = c.unsaved
	}
	if e.Type == "workspace.snapshot.created" {
		c.unsaved = false
	}
	if e.Type == "run.finished" {
		var d journal.FinishData
		journal.Decode(e.Data, &d)
		c.unsaved = c.unsaved || d.SnapshotError != ""
	}
	if keep {
		raw, _ := json.Marshal(e)
		c.bytes += len(raw)
		c.items = append(c.items, e)
	} else {
		if len(c.ranges) == 0 || c.ranges[len(c.ranges)-1].Through != e.Sequence-1 {
			c.ranges = append(c.ranges, run.ContextRange{From: e.Sequence, Through: e.Sequence - 1, Reason: "audit_only"})
			c.bytes += 256
		}
		r := &c.ranges[len(c.ranges)-1]
		r.Through = e.Sequence
		r.Digest = fold(r.Digest, e)
	}
	if c.bytes > MaxHistoryBytes {
		return run.ErrOverflow
	}
	return nil
}
func ReadHistory(ctx context.Context, j *journal.Journal, s run.Snapshot) ([]journal.Event, []run.ContextRange, int64, error) {
	c := selection{snapshot: s, items: []journal.Event{}, ranges: []run.ContextRange{}}
	through, err := resource.WalkHistory(ctx, j, s.ActorID, s.SpaceID, s.SessionID, c.add)
	return c.items, c.ranges, through, err
}
func compactHistory(ctx context.Context, s run.Snapshot, events []journal.Event, through int64) ([]journal.Event, []run.ContextRange, error) {
	c := selection{snapshot: s, items: []journal.Event{}, ranges: []run.ContextRange{}}
	var previous int64
	for _, e := range events {
		if e.Sequence > through {
			break
		}
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if e.Sequence <= previous || e.SpaceID != s.SpaceID || e.SessionID != s.SessionID {
			return nil, nil, storage.ErrIntegrity
		}
		previous = e.Sequence
		if err := journal.Validate(e.Proposed); err != nil {
			return nil, nil, err
		}
		if err := c.add(e); err != nil {
			return nil, nil, err
		}
	}
	return c.items, c.ranges, nil
}

// SelectRanges accepts both a full replay and a paged semantic selection. Full
// replays additionally verify the range digest against every omitted fact.
func selectRanges(events []journal.Event, ranges []run.ContextRange, through int64) ([]journal.Event, error) {
	for i, r := range ranges {
		hash, err := hex.DecodeString(r.Digest)
		if r.From < 1 || r.Through < r.From || r.Through > through || err != nil || len(hash) != 32 || hex.EncodeToString(hash) != r.Digest || r.Reason != "audit_only" || i > 0 && r.From <= ranges[i-1].Through {
			return nil, storage.ErrIntegrity
		}
	}
	counts := make([]int64, len(ranges))
	digests := make([]string, len(ranges))
	selected := make([]journal.Event, 0, len(events))
	index := 0
	var last int64
	for _, e := range events {
		if e.Sequence > through {
			break
		}
		if e.Sequence <= last {
			return nil, storage.ErrIntegrity
		}
		last = e.Sequence
		for index < len(ranges) && ranges[index].Through < e.Sequence {
			index++
		}
		if index < len(ranges) && e.Sequence >= ranges[index].From {
			counts[index]++
			digests[index] = fold(digests[index], e)
		} else {
			selected = append(selected, e)
		}
	}
	for i, r := range ranges {
		if counts[i] != 0 && (counts[i] != r.Through-r.From+1 || digests[i] != r.Digest) {
			return nil, storage.ErrIntegrity
		}
	}
	return selected, nil
}
