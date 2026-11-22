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
	RuntimeSessions(context.Context, string, string, string, identity.Query) ([]run.Session, error)
	RuntimeRun(context.Context, string, string, string) (run.View, error)
	RuntimeMessages(context.Context, string, string, string, int64, int) ([]run.MessageView, error)
	CancelRuntimeRun(context.Context, string, string, string) (run.View, error)
	RuntimeArtifacts(context.Context, string, string, string) ([]run.Artifact, error)
	RuntimeArtifact(context.Context, string, string, string) (run.Artifact, error)
}
type Service struct {
	ModelAttempts int
	ToolPolicy    run.ToolPolicy
	ResolveTools  func([]run.ToolSpec) ([]run.ToolSpec, error)
	Repo          Repository
	Catalog       *catalog.Service
	Identity      *identity.Service
	Content       *resource.ContentStore
	Workspaces    *resource.RunWorkspaces
	Build         string
	Ready         func(context.Context) error
	Notify        func()
}
type CreateInput struct {
	AgentSourceSpaceID    string           `json:"agent_source_space_id,omitempty"`
	Creativity            *run.Creativity  `json:"-"`
	Hide                  bool             `json:"hide,omitempty"`
	Key                   string           `json:"idempotency_key"`
	AgentID               string           `json:"agent_id"`
	VersionID             string           `json:"version_id"`
	SourceSnapshotID      string           `json:"source_workspace_snapshot_id"`
	Title                 string           `json:"title"`
	ExpectedDraftRevision int64            `json:"expected_draft_revision,omitempty"`
	ExpectedSkillDrafts   map[string]int64 `json:"-"`
}
type SubmitInput struct {
	Key         string   `json:"idempotency_key"`
	Text        string   `json:"text"`
	Attachments []string `json:"attachment_ids,omitempty"`
}

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

func ValidRequestKey(key string) bool { return keyPattern.MatchString(key) }

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
	if in.AgentSourceSpaceID == space {
		in.AgentSourceSpaceID = ""
	}
	shared := in.AgentSourceSpaceID != ""
	draftRequested := in.VersionID == catalog.Latest
	if (draftRequested && (shared || s.Content.Inline == nil || in.Creativity != nil)) || in.ExpectedDraftRevision < 0 || (in.ExpectedDraftRevision != 0 && !draftRequested) {
		return run.Session{}, identity.ErrInvalid
	}
	if shared && (!storage.ValidShortID(in.AgentSourceSpaceID) || in.VersionID != "" || in.SourceSnapshotID != "") {
		return run.Session{}, identity.ErrInvalid
	}
	if shared && s.Content.Inline == nil {
		return run.Session{}, catalog.ErrAgentNotPublic
	}
	if s.Content.Inline != nil && in.SourceSnapshotID != "" {
		return run.Session{}, identity.ErrInvalid
	}
	if !ValidRequestKey(in.Key) || !storage.ValidShortID(in.AgentID) || len(in.Title) > 200 || !utf8.ValidString(in.Title) || (in.VersionID != "" && !draftRequested && !storage.ValidID(in.VersionID)) || (in.SourceSnapshotID != "" && !storage.ValidID(in.SourceSnapshotID)) {
		return run.Session{}, identity.ErrInvalid
	}
	digest := journal.Digest(in)
	if in.Creativity != nil {
		digest = journal.Digest(struct {
			CreateInput
			Creativity *run.Creativity `json:"creativity"`
		}{in, in.Creativity})
	}
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
	if in.Creativity != nil && in.Creativity.TargetAgentID != "" {
		if s.Content.Inline == nil || !storage.ValidShortID(in.Creativity.TargetAgentID) {
			return run.Session{}, identity.ErrInvalid
		}
		in.Creativity, err = s.Catalog.ResolveIteration(ctx, actor, space, in.Creativity.TargetAgentID)
		if err != nil {
			return run.Session{}, err
		}
	}
	var agentName string
	var version catalog.Version
	var boundSkills []catalog.Bundle
	var draftRevision int64
	var draftPackages []run.DraftSkillPackage
	if shared {
		publicAgent, loadErr := s.Catalog.LoadPublicAgent(ctx, in.AgentSourceSpaceID, in.AgentID)
		if loadErr != nil {
			return run.Session{}, loadErr
		}
		agentName, version, boundSkills = publicAgent.Name, publicAgent.Version, publicAgent.Skills
	} else if draftRequested {
		var selection catalog.DraftAgentSelection
		selection, boundSkills, draftPackages, err = s.Catalog.LoadDraftAgent(ctx, actor, space, in.AgentID, in.ExpectedDraftRevision, in.ExpectedSkillDrafts)
		if err != nil {
			return run.Session{}, err
		}
		version, draftRevision = selection.Version, selection.Revision
		if draftRevision > 0 {
			agentName = version.Bundle.Name
		}
	} else {
		version, err = s.localAgentVersion(ctx, actor, space, in)
		if err != nil {
			return run.Session{}, err
		}
	}
	if version.Archived || in.Creativity != nil && version.Debug || version.Bundle.Config == nil || version.Bundle.Model == nil {
		return run.Session{}, catalog.ErrConfig
	}
	b := version.Bundle
	c := b.Config
	c.Context.Normalize()
	d := run.Definition{VersionID: version.ID, AgentID: in.AgentID, Debug: version.Debug, PlatformVersion: run.PlatformVersion, Platform: run.PlatformInstructions, Instructions: b.Document.Markdown, Skills: []run.Skill{}, Model: Model(*b.Model), Tools: []run.ToolSpec{}, Compression: c.Context.Compression, MaxInputTokens: c.Context.MaxInputTokens, OutputTokens: c.Context.MaxTokens, MaxSteps: c.Limits.MaxSteps, TimeoutSeconds: c.Limits.TimeoutSeconds}
	var refs []storage.Ref
	if !shared && draftRevision == 0 {
		refs = append(refs, storage.Ref{Role: "agent_version", ObjectID: version.ContentID, SHA256: version.SHA256})
	}
	for i, binding := range b.Bindings {
		var skillBundle catalog.Bundle
		if shared || draftRevision > 0 {
			skillBundle = boundSkills[i]
		} else {
			skill, err := s.Catalog.Version(ctx, actor, space, "skill", binding.SkillID, binding.VersionID)
			if err != nil {
				return run.Session{}, err
			}
			if skill.SHA256 != binding.SHA256 {
				return run.Session{}, storage.ErrIntegrity
			}
			skillBundle = skill.Bundle
			refs = append(refs, storage.Ref{Role: "skill_version", Ordinal: i, ObjectID: skill.ContentID, SHA256: skill.SHA256})
		}
		resources := []run.SkillResource{}
		for _, a := range skillBundle.Document.Assets {
			resources = append(resources, run.SkillResource{Path: a.Path, ObjectID: a.ObjectID, SHA256: a.SHA256, Size: a.Size, MediaType: a.MediaType, Package: a.Package})
		}
		d.Skills = append(d.Skills, run.Skill{Resources: resources, ID: binding.SkillID, VersionID: binding.VersionID, Version: binding.Version, SHA256: binding.SHA256, Markdown: skillBundle.Document.Markdown})
	}
	for _, name := range c.Tools {
		d.Tools = append(d.Tools, run.ToolSpec{Name: name, Version: "1"})
	}
	if shared {
		d.Model.Endpoint = ""
	}
	accept := func() error {
		var acceptErr error
		id, acceptErr = s.acceptSession(ctx, actor, space, in, d, refs, agentName, digest, draftRevision, draftPackages)
		return acceptErr
	}
	if shared {
		err = s.Catalog.WithPublicAgent(ctx, in.AgentSourceSpaceID, in.AgentID, version.ID, accept)
	} else {
		err = accept()
	}
	if err != nil {
		return run.Session{}, err
	}
	return s.Repo.RuntimeSession(ctx, actor, space, id)
}

func (s *Service) localAgentVersion(ctx context.Context, actor, space string, in CreateInput) (catalog.Version, error) {
	draft, err := s.Catalog.Repo.CatalogDraft(ctx, actor, space, "agent", in.AgentID)
	if err != nil {
		return catalog.Version{}, err
	}
	if draft.Archived {
		return catalog.Version{}, catalog.ErrConfig
	}
	versionID := in.VersionID
	if versionID == "" {
		if in.Creativity != nil {
			if finder, ok := s.Catalog.Repo.(interface {
				LatestFormalVersion(context.Context, string, string, string, string) (catalog.Version, bool, error)
			}); ok {
				latest, _, err := finder.LatestFormalVersion(ctx, actor, space, "agent", in.AgentID)
				if err != nil {
					return catalog.Version{}, err
				}
				versionID = latest.ID
			} else {
				versionID = draft.DefaultVersionID
			}
		} else {
			versionID = draft.DefaultVersionID
		}
	}
	if versionID == "" {
		return catalog.Version{}, catalog.ErrConfig
	}
	return s.Catalog.Version(ctx, actor, space, "agent", in.AgentID, versionID)
}

func (s *Service) acceptSession(ctx context.Context, actor, space string, in CreateInput, d run.Definition, refs []storage.Ref, agentName, digest string, draftRevision int64, draftPackages []run.DraftSkillPackage) (string, error) {
	raw, _ := json.Marshal(d)
	o, err := s.Content.Put(ctx, actor, space, uuid.NewString(), "session.definition", "application/json", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	defRef := Ref(o, "definition")
	refs = append(refs, defRef)
	id := storage.NewShortID()
	title := in.Title
	if title == "" {
		title = id
	}
	event := journal.New(actor, "session.created", "", journal.SessionData{Hide: in.Hide, Creativity: in.Creativity, SourceSnapshotID: in.SourceSnapshotID, DefinitionRef: &defRef, DefinitionID: in.AgentID, VersionID: d.VersionID, Title: title, AgentSourceSpaceID: in.AgentSourceSpaceID, AgentName: agentName, DraftRevision: draftRevision, DraftSkillPackages: draftPackages}, refs...)
	batch, err := journal.Prepare(actor, journal.Request{SpaceID: space, SessionID: id, AppendKey: uuid.NewString(), Events: []journal.Proposed{event}})
	if err != nil {
		return "", err
	}
	id, _, err = s.Repo.AcceptRuntimeRequest(ctx, actor, space, "session", in.Key, digest, batch)
	return id, err
}
func Model(m identity.Model) run.ModelConfig {
	return run.ModelConfig{ID: m.ID, Provider: m.Provider, ModelID: m.ModelID, Endpoint: m.BaseURL, Revision: m.Revision, ContextWindow: m.ContextWindow, MaxOutputTokens: m.MaxOutputTokens, Temperature: m.Temperature}
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

func (s *Service) PrepareIteration(ctx context.Context, actor string, session run.Session) error {
	if session.Creativity == nil || session.Creativity.TargetAgentID == "" || session.LatestSnapshotID != "" {
		return nil
	}
	return s.Workspaces.InitializeIteration(ctx, actor, session.SpaceID, session.ID)
}
func (s *Service) Submit(ctx context.Context, actor, space, sessionID string, in SubmitInput) (run.View, error) {
	return s.submit(ctx, actor, space, sessionID, in, false)
}

func (s *Service) SubmitWorkspaceCleanup(ctx context.Context, actor, space, sessionID, key string) (run.View, error) {
	return s.submit(ctx, actor, space, sessionID, SubmitInput{Key: key, Text: "帮我清理当前工作区的非产物中间文件"}, true)
}

func (s *Service) submit(ctx context.Context, actor, space, sessionID string, in SubmitInput, workspaceCleanup bool) (run.View, error) {
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
		Session          string
		Input            SubmitInput
		WorkspaceCleanup bool `json:",omitempty"`
	}{sessionID, in, workspaceCleanup})
	var id string
	var found bool
	var err error
	if repo, ok := s.Repo.(interface {
		FindRunRequest(context.Context, string, string, string, string, string) (string, bool, error)
	}); ok {
		id, found, err = repo.FindRunRequest(ctx, actor, space, sessionID, in.Key, digest)
	} else {
		_, id, found, err = s.Repo.FindRuntimeRequest(ctx, actor, space, "run", in.Key, digest)
	}
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
	if !workspaceCleanup {
		if err := s.PrepareIteration(ctx, actor, session); err != nil {
			return run.View{}, err
		}
	}
	if session.AgentSourceSpaceID == "" {
		return s.submitNewRun(ctx, actor, session, in, digest, workspaceCleanup)
	}
	var view run.View
	err = s.Catalog.WithPublicAgent(ctx, session.AgentSourceSpaceID, session.DefinitionID, session.VersionID, func() error {
		var submitErr error
		view, submitErr = s.submitNewRun(ctx, actor, session, in, digest, workspaceCleanup)
		return submitErr
	})
	return view, err
}

func (s *Service) submitNewRun(ctx context.Context, actor string, session run.Session, in SubmitInput, digest string, workspaceCleanup bool) (run.View, error) {
	space, sessionID := session.SpaceID, session.ID
	if workspaceCleanup && !s.Workspaces.LocalExists(space, sessionID) {
		return run.View{}, storage.ErrWorkspace
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
	var model identity.Model
	if session.AgentSourceSpaceID == "" {
		model, err = s.Identity.Repo.Model(ctx, actor, space, definition.Model.ID)
	} else {
		model, err = s.Identity.CheckSharedRunModel(ctx, actor, space, session.AgentSourceSpaceID, definition.Model.ID)
	}
	if err != nil {
		return run.View{}, err
	}
	definition.Model = Model(model)
	if !model.Enabled || definition.MaxInputTokens+definition.OutputTokens > model.ContextWindow || definition.OutputTokens > model.MaxOutputTokens {
		return run.View{}, identity.ErrCapability
	}
	if session.AgentSourceSpaceID == "" {
		if _, err = s.Identity.ModelSecret(ctx, actor, space, model.ID); err != nil {
			return run.View{}, err
		}
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
	definition.ModelAttempts = max(1, s.ModelAttempts)
	id := uuid.NewString()
	if s.Content.Inline != nil {
		id = storage.NewShortID()
	}
	definition.Instructions = ""
	definition.Platform = ""
	for i := range definition.Skills {
		definition.Skills[i].Markdown = ""
		// The fixed version inventory is retained in the immutable definition and
		// session.created references, rather than duplicated in the bounded event.
		definition.Skills[i].Resources = nil
	}
	snapshot := run.Snapshot{ID: id, SpaceID: space, SessionID: sessionID, ActorID: actor, DefinitionRef: session.DefinitionRef, UserEventID: message.EventID, BaseSnapshotID: session.LatestSnapshotID, Definition: definition, Build: s.Build, Profile: "mass-agui-v1", MapperVersion: 1, ParserVersion: 1, CreatedAt: time.Now().UTC().Truncate(time.Microsecond)}
	snapshot.AgentSourceSpaceID = session.AgentSourceSpaceID
	snapshot.WorkspaceCleanup = workspaceCleanup
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
