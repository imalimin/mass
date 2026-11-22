package archive

import (
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
	"github.com/imalimin/mass/internal/resource/sessionlog"
	"github.com/imalimin/mass/internal/resource/trace"
)

func TestFirstRunTitleExtraction(t *testing.T) {
	firstRun := storage.NewShortID()
	secondRun := storage.NewShortID()
	encode := func(v any) json.RawMessage {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}

	records := []json.RawMessage{
		// Tool-call step with text must be skipped.
		encode(sessionlog.Step{
			Header:  sessionlog.Header{Seq: 3, Type: sessionlog.ModelStep, Role: "assistant", RunID: firstRun},
			Purpose: "task",
			Model: trace.Model{
				Status:    "succeeded",
				Text:      "先读取工作区文件确认现状。",
				ToolCalls: []run.NativeToolCall{{ID: "tc1", Type: "function"}},
			},
			Tools: []sessionlog.Tool{{CallID: "c1", Name: "read_file"}},
		}),
		// Summary step must be skipped.
		encode(sessionlog.Step{
			Header:  sessionlog.Header{Seq: 4, Type: sessionlog.ModelStep, Role: "assistant", RunID: firstRun},
			Purpose: "summary",
			Model:   trace.Model{Status: "succeeded", Text: "上下文摘要不应作为标题"},
		}),
		// Failed step must be skipped.
		encode(sessionlog.Step{
			Header:  sessionlog.Header{Seq: 5, Type: sessionlog.ModelStep, Role: "assistant", RunID: firstRun},
			Purpose: "task",
			Model:   trace.Model{Status: "failed", Text: "失败的半截响应"},
		}),
		// First succeeded non-tool-call task text: whitespace collapsed & truncated to 16 runes.
		encode(sessionlog.Step{
			Header:  sessionlog.Header{Seq: 6, Type: sessionlog.ModelStep, Role: "assistant", RunID: firstRun},
			Purpose: "task",
			Model:   trace.Model{Status: "succeeded", Text: "\n  已完成写入、读取和发布\n  notes.txt 文件！"},
		}),
		// Second run's text must be ignored when querying firstRun.
		encode(sessionlog.Step{
			Header:  sessionlog.Header{Seq: 8, Type: sessionlog.ModelStep, Role: "assistant", RunID: secondRun},
			Purpose: "task",
			Model:   trace.Model{Status: "succeeded", Text: "第二轮对话的回复内容"},
		}),
	}

	got := firstRunTitle(records, firstRun)
	want := "已完成写入、读取和发布 note"
	if got != want {
		t.Fatalf("firstRunTitle = %q (%d runes), want %q (%d runes)", got, len([]rune(got)), want, len([]rune(want)))
	}

	// Only tool-call step returns empty title.
	if empty := firstRunTitle(records[:3], firstRun); empty != "" {
		t.Fatalf("expected empty title when no succeeded non-tool-call step exists, got %q", empty)
	}

	// Short reply (<= 16 runes) stays intact.
	shortRecords := []json.RawMessage{
		encode(sessionlog.Step{
			Header:  sessionlog.Header{Seq: 3, Type: sessionlog.ModelStep, Role: "assistant", RunID: firstRun},
			Purpose: "task",
			Model:   trace.Model{Status: "succeeded", Text: "  你好，MASS！ "},
		}),
	}
	if gotShort := firstRunTitle(shortRecords, firstRun); gotShort != "你好，MASS！" {
		t.Fatalf("short title = %q, want %q", gotShort, "你好，MASS！")
	}
}

func TestRehydrateTracksFirstRunAndStoredTitle(t *testing.T) {
	sessionID := storage.NewShortID()
	spaceID := storage.NewShortID()
	firstRun := storage.NewShortID()
	secondRun := storage.NewShortID()
	encode := func(v any) json.RawMessage {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}

	records := []json.RawMessage{
		encode(sessionlog.Created{
			Header:        sessionlog.Header{Seq: 1, Type: sessionlog.SessionCreated, Role: "system"},
			SchemaVersion: 1, SessionID: sessionID, SpaceID: spaceID, Title: sessionID,
		}),
		encode(sessionlog.Accepted{
			Header: sessionlog.Header{Seq: 2, Type: sessionlog.RunAccepted, Role: "user", RunID: firstRun},
		}),
		encode(sessionlog.Accepted{
			Header: sessionlog.Header{Seq: 3, Type: sessionlog.RunAccepted, Role: "user", RunID: secondRun},
		}),
	}
	restored := newConversation()
	if err := (&Runtime{}).rehydrate(restored, records); err != nil {
		t.Fatal(err)
	}
	if restored.firstRunID != firstRun {
		t.Fatalf("firstRunID = %q, want %q", restored.firstRunID, firstRun)
	}

	// Legacy session_created with title == sessionID clears to "" when DB title is NULL.
	applyStoredTitle(&restored.session, sql.NullString{})
	if restored.session.Title != "" {
		t.Fatalf("expected empty title for legacy NULL DB title, got %q", restored.session.Title)
	}

	// Non-null DB title takes precedence.
	applyStoredTitle(&restored.session, sql.NullString{String: "自动生成的会话标题", Valid: true})
	if restored.session.Title != "自动生成的会话标题" {
		t.Fatalf("expected stored DB title, got %q", restored.session.Title)
	}
}

func TestIsSummaryAttemptAndImmediateTitleOnResponseCompleted(t *testing.T) {
	runID := storage.NewShortID()
	spaceID := storage.NewShortID()
	sessionID := storage.NewShortID()
	rt := &Runtime{}
	conv := newConversation()
	conv.loaded = true
	conv.firstRunID = runID
	conv.session = run.Session{ID: sessionID, SpaceID: spaceID}
	rt.sessions.Store(spaceID+"/"+sessionID, conv)

	appendBatch := func(events ...journal.Proposed) {
		t.Helper()
		if _, err := rt.appendEvents(t.Context(), journal.Batch{Request: journal.Request{SpaceID: spaceID, SessionID: sessionID, Events: events}}); err != nil {
			t.Fatal(err)
		}
	}

	// 1. Summary attempt should NOT populate title.
	sumAttempt := "attempt-summary"
	sumStart := journal.New("actor", "model.attempt.started", runID, journal.AttemptData{ID: sumAttempt, Purpose: "summary"})
	sumResp := journal.New("actor", "model.response.completed", runID, journal.ResponseData{AttemptID: sumAttempt, Preview: "这是上下文摘要"})
	appendBatch(sumStart, sumResp)
	if conv.session.Title != "" {
		t.Fatalf("summary response must not set title, got %q", conv.session.Title)
	}

	// 2. Tool-call task attempt should NOT populate title.
	toolAttempt := "attempt-tool"
	toolStart := journal.New("actor", "model.attempt.started", runID, journal.AttemptData{ID: toolAttempt, Purpose: "task"})
	toolResp := journal.New("actor", "model.response.completed", runID, journal.ResponseData{
		AttemptID: toolAttempt,
		Calls:     []run.Call{{ID: "c1", Name: "read_file"}},
		Preview:   "先查一下文件。",
	})
	appendBatch(toolStart, toolResp)
	if conv.session.Title != "" {
		t.Fatalf("tool-call response must not set title, got %q", conv.session.Title)
	}

	// 3. Non-tool-call task attempt immediately sets title (first 16 runes).
	finalAttempt := "attempt-final"
	finalStart := journal.New("actor", "model.attempt.started", runID, journal.AttemptData{ID: finalAttempt, Purpose: "task"})
	finalResp := journal.New("actor", "model.response.completed", runID, journal.ResponseData{
		AttemptID: finalAttempt,
		Preview:   "  帮我整理一份完整的Agent设计方案与发布清单  ",
	})
	appendBatch(finalStart, finalResp)
	want := "帮我整理一份完整的Agent设计"
	if conv.session.Title != want {
		t.Fatalf("immediate title = %q, want %q", conv.session.Title, want)
	}
}
