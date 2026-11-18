package archive

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/imalimin/mass/internal/app/identity"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
	"github.com/imalimin/mass/internal/resource/sessionlog"
	"github.com/imalimin/mass/internal/resource/trace"
	"github.com/imalimin/mass/internal/transport/agui"
)

func (r *Runtime) RuntimeSession(ctx context.Context, actor, space, id string) (run.Session, error) {
	s, err := r.memberSession(ctx, actor, space, id)
	if err != nil {
		return run.Session{}, err
	}
	defer s.Unlock()
	return s.session, nil
}

func (r *Runtime) RuntimeSessions(ctx context.Context, actor, space, agent string, q identity.Query) ([]run.Session, error) {
	if err := r.access(ctx, actor, space); err != nil {
		return nil, err
	}
	query := "SELECT id FROM sessions WHERE space_id=? AND id>?"
	args := []any{space, q.After}
	if agent != "" {
		query += " AND agent_id=?"
		args = append(args, agent)
	}
	rows, err := r.DB.DB.QueryContext(ctx, query+" ORDER BY id LIMIT ?", append(args, q.Limit)...)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	items := make([]run.Session, 0, len(ids))
	for _, id := range ids {
		s, err := r.lock(ctx, space, id)
		if err != nil {
			return nil, err
		}
		items = append(items, s.session)
		s.Unlock()
	}
	return items, nil
}

func (r *Runtime) RuntimeRun(ctx context.Context, actor, space, id string) (run.View, error) {
	s, err := r.runSession(ctx, actor, space, id)
	if err != nil {
		return run.View{}, err
	}
	defer s.Unlock()
	return view(s, id)
}

func view(s *conversation, id string) (run.View, error) {
	state := s.runs[id]
	if state == nil {
		return run.View{}, identity.ErrNotFound
	}
	v := state.view
	var calls int
	var totals [4]int64
	var missing [4]bool
	for _, e := range s.events {
		if e.RunID != id {
			continue
		}
		var use run.Usage
		switch e.Type {
		case "model.response.completed":
			use = fromJSON[journal.ResponseData](e.Data).Usage
		case "model.attempt.failed":
			use = fromJSON[journal.AttemptFailedData](e.Data).Usage
		default:
			continue
		}
		calls++
		for i, n := range []*int64{use.Input, use.Output, use.CacheRead, use.CacheWrite} {
			if n == nil {
				missing[i] = true
			} else {
				totals[i] += *n
			}
		}
	}
	if calls > 0 {
		for i, p := range []**int64{&v.Usage.Input, &v.Usage.Output, &v.Usage.CacheRead, &v.Usage.CacheWrite} {
			if !missing[i] {
				*p = &totals[i]
			}
		}
	}
	return v, nil
}

func (r *Runtime) RuntimeRuns(ctx context.Context, actor, space, session string, q identity.Query) ([]run.View, error) {
	s, err := r.memberSession(ctx, actor, space, session)
	if err != nil {
		return nil, err
	}
	defer s.Unlock()
	items := []run.View{}
	for id := range s.runs {
		if id <= q.After {
			continue
		}
		v, err := view(s, id)
		if err != nil {
			return nil, err
		}
		items = append(items, v)
	}
	return catalogPage(items, q.Limit, func(v run.View) string { return v.ID }), nil
}

func (r *Runtime) RuntimeLastRun(ctx context.Context, actor, space, session string) (run.View, error) {
	s, err := r.memberSession(ctx, actor, space, session)
	if err != nil {
		return run.View{}, err
	}
	defer s.Unlock()
	var id string
	var latest time.Time
	for _, v := range s.runs {
		if v.view.CreatedAt.After(latest) || v.view.CreatedAt.Equal(latest) && v.view.ID > id {
			id, latest = v.view.ID, v.view.CreatedAt
		}
	}
	return view(s, id)
}

func (r *Runtime) RuntimePlan(ctx context.Context, actor, space, id string) (run.Plan, error) {
	s, err := r.runSession(ctx, actor, space, id)
	if err != nil {
		return run.Plan{}, err
	}
	defer s.Unlock()
	for i := len(s.events) - 1; i >= 0; i-- {
		e := s.events[i]
		if e.RunID == id && e.Type == "plan.updated" {
			return fromJSON[run.Plan](e.Data), nil
		}
	}
	return run.Plan{Steps: []run.PlanStep{}}, nil
}

func (r *Runtime) RuntimeArtifacts(ctx context.Context, actor, space, id string) ([]run.Artifact, error) {
	s, err := r.runSession(ctx, actor, space, id)
	if err != nil {
		return nil, err
	}
	defer s.Unlock()
	items := []run.Artifact{}
	for _, e := range s.events {
		if e.RunID == id && e.Type == "artifact.published" {
			items = append(items, fromJSON[run.Artifact](e.Data))
		}
	}
	return items, nil
}

func (r *Runtime) RuntimeArtifact(ctx context.Context, actor, space, id string) (run.Artifact, error) {
	if err := r.access(ctx, actor, space); err != nil {
		return run.Artifact{}, err
	}
	sessions, err := r.sessionIDs(ctx, space)
	if err != nil {
		return run.Artifact{}, err
	}
	for _, session := range sessions {
		s, err := r.lock(ctx, space, session)
		if err != nil {
			return run.Artifact{}, err
		}
		for _, e := range s.events {
			if e.Type == "artifact.published" {
				a := fromJSON[run.Artifact](e.Data)
				if a.ID == id {
					s.Unlock()
					return a, nil
				}
			}
		}
		s.Unlock()
	}
	return run.Artifact{}, identity.ErrNotFound
}

func (r *Runtime) RuntimeTrace(ctx context.Context, actor, space, id string, after int64, through *int64, limit int) (journal.Page, error) {
	s, err := r.runSession(ctx, actor, space, id)
	if err != nil {
		return journal.Page{}, err
	}
	defer s.Unlock()
	return sequencePage(s.events, id, after, through, limit)
}

func (r *Runtime) Diagnostics(ctx context.Context, actor, session, id string) ([]json.RawMessage, error) {
	space, session, err := r.DiagnosticScope(ctx, actor, session, id)
	if err != nil {
		return nil, err
	}
	records, err := r.Log.ReadRemote(ctx, space, session)
	if err != nil {
		return nil, notFound(err)
	}
	return sessionlog.Diagnostic(records, id)
}

func (r *Runtime) DiagnosticScope(ctx context.Context, actor, session, id string) (string, string, error) {
	var space string
	var err error
	if id != "" {
		space, session, err = r.locateRun(ctx, id)
	} else {
		err = r.DB.DB.QueryRowContext(ctx, "SELECT space_id FROM sessions WHERE id=?", session).Scan(&space)
	}
	if err != nil {
		return "", "", notFound(err)
	}
	if err := r.access(ctx, actor, space); err != nil {
		if errors.Is(err, identity.ErrForbidden) {
			return "", "", identity.ErrNotFound
		}
		return "", "", err
	}
	return space, session, nil
}

func (r *Runtime) RuntimeDiagnosticSpace(ctx context.Context, actor, id, session string) (string, error) {
	var space string
	var err error
	if id != "" {
		space, _, err = r.locateRun(ctx, id)
	} else {
		err = r.DB.DB.QueryRowContext(ctx, "SELECT space_id FROM sessions WHERE id=?", session).Scan(&space)
	}
	if err != nil {
		return "", notFound(err)
	}
	return space, r.access(ctx, actor, space)
}

func (r *Runtime) RuntimeSnapshots(ctx context.Context, actor, space, id string) ([]storage.Snapshot, error) {
	s, err := r.memberSession(ctx, actor, space, id)
	if err != nil {
		return nil, err
	}
	defer s.Unlock()
	items := []storage.Snapshot{}
	for _, v := range s.snapshots {
		items = append(items, v)
	}
	slices.SortFunc(items, func(a, b storage.Snapshot) int {
		return s.runs[b.SourceRunID].view.CreatedAt.Compare(s.runs[a.SourceRunID].view.CreatedAt)
	})
	return items, nil
}

func (r *Runtime) Snapshot(ctx context.Context, actor, space, id string) (storage.Snapshot, error) {
	if err := r.access(ctx, actor, space); err != nil {
		return storage.Snapshot{}, err
	}
	return r.lookupSnapshot(ctx, space, id)
}

func (r *Runtime) RunBaseSnapshot(ctx context.Context, run run.Snapshot) (storage.Snapshot, error) {
	if run.BaseSnapshotID == "" {
		return storage.Snapshot{}, nil
	}
	session, err := r.memberSession(ctx, run.ActorID, run.SpaceID, run.SessionID)
	if err != nil {
		return storage.Snapshot{}, err
	}
	snapshot, found := session.snapshots[run.BaseSnapshotID]
	session.Unlock()
	if found {
		return snapshot, nil
	}
	return r.lookupSnapshot(ctx, run.SpaceID, run.BaseSnapshotID)
}

func (r *Runtime) lookupSnapshot(ctx context.Context, space, id string) (storage.Snapshot, error) {
	ids, err := r.sessionIDs(ctx, space)
	if err != nil {
		return storage.Snapshot{}, err
	}
	for _, session := range ids {
		s, err := r.lock(ctx, space, session)
		if err != nil {
			return storage.Snapshot{}, err
		}
		v, ok := s.snapshots[id]
		s.Unlock()
		if ok {
			return v, nil
		}
	}
	return storage.Snapshot{}, identity.ErrNotFound
}

func (r *Runtime) SnapshotBase(ctx context.Context, actor, space, session, id string) (string, error) {
	s, err := r.memberSession(ctx, actor, space, session)
	if err != nil {
		return "", err
	}
	defer s.Unlock()
	if s.active != "" && s.active != id {
		return "", storage.ErrActive
	}
	return s.session.LatestSnapshotID, nil
}

func (r *Runtime) SnapshotEvent(ctx context.Context, actor, space, session, eventID string) (storage.Snapshot, bool, error) {
	s, err := r.memberSession(ctx, actor, space, session)
	if err != nil {
		return storage.Snapshot{}, false, err
	}
	defer s.Unlock()
	return snapshotEvent(s, eventID)
}

func snapshotEvent(s *conversation, eventID string) (storage.Snapshot, bool, error) {
	for _, snapshot := range s.snapshots {
		if stable(snapshot.SourceRunID, "snapshot.event") == eventID {
			return snapshot, true, nil
		}
	}
	return storage.Snapshot{}, false, nil
}

func (r *Runtime) RuntimeCleanupPlan(ctx context.Context, actor, space, id string) (run.CleanupPlan, error) {
	s, err := r.runSession(ctx, actor, space, id)
	if err != nil {
		return run.CleanupPlan{}, err
	}
	defer s.Unlock()
	state := s.runs[id]
	if state == nil {
		return run.CleanupPlan{}, identity.ErrNotFound
	}
	if state.plan != nil {
		return *state.plan, nil
	}
	if state.final == nil {
		return run.CleanupPlan{}, run.ErrState
	}
	return run.CleanupPlan{At: state.final.FinishedAt,
		Outcome: run.Outcome{Status: state.view.Status, Error: state.view.Error}, WorkspaceReady: state.view.WorkspaceSnapshotID != ""}, nil
}

func (r *Runtime) LoadStream(ctx context.Context, actor, space, id string) (agui.Checkpoint, error) {
	s, err := r.runSession(ctx, actor, space, id)
	if err != nil {
		return agui.Checkpoint{}, err
	}
	defer s.Unlock()
	cp := r.Live.Checkpoint(space + "/" + id)
	if cp.SessionID == "" {
		cp.SessionID, cp.Profile, cp.Mapper, cp.Parser = s.session.ID, agui.Profile, 1, 1
	}
	return cp, nil
}

func (r *Runtime) CommitStream(_ context.Context, _, space, id string, old, next agui.Checkpoint, frames []agui.Frame) error {
	if !r.Live.Commit(space+"/"+id, old, next, frames) {
		return run.ErrState
	}
	return nil
}

func (r *Runtime) ReadFrames(ctx context.Context, actor, space, id string, after int64, limit int) ([]agui.Frame, bool, error) {
	p := &agui.Projector{Repo: r, Live: r.Live, ResponseText: r.ResponseText, ToolState: r.ToolState}
	if _, err := p.CatchUp(ctx, actor, space, id); err != nil {
		return nil, false, err
	}
	frames, closed, ok := r.Live.Frames(space+"/"+id, after, limit)
	if !ok {
		return nil, false, run.ErrCursor
	}
	return frames, closed, nil
}

func (r *Runtime) ToolState(id, call string) trace.Tool {
	if v, ok := r.tools.Load(id + "/" + call); ok {
		return v.(trace.Tool)
	}
	return r.Recorder.ToolState(id, call)
}

func (r *Runtime) ResponseText(ctx context.Context, actor, space, id string) (string, error) {
	if err := r.access(ctx, actor, space); err != nil {
		return "", err
	}
	raw, err := r.inlineBytes(space, id)
	if err != nil {
		return "", err
	}
	return fromJSON[run.Reply](raw).Message.Content, nil
}
