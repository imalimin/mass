package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"

	"github.com/imalimin/mass/internal/app/identity"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
	"github.com/imalimin/mass/internal/resource"
	"github.com/imalimin/mass/internal/resource/sessionlog"
)

func (r *Runtime) FindRuntimeRequest(ctx context.Context, actor, space, operation, key, digest string) (string, string, bool, error) {
	if operation == "run" {
		// The accepting path checks the request key in the target session while locked.
		return "", "", false, nil
	}
	if err := r.access(ctx, actor, space); err != nil {
		return "", "", false, err
	}
	var session, previous string
	err := r.DB.DB.QueryRowContext(ctx, "SELECT id,request_digest FROM sessions WHERE space_id=? AND actor_id=? AND request_key=?", space, actor, key).Scan(&session, &previous)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err == nil && digest != previous {
		err = storage.ErrConflict
	}
	return session, "", err == nil, err
}

func (r *Runtime) FindRunRequest(ctx context.Context, actor, space, session, key, digest string) (string, bool, error) {
	s, err := r.memberSession(ctx, actor, space, session)
	if err != nil {
		return "", false, err
	}
	defer s.Unlock()
	for _, v := range s.runs {
		if v.key != key || v.view.ActorID != actor {
			continue
		}
		if v.digest != digest {
			return "", false, storage.ErrConflict
		}
		if err := r.ensureRun(ctx, s, v.view.ID); err != nil {
			return "", false, err
		}
		return v.view.ID, true, nil
	}
	return "", false, nil
}

func (r *Runtime) inlineBytes(space, id string) ([]byte, error) {
	v, ok := r.inline.Load(space + "/" + id)
	if !ok {
		return nil, storage.ErrIntegrity
	}
	return v.(inline).raw, nil
}

func (r *Runtime) AcceptRuntimeRequest(ctx context.Context, actor, space, operation, key, digest string, batch journal.Batch) (string, string, error) {
	if err := r.access(ctx, actor, space); err != nil {
		return "", "", err
	}
	if batch.SpaceID != space || len(batch.Events) == 0 {
		return "", "", storage.ErrInvalid
	}
	if operation == "session" {
		r.create.Lock()
		defer r.create.Unlock()
		id, _, ok, err := r.FindRuntimeRequest(ctx, actor, space, operation, key, digest)
		if ok || err != nil {
			return id, "", err
		}
		e := batch.Events[0]
		d := fromJSON[journal.SessionData](e.Data)
		if e.Type != "session.created" || d.DefinitionRef == nil {
			return "", "", storage.ErrInvalid
		}
		raw, err := r.inlineBytes(space, d.DefinitionRef.ObjectID)
		if err != nil {
			return "", "", err
		}
		defer r.inline.Delete(space + "/" + d.DefinitionRef.ObjectID)
		created := sessionlog.Created{Header: sessionlog.Header{Seq: 1, Type: sessionlog.SessionCreated, Role: "system"},
			SchemaVersion: 1, SessionID: batch.SessionID, SpaceID: space, AgentID: d.DefinitionID, VersionID: d.VersionID,
			CreatedAt: e.OccurredAt, ActorID: actor, Title: d.Title, EventID: e.EventID, Definition: sessionlog.FixedDefinition(fromJSON[run.Definition](raw))}
		s := newConversation()
		s.loaded = true
		r.restoreCreated(s, created)
		if err := r.commit(ctx, s, "", created); err != nil {
			return "", "", err
		}
		_, err = r.DB.DB.ExecContext(ctx, "INSERT INTO sessions(id,space_id,agent_id,actor_id,created_at,request_key,request_digest) VALUES(?,?,?,?,?,?,?)",
			batch.SessionID, space, d.DefinitionID, actor, e.OccurredAt, key, digest)
		if err != nil {
			return "", "", err
		}
		r.sessions.Store(space+"/"+batch.SessionID, s)
		return batch.SessionID, "", nil
	}
	if operation != "run" || len(batch.Events) != 2 || batch.Events[0].Type != "user.message.accepted" || batch.Events[1].Type != "run.created" {
		return "", "", storage.ErrInvalid
	}
	s, err := r.memberSession(ctx, actor, space, batch.SessionID)
	if err != nil {
		return "", "", err
	}
	defer s.Unlock()
	msgEvent, runEvent := batch.Events[0], batch.Events[1]
	message := fromJSON[journal.UserMessageData](msgEvent.Data)
	defer r.inline.Delete(space + "/" + message.ContentID)
	for _, existing := range s.runs {
		if existing.key == key && existing.view.ActorID == actor {
			if existing.digest != digest {
				return "", "", storage.ErrConflict
			}
			if err := r.ensureRun(ctx, s, existing.view.ID); err != nil {
				return "", "", err
			}
			return batch.SessionID, existing.view.ID, nil
		}
	}
	if s.active != "" || s.maintenance {
		return "", "", storage.ErrActive
	}
	d := fromJSON[journal.RunData](runEvent.Data)
	if d.Snapshot == nil || !storage.ValidShortID(d.Snapshot.ID) {
		return "", "", storage.ErrInvalid
	}
	if err := run.CheckWorkspaceSubmission(s.session.WorkspaceError, d.Snapshot.WorkspaceCleanup); err != nil {
		return "", "", err
	}
	if d.Snapshot.BaseSnapshotID != s.session.LatestSnapshotID {
		return "", "", storage.ErrConflict
	}
	body, err := r.inlineBytes(space, message.ContentID)
	if err != nil {
		return "", "", err
	}
	if message.Attachments == nil {
		message.Attachments = []run.Attachment{}
	}
	v := d.Snapshot
	entry := sessionlog.Accepted{Header: sessionlog.Header{Seq: int64(len(s.records)) + 1, Type: sessionlog.RunAccepted, Role: "user", RunID: v.ID},
		ActorID: actor, EventID: runEvent.EventID, MessageID: message.ID, UserEventID: msgEvent.EventID,
		AcceptedAt: v.CreatedAt, RequestKey: key, RequestDigest: digest, BaseSnapshotID: orNil(v.BaseSnapshotID), WorkspaceCleanup: v.WorkspaceCleanup,
		Message:   sessionlog.Message{Text: string(body), Attachments: message.Attachments},
		Execution: sessionlog.Execution{Build: v.Build, Model: v.Definition.Model, Tools: v.Definition.Tools, ToolPolicy: v.Definition.ToolPolicy, ModelAttempts: v.Definition.ModelAttempts}}
	err = r.commit(ctx, s, v.ID, entry)
	if int64(len(s.records)) == entry.Seq {
		r.restoreAccepted(s, entry)
	}
	if err != nil {
		return "", "", err
	}
	if err := r.insertRun(ctx, s.session.ID, v.ID); err != nil {
		return "", "", err
	}
	return batch.SessionID, v.ID, nil
}

func (r *Runtime) ensureRun(ctx context.Context, s *conversation, id string) error {
	var status string
	err := r.DB.DB.QueryRowContext(ctx, "SELECT status FROM runs WHERE id=?", id).Scan(&status)
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err := r.uploadLocal(ctx, s); err != nil {
		return err
	}
	return r.insertRun(ctx, s.session.ID, id)
}

func (r *Runtime) uploadLocal(ctx context.Context, s *conversation) error {
	err := r.Log.Upload(ctx, s.session.SpaceID, s.session.ID)
	if errors.Is(err, os.ErrNotExist) {
		// On cold reads the records already came from the committed object.
		return nil
	}
	return err
}

func (r *Runtime) insertRun(ctx context.Context, session, id string) error {
	_, err := r.DB.DB.ExecContext(ctx, "INSERT INTO runs(id,session_id,status,error_code) VALUES(?,?,'queued','')", id, session)
	return err
}

func (r *Runtime) RegisterAttachment(ctx context.Context, actor, space string, a run.Attachment) error {
	s, err := r.memberSession(ctx, actor, space, a.SessionID)
	if err != nil {
		return err
	}
	defer s.Unlock()
	o, err := r.Objects.Object(ctx, actor, space, a.Ref.ObjectID, false)
	if err != nil {
		return err
	}
	defer r.Objects.Forget(space, o.ID)
	if o.SHA256 != a.Ref.SHA256 || o.Size != a.Size {
		return storage.ErrIntegrity
	}
	entry := sessionlog.Attachment{Header: sessionlog.Header{Seq: int64(len(s.records)) + 1, Type: sessionlog.AttachmentAdded, Role: "system"}, Value: a}
	if err := r.commit(ctx, s, "", entry); err != nil {
		return err
	}
	s.attachments[a.ID] = a
	s.blobs[o.ID] = Reference(o)
	return nil
}

func (r *Runtime) RuntimeAttachment(ctx context.Context, actor, space, session, id string) (run.Attachment, error) {
	s, err := r.memberSession(ctx, actor, space, session)
	if err != nil {
		return run.Attachment{}, err
	}
	defer s.Unlock()
	a, ok := s.attachments[id]
	if !ok {
		return a, identity.ErrNotFound
	}
	return a, nil
}

func (r *Runtime) AppendEvents(ctx context.Context, actor string, batch journal.Batch) ([]journal.Event, error) {
	if err := r.access(ctx, actor, batch.SpaceID); err != nil {
		return nil, err
	}
	return r.appendEvents(ctx, batch)
}

func (r *Runtime) appendEvents(ctx context.Context, batch journal.Batch) ([]journal.Event, error) {
	s, err := r.lock(ctx, batch.SpaceID, batch.SessionID)
	if err != nil {
		return nil, err
	}
	defer s.Unlock()
	if batch.ExpectedSequence != nil && *batch.ExpectedSequence != s.session.LastSequence {
		return nil, storage.ErrConflict
	}
	items := make([]journal.Event, 0, len(batch.Events))
	for _, p := range batch.Events {
		var prior *journal.Event
		for i := len(s.events) - 1; i >= 0; i-- {
			if s.events[i].EventID == p.EventID {
				prior = &s.events[i]
				break
			}
		}
		if prior != nil {
			if p.Type == "run.finished" {
				if err := r.publishSummary(ctx, s, s.runs[p.RunID]); err != nil {
					return nil, err
				}
			}
			items = append(items, *prior)
			continue
		}
		switch p.Type {
		case "artifact.published":
			v := fromJSON[run.Artifact](p.Data)
			o, ok := r.Objects.get(batch.SpaceID, v.ObjectID)
			if !ok {
				return nil, storage.ErrIntegrity
			}
			s.blobs[o.ID] = Reference(o)
			r.Objects.Forget(batch.SpaceID, o.ID)
		case "workspace.snapshot.created":
			snap := fromJSON[storage.Snapshot](p.Data)
			if o, ok := r.Objects.get(batch.SpaceID, snap.ObjectID); ok {
				s.blobs[o.ID] = Reference(o)
				r.Objects.Forget(batch.SpaceID, o.ID)
			}
			s.snapshots[snap.ID] = snap
			if snap.SourceRunID != "" {
				state := s.runs[snap.SourceRunID]
				state.view.WorkspaceSnapshotID = snap.ID
				if state.finished {
					if err := r.attachSnapshot(ctx, s, state, snap); err != nil {
						return nil, err
					}
				}
			}
		case "workspace.restored":
			v := fromJSON[journal.RestoreData](p.Data)
			entry := sessionlog.Restored{Header: sessionlog.Header{Seq: int64(len(s.records)) + 1, Type: sessionlog.WorkspaceRestored, Role: "system", RunID: p.RunID},
				EventID: p.EventID, OccurredAt: p.OccurredAt, SnapshotID: v.SnapshotID, Reason: v.Reason}
			// A start-time restoration is only needed for the active run.
			if p.RunID == "" {
				if err := r.commit(ctx, s, "", entry); err != nil {
					return nil, err
				}
				s.session.WorkspaceError = ""
			}
		case "run.finished":
			state := s.runs[p.RunID]
			if state == nil {
				return nil, identity.ErrNotFound
			}
			if err := r.finish(ctx, s, state, p); err != nil {
				return nil, err
			}
			if state.finished && s.events[len(s.events)-1].EventID == p.EventID {
				items = append(items, s.events[len(s.events)-1])
			}
			continue
		}
		items = append(items, s.append(p))
	}
	return items, nil
}

func (r *Runtime) finishedRecords(s *conversation, id string, final journal.Proposed) ([]any, sessionlog.Finished, error) {
	steps := r.Recorder.Steps(id)
	starts := map[string]journal.Event{}
	replies := map[string]journal.Event{}
	artifacts := map[string]run.Artifact{}
	summaries := map[string]journal.Event{}
	contexts := map[string]journal.ProjectionData{}
	for _, e := range s.events {
		if e.RunID != id {
			continue
		}
		switch e.Type {
		case "model.attempt.started":
			d := fromJSON[journal.AttemptData](e.Data)
			starts[d.ID] = e
		case "model.response.completed":
			d := fromJSON[journal.ResponseData](e.Data)
			replies[d.AttemptID] = e
		case "model.attempt.failed":
			d := fromJSON[journal.AttemptFailedData](e.Data)
			replies[d.AttemptID] = e
		case "artifact.published":
			d := fromJSON[run.Artifact](e.Data)
			artifacts[d.CallID] = d
		case "context.summary.created":
			d := fromJSON[journal.SummaryData](e.Data)
			summaries[d.ResponseEventID] = e
		case "context.projected":
			contexts[e.EventID] = fromJSON[journal.ProjectionData](e.Data)
		}
	}
	f := fromJSON[journal.FinishData](final.Data)
	state := s.runs[id]
	seq := int64(len(s.records))
	end := sessionlog.Finished{Status: f.Status, StartedAt: state.view.StartedAt, FinishedAt: final.OccurredAt,
		Error: orNil(f.Error), Failure: f.FailureDetail, EventID: final.EventID, SnapshotError: f.SnapshotError}
	if end.StartedAt != nil {
		end.DurationMS = ptr(end.FinishedAt.Sub(*end.StartedAt).Milliseconds())
	}
	var batch []any
	for _, step := range steps {
		start := starts[step.AttemptID]
		a := fromJSON[journal.AttemptData](start.Data)
		reply := replies[step.AttemptID]
		if start.EventID == "" || reply.EventID == "" {
			continue
		}
		seq++
		row := sessionlog.Step{Header: sessionlog.Header{Seq: seq, Type: sessionlog.ModelStep, Role: "assistant", RunID: id},
			Step: step.Step, AttemptID: step.AttemptID, RetryOf: step.RetryOf, Purpose: step.Purpose, StartedAt: step.StartedAt,
			StartID: start.EventID, EventID: reply.EventID, MessageID: a.MessageID, ProjectionEventID: a.ProjectionEventID,
			Model: step.Model, Tools: []sessionlog.Tool{}}
		var observation *run.ModelObservation
		if reply.Type == "model.response.completed" {
			observation = fromJSON[journal.ResponseData](reply.Data).Observation
		} else {
			observation = fromJSON[journal.AttemptFailedData](reply.Data).Observation
		}
		if observation != nil {
			row.Component = observation.Component
		}
		if projection, ok := contexts[a.ProjectionEventID]; ok {
			raw, err := r.inlineBytes(s.session.SpaceID, projection.ContentID)
			if err != nil {
				return nil, end, err
			}
			prepared := fromJSON[run.Prepared](raw)
			prepared.Messages, prepared.Tools = nil, nil
			row.Context = &prepared
			delete(contexts, a.ProjectionEventID)
		}
		for _, t := range step.Tools {
			tool := sessionlog.Tool{Role: "tool", ToolCallID: t.NativeCallID, CallID: t.CallID, Name: t.Name,
				Status: t.Status, DurationMS: t.DurationMS, Result: t.Result}
			if a, ok := artifacts[t.CallID]; ok {
				tool.Artifact = ptr(a)
				tool.ArtifactSize = s.blobs[a.ObjectID].Size
			}
			row.Tools = append(row.Tools, tool)
			r.tools.Store(id+"/"+t.CallID, t)
		}
		batch = append(batch, row)
		if reply.EventID == f.FinalResponseEventID {
			end.FinalModelSeq = ptr(seq)
		}
		if e, ok := summaries[reply.EventID]; ok {
			d := fromJSON[journal.SummaryData](e.Data)
			raw, err := r.inlineBytes(s.session.SpaceID, d.ContentID)
			if err != nil {
				return nil, end, err
			}
			meta := fromJSON[run.ContextSummary](raw)
			meta.Text = ""
			modelSeq := seq
			seq++
			batch = append(batch, sessionlog.Summary{Header: sessionlog.Header{Seq: seq, Type: sessionlog.ContextSummary, Role: "assistant", RunID: id},
				ModelSeq: modelSeq, CoveredThrough: d.Through, EventID: e.EventID, ContentID: d.ContentID, Metadata: meta})
		}
	}
	if state.view.WorkspaceSnapshotID != "" {
		snap := s.snapshots[state.view.WorkspaceSnapshotID]
		spec, err := r.snapshotRecord(s, snap)
		if err != nil {
			return nil, end, err
		}
		end.WorkspaceSnapshot = spec
	} else if state.view.WorkspaceCleanup && end.SnapshotError == "" {
		// A cleanup that ends before saving (for example, while queued) leaves the workspace blocked.
		end.SnapshotError = s.session.WorkspaceError
	}
	end.Header = sessionlog.Header{Seq: seq + 1, Type: sessionlog.RunFinished, Role: "system", RunID: id}
	return append(batch, end), end, nil
}

func (r *Runtime) finish(ctx context.Context, s *conversation, state *runState, event journal.Proposed) error {
	if state.final != nil {
		return r.publishSummary(ctx, s, state)
	}
	batch, end, err := r.finishedRecords(s, event.RunID, event)
	if err != nil {
		return err
	}
	err = r.commit(ctx, s, end.RunID, batch...)
	if int64(len(s.records)) == end.Seq {
		state.final = &end
		if err != nil {
			// The local terminal record is complete and the upload worker retries it.
			state.uploadFailure = true
			return nil
		}
	}
	if err != nil {
		return err
	}
	return r.updateSummary(ctx, s, state)
}

func (r *Runtime) publishSummary(ctx context.Context, s *conversation, state *runState) error {
	if state == nil || state.final == nil {
		return storage.ErrIntegrity
	}
	if err := r.uploadLocal(ctx, s); err != nil {
		if !state.uploadFailure {
			r.persistFailed(s, state.final.RunID, "upload", state.final.Seq, err)
			state.uploadFailure = true
		}
		return err
	}
	state.uploadFailure = false
	return r.updateSummary(ctx, s, state)
}

func (r *Runtime) updateSummary(ctx context.Context, s *conversation, state *runState) error {
	end := state.final
	code := ""
	if end.Error != nil {
		code = *end.Error
	}
	_, err := r.DB.DB.ExecContext(ctx, "UPDATE runs SET status=?,error_code=?,started_at=?,finished_at=? WHERE id=? AND session_id=?",
		end.Status, code, end.StartedAt, end.FinishedAt, end.RunID, s.session.ID)
	if err != nil {
		if !state.summaryFailure {
			r.persistFailed(s, end.RunID, "terminal_index", end.Seq, err)
			state.summaryFailure = true
		}
		return err
	}
	state.summaryFailure = false
	state.view.Status, state.view.Error, state.view.FinishedAt, state.view.SnapshotError = end.Status, code, ptr(end.FinishedAt), end.SnapshotError
	if end.FinalModelSeq != nil {
		state.view.FinalResponseEventID = fromJSON[sessionlog.Step](s.records[*end.FinalModelSeq-1]).EventID
	}
	s.session.WorkspaceError = end.SnapshotError
	if end.WorkspaceSnapshot != nil {
		s.session.LatestSnapshotID = end.WorkspaceSnapshot.ID
	}
	if s.active == end.RunID {
		s.active = ""
	}
	if !state.finished {
		s.append(finishedEvent(state.view, *end))
	}
	state.finished = true
	r.Recorder.Forget(end.RunID)
	r.Live.Finished(s.session.SpaceID, end.RunID)
	return nil
}

func (r *Runtime) snapshotRecord(s *conversation, snap storage.Snapshot) (*sessionlog.WorkspaceSnapshot, error) {
	raw, err := r.inlineBytes(s.session.SpaceID, snap.ManifestRef)
	if err != nil {
		return nil, err
	}
	ref, ok := s.blobs[snap.ObjectID]
	if !ok {
		return nil, storage.ErrIntegrity
	}
	return &sessionlog.WorkspaceSnapshot{ID: snap.ID, ArchiveSHA256: snap.SHA256, ArchiveSize: ref.Size,
		Manifest: fromJSON[storage.Manifest](raw)}, nil
}

// Explicit retry enriches the terminal record with the first committed snapshot.
func (r *Runtime) attachSnapshot(ctx context.Context, s *conversation, state *runState, snap storage.Snapshot) error {
	spec, err := r.snapshotRecord(s, snap)
	if err != nil {
		return err
	}
	f := *state.final
	f.WorkspaceSnapshot, f.SnapshotError = spec, ""
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	records := append([]json.RawMessage(nil), s.records...)
	records[f.Seq-1] = raw
	if err = r.Log.Save(ctx, s.session.SpaceID, s.session.ID, records); err != nil {
		r.persistFailed(s, f.RunID, "local_save", f.Seq, err)
		return err
	}
	s.records, state.final = records, &f
	return r.publishSummary(ctx, s, state)
}

func (r *Runtime) Cleanup(s run.Snapshot) resource.CleanupStore {
	return &cleanup{Runtime: r, snapshot: s}
}
