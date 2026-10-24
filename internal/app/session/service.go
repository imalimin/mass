// Package session accepts requests by committing facts; it never executes runs.
package session

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/app/catalog"
	"github.com/imalimin/mass/internal/app/identity"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
	"github.com/imalimin/mass/internal/resource"
)

type Repository interface {
	RegisterAttachment(context.Context, string, string, run.Attachment) error
	RuntimeAttachment(context.Context, string, string, string, string) (run.Attachment, error)
	FindRuntimeRequest(context.Context, string, string, string, string, string) (string, string, bool, error)
	AcceptRuntimeRequest(context.Context, string, string, string, string, string, journal.Batch) (string, string, error)
	RuntimeSession(context.Context, string, string, string) (run.Session, error)
	RuntimeSessions(context.Context, string, string, identity.Query) ([]run.Session, error)
	RuntimeRun(context.Context, string, string, string) (run.View, error)
	RuntimeMessages(context.Context, string, string, string, int64, int) ([]run.MessageView, error)
	CancelRuntimeRun(context.Context, string, string, string) (run.View, error)
	RuntimeArtifacts(context.Context, string, string, string) ([]run.Artifact, error)
	RuntimeArtifact(context.Context, string, string, string) (run.Artifact, error)
}
type Service struct {
	ToolPolicy   run.ToolPolicy
	ResolveTools func([]run.ToolSpec) ([]run.ToolSpec, error)
	Repo         Repository
	Catalog      *catalog.Service
	Identity     *identity.Service
	Content      *resource.ContentStore
	Build        string
	Ready        func(context.Context) error
	Notify       func()
}
type CreateInput struct {
	Key              string `json:"idempotency_key"`
	AgentID          string `json:"agent_id"`
	VersionID        string `json:"version_id"`
	SourceSnapshotID string `json:"source_workspace_snapshot_id"`
	Title            string `json:"title"`
}
type SubmitInput struct {
	Key         string   `json:"idempotency_key"`
	Text        string   `json:"text"`
	Attachments []string `json:"attachment_ids,omitempty"`
}

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

func Ref(o storage.Object, role string) storage.Ref {
	return storage.Ref{Role: role, ObjectID: o.ID, SHA256: o.SHA256}
}
func (s *Service) ready(ctx context.Context) error {
	if s.Ready != nil {
		return s.Ready(ctx)
	}
	return nil
}
func (s *Service) Create(ctx context.Context, actor, space string, in CreateInput) (run.Session, error) {
	if !keyPattern.MatchString(in.Key) || !storage.ValidID(in.AgentID) || len(in.Title) > 200 || !utf8.ValidString(in.Title) || (in.VersionID != "" && !storage.ValidID(in.VersionID)) || (in.SourceSnapshotID != "" && !storage.ValidID(in.SourceSnapshotID)) {
		return run.Session{}, identity.ErrInvalid
	}
	digest := journal.Digest(in)
	id, _, found, err := s.Repo.FindRuntimeRequest(ctx, actor, space, "session", in.Key, digest)
	if err != nil {
		return run.Session{}, err
	}
	if found {
		return s.Repo.RuntimeSession(ctx, actor, space, id)
	}
	if err = s.ready(ctx); err != nil {
		return run.Session{}, err
	}
	draft, err := s.Catalog.Repo.CatalogDraft(ctx, actor, space, "agent", in.AgentID)
	if err != nil {
		return run.Session{}, err
	}
	if draft.Archived {
		return run.Session{}, catalog.ErrConfig
	}
	versionID := in.VersionID
	if versionID == "" {
		versionID = draft.DefaultVersionID
	}
	if versionID == "" {
		return run.Session{}, catalog.ErrConfig
	}
	version, err := s.Catalog.Version(ctx, actor, space, "agent", in.AgentID, versionID)
	if err != nil {
		return run.Session{}, err
	}
	if version.Archived || version.Bundle.Config == nil || version.Bundle.Model == nil {
		return run.Session{}, catalog.ErrConfig
	}
	b := version.Bundle
	c := b.Config
	m := b.Model
	d := run.Definition{VersionID: version.ID, AgentID: in.AgentID, Debug: version.Debug, PlatformVersion: run.PlatformVersion, Platform: run.PlatformInstructions, Instructions: b.Document.Markdown, Skills: []run.Skill{}, Model: Model(*m), Tools: []run.ToolSpec{}, MaxInputTokens: c.Context.MaxInputTokens, OutputTokens: c.Context.ReservedOutputTokens, MaxSteps: c.Limits.MaxSteps, TimeoutSeconds: c.Limits.TimeoutSeconds}
	refs := []storage.Ref{{Role: "agent_version", ObjectID: version.ContentID, SHA256: version.SHA256}}
	for i, binding := range b.Bindings {
		skill, err := s.Catalog.Version(ctx, actor, space, "skill", binding.SkillID, binding.VersionID)
		if err != nil {
			return run.Session{}, err
		}
		if skill.SHA256 != binding.SHA256 {
			return run.Session{}, storage.ErrIntegrity
		}
		resources := []run.SkillResource{}
		for _, a := range skill.Bundle.Document.Assets {
			resources = append(resources, run.SkillResource{Path: a.Path, ObjectID: a.ObjectID, SHA256: a.SHA256, Size: a.Size, MediaType: a.MediaType})
		}
		d.Skills = append(d.Skills, run.Skill{Resources: resources, ID: binding.SkillID, VersionID: binding.VersionID, Version: binding.Version, SHA256: binding.SHA256, Markdown: skill.Bundle.Document.Markdown})
		refs = append(refs, storage.Ref{Role: "skill_version", Ordinal: i, ObjectID: skill.ContentID, SHA256: skill.SHA256})
	}
	for _, name := range c.Tools {
		d.Tools = append(d.Tools, run.ToolSpec{Name: name, Version: "1"})
	}
	raw, _ := json.Marshal(d)
	o, err := s.Content.Put(ctx, actor, space, uuid.NewString(), "session.definition", "application/json", bytes.NewReader(raw))
	if err != nil {
		return run.Session{}, err
	}
	defRef := Ref(o, "definition")
	refs = append(refs, defRef)
	id = uuid.NewString()
	event := journal.New(actor, "session.created", "", journal.SessionData{SourceSnapshotID: in.SourceSnapshotID, DefinitionRef: &defRef, DefinitionID: in.AgentID, VersionID: version.ID, Title: in.Title}, refs...)
	batch, err := journal.Prepare(actor, journal.Request{SpaceID: space, SessionID: id, AppendKey: uuid.NewString(), Events: []journal.Proposed{event}})
	if err != nil {
		return run.Session{}, err
	}
	id, _, err = s.Repo.AcceptRuntimeRequest(ctx, actor, space, "session", in.Key, digest, batch)
	if err != nil {
		return run.Session{}, err
	}
	return s.Repo.RuntimeSession(ctx, actor, space, id)
}
func Model(m identity.Model) run.ModelConfig {
	return run.ModelConfig{ID: m.ID, Provider: m.Provider, ModelID: m.ModelID, Endpoint: m.Endpoint, CredentialID: m.CredentialID, Revision: m.Revision, ContextWindow: m.ContextWindow, MaxOutputTokens: m.MaxOutputTokens, Temperature: m.Temperature}
}
func (s *Service) Definition(ctx context.Context, actor, space string, ref storage.Ref) (run.Definition, error) {
	var d run.Definition
	o, f, err := s.Content.Read(ctx, actor, space, ref.ObjectID)
	if err != nil {
		return d, err
	}
	defer f.Close()
	if o.SHA256 != ref.SHA256 {
		return d, storage.ErrIntegrity
	}
	raw, err := io.ReadAll(io.LimitReader(f, 16<<20+1))
	if err != nil || len(raw) > 16<<20 {
		return d, storage.ErrIntegrity
	}
	if err = journal.Decode(raw, &d); err != nil {
		return d, err
	}
	return d, nil
}
func (s *Service) Submit(ctx context.Context, actor, space, sessionID string, in SubmitInput) (run.View, error) {
	if !keyPattern.MatchString(in.Key) || len(in.Text) > 256<<10 || !utf8.ValidString(in.Text) || strings.TrimSpace(in.Text) == "" {
		return run.View{}, identity.ErrInvalid
	}
	if len(in.Attachments) > 16 {
		return run.View{}, identity.ErrInvalid
	}
	seen := map[string]bool{}
	for _, id := range in.Attachments {
		if !storage.ValidID(id) || seen[id] {
			return run.View{}, identity.ErrInvalid
		}
		seen[id] = true
	}
	digest := journal.Digest(struct {
		Session string
		Input   SubmitInput
	}{sessionID, in})
	_, id, found, err := s.Repo.FindRuntimeRequest(ctx, actor, space, "run", in.Key, digest)
	if err != nil {
		return run.View{}, err
	}
	if found {
		return s.Repo.RuntimeRun(ctx, actor, space, id)
	}
	if err = s.ready(ctx); err != nil {
		return run.View{}, err
	}
	session, err := s.Repo.RuntimeSession(ctx, actor, space, sessionID)
	if err != nil {
		return run.View{}, err
	}
	definition, err := s.Definition(ctx, actor, space, session.DefinitionRef)
	if err != nil {
		return run.View{}, err
	}
	if s.ResolveTools != nil {
		definition.Tools, err = s.ResolveTools(definition.Tools)
		if err != nil {
			return run.View{}, err
		}
	}
	model, err := s.Identity.Repo.Model(ctx, actor, space, definition.Model.ID)
	if err != nil {
		return run.View{}, err
	}
	definition.Model = Model(model)
	if !model.Enabled || definition.MaxInputTokens+definition.OutputTokens > model.ContextWindow || definition.OutputTokens > model.MaxOutputTokens {
		return run.View{}, identity.ErrCapability
	}
	if _, err = s.Identity.ModelSecret(ctx, actor, space, model.ID); err != nil {
		return run.View{}, err
	}
	o, err := s.Content.Put(ctx, actor, space, uuid.NewString(), "user.message", "text/plain; charset=utf-8", strings.NewReader(in.Text))
	if err != nil {
		return run.View{}, err
	}
	attachments := []run.Attachment{}
	refs := []storage.Ref{Ref(o, "content")}
	for i, id := range in.Attachments {
		a, e := s.Repo.RuntimeAttachment(ctx, actor, space, sessionID, id)
		if e != nil {
			return run.View{}, e
		}
		attachments = append(attachments, a)
		ref := a.Ref
		ref.Ordinal = i
		refs = append(refs, ref)
	}
	message := journal.New(actor, "user.message.accepted", "", journal.UserMessageData{ID: uuid.NewString(), ContentID: o.ID, Attachments: attachments}, refs...)
	definition.ToolPolicy = s.ToolPolicy
	id = uuid.NewString()
	definition.Instructions = ""
	definition.Platform = ""
	for i := range definition.Skills {
		definition.Skills[i].Markdown = ""
		// The fixed version inventory is retained in the immutable definition and
		// session.created references, rather than duplicated in the bounded event.
		definition.Skills[i].Resources = nil
	}
	snapshot := run.Snapshot{ID: id, SpaceID: space, SessionID: sessionID, ActorID: actor, DefinitionRef: session.DefinitionRef, UserEventID: message.EventID, BaseSnapshotID: session.LatestSnapshotID, Definition: definition, Build: s.Build, Profile: "mass-agui-v1", MapperVersion: 1, ParserVersion: 1, CreatedAt: time.Now().UTC().Truncate(time.Microsecond)}
	created := journal.New(actor, "run.created", id, journal.RunData{Snapshot: &snapshot})
	created.CausationID = message.EventID
	batch, err := journal.Prepare(actor, journal.Request{SpaceID: space, SessionID: sessionID, AppendKey: uuid.NewString(), Events: []journal.Proposed{message, created}})
	if err != nil {
		return run.View{}, err
	}
	_, id, err = s.Repo.AcceptRuntimeRequest(ctx, actor, space, "run", in.Key, digest, batch)
	if err != nil {
		return run.View{}, err
	}
	if s.Notify != nil {
		s.Notify()
	}
	return s.Repo.RuntimeRun(ctx, actor, space, id)
}
