package archive

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"time"

	"github.com/imalimin/mass/internal/app/identity"
	"github.com/imalimin/mass/internal/diagnostic"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
)

func (r *Runtime) Check(ctx context.Context) error { return r.Lease.Check(ctx) }

func (r *Runtime) candidates(ctx context.Context, where string, limit int) ([]run.Snapshot, error) {
	if err := r.Check(ctx); err != nil {
		return nil, err
	}
	rows, err := r.DB.DB.QueryContext(ctx, "SELECT s.space_id,s.id,r.id FROM runs r JOIN sessions s ON s.id=r.session_id WHERE "+where+" ORDER BY s.created_at,r.id LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	type locator struct{ space, session, id string }
	var locations []locator
	for rows.Next() {
		var v locator
		if err = rows.Scan(&v.space, &v.session, &v.id); err != nil {
			rows.Close()
			return nil, err
		}
		locations = append(locations, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	items := []run.Snapshot{}
	for _, v := range locations {
		s, err := r.lock(ctx, v.space, v.session)
		if err != nil {
			return nil, err
		}
		state := s.runs[v.id]
		if state == nil {
			s.Unlock()
			return nil, storage.ErrIntegrity
		}
		items = append(items, state.view.Snapshot)
		s.Unlock()
	}
	return items, nil
}
func (r *Runtime) Candidates(ctx context.Context, limit int) ([]run.Snapshot, error) {
	return r.candidates(ctx, "r.status='queued'", limit)
}
func (r *Runtime) RecoveryCandidates(ctx context.Context) ([]run.Snapshot, error) {
	return r.candidates(ctx, "r.status='running' AND (r.lease_expires_at IS NULL OR r.lease_expires_at < UTC_TIMESTAMP(6))", 1<<31-1)
}

func (r *Runtime) Recover(ctx context.Context, snapshot run.Snapshot) (bool, error) {
	if err := r.Check(ctx); err != nil {
		return false, err
	}
	leaseSec := 30
	if r.Lease != nil {
		leaseSec = max(1, int(r.Lease.LeaseDuration()/time.Second))
	}
	res, err := r.DB.DB.ExecContext(ctx,
		"UPDATE runs SET owner_node_id=?,lease_expires_at=DATE_ADD(UTC_TIMESTAMP(6), INTERVAL ? SECOND) WHERE id=? AND status='running' AND (lease_expires_at IS NULL OR lease_expires_at < UTC_TIMESTAMP(6))",
		r.Lease.ID, leaseSec, snapshot.ID,
	)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return true, nil
	}
	var done bool
	err = r.withSessionLock(ctx, snapshot.SessionID, func(ctx context.Context) error {
		s, err := r.lock(ctx, snapshot.SpaceID, snapshot.SessionID)
		if err != nil {
			return err
		}
		defer s.Unlock()
		state := s.runs[snapshot.ID]
		if state == nil {
			return identity.ErrNotFound
		}
		if run.Terminal(state.view.Status) {
			done = true
			return nil
		}
		if state.final != nil {
			done = true
			return r.publishSummary(ctx, s, state)
		}
		var start sql.NullTime
		if err := r.DB.DB.QueryRowContext(ctx, "SELECT started_at FROM runs WHERE id=?", snapshot.ID).Scan(&start); err != nil {
			return err
		}
		if !start.Valid {
			diagnostic.Record(diagnostic.WithRun(ctx, r.DiagnosticLog, snapshot), "warn", "run.interrupted", "reason=recovery state=queued")
			event := journal.New(snapshot.ActorID, "run.finished", snapshot.ID, journal.FinishData{Status: "failed", Error: "interrupted"})
			if err := r.finish(ctx, s, state, event); err != nil {
				return err
			}
			done = true
			return nil
		}
		state.view.Status = "running"
		state.view.OwnerID = r.Lease.ID
		state.view.StartedAt = &start.Time
		return nil
	})
	return done, err
}

func (r *Runtime) Claim(ctx context.Context, snapshot run.Snapshot) (run.View, error) {
	if err := r.Check(ctx); err != nil {
		return run.View{}, err
	}
	s, err := r.lock(ctx, snapshot.SpaceID, snapshot.SessionID)
	if err != nil {
		return run.View{}, err
	}
	defer s.Unlock()
	state := s.runs[snapshot.ID]
	if state == nil || state.final != nil || state.view.Status != "queued" || state.view.CancelRequested {
		return run.View{}, run.ErrState
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	leaseSec := 30
	if r.Lease != nil {
		leaseSec = max(1, int(r.Lease.LeaseDuration()/time.Second))
	}
	result, err := r.DB.DB.ExecContext(ctx,
		"UPDATE runs SET status='running',started_at=?,owner_node_id=?,lease_expires_at=DATE_ADD(UTC_TIMESTAMP(6), INTERVAL ? SECOND) WHERE id=? AND status='queued'",
		now, r.Lease.ID, leaseSec, snapshot.ID,
	)
	if err != nil {
		return run.View{}, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return run.View{}, run.ErrState
	}
	state.view.Status, state.view.StartedAt, state.view.OwnerID = "running", &now, r.Lease.ID
	diagnostic.Record(diagnostic.WithRun(ctx, r.DiagnosticLog, snapshot), "info", "run.claimed", "queue_ms=%d", now.Sub(snapshot.CreatedAt).Milliseconds())
	event := journal.New(snapshot.ActorID, "run.started", snapshot.ID, journal.StartData{OwnerID: r.Lease.ID})
	event.OccurredAt = now
	s.append(event)
	return state.view, nil
}

func (r *Runtime) CheckRun(ctx context.Context, snapshot run.Snapshot) error {
	if err := r.Check(ctx); err != nil {
		return err
	}
	s, err := r.lock(ctx, snapshot.SpaceID, snapshot.SessionID)
	if err != nil {
		return err
	}
	state := s.runs[snapshot.ID]
	v := state.view
	s.Unlock()
	if v.Status != "running" || v.OwnerID != r.Lease.ID {
		return run.ErrState
	}
	if v.CancelRequested {
		return run.ErrCancelled
	}
	var dbStatus, dbOwner string
	if err := r.DB.DB.QueryRowContext(ctx, "SELECT status,COALESCE(owner_node_id,'') FROM runs WHERE id=?", snapshot.ID).Scan(&dbStatus, &dbOwner); err != nil {
		return err
	}
	if dbStatus != "running" || dbOwner != r.Lease.ID {
		return run.ErrState
	}
	var model identity.Model
	if v.AgentSourceSpaceID == "" {
		model, err = r.DB.Model(ctx, v.ActorID, v.SpaceID, v.Definition.Model.ID)
	} else {
		model, err = r.DB.SharedRunModel(ctx, v.ActorID, v.SpaceID, v.AgentSourceSpaceID, v.Definition.Model.ID)
	}
	if err != nil {
		return err
	}
	if !model.Enabled {
		return identity.ErrCapability
	}
	return nil
}

func (r *Runtime) RejectQueued(ctx context.Context, snapshot run.Snapshot, code string) error {
	return r.withSessionLock(ctx, snapshot.SessionID, func(ctx context.Context) error {
		s, err := r.lock(ctx, snapshot.SpaceID, snapshot.SessionID)
		if err != nil {
			return err
		}
		defer s.Unlock()
		state := s.runs[snapshot.ID]
		if state == nil || state.view.Status != "queued" {
			return run.ErrState
		}
		diagnostic.Record(diagnostic.WithRun(ctx, r.DiagnosticLog, snapshot), "warn", "run.start_failed", "stage=claim cause=%s", code)
		event := journal.New(snapshot.ActorID, "run.finished", snapshot.ID, journal.FinishData{Status: "failed", Error: code})
		return r.finish(ctx, s, state, event)
	})
}

func (r *Runtime) CancelRuntimeRun(ctx context.Context, actor, space, id string) (run.View, error) {
	if err := r.access(ctx, actor, space); err != nil {
		return run.View{}, err
	}
	actual, session, err := r.locateRun(ctx, id)
	if err != nil {
		return run.View{}, err
	}
	if actual != space {
		return run.View{}, identity.ErrNotFound
	}
	var out run.View
	err = r.withSessionLock(ctx, session, func(ctx context.Context) error {
		s, err := r.lock(ctx, space, session)
		if err != nil {
			return err
		}
		defer s.Unlock()
		state := s.runs[id]
		if state == nil {
			return identity.ErrNotFound
		}
		if run.Terminal(state.view.Status) || state.view.CancelRequested {
			out = state.view
			return nil
		}
		var dbStatus, dbOwner string
		if err := r.DB.DB.QueryRowContext(ctx, "SELECT status,COALESCE(owner_node_id,'') FROM runs WHERE id=?", id).Scan(&dbStatus, &dbOwner); err != nil {
			return notFound(err)
		}
		if run.Terminal(dbStatus) {
			out = state.view
			return nil
		}
		if dbStatus == "running" && dbOwner != "" && r.Lease != nil && dbOwner != r.Lease.ID {
			return run.ErrState
		}
		state.view.CancelRequested = true
		s.append(journal.New(actor, "run.cancel.requested", id, struct{}{}))
		if dbStatus == "queued" {
			event := journal.New(actor, "run.finished", id, journal.FinishData{Status: "cancelled", Error: "run_cancelled"})
			if err := r.finish(ctx, s, state, event); err != nil {
				return err
			}
		}
		out = state.view
		return nil
	})
	return out, err
}

func (r *Runtime) BeginWorkspaceMaintenance(ctx context.Context, actor, space, session string) error {
	s, err := r.memberSession(ctx, actor, space, session)
	if err != nil {
		return err
	}
	defer s.Unlock()
	if s.active != "" || s.maintenance {
		return storage.ErrActive
	}
	s.maintenance = true
	return nil
}
func (r *Runtime) EndWorkspaceMaintenance(ctx context.Context, _, space, session string) error {
	s, err := r.lock(ctx, space, session)
	if err != nil {
		return err
	}
	defer s.Unlock()
	s.maintenance = false
	return nil
}

type cleanup struct {
	*Runtime
	snapshot run.Snapshot
}

func (c *cleanup) State(ctx context.Context) (run.View, *run.CleanupPlan, error) {
	s, err := c.lock(ctx, c.snapshot.SpaceID, c.snapshot.SessionID)
	if err != nil {
		return run.View{}, nil, err
	}
	defer s.Unlock()
	state := s.runs[c.snapshot.ID]
	if state == nil {
		return run.View{}, nil, identity.ErrNotFound
	}
	return state.view, state.plan, nil
}

func (c *cleanup) SavePlan(ctx context.Context, value run.CleanupPlan) error {
	s, err := c.lock(ctx, c.snapshot.SpaceID, c.snapshot.SessionID)
	if err != nil {
		return err
	}
	defer s.Unlock()
	state := s.runs[c.snapshot.ID]
	if state == nil {
		return identity.ErrNotFound
	}
	if state.plan == nil {
		state.plan = &value
	}
	return nil
}

func (c *cleanup) AppendEvents(ctx context.Context, _ string, batch journal.Batch) ([]journal.Event, error) {
	if batch.SessionID != c.snapshot.SessionID || batch.SpaceID != c.snapshot.SpaceID {
		return nil, storage.ErrInvalid
	}
	for _, e := range batch.Events {
		if e.RunID != c.snapshot.ID {
			return nil, storage.ErrInvalid
		}
	}
	return c.appendEvents(ctx, batch)
}
func (c *cleanup) ReadEvents(ctx context.Context, actor, space, session string, after int64, through *int64, limit int) (journal.Page, error) {
	return c.ReadRunEvents(ctx, actor, space, session, c.snapshot.ID, after, through, limit)
}
func (c *cleanup) ReadRunEvents(ctx context.Context, _, space, session, id string, after int64, through *int64, limit int) (journal.Page, error) {
	if space != c.snapshot.SpaceID || session != c.snapshot.SessionID || id != c.snapshot.ID {
		return journal.Page{}, storage.ErrInvalid
	}
	s, err := c.lock(ctx, space, session)
	if err != nil {
		return journal.Page{}, err
	}
	defer s.Unlock()
	return sequencePage(s.events, id, after, through, limit)
}
func (c *cleanup) SnapshotBase(ctx context.Context, _, space, session, id string) (string, error) {
	if space != c.snapshot.SpaceID || session != c.snapshot.SessionID || id != c.snapshot.ID {
		return "", storage.ErrInvalid
	}
	s, err := c.lock(ctx, space, session)
	if err != nil {
		return "", err
	}
	defer s.Unlock()
	if s.active != "" && s.active != id {
		return "", storage.ErrActive
	}
	return s.session.LatestSnapshotID, nil
}
func (c *cleanup) SnapshotEvent(ctx context.Context, _, space, session, eventID string) (storage.Snapshot, bool, error) {
	if space != c.snapshot.SpaceID || session != c.snapshot.SessionID {
		return storage.Snapshot{}, false, storage.ErrInvalid
	}
	s, err := c.lock(ctx, space, session)
	if err != nil {
		return storage.Snapshot{}, false, err
	}
	defer s.Unlock()
	return snapshotEvent(s, eventID)
}

func (c *cleanup) BeginObject(ctx context.Context, actor string, o storage.Object) (storage.Object, error) {
	if o.SpaceID != c.snapshot.SpaceID || o.ID != stable(c.snapshot.ID, "archive") {
		return o, storage.ErrInvalid
	}
	return c.Objects.BeginObject(ctx, actor, o)
}
func (c *cleanup) ReadyObject(ctx context.Context, actor string, o storage.Object) error {
	if o.SpaceID != c.snapshot.SpaceID || o.ID != stable(c.snapshot.ID, "archive") {
		return storage.ErrInvalid
	}
	return c.Objects.ReadyObject(ctx, actor, o)
}
func (c *cleanup) Object(ctx context.Context, _, space, id string, _ bool) (storage.Object, error) {
	if space != c.snapshot.SpaceID || id != stable(c.snapshot.ID, "archive") {
		return storage.Object{}, storage.ErrInvalid
	}
	if o, ok := c.Objects.get(space, id); ok {
		return o, nil
	}
	s, err := c.lock(ctx, space, c.snapshot.SessionID)
	if err != nil {
		return storage.Object{}, err
	}
	defer s.Unlock()
	if ref, ok := s.blobs[id]; ok {
		return ref.Metadata(), nil
	}
	return storage.Object{}, identity.ErrNotFound
}

func (c *cleanup) StoreInline(ctx context.Context, o storage.Object, r []byte) error {
	if o.SpaceID != c.snapshot.SpaceID || o.ID != stable(c.snapshot.ID, "manifest") {
		return storage.ErrInvalid
	}
	return c.Runtime.StoreInline(ctx, o, r)
}

func (c *cleanup) OpenInline(_ context.Context, _, space, id string) (storage.Object, io.ReadCloser, error) {
	if space != c.snapshot.SpaceID || id != stable(c.snapshot.ID, "manifest") {
		return storage.Object{}, nil, storage.ErrInvalid
	}
	v, ok := c.inline.Load(space + "/" + id)
	if !ok {
		return storage.Object{}, nil, storage.ErrBlobNotFound
	}
	item := v.(inline)
	return item.object, io.NopCloser(bytes.NewReader(item.raw)), nil
}

func (r *Runtime) RetryUploads(ctx context.Context) error {
	type pending struct {
		sessionID string
		conv      *conversation
	}
	var targets []pending
	r.sessions.Range(func(_, value any) bool {
		s := value.(*conversation)
		s.Lock()
		for _, state := range s.runs {
			if state.final != nil && !state.finished {
				targets = append(targets, pending{sessionID: s.session.ID, conv: s})
				break
			}
		}
		s.Unlock()
		return ctx.Err() == nil
	})
	var result error
	for _, target := range targets {
		if ctx.Err() != nil {
			break
		}
		err := r.withSessionLock(ctx, target.sessionID, func(ctx context.Context) error {
			s := target.conv
			s.Lock()
			defer s.Unlock()
			var sessionErr error
			for _, state := range s.runs {
				if state.final != nil && !state.finished {
					if err := r.publishSummary(ctx, s, state); err != nil {
						sessionErr = errors.Join(sessionErr, err)
					}
				}
			}
			return sessionErr
		})
		if err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}
