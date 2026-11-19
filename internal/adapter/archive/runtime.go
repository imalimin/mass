package archive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/adapter/mariadb"
	"github.com/imalimin/mass/internal/app/identity"
	"github.com/imalimin/mass/internal/diagnostic"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
	"github.com/imalimin/mass/internal/resource/sessionlog"
	"github.com/imalimin/mass/internal/resource/trace"
	"github.com/imalimin/mass/internal/transport/agui"
)

type Runtime struct {
	DB            *mariadb.Store
	Lease         *mariadb.RuntimeLease
	Objects       *Objects
	Log           *sessionlog.Store
	DiagnosticLog *diagnostic.Store
	Recorder      *trace.Recorder
	Live          *agui.Live
	CatalogObject ObjectLookup
	sessions      sync.Map
	inline        sync.Map
	tools         sync.Map
	create        sync.Mutex
}

type conversation struct {
	sync.Mutex
	loaded      bool
	records     []json.RawMessage
	session     run.Session
	definition  run.Definition
	runs        map[string]*runState
	events      []journal.Event
	attachments map[string]run.Attachment
	blobs       map[string]Ref
	snapshots   map[string]storage.Snapshot
	maintenance bool
	active      string
}

type runState struct {
	uploadFailure  bool
	summaryFailure bool
	view           run.View
	key            string
	digest         string
	finished       bool
	plan           *run.CleanupPlan
	final          *sessionlog.Finished
}

type inline struct {
	object storage.Object
	raw    []byte
}

func (r *Runtime) access(ctx context.Context, actor, space string) error {
	return r.DB.CheckAccess(ctx, actor, space, false)
}

func (r *Runtime) StoreInline(_ context.Context, o storage.Object, raw []byte) error {
	r.inline.Store(o.SpaceID+"/"+o.ID, inline{o, raw})
	return nil
}

func (r *Runtime) OpenInline(ctx context.Context, actor, space, id string) (storage.Object, io.ReadCloser, error) {
	if value, ok := r.inline.Load(space + "/" + id); ok {
		if err := r.access(ctx, actor, space); err != nil {
			return storage.Object{}, nil, err
		}
		item := value.(inline)
		return item.object, io.NopCloser(bytes.NewReader(item.raw)), nil
	}
	return storage.Object{}, nil, storage.ErrBlobNotFound
}

func (r *Runtime) remember(space, id, kind string, content any) storage.Ref {
	raw, ok := content.([]byte)
	if !ok {
		raw, _ = json.Marshal(content)
	}
	hash := sha256.Sum256(raw)
	o := storage.Object{SpaceID: space, ID: id, Kind: kind, State: "ready", Size: int64(len(raw)), SHA256: hex.EncodeToString(hash[:])}
	r.inline.Store(space+"/"+id, inline{o, raw})
	return storage.Ref{ObjectID: id, SHA256: o.SHA256}
}

func stable(id, tag string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(id+":"+tag)).String()
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, storage.ErrBlobNotFound) {
		return identity.ErrNotFound
	}
	return err
}

func newConversation() *conversation {
	return &conversation{runs: make(map[string]*runState), attachments: make(map[string]run.Attachment),
		blobs: make(map[string]Ref), snapshots: make(map[string]storage.Snapshot)}
}

// lock returns a conversation reconstructed from its log, with the write lock held.
func (r *Runtime) lock(ctx context.Context, space, id string) (*conversation, error) {
	if !storage.ValidShortID(space) || !storage.ValidShortID(id) {
		return nil, storage.ErrInvalid
	}
	value, _ := r.sessions.LoadOrStore(space+"/"+id, newConversation())
	s := value.(*conversation)
	s.Lock()
	if !s.loaded {
		records, err := r.Log.Read(ctx, space, id)
		if err == nil {
			err = r.rehydrate(s, records)
		}
		if err != nil {
			r.sessions.Delete(space + "/" + id)
			s.Unlock()
			return nil, notFound(err)
		}
		if s.session.SpaceID != space || s.session.ID != id {
			s.Unlock()
			r.sessions.Delete(space + "/" + id)
			return nil, storage.ErrIntegrity
		}
		s.records = records
		s.loaded = true
		if err := r.reconcileAccepted(ctx, s); err != nil {
			s.loaded = false
			r.sessions.Delete(space + "/" + id)
			s.Unlock()
			return nil, err
		}
	}
	return s, nil
}

// An accepted record can survive a process exit just before its SQL summary
// was inserted. Index it as interrupted so reopening the session never replays it.
func (r *Runtime) reconcileAccepted(ctx context.Context, s *conversation) error {
	if s.active == "" {
		return nil
	}
	var id string
	err := r.DB.DB.QueryRowContext(ctx, "SELECT id FROM runs WHERE id=?", s.active).Scan(&id)
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	state := s.runs[s.active]
	if err = r.insertRun(ctx, s.session.ID, state.view.ID); err != nil {
		return err
	}
	p := journal.New(state.view.ActorID, "run.finished", state.view.ID, journal.FinishData{Status: "failed", Error: "interrupted"})
	diagnostic.Record(diagnostic.WithRun(ctx, r.DiagnosticLog, state.view.Snapshot), "warn", "run.interrupted", "reason=recovery state=queued")
	return r.finish(ctx, s, state, p)
}

func (r *Runtime) commit(ctx context.Context, s *conversation, runID string, records ...any) error {
	space, session := s.session.SpaceID, s.session.ID
	if err := r.Log.Save(ctx, space, session, s.records, records...); err != nil {
		r.persistFailed(s, runID, "local_save", int64(len(s.records)+len(records)), err)
		return err
	}
	for _, value := range records {
		raw, _ := json.Marshal(value)
		s.records = append(s.records, raw)
	}
	err := r.Log.Upload(ctx, space, session)
	if err != nil {
		r.persistFailed(s, runID, "upload", int64(len(s.records)), err)
	}
	return err
}

func (r *Runtime) persistFailed(s *conversation, runID, stage string, seq int64, err error) {
	if r.DiagnosticLog != nil {
		msg := fmt.Sprintf("stage=%s through_seq=%d", stage, seq)
		if cause := diagnostic.Cause(err); cause != "unknown" {
			msg += " cause=" + cause
		}
		r.DiagnosticLog.Write(s.session.SpaceID, s.session.ID, runID, "warn", "session.persist_failed", msg)
	}
}

func (s *conversation) append(p journal.Proposed) journal.Event {
	if p.Refs == nil {
		p.Refs = []storage.Ref{}
	}
	e := journal.Event{Proposed: p, SpaceID: s.session.SpaceID, SessionID: s.session.ID,
		Sequence: int64(len(s.events)) + 1, RecordedAt: p.OccurredAt}
	s.events = append(s.events, e)
	s.session.LastSequence = e.Sequence
	return e
}

func (r *Runtime) rehydrate(s *conversation, records []json.RawMessage) error {
	for _, raw := range records {
		var h sessionlog.Header
		if err := json.Unmarshal(raw, &h); err != nil {
			return err
		}
		switch h.Type {
		case sessionlog.SessionCreated:
			var d sessionlog.Created
			if err := json.Unmarshal(raw, &d); err != nil {
				return err
			}
			r.restoreCreated(s, d)
		case sessionlog.RunAccepted:
			var d sessionlog.Accepted
			if err := json.Unmarshal(raw, &d); err != nil {
				return err
			}
			r.restoreAccepted(s, d)
		case sessionlog.ModelStep:
			var d sessionlog.Step
			if err := json.Unmarshal(raw, &d); err != nil {
				return err
			}
			r.restoreStep(s, d)
		case sessionlog.ContextSummary:
			var d sessionlog.Summary
			if err := json.Unmarshal(raw, &d); err != nil {
				return err
			}
			if err := r.restoreSummary(s, records, d); err != nil {
				return err
			}
		case sessionlog.RunFinished:
			var d sessionlog.Finished
			if err := json.Unmarshal(raw, &d); err != nil {
				return err
			}
			if err := r.restoreFinished(s, records, d); err != nil {
				return err
			}
		case sessionlog.AttachmentAdded:
			var d sessionlog.Attachment
			if err := json.Unmarshal(raw, &d); err != nil {
				return err
			}
			s.attachments[d.Value.ID] = d.Value
			ref := r.fileRef(s, d.Value.Ref.ObjectID, "attachment", d.Value.MediaType, d.Value.Ref.SHA256, d.Value.Size)
			s.blobs[ref.ID] = ref
		case sessionlog.WorkspaceRestored:
			var d sessionlog.Restored
			if err := json.Unmarshal(raw, &d); err != nil {
				return err
			}
			s.session.WorkspaceError = ""
			e := journal.New(s.session.ActorID, "workspace.restored", d.RunID, journal.RestoreData{SnapshotID: d.SnapshotID, Reason: d.Reason})
			e.EventID, e.OccurredAt = d.EventID, d.OccurredAt
			s.append(e)
		}
	}
	return nil
}

func fromJSON[T any](raw []byte) T {
	var v T
	_ = json.Unmarshal(raw, &v)
	return v
}

func ptr[T any](value T) *T { return &value }
func orNil(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func (r *Runtime) restoreCreated(s *conversation, d sessionlog.Created) {
	s.definition = d.Definition.Runtime(d.AgentID, d.VersionID)
	ref := r.remember(d.SpaceID, stable(d.SessionID, "definition"), "session.definition", s.definition)
	ref.Role = "definition"
	s.session = run.Session{ID: d.SessionID, SpaceID: d.SpaceID, ActorID: d.ActorID, Title: d.Title,
		DefinitionID: d.AgentID, VersionID: d.VersionID, DefinitionRef: ref, CreatedAt: d.CreatedAt}
	e := journal.New(d.ActorID, "session.created", "", journal.SessionData{DefinitionRef: &ref,
		DefinitionID: d.AgentID, VersionID: d.VersionID, Title: d.Title}, ref)
	e.EventID, e.OccurredAt = d.EventID, d.CreatedAt
	s.append(e)
}

func (r *Runtime) restoreAccepted(s *conversation, d sessionlog.Accepted) {
	ref := r.remember(s.session.SpaceID, stable(d.RunID, "user"), "user.message", []byte(d.Message.Text))
	ref.Role = "content"
	e := journal.New(d.ActorID, "user.message.accepted", "", journal.UserMessageData{
		ID: d.MessageID, ContentID: ref.ObjectID, Attachments: d.Message.Attachments}, ref)
	e.EventID, e.OccurredAt = d.UserEventID, d.AcceptedAt
	for i, attachment := range d.Message.Attachments {
		ref := attachment.Ref
		ref.Ordinal = i
		e.Refs = append(e.Refs, ref)
	}
	s.append(e)
	def := s.definition
	def.Model, def.Tools, def.ToolPolicy, def.ModelAttempts = d.Execution.Model, d.Execution.Tools, d.Execution.ToolPolicy, d.Execution.ModelAttempts
	def.Instructions, def.Platform = "", ""
	def.Skills = slices.Clone(def.Skills)
	for i := range def.Skills {
		def.Skills[i].Markdown, def.Skills[i].Resources = "", nil
	}
	v := run.Snapshot{ID: d.RunID, SpaceID: s.session.SpaceID, SessionID: s.session.ID, ActorID: d.ActorID,
		DefinitionRef: s.session.DefinitionRef, UserEventID: d.UserEventID, WorkspaceCleanup: d.WorkspaceCleanup, Definition: def, Build: d.Execution.Build,
		Profile: "mass-agui-v1", MapperVersion: 1, ParserVersion: 1, CreatedAt: d.AcceptedAt}
	if d.BaseSnapshotID != nil {
		v.BaseSnapshotID = *d.BaseSnapshotID
	}
	created := journal.New(d.ActorID, "run.created", d.RunID, journal.RunData{Snapshot: &v})
	created.EventID, created.CausationID, created.OccurredAt = d.EventID, d.UserEventID, d.AcceptedAt
	s.append(created)
	s.runs[d.RunID] = &runState{view: run.View{Snapshot: v, Status: "queued"}, key: d.RequestKey, digest: d.RequestDigest}
	s.active = d.RunID
}

func stepUsage(u *trace.Usage) run.Usage {
	if u == nil {
		return run.Usage{}
	}
	return run.Usage{Input: ptr(u.InputTokens), Output: ptr(u.OutputTokens), CacheRead: ptr(u.InputCachedTokens)}
}

func (r *Runtime) restoreStep(s *conversation, d sessionlog.Step) {
	state := s.runs[d.RunID]
	if state == nil {
		return
	}
	a := state.view.ActorID
	if state.view.Status == "queued" {
		start := journal.New(a, "run.started", d.RunID, journal.StartData{})
		start.EventID, start.OccurredAt = stable(d.RunID, "started"), d.StartedAt
		s.append(start)
		state.view.Status = "running"
	}
	if d.Context != nil {
		v := d.Context
		ref := r.remember(s.session.SpaceID, stable(v.EventID, "projection"), "context.projection", v)
		ref.Role = "projection"
		e := journal.New(a, "context.projected", d.RunID, journal.ProjectionData{ID: v.ID, ContentID: ref.ObjectID,
			Fingerprint: v.Fingerprint, Through: v.Spec.Through, Counter: v.Spec.Counter, Epoch: v.Spec.Epoch,
			Purpose: v.Spec.Purpose, ParentID: v.Spec.ParentID, PrefixChangeReason: v.Spec.PrefixChangeReason}, ref)
		e.EventID, e.OccurredAt = v.EventID, d.StartedAt
		s.append(e)
	}
	start := journal.New(a, "model.attempt.started", d.RunID, journal.AttemptData{
		ID: d.AttemptID, RetryOf: d.RetryOf, MessageID: d.MessageID, Purpose: d.Purpose,
		ProjectionEventID: d.ProjectionEventID, Source: "ark"})
	start.EventID, start.OccurredAt = d.StartID, d.StartedAt
	s.append(start)
	when := d.StartedAt.Add(time.Duration(d.Model.DurationMS) * time.Millisecond)
	usage := stepUsage(d.Model.Usage)
	var observation *run.ModelObservation
	if d.Component != "" {
		observation = &run.ModelObservation{HTTPStatus: d.Model.HTTPStatus, DurationMS: d.Model.DurationMS, FirstTokenMS: d.Model.FirstTextMS, Component: d.Component}
	}
	if d.Model.Status != "succeeded" {
		e := journal.New(a, "model.attempt.failed", d.RunID, journal.AttemptFailedData{
			AttemptID: d.AttemptID, MessageID: d.MessageID, Error: d.Model.Error, Preview: preview(d.Model.Text), Usage: usage, Observation: observation})
		e.EventID, e.OccurredAt, e.CausationID = d.EventID, when, d.StartID
		s.append(e)
		return
	}
	calls := []run.Call{}
	for i, call := range d.Model.ToolCalls {
		if i >= len(d.Tools) {
			break
		}
		tool := d.Tools[i]
		calls = append(calls, run.Call{ID: tool.CallID, NativeID: call.ID, Name: call.Function.Name,
			Arguments: call.Function.Arguments, ArgumentsSHA256: journal.Digest(call.Function.Arguments), ResponseEventID: d.EventID})
	}
	reply := run.Reply{MessageID: d.MessageID, Message: run.Message{Role: "assistant", Content: d.Model.Text, ToolCalls: calls},
		FinishReason: d.Model.FinishReason, Usage: usage, Observation: observation, ResponseEventID: d.EventID, Source: "ark"}
	ref := r.remember(s.session.SpaceID, stable(d.EventID, "response"), "model.response", reply)
	ref.Role = "response"
	res := journal.New(a, "model.response.completed", d.RunID, journal.ResponseData{AttemptID: d.AttemptID, MessageID: d.MessageID,
		ContentID: ref.ObjectID, FinishReason: d.Model.FinishReason, Usage: usage, Observation: observation, Calls: calls, Preview: preview(d.Model.Text)}, ref)
	res.EventID, res.OccurredAt, res.CausationID = d.EventID, when, d.StartID
	s.append(res)
	for _, tool := range d.Tools {
		r.tools.Store(d.RunID+"/"+tool.CallID, trace.Tool{CallID: tool.CallID, NativeCallID: tool.ToolCallID, Name: tool.Name,
			Status: tool.Status, DurationMS: tool.DurationMS, Result: tool.Result})
		if tool.Artifact != nil {
			v := *tool.Artifact
			ref := r.fileRef(s, v.ObjectID, "artifact", v.MediaType, v.SHA256, tool.ArtifactSize)
			s.blobs[ref.ID] = ref
			e := journal.New(a, "artifact.published", d.RunID, v)
			e.OccurredAt = when
			s.append(e)
		}
		if tool.Name == "update_plan" && tool.Status == "completed" {
			result := fromJSON[run.ToolResult](tool.Result)
			plan := fromJSON[run.Plan](result.Data)
			if plan.Revision != 0 {
				e := journal.New(a, "plan.updated", d.RunID, plan)
				e.OccurredAt = when
				s.append(e)
			}
		}
	}
}

func preview(text string) string {
	if len(text) <= 8192 {
		return text
	}
	text = text[:8192]
	return strings.ToValidUTF8(text, "")
}

func (r *Runtime) restoreSummary(s *conversation, records []json.RawMessage, d sessionlog.Summary) error {
	if d.ModelSeq < 1 || d.ModelSeq > int64(len(records)) {
		return storage.ErrIntegrity
	}
	step := fromJSON[sessionlog.Step](records[d.ModelSeq-1])
	metadata := d.Metadata
	metadata.Text = step.Model.Text
	sources := make(map[string]int64, len(s.events))
	for _, e := range s.events {
		sources[e.EventID] = e.Sequence
	}
	var coverage int64
	for _, id := range metadata.SourceEventIDs {
		seq, ok := sources[id]
		if !ok {
			return storage.ErrIntegrity
		}
		coverage = max(coverage, seq)
	}
	if coverage > 0 {
		metadata.Through = coverage
	}
	if metadata.PreviousRef != nil {
		if item, ok := r.inline.Load(s.session.SpaceID + "/" + metadata.PreviousRef.ObjectID); ok {
			metadata.PreviousRef.SHA256 = item.(inline).object.SHA256
		}
	}
	ref := r.remember(s.session.SpaceID, d.ContentID, "context.summary", metadata)
	ref.Role = "summary"
	e := journal.New(s.session.ActorID, "context.summary.created", d.RunID, journal.SummaryData{
		ContentID: ref.ObjectID, ResponseEventID: step.EventID, ProjectionID: metadata.ProjectionID,
		Through: metadata.Through, Policy: metadata.Policy}, ref)
	e.EventID = d.EventID
	s.append(e)
	return nil
}

func (r *Runtime) restoreFinished(s *conversation, records []json.RawMessage, d sessionlog.Finished) error {
	state := s.runs[d.RunID]
	if state == nil {
		return storage.ErrIntegrity
	}
	v := &state.view
	v.Status, v.StartedAt, v.FinishedAt, v.SnapshotError = d.Status, d.StartedAt, ptr(d.FinishedAt), d.SnapshotError
	if d.Error != nil {
		v.Error = *d.Error
	}
	if d.FinalModelSeq != nil {
		seq := *d.FinalModelSeq
		if seq < 1 || seq > int64(len(records)) {
			return storage.ErrIntegrity
		}
		v.FinalResponseEventID = fromJSON[sessionlog.Step](records[seq-1]).EventID
	}
	if d.WorkspaceSnapshot != nil {
		spec := d.WorkspaceSnapshot
		archiveID, manifestID := stable(d.RunID, "archive"), stable(d.RunID, "manifest")
		ref := r.fileRef(s, archiveID, "workspace.archive", "application/gzip", spec.ArchiveSHA256, spec.ArchiveSize)
		ref.Key = r.workspaceKey(s, spec.ID)
		ref.URI = r.Log.Blobs.URI(ref.Key)
		s.blobs[archiveID] = ref
		r.remember(s.session.SpaceID, manifestID, "workspace.manifest", spec.Manifest)
		snapshot := storage.Snapshot{ID: spec.ID, SpaceID: s.session.SpaceID, SessionID: s.session.ID, SourceRunID: d.RunID,
			BaseSnapshotID: v.BaseSnapshotID, ObjectID: archiveID, ManifestRef: manifestID, SHA256: spec.ArchiveSHA256, URI: ref.URI, FormatVersion: 1}
		s.snapshots[spec.ID] = snapshot
		v.WorkspaceSnapshotID, s.session.LatestSnapshotID = spec.ID, spec.ID
		e := journal.New(v.ActorID, "workspace.snapshot.created", d.RunID, snapshot)
		e.EventID, e.OccurredAt = stable(d.RunID, "snapshot.event"), d.FinishedAt
		s.append(e)
	}
	s.append(finishedEvent(*v, d))
	s.session.WorkspaceError = d.SnapshotError
	state.finished, state.final = true, &d
	if s.active == d.RunID {
		s.active = ""
	}
	return nil
}

func finishedEvent(v run.View, d sessionlog.Finished) journal.Proposed {
	e := journal.New(v.ActorID, "run.finished", d.RunID, journal.FinishData{Status: d.Status, Error: v.Error,
		SnapshotError: d.SnapshotError, FinalResponseEventID: v.FinalResponseEventID, FailureDetail: d.Failure})
	e.EventID, e.OccurredAt, e.Actor.Kind = d.EventID, d.FinishedAt, "system"
	return e
}

func (r *Runtime) fileRef(s *conversation, id, kind, media, hash string, size int64) Ref {
	key := strings.TrimSuffix(r.Log.Prefix, "/") + "/" + s.session.SpaceID + "/objects/" + id
	return Reference(storage.Object{ID: id, SpaceID: s.session.SpaceID, Kind: kind, MediaType: media, SHA256: hash, Size: size,
		Key: key, URI: r.Log.Blobs.URI(key), State: "ready"})
}

func (r *Runtime) workspaceKey(s *conversation, id string) string {
	return Key(r.Log.Prefix, s.session.SpaceID, "sessions", s.session.ID, "workspace", id+".tar.gz")
}

func (r *Runtime) memberSession(ctx context.Context, actor, space, id string) (*conversation, error) {
	if err := r.access(ctx, actor, space); err != nil {
		return nil, err
	}
	var hide bool
	err := r.DB.DB.QueryRowContext(ctx, "SELECT hide FROM sessions WHERE space_id=? AND id=?", space, id).Scan(&hide)
	if err != nil {
		return nil, notFound(err)
	}
	s, err := r.lock(ctx, space, id)
	if err == nil {
		s.session.Hide = hide
	}
	return s, err
}

func (r *Runtime) locateRun(ctx context.Context, runID string) (space, session string, err error) {
	err = r.DB.DB.QueryRowContext(ctx, "SELECT s.space_id,s.id FROM runs r JOIN sessions s ON s.id=r.session_id WHERE r.id=?", runID).Scan(&space, &session)
	return space, session, notFound(err)
}

func (r *Runtime) runSession(ctx context.Context, actor, space, id string) (*conversation, error) {
	if err := r.access(ctx, actor, space); err != nil {
		return nil, err
	}
	actual, session, err := r.locateRun(ctx, id)
	if err != nil {
		return nil, err
	}
	if actual != space {
		return nil, identity.ErrNotFound
	}
	return r.lock(ctx, space, session)
}

func (r *Runtime) sessionIDs(ctx context.Context, space string) ([]string, error) {
	rows, err := r.DB.DB.QueryContext(ctx, "SELECT id FROM sessions WHERE space_id=?", space)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *Runtime) Lookup(ctx context.Context, space, id string) (Ref, error) {
	ids, err := r.sessionIDs(ctx, space)
	if err != nil {
		return Ref{}, err
	}
	for _, session := range ids {
		s, err := r.lock(ctx, space, session)
		if err != nil {
			return Ref{}, err
		}
		ref, ok := s.blobs[id]
		s.Unlock()
		if ok {
			return ref, nil
		}
	}
	return Ref{}, identity.ErrNotFound
}

func sequencePage(events []journal.Event, id string, after int64, through *int64, limit int) (journal.Page, error) {
	page := journal.Page{Events: []journal.Event{}, Through: int64(len(events)), Next: after}
	if through != nil {
		if *through > page.Through {
			return page, storage.ErrInvalid
		}
		page.Through = *through
	}
	if after < 0 || after > page.Through || limit < 1 || limit > 100 {
		return page, storage.ErrInvalid
	}
	for _, e := range events[after:page.Through] {
		if id != "" && e.RunID != id {
			continue
		}
		if len(page.Events) == limit {
			page.HasMore = true
			break
		}
		page.Events = append(page.Events, e)
		page.Next = e.Sequence
	}
	if !page.HasMore {
		page.Next = page.Through
	}
	return page, nil
}

func (r *Runtime) ReadEvents(ctx context.Context, actor, space, session string, after int64, through *int64, limit int) (journal.Page, error) {
	s, err := r.memberSession(ctx, actor, space, session)
	if err != nil {
		return journal.Page{}, err
	}
	defer s.Unlock()
	return sequencePage(s.events, "", after, through, limit)
}
func (r *Runtime) ReadRunEvents(ctx context.Context, actor, space, session, id string, after int64, through *int64, limit int) (journal.Page, error) {
	s, err := r.memberSession(ctx, actor, space, session)
	if err != nil {
		return journal.Page{}, err
	}
	defer s.Unlock()
	if s.runs[id] == nil {
		return journal.Page{}, identity.ErrNotFound
	}
	return sequencePage(s.events, id, after, through, limit)
}

func (r *Runtime) sessionViews(s *conversation, id string) []run.MessageView {
	items := []run.MessageView{}
	purposes := map[string]string{}
	runByUser := map[string]string{}
	for _, v := range s.runs {
		runByUser[v.view.UserEventID] = v.view.ID
	}
	for _, e := range s.events {
		switch e.Type {
		case "model.attempt.started":
			d := fromJSON[journal.AttemptData](e.Data)
			purposes[d.ID] = d.Purpose
		case "user.message.accepted":
			d := fromJSON[journal.UserMessageData](e.Data)
			items = append(items, run.MessageView{ID: d.ID, Role: "user", RunID: runByUser[e.EventID], ContentID: d.ContentID,
				SourceEventID: e.EventID, Sequence: e.Sequence, Complete: true, Attachments: d.Attachments})
		case "model.response.completed":
			d := fromJSON[journal.ResponseData](e.Data)
			if purposes[d.AttemptID] == "summary" {
				continue
			}
			items = append(items, run.MessageView{ID: d.MessageID, Role: "assistant", RunID: e.RunID, ContentID: d.ContentID,
				SourceEventID: e.EventID, Sequence: e.Sequence, Complete: true})
		}
	}
	return items
}

func (r *Runtime) RuntimeMessages(ctx context.Context, actor, space, session string, after int64, limit int) ([]run.MessageView, error) {
	s, err := r.memberSession(ctx, actor, space, session)
	if err != nil {
		return nil, err
	}
	defer s.Unlock()
	items := r.sessionViews(s, "")
	items = slices.DeleteFunc(items, func(v run.MessageView) bool { return v.Sequence <= after })
	return items[:min(limit, len(items))], nil
}
func (r *Runtime) RuntimeMessagesBefore(ctx context.Context, actor, space, session string, before int64, limit int) ([]run.MessageView, error) {
	s, err := r.memberSession(ctx, actor, space, session)
	if err != nil {
		return nil, err
	}
	defer s.Unlock()
	items := r.sessionViews(s, "")
	if before != 0 {
		items = slices.DeleteFunc(items, func(v run.MessageView) bool { return v.Sequence >= before })
	}
	slices.Reverse(items)
	return items[:min(limit+1, len(items))], nil
}
