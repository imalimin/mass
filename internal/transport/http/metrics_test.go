package httptransport

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/journal"
)

type longMetricTrace struct {
	RuntimeRepository
	actor, space, session, runID, attempt, projection, content string
	pages                                                      int
}

func (r *longMetricTrace) RuntimeTrace(ctx context.Context, actor, space, id string, after int64, through *int64, limit int) (journal.Page, error) {
	if err := ctx.Err(); err != nil {
		return journal.Page{}, err
	}
	r.pages++
	end := int64(20104)
	p := journal.Page{Through: end, Next: after, Events: []journal.Event{}}
	for seq := after + 1; seq <= min(after+int64(limit), end); seq++ {
		e := journal.New(actor, "run.cancel.requested", id, struct{}{})
		if seq == 1 {
			e = journal.New(actor, "context.projected", id, journal.ProjectionData{ID: r.projection, ContentID: r.content, Fingerprint: journal.Digest("fixture"), Through: 1, Counter: "fixture", Epoch: 1, Purpose: "task", PrefixChangeReason: "initial"})
			e.EventID = r.projection
		}
		if seq == 2 {
			e = journal.New(actor, "model.attempt.started", id, journal.AttemptData{ID: r.attempt, ProjectionEventID: r.projection, MessageID: r.content, Purpose: "task", Source: "fake"})
		}
		if seq == end {
			input, output := int64(10), int64(2)
			e = journal.New(actor, "model.response.completed", id, journal.ResponseData{AttemptID: r.attempt, MessageID: r.content, ContentID: r.content, FinishReason: "stop", Usage: run.Usage{Input: &input, Output: &output}})
		}
		p.Events = append(p.Events, journal.Event{Proposed: e, SpaceID: space, SessionID: r.session, Sequence: seq})
		p.Next = seq
	}
	p.HasMore = p.Next < end
	return p, nil
}

func TestMetricsStreamPastTwentyThousandBusinessFacts(t *testing.T) {
	r := &longMetricTrace{actor: uuid.NewString(), space: uuid.NewString(), session: uuid.NewString(), runID: uuid.NewString(), attempt: uuid.NewString(), projection: uuid.NewString(), content: uuid.NewString()}
	groups, through, err := modelMetrics(context.Background(), r, r.actor, r.space, r.runID)
	if err != nil || through != 20104 || r.pages < 200 || len(groups) != 1 || groups[0].Cache.Attempts != 1 {
		t.Fatal("long metrics", groups, through, r.pages, err)
	}
}
