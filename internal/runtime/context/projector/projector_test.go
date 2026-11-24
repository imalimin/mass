package projector_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
	"github.com/imalimin/mass/internal/resource"
	"github.com/imalimin/mass/internal/runtime/context/projector"
	"github.com/imalimin/mass/tests/fakes"
)

type objects map[string]storage.Object

func (o objects) BeginObject(_ context.Context, _ string, v storage.Object) (storage.Object, error) {
	o[v.ID] = v
	return v, nil
}
func (o objects) ReadyObject(_ context.Context, _ string, v storage.Object) error {
	v.State = "ready"
	o[v.ID] = v
	return nil
}
func (o objects) Object(_ context.Context, _, space, id string, _ bool) (storage.Object, error) {
	v, ok := o[id]
	if !ok || v.SpaceID != space {
		return v, storage.ErrIntegrity
	}
	return v, nil
}
func TestCompleteGroupsAndExplicitFailures(t *testing.T) {
	ctx := context.Background()
	blobs := fakes.NewBlobs()
	repo := objects{}
	content := &resource.ContentStore{Repo: repo, Blobs: blobs, Prefix: "test", MaxBytes: 32 << 20}
	s := run.Snapshot{ID: uuid.NewString(), ActorID: uuid.NewString(), SpaceID: storage.NewShortID(), SessionID: storage.NewShortID(), Definition: run.Definition{Platform: "fixed", Instructions: "ordered", MaxInputTokens: 100000, OutputTokens: 256, Model: run.ModelConfig{ContextWindow: 120000}}}
	put := func(v any) storage.Ref {
		raw, _ := json.Marshal(v)
		o, e := content.Put(ctx, s.ActorID, s.SpaceID, uuid.NewString(), "test", "application/json", bytes.NewReader(raw))
		if e != nil {
			t.Fatal(e)
		}
		return storage.Ref{Role: "content", ObjectID: o.ID, SHA256: o.SHA256}
	}
	s.DefinitionRef = put(s.Definition)
	var history []journal.Event
	add := func(kind string, v any, refs ...storage.Ref) journal.Event {
		e := journal.New(s.ActorID, kind, s.ID, v, refs...)
		event := journal.Event{Proposed: e, SpaceID: s.SpaceID, SessionID: s.SessionID, Sequence: int64(len(history) + 1)}
		history = append(history, event)
		return event
	}
	attempt := uuid.NewString()
	add("model.attempt.started", journal.AttemptData{ID: attempt, ProjectionEventID: uuid.NewString(), MessageID: uuid.NewString(), Purpose: "task", Source: "fake"})
	responseID := uuid.NewString()
	calls := []run.Call{{ID: uuid.NewString(), NativeID: "wire-1", Name: "read_file", Arguments: `{"path":"a"}`, ResponseEventID: responseID}, {ID: uuid.NewString(), NativeID: "wire-2", Name: "write_file", Arguments: `{"path":"b"}`, ResponseEventID: responseID}}
	response := run.Reply{Message: run.Message{Role: "assistant", Content: "checking", ToolCalls: calls, Native: json.RawMessage(`{"role":"assistant","content":"checking","reasoning_content":"preserve protocol"}`)}, FinishReason: "tool_calls"}
	ref := put(response)
	e := add("model.response.completed", journal.ResponseData{AttemptID: attempt, MessageID: uuid.NewString(), ContentID: ref.ObjectID, FinishReason: "tool_calls", Calls: calls}, ref)
	history[len(history)-1].EventID = responseID
	_ = e
	result := run.ToolResult{CallID: calls[0].ID, Status: "completed", Complete: true, SideEffect: "none", Data: json.RawMessage(`{"text":"` + strings.Repeat("中", 5000) + `"}`)}
	ref = put(result)
	add("tool.execution.completed", journal.ToolData{CallID: calls[0].ID, ResponseEventID: responseID, Name: "read_file", Version: "1", ArgumentsSHA256: journal.Digest(calls[0].Arguments), ContentID: ref.ObjectID, Status: "completed", SideEffect: "none"}, ref)
	p := &projector.Projector{Content: content}
	if _, err := p.Project(ctx, s, history, int64(len(history)), nil); !errors.Is(err, run.ErrProtocol) {
		t.Fatal("open group accepted", err)
	}
	partial := append([]journal.Event(nil), history...)
	result = run.ToolResult{CallID: calls[1].ID, Status: "failed", Complete: true, SideEffect: "committed", Error: "after_write", Data: json.RawMessage(`{}`)}
	ref = put(result)
	add("tool.execution.completed", journal.ToolData{CallID: calls[1].ID, ResponseEventID: responseID, Name: "write_file", Version: "1", ArgumentsSHA256: journal.Digest(calls[1].Arguments), ContentID: ref.ObjectID, Status: "failed", SideEffect: "committed"}, ref)
	add("run.finished", journal.FinishData{Status: "failed", Error: "after_write"})
	a, err := p.Project(ctx, s, history, int64(len(history)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Messages) != 5 || a.Messages[1].Role != "assistant" || a.Messages[2].ToolCallID != "wire-1" || a.Messages[3].ToolCallID != "wire-2" || !strings.Contains(a.Messages[3].Content, "committed") {
		t.Fatal("complete failed-run facts dropped or reordered")
	}
	if a.Spec.Blocks[1].Reason != "fixed_result_excerpt" || !strings.Contains(a.Messages[2].Content, "original_content_id") || len(a.Messages[2].Content) > 6000 {
		t.Fatal("large result excerpt")
	}
	if !bytes.Equal(a.Messages[1].Native, response.Message.Native) {
		t.Fatal("native protocol lost")
	}
	for i := range history {
		history[i].OccurredAt = time.Now().Add(time.Duration(i) * time.Hour)
		history[i].RecordedAt = time.Now()
	}
	b, err := p.Project(ctx, s, history, int64(len(history)), nil)
	if err != nil || a.Fingerprint != b.Fingerprint {
		t.Fatal("clock changed content", err)
	}
	boundary := int64(len(history))
	add("run.finished", journal.FinishData{Status: "cancelled"})
	b, err = p.Project(ctx, s, history, boundary, nil)
	if err != nil || a.Fingerprint != b.Fingerprint {
		t.Fatal("future history changed projection", err)
	}
	// A terminal run with an incomplete tool group contributes no tool messages.
	history = partial
	add("run.finished", journal.FinishData{Status: "cancelled", Error: "cancelled"})
	b, err = p.Project(ctx, s, history, int64(len(history)), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range b.Messages {
		if m.Role == "tool" || len(m.ToolCalls) > 0 {
			t.Fatal("partial tool group leaked")
		}
	}
	if len(b.Spec.Blocks) < 2 || b.Spec.Blocks[1].Reason != "failed_run" {
		t.Fatal("missing terminal failure after discarding tool state")
	}
	// Bad parser/schema, broken bytes, cross-space refs all fail closed.
	spec := b.Spec
	spec.Parser = 999
	if _, err = p.Build(ctx, s, history, nil, spec); err == nil {
		t.Fatal("unknown parser accepted")
	}
	p = &projector.Projector{Content: content}
	blobs.Corrupt(repo[s.DefinitionRef.ObjectID].Key)
	if _, err = p.Project(ctx, s, history, int64(len(history)), nil); err == nil {
		t.Fatal("corrupt content accepted")
	}
}

func TestSkillResourcesInjectedIntoSystemPrompt(t *testing.T) {
	ctx := context.Background()
	blobs := fakes.NewBlobs()
	repo := objects{}
	content := &resource.ContentStore{Repo: repo, Blobs: blobs, Prefix: "test", MaxBytes: 32 << 20}
	s := run.Snapshot{
		ID: uuid.NewString(), ActorID: uuid.NewString(), SpaceID: storage.NewShortID(), SessionID: storage.NewShortID(),
		Definition: run.Definition{
			Platform: "platform", Instructions: "agent", MaxInputTokens: 100000, OutputTokens: 256,
			Model: run.ModelConfig{ContextWindow: 120000},
			Skills: []run.Skill{
				{ID: "s1", VersionID: "ver-1", Name: "rules", Version: "v1", Description: "Apply calculation rules.", Markdown: "# Skill One\nSECRET_BODY_DO_NOT_INLINE", Resources: []run.SkillResource{{Path: "assets/rules.txt", Size: 12}, {Path: "scripts/run.py", Size: 64}}},
				{ID: "s2", VersionID: "ver-2", Name: "helper", Version: "v1", Markdown: "# Skill Two\nSECOND_BODY_DO_NOT_INLINE"},
			},
		},
	}
	raw, _ := json.Marshal(s.Definition)
	o, err := content.Put(ctx, s.ActorID, s.SpaceID, uuid.NewString(), "test", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	s.DefinitionRef = storage.Ref{Role: "definition", ObjectID: o.ID, SHA256: o.SHA256}
	ev := journal.New(s.ActorID, "session.created", "", journal.SessionData{DefinitionRef: &s.DefinitionRef, DefinitionID: storage.NewShortID(), VersionID: uuid.NewString()}, s.DefinitionRef)
	history := []journal.Event{{Proposed: ev, SpaceID: s.SpaceID, SessionID: s.SessionID, Sequence: 1}}
	p := &projector.Projector{Content: content}
	prepared, err := p.Project(ctx, s, history, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	sys := prepared.Messages[0].Content
	for _, want := range []string{
		"<available_skills>",
		`- name="rules" version="v1" entry="$MASS_SKILLS_DIR/rules/SKILL.md"`,
		"description: Apply calculation rules.",
		"  resources:\n  - $MASS_SKILLS_DIR/rules/assets/rules.txt (12 bytes)\n  - $MASS_SKILLS_DIR/rules/scripts/run.py (64 bytes)",
		`- name="helper" version="v1" entry="$MASS_SKILLS_DIR/helper/SKILL.md"`,
	} {
		if !strings.Contains(sys, want) {
			t.Fatalf("expected %q in system prompt, got:\n%s", want, sys)
		}
	}
	for _, forbidden := range []string{"SECRET_BODY_DO_NOT_INLINE", "SECOND_BODY_DO_NOT_INLINE"} {
		if strings.Contains(sys, forbidden) {
			t.Fatalf("skill body should not be injected eagerly (%q):\n%s", forbidden, sys)
		}
	}
	if strings.Count(sys, "  resources:\n") != 1 {
		t.Fatalf("skill without resources should not render resources block:\n%s", sys)
	}
}
