package archive

import (
	"bytes"
	"context"
	"io"

	"github.com/imalimin/mass/internal/app/identity"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
	"github.com/imalimin/mass/internal/resource"
)

// RunServices is scoped to the session whose permissions were checked on start.
// The supervisor cancels its context on revocation or when execution stops.
func (r *Runtime) RunServices(s run.Snapshot, content *resource.ContentStore) (*journal.Journal, *resource.ContentStore) {
	active := &activeRun{Runtime: r, snapshot: s}
	scopedContent := *content
	scopedContent.Repo, scopedContent.Inline = active, active
	return &journal.Journal{Repo: active}, &scopedContent
}

type activeRun struct {
	*Runtime
	snapshot run.Snapshot
}

func (a *activeRun) within(ctx context.Context, space string) error {
	if space != a.snapshot.SpaceID {
		return storage.ErrInvalid
	}
	return ctx.Err()
}

func (a *activeRun) AppendEvents(ctx context.Context, _ string, batch journal.Batch) ([]journal.Event, error) {
	if err := a.within(ctx, batch.SpaceID); err != nil {
		return nil, err
	}
	if batch.SessionID != a.snapshot.SessionID {
		return nil, storage.ErrInvalid
	}
	for _, e := range batch.Events {
		if e.RunID != a.snapshot.ID {
			return nil, storage.ErrInvalid
		}
	}
	return a.appendEvents(ctx, batch)
}

func (a *activeRun) ReadEvents(ctx context.Context, _, space, session string, after int64, through *int64, limit int) (journal.Page, error) {
	return a.read(ctx, space, session, "", after, through, limit)
}

func (a *activeRun) ReadRunEvents(ctx context.Context, _, space, session, runID string, after int64, through *int64, limit int) (journal.Page, error) {
	return a.read(ctx, space, session, runID, after, through, limit)
}

func (a *activeRun) read(ctx context.Context, space, session, runID string, after int64, through *int64, limit int) (journal.Page, error) {
	if err := a.within(ctx, space); err != nil {
		return journal.Page{}, err
	}
	if session != a.snapshot.SessionID {
		return journal.Page{}, storage.ErrInvalid
	}
	s, err := a.lock(ctx, space, session)
	if err != nil {
		return journal.Page{}, err
	}
	defer s.Unlock()
	return sequencePage(s.events, runID, after, through, limit)
}

func (a *activeRun) BeginObject(ctx context.Context, actor string, o storage.Object) (storage.Object, error) {
	if err := a.within(ctx, o.SpaceID); err != nil {
		return o, err
	}
	return a.Objects.BeginObject(ctx, actor, o)
}

func (a *activeRun) ReadyObject(ctx context.Context, actor string, o storage.Object) error {
	if err := a.within(ctx, o.SpaceID); err != nil {
		return err
	}
	return a.Objects.ReadyObject(ctx, actor, o)
}

func (a *activeRun) Object(ctx context.Context, _, space, id string, committed bool) (storage.Object, error) {
	if err := a.within(ctx, space); err != nil {
		return storage.Object{}, err
	}
	if !committed {
		if o, ok := a.Objects.get(space, id); ok {
			return o, nil
		}
	}
	s, err := a.lock(ctx, space, a.snapshot.SessionID)
	if err != nil {
		return storage.Object{}, err
	}
	ref, ok := s.blobs[id]
	if !ok {
		ref, ok = s.draftPackages[id]
	}
	s.Unlock()
	if ok {
		return ref.Metadata(), nil
	}
	if a.CatalogObject == nil {
		return storage.Object{}, identity.ErrNotFound
	}
	ref, err = a.CatalogObject(ctx, space, id)
	return ref.Metadata(), err
}

func (a *activeRun) OpenInline(ctx context.Context, _, space, id string) (storage.Object, io.ReadCloser, error) {
	if err := a.within(ctx, space); err != nil {
		return storage.Object{}, nil, err
	}
	value, ok := a.inline.Load(space + "/" + id)
	if !ok {
		return storage.Object{}, nil, storage.ErrBlobNotFound
	}
	item := value.(inline)
	return item.object, io.NopCloser(bytes.NewReader(item.raw)), nil
}
