// Package planner persists a capacity-checked projection before model attempts.
package planner

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
	"github.com/imalimin/mass/internal/resource"
	"github.com/imalimin/mass/internal/runtime/context/projector"
)

type Projector interface {
	Project(context.Context, run.Snapshot, []journal.Event, int64, []run.ToolSpec) (run.Prepared, error)
}
type ToolResolver interface {
	Describe([]run.ToolSpec) ([]run.ToolSpec, error)
}
type Planner struct {
	Journal   *journal.Journal
	Content   *resource.ContentStore
	Projector Projector
	Tools     ToolResolver
}

func (p *Planner) Prepare(ctx context.Context, s run.Snapshot) (run.Prepared, error) {
	events, through, err := resource.History(ctx, p.Journal, s.ActorID, s.SpaceID, s.SessionID)
	if err != nil {
		return run.Prepared{}, err
	}
	tools, err := p.Tools.Describe(s.Definition.Tools)
	if err != nil {
		return run.Prepared{}, err
	}
	prepared, err := p.Projector.Project(ctx, s, events, through, tools)
	if err != nil {
		return prepared, err
	}
	prepared.ID = uuid.NewString()
	event := journal.New(s.ActorID, "context.projected", s.ID, struct{}{})
	prepared.EventID = event.EventID
	raw, _ := json.Marshal(prepared)
	o, err := p.Content.Put(ctx, s.ActorID, s.SpaceID, uuid.NewString(), "context.projection", "application/json", bytes.NewReader(raw))
	if err != nil {
		return prepared, err
	}
	data := journal.ProjectionData{ID: prepared.ID, ContentID: o.ID, Fingerprint: prepared.Fingerprint, Through: through, Counter: projector.Counter, Epoch: 1}
	event.Data, _ = json.Marshal(data)
	event.Refs = []storage.Ref{{Role: "projection", ObjectID: o.ID, SHA256: o.SHA256}}
	_, err = resource.AppendRun(ctx, p.Journal, s, event)
	return prepared, err
}
