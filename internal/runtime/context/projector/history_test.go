package projector

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
)

type generatedHistory struct {
	snapshot              run.Snapshot
	count, calls, maxPage int
	gap, large, unknown   bool
	denied                error
}

func (*generatedHistory) AppendEvents(context.Context, string, journal.Batch) ([]journal.Event, error) {
	panic("read-only fixture")
}
func (r *generatedHistory) ReadEvents(_ context.Context, actor, space, session string, after int64, through *int64, limit int) (journal.Page, error) {
	if r.denied != nil {
		return journal.Page{}, r.denied
	}
	if actor != r.snapshot.ActorID || space != r.snapshot.SpaceID || session != r.snapshot.SessionID {
		return journal.Page{}, storage.ErrInvalid
	}
	r.calls++
	r.maxPage = max(r.maxPage, limit)
	end := int64(r.count)
	if through != nil {
		end = *through
	}
	p := journal.Page{Through: end, Next: after, Events: []journal.Event{}}
	for n := after + 1; n <= min(after+int64(limit), end); n++ {
		e := journal.New(actor, "workspace.restored", "", journal.RestoreData{Reason: "explicit_restore"})
		if r.large {
			e = journal.New(actor, "context.projected", r.snapshot.ID, journal.ProjectionData{ID: uuid.NewString(), ContentID: uuid.NewString(), Fingerprint: strings.Repeat("a", 64), Through: 1, Counter: strings.Repeat("c", 32<<10), Epoch: 1})
		}
		if r.unknown && n == 101 {
			e.SchemaVersion = 99
		}
		e.OccurredAt = time.Unix(1, 0)
		sequence := n
		if r.gap && n == 101 {
			sequence++
		}
		p.Events = append(p.Events, journal.Event{Proposed: e, SpaceID: space, SessionID: session, Sequence: sequence})
		p.Next = n
	}
	if r.calls == 1 && !r.large {
		r.count += 100
	}
	p.HasMore = p.Next < end
	return p, nil
}
func TestPagedHistoryBoundaries(t *testing.T) {
	s := run.Snapshot{ID: uuid.NewString(), ActorID: uuid.NewString(), SpaceID: uuid.NewString(), SessionID: uuid.NewString()}
	r := &generatedHistory{snapshot: s, count: 30100}
	items, ranges, through, err := ReadHistory(context.Background(), &journal.Journal{Repo: r}, s)
	if err != nil || len(items) != 0 || len(ranges) != 1 || through != 30100 || r.maxPage > 100 {
		t.Fatal(len(items), len(ranges), through, err)
	}
	if _, err = selectRanges(items, ranges, through); err != nil {
		t.Fatal(err)
	}
	r.gap = true
	if _, _, _, err = ReadHistory(context.Background(), &journal.Journal{Repo: r}, s); !errors.Is(err, storage.ErrIntegrity) {
		t.Fatal("gap accepted", err)
	}
	r.gap = false
	r.unknown = true
	if _, _, _, err = ReadHistory(context.Background(), &journal.Journal{Repo: r}, s); !errors.Is(err, storage.ErrSchema) {
		t.Fatal("unknown version hidden in audit range", err)
	}
	r.unknown = false
	r.large = true
	r.count = 1000
	if _, _, _, err = ReadHistory(context.Background(), &journal.Journal{Repo: r}, s); !errors.Is(err, run.ErrOverflow) {
		t.Fatal("semantic byte limit absent", err)
	}
	r.denied = errors.New("forbidden")
	if _, _, _, err = ReadHistory(context.Background(), &journal.Journal{Repo: r}, s); !errors.Is(err, r.denied) {
		t.Fatal("authorization failure hidden", err)
	}
}

func TestAuditRangeReplayRejectsChangedOrPartialFacts(t *testing.T) {
	s := run.Snapshot{ID: uuid.NewString(), ActorID: uuid.NewString(), SpaceID: uuid.NewString(), SessionID: uuid.NewString()}
	r := &generatedHistory{snapshot: s, count: 2}
	p, err := r.ReadEvents(context.Background(), s.ActorID, s.SpaceID, s.SessionID, 0, nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	rangeValue := run.ContextRange{From: 1, Through: 2, Reason: "audit_only"}
	for _, e := range p.Events {
		rangeValue.Digest = fold(rangeValue.Digest, e)
	}
	if _, err = selectRanges(p.Events, []run.ContextRange{rangeValue}, 2); err != nil {
		t.Fatal(err)
	}
	if _, err = selectRanges(p.Events[:1], []run.ContextRange{rangeValue}, 2); !errors.Is(err, storage.ErrIntegrity) {
		t.Fatal("partial range accepted", err)
	}
	changedScope := append([]journal.Event(nil), p.Events...)
	changedScope[0].SpaceID = uuid.NewString()
	spec := run.ProjectionSpec{Purpose: "task", Through: 2, Counter: Counter, Renderer: Renderer, Parser: 1, Policy: Policy, Epoch: 1, AuditRanges: []run.ContextRange{rangeValue}}
	if _, err = (&Projector{}).Build(context.Background(), s, changedScope, nil, spec); !errors.Is(err, storage.ErrIntegrity) {
		t.Fatal("omitted cross-space fact accepted", err)
	}
	p.Events[0].OccurredAt = time.Unix(2, 0)
	if _, err = selectRanges(p.Events, []run.ContextRange{rangeValue}, 2); !errors.Is(err, storage.ErrIntegrity) {
		t.Fatal("modified omitted fact accepted", err)
	}
}
