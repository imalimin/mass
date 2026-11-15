package projector

import (
	"context"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
)

// Validate the complete recursive provenance chain at the fixed log boundary.
// A ref to an uploaded object without a committed creation event is insufficient.
func (p *Projector) loadSummary(ctx context.Context, s run.Snapshot, ref storage.Ref, events map[string]journal.Event) (run.ContextSummary, journal.Event, map[string]bool, error) {
	var first run.ContextSummary
	var firstEvent journal.Event
	ids := map[string]bool{}
	seen := map[string]bool{}
	for depth := 0; ; depth++ {
		if depth >= 64 || seen[ref.ObjectID] {
			return first, firstEvent, nil, storage.ErrIntegrity
		}
		seen[ref.ObjectID] = true
		var source journal.Event
		var data journal.SummaryData
		for _, e := range events {
			if e.Type == "context.summary.created" {
				var d journal.SummaryData
				journal.Decode(e.Data, &d)
				if d.ContentID == ref.ObjectID {
					if source.EventID != "" {
						return first, firstEvent, nil, storage.ErrIntegrity
					}
					source = e
					data = d
				}
			}
		}
		if source.EventID == "" {
			return first, firstEvent, nil, storage.ErrIntegrity
		}
		object, err := p.Content.Object(ctx, s.ActorID, s.SpaceID, ref.ObjectID)
		if err != nil {
			return first, firstEvent, nil, err
		}
		if object.SHA256 != ref.SHA256 {
			return first, firstEvent, nil, storage.ErrIntegrity
		}
		var summary run.ContextSummary
		if err = p.readJSON(ctx, s, ref.ObjectID, &summary); err != nil {
			return first, firstEvent, nil, err
		}
		if summary.Through != data.Through || summary.Through >= source.Sequence || summary.Policy != Policy || summary.Policy != data.Policy || summary.ResponseEventID != data.ResponseEventID || summary.ProjectionID != data.ProjectionID || summary.Text == "" {
			return first, firstEvent, nil, storage.ErrIntegrity
		}
		response, ok := events[summary.ResponseEventID]
		if !ok || response.Type != "model.response.completed" || response.RunID != source.RunID {
			return first, firstEvent, nil, storage.ErrIntegrity
		}
		var d journal.ResponseData
		journal.Decode(response.Data, &d)
		var reply run.Reply
		if err = p.readJSON(ctx, s, d.ContentID, &reply); err != nil {
			return first, firstEvent, nil, err
		}
		if reply.Message.Content != summary.Text || len(reply.Message.ToolCalls) > 0 {
			return first, firstEvent, nil, storage.ErrIntegrity
		}
		for _, id := range summary.SourceEventIDs {
			event, ok := events[id]
			if !ok || event.Sequence > summary.Through {
				return first, firstEvent, nil, storage.ErrIntegrity
			}
			ids[id] = true
		}
		if depth == 0 {
			first = summary
			firstEvent = source
		}
		if summary.PreviousRef == nil {
			break
		}
		ref = *summary.PreviousRef
	}
	return first, firstEvent, ids, nil
}
