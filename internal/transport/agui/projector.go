package agui

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/journal"
	"github.com/imalimin/mass/internal/resource/trace"
)

type MessageState struct {
	ID         string `json:"id"`
	Text       string `json:"text"`
	Closed     bool   `json:"closed"`
	Incomplete bool   `json:"incomplete"`
}
type ToolState struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Result    string `json:"result"`
	Status    string `json:"status"`
	Standard  bool   `json:"standard"`
}
type State struct {
	Plan            *run.Plan      `json:"plan,omitempty"`
	Status          string         `json:"status"`
	CancelRequested bool           `json:"cancel_requested"`
	Messages        []MessageState `json:"messages"`
	Tools           []ToolState    `json:"tools"`
	Artifacts       []run.Artifact `json:"artifacts"`
	Usage           run.Usage      `json:"usage"`
	Error           string         `json:"error"`
}
type activeMessage struct {
	AttemptID string
	MessageID string
}

type Checkpoint struct {
	active          *activeMessage
	responseText    map[string]string
	SummaryAttempts map[string]bool `json:"summary_attempts,omitempty"`
	StreamID        string          `json:"stream_id"`
	SessionID       string          `json:"session_id"`
	Source          int64           `json:"source_sequence,string"`
	Cursor          int64           `json:"cursor,string"`
	Profile         string          `json:"protocol_profile"`
	Mapper          int             `json:"mapper_version"`
	Parser          int             `json:"parser_version"`
	Closed          bool            `json:"closed"`
	State           State           `json:"state"`
}
type Frame struct {
	Sequence int64           `json:"sequence,string"`
	SourceID string          `json:"source_event_id"`
	Ordinal  int             `json:"source_ordinal"`
	JSON     json.RawMessage `json:"event"`
}
type Repository interface {
	LoadStream(context.Context, string, string, string) (Checkpoint, error)
	CommitStream(context.Context, string, string, string, Checkpoint, Checkpoint, []Frame) error
	ReadEvents(context.Context, string, string, string, int64, *int64, int) (journal.Page, error)
	ReadFrames(context.Context, string, string, string, int64, int) ([]Frame, bool, error)
}
type Projector struct {
	Repo         Repository
	Live         *Live
	ResponseText func(context.Context, string, string, string) (string, error)
	ToolState    func(string, string) trace.Tool
}

func (p *Projector) CatchUp(ctx context.Context, actor, space, runID string) (Checkpoint, error) {
	for {
		if err := ctx.Err(); err != nil {
			return Checkpoint{}, err
		}
		old, err := p.Repo.LoadStream(ctx, actor, space, runID)
		if err != nil {
			return old, err
		}
		if old.Profile != Profile || old.Mapper != 1 || old.Parser != 1 {
			return old, run.ErrProtocol
		}
		if old.Closed {
			return old, nil
		}
		var page journal.Page
		if reader, ok := p.Repo.(journal.RunReader); ok {
			page, err = reader.ReadRunEvents(ctx, actor, space, old.SessionID, runID, old.Source, nil, 100)
		} else {
			page, err = p.Repo.ReadEvents(ctx, actor, space, old.SessionID, old.Source, nil, 100)
		}
		if err != nil {
			return old, err
		}
		if len(page.Events) == 0 && old.active == nil && len(old.State.Tools) == 0 {
			return old, nil
		}
		old.responseText = make(map[string]string)
		for _, e := range page.Events {
			if e.Type == "model.attempt.failed" && p.Live != nil {
				var d journal.AttemptFailedData
				journal.Decode(e.Data, &d)
				text := p.Live.Text(space+"/"+runID, d.AttemptID)
				if len(text) > len(d.Preview) {
					old.responseText[e.EventID] = text
				}
			}
			if e.Type == "model.response.completed" && p.ResponseText != nil {
				var d journal.ResponseData
				journal.Decode(e.Data, &d)
				// The preview can end up to three bytes short at a rune boundary.
				if len(d.Preview) >= 8189 && !old.SummaryAttempts[d.AttemptID] {
					text, err := p.ResponseText(ctx, actor, space, d.ContentID)
					if err != nil {
						return old, err
					}
					old.responseText[e.EventID] = text
				}
			}
		}
		// Apply in-memory tool states after their declaring model reply and before
		// any terminal Run frame. These events do not advance the SQL cursor.
		var tail []journal.Event
		if n := len(page.Events); n > 0 && page.Events[n-1].Type == "run.finished" {
			tail = page.Events[n-1:]
			page.Events = page.Events[:n-1]
		}
		next, frames, err := Map(old, runID, page.Events)
		if err == nil && p.ToolState != nil && !next.Closed {
			var updates []journal.Event
			for _, current := range next.State.Tools {
				tool := p.ToolState(runID, current.ID)
				if tool.Status == "" || tool.Status == "not_started" || tool.Status == current.Status {
					continue
				}
				data := journal.ToolData{CallID: tool.CallID, Status: tool.Status}
				if len(tool.Result) > 0 {
					var result run.ToolResult
					json.Unmarshal(tool.Result, &result)
					data.SideEffect = result.SideEffect
					data.Preview = string(tool.Result)
					if len(tool.Result) > 4096 {
						preview, _ := json.Marshal(map[string]any{"status": result.Status, "truncated": true, "side_effect": result.SideEffect})
						data.Preview = string(preview)
					}
				}
				event := journal.New(actor, liveToolEvent, runID, data)
				event.EventID = tool.CallID
				updates = append(updates, journal.Event{Proposed: event, Sequence: next.Source})
			}
			if len(updates) > 0 {
				updated, output, mapErr := Map(next, runID, updates)
				next, frames, err = updated, append(frames, output...), mapErr
			}
		}
		if err == nil && len(tail) > 0 {
			updated, output, mapErr := Map(next, runID, tail)
			next, frames, err = updated, append(frames, output...), mapErr
		}
		if err == nil && p.Live != nil && next.active != nil {
			text := p.Live.Text(space+"/"+runID, next.active.AttemptID)
			var offset int
			for _, message := range next.State.Messages {
				if message.ID == next.active.MessageID {
					offset = len(message.Text)
					break
				}
			}
			if len(text) > offset {
				delta := journal.Event{
					Proposed: journal.Proposed{EventID: next.active.AttemptID, RunID: runID, OccurredAt: time.Now()},
					Sequence: next.Source,
				}
				delta.Type = liveTextEvent
				delta.Data, _ = json.Marshal(journal.DeltaData{AttemptID: next.active.AttemptID, MessageID: next.active.MessageID, Text: text[offset:]})
				updated, output, mapErr := Map(next, runID, []journal.Event{delta})
				next, frames, err = updated, append(frames, output...), mapErr
			}
		}
		if err != nil {
			return old, err
		}
		if next.Source == old.Source && next.Cursor == old.Cursor {
			return next, nil
		}
		if err = p.Repo.CommitStream(ctx, actor, space, runID, old, next, frames); err != nil {
			if err == run.ErrState {
				continue
			}
			return old, err
		}
		if !page.HasMore || next.Closed {
			return next, nil
		}
	}
}

const liveTextEvent = "live.model.text"
const liveToolEvent = "live.tool.status"

func Map(old Checkpoint, runID string, input []journal.Event) (Checkpoint, []Frame, error) {
	// Deep copy before mutating slices; a failed CAS cannot mutate the old checkpoint.
	raw, _ := json.Marshal(old)
	var cp Checkpoint
	json.Unmarshal(raw, &cp)
	cp.active = old.active
	frames := []Frame{}
	if cp.State.Messages == nil {
		cp.State.Messages = []MessageState{}
	}
	if cp.State.Tools == nil {
		cp.State.Tools = []ToolState{}
	}
	if cp.State.Artifacts == nil {
		cp.State.Artifacts = []run.Artifact{}
	}
	for _, source := range input {
		if cp.Closed {
			break
		}
		if source.Type != liveTextEvent && source.Type != liveToolEvent {
			if err := journal.Validate(source.Proposed); err != nil {
				return cp, nil, err
			}
		}
		cp.Source = source.Sequence
		if source.RunID != runID {
			continue
		}
		ordinal := 0
		var emitErr error
		emit := func(event events.Event) error {
			event.(interface{ SetTimestamp(int64) }).SetTimestamp(source.OccurredAt.UnixMilli())
			if err := event.Validate(); err != nil {
				emitErr = err
				return err
			}
			body, err := json.Marshal(event)
			if err != nil {
				emitErr = err
				return err
			}
			cp.Cursor++
			frames = append(frames, Frame{Sequence: cp.Cursor, SourceID: source.EventID, Ordinal: ordinal, JSON: body})
			ordinal++
			return nil
		}
		custom := func(name string, value map[string]any) error {
			value["schemaVersion"] = 1
			value["threadId"] = cp.SessionID
			value["runId"] = runID
			value["sourceEventIds"] = []string{source.EventID}
			return emit(events.NewCustomEvent(name, events.WithValue(value)))
		}
		message := func(id string) *MessageState {
			for i := range cp.State.Messages {
				if cp.State.Messages[i].ID == id {
					return &cp.State.Messages[i]
				}
			}
			cp.State.Messages = append(cp.State.Messages, MessageState{ID: id})
			return &cp.State.Messages[len(cp.State.Messages)-1]
		}
		appendText := func(id, text string) {
			if text == "" {
				return
			}
			m := message(id)
			if m.Text == "" {
				emit(events.NewTextMessageStartEvent(id, events.WithRole("assistant")))
			}
			m.Text += text
			for len(text) > 0 {
				n := min(len(text), 8192)
				for n < len(text) && !utf8.RuneStart(text[n]) {
					n--
				}
				emit(events.NewTextMessageContentEvent(id, text[:n]))
				text = text[n:]
			}
		}
		fillText := func(id, text string) {
			for _, m := range cp.State.Messages {
				if m.ID == id {
					if strings.HasPrefix(text, m.Text) {
						text = text[len(m.Text):]
					} else {
						return
					}
					break
				}
			}
			appendText(id, text)
		}
		if cp.SummaryAttempts == nil {
			cp.SummaryAttempts = map[string]bool{}
		}
		switch source.Type {
		case "model.attempt.started":
			var d journal.AttemptData
			journal.Decode(source.Data, &d)
			if d.Purpose == "summary" {
				cp.SummaryAttempts[d.ID] = true
			} else {
				cp.active = &activeMessage{AttemptID: d.ID, MessageID: d.MessageID}
			}
			custom("mass.trace.v1", map[string]any{"eventType": source.Type, "purpose": d.Purpose, "attemptId": d.ID})
		case "run.created":
			cp.State.Status = "queued"
			if err := emit(events.NewRunStartedEvent(cp.SessionID, runID)); err != nil {
				return cp, nil, err
			}
			custom("mass.run.status.v1", map[string]any{"status": "queued"})
		case "run.started":
			cp.State.Status = "running"
			custom("mass.run.status.v1", map[string]any{"status": "running"})
		case "run.cancel.requested":
			cp.State.CancelRequested = true
			custom("mass.run.status.v1", map[string]any{"status": cp.State.Status, "cancelRequested": true})
		case liveTextEvent:
			var d journal.DeltaData
			journal.Decode(source.Data, &d)
			appendText(d.MessageID, d.Text)
		case "model.response.completed":
			var d journal.ResponseData
			journal.Decode(source.Data, &d)
			if cp.SummaryAttempts[d.AttemptID] {
				delete(cp.SummaryAttempts, d.AttemptID)
				if err := custom("mass.usage.v1", map[string]any{"usage": d.Usage, "attemptId": d.AttemptID, "purpose": "summary"}); err != nil {
					return cp, nil, err
				}
				continue
			}
			cp.active = nil
			text, ok := old.responseText[source.EventID]
			if !ok {
				text = d.Preview
			}
			fillText(d.MessageID, text)
			for i := range cp.State.Messages {
				m := &cp.State.Messages[i]
				if m.ID == d.MessageID && !m.Closed {
					emit(events.NewTextMessageEndEvent(m.ID))
					m.Closed = true
				}
			}
			for _, call := range d.Calls {
				t := ToolState{ID: call.ID, Name: call.Name, Status: "parameters_complete", Arguments: call.Arguments, Standard: call.Arguments != "" && len(call.Arguments) <= 8192}
				if t.Standard {
					emit(events.NewToolCallStartEvent(call.ID, call.Name, events.WithParentMessageID(d.MessageID)))
					emit(events.NewToolCallArgsEvent(call.ID, call.Arguments))
					emit(events.NewToolCallEndEvent(call.ID))
				} else {
					t.Arguments = ""
					custom("mass.tool.status.v1", map[string]any{"toolCallId": call.ID, "toolName": call.Name, "status": "parameters_complete", "truncated": true, "payloadRef": d.ContentID})
				}
				cp.State.Tools = append(cp.State.Tools, t)
			}
			cp.State.Usage = d.Usage
			custom("mass.usage.v1", map[string]any{"usage": d.Usage, "attemptId": d.AttemptID})
		case "model.attempt.failed":
			var d journal.AttemptFailedData
			journal.Decode(source.Data, &d)
			if cp.SummaryAttempts[d.AttemptID] {
				delete(cp.SummaryAttempts, d.AttemptID)
				if err := custom("mass.usage.v1", map[string]any{"usage": d.Usage, "attemptId": d.AttemptID, "purpose": "summary"}); err != nil {
					return cp, nil, err
				}
				continue
			}
			cp.active = nil
			text, ok := old.responseText[source.EventID]
			if !ok {
				text = d.Preview
			}
			fillText(d.MessageID, text)
			for i := range cp.State.Messages {
				m := &cp.State.Messages[i]
				if m.ID == d.MessageID && !m.Closed {
					m.Incomplete = true
					m.Closed = true
					custom("mass.message.status.v1", map[string]any{"messageId": m.ID, "status": "incomplete"})
					emit(events.NewTextMessageEndEvent(m.ID))
				}
			}
			custom("mass.trace.v1", map[string]any{"eventType": source.Type, "attemptId": d.AttemptID, "error": d.Error})
		case "tool.execution.started":
			var d journal.ToolData
			journal.Decode(source.Data, &d)
			for i := range cp.State.Tools {
				if cp.State.Tools[i].ID == d.CallID {
					cp.State.Tools[i].Status = "running"
				}
			}
			custom("mass.tool.status.v1", map[string]any{"toolCallId": d.CallID, "status": "running"})
		case "tool.execution.completed", liveToolEvent:
			var d journal.ToolData
			journal.Decode(source.Data, &d)
			for i := range cp.State.Tools {
				t := &cp.State.Tools[i]
				if t.ID == d.CallID {
					t.Status = d.Status
					t.Result = d.Preview
					if t.Standard && d.Preview != "" {
						role := "tool"
						event := events.NewToolCallResultEvent(source.EventID, d.CallID, d.Preview)
						event.Role = &role
						emit(event)
					}
				}
			}
			custom("mass.tool.status.v1", map[string]any{"toolCallId": d.CallID, "status": d.Status, "sideEffect": d.SideEffect, "payloadRef": d.ContentID})
		case "plan.updated":
			var plan run.Plan
			journal.Decode(source.Data, &plan)
			cp.State.Plan = &plan
			custom("mass.plan.v1", map[string]any{"plan": plan})
		case "artifact.published":
			var d run.Artifact
			journal.Decode(source.Data, &d)
			cp.State.Artifacts = append(cp.State.Artifacts, d)
			custom("mass.artifact.v1", map[string]any{"artifact": d})
		case "context.projected":
			var d journal.ProjectionData
			journal.Decode(source.Data, &d)
			custom("mass.context.v1", map[string]any{"projectionId": d.ID, "fingerprint": d.Fingerprint, "epoch": d.Epoch, "purpose": d.Purpose, "prefixChangeReason": d.PrefixChangeReason, "payloadRef": d.ContentID})
		case "run.finished":
			var d journal.FinishData
			journal.Decode(source.Data, &d)
			for i := range cp.State.Tools {
				tool := &cp.State.Tools[i]
				if tool.Status == "running" || tool.Status == "parameters_complete" {
					tool.Status = "result_unavailable"
					custom("mass.tool.status.v1", map[string]any{"toolCallId": tool.ID, "status": tool.Status})
				}
			}
			for i := range cp.State.Messages {
				m := &cp.State.Messages[i]
				if !m.Closed {
					m.Closed = true
					m.Incomplete = true
					custom("mass.message.status.v1", map[string]any{"messageId": m.ID, "status": "incomplete"})
					emit(events.NewTextMessageEndEvent(m.ID))
				}
			}
			cp.State.Status = d.Status
			cp.State.Error = d.Error
			custom("mass.run.status.v1", map[string]any{"status": d.Status, "snapshotError": d.SnapshotError})
			if d.Status == "succeeded" {
				emit(events.NewRunFinishedEvent(cp.SessionID, runID))
			} else {
				code := d.Error
				if d.Status == "cancelled" {
					code = "RUN_CANCELLED"
				}
				emit(events.NewRunErrorEvent("Run "+d.Status, events.WithErrorCode(code)))
			}
			cp.Closed = true
			cp.active = nil
		default:
			custom("mass.trace.v1", map[string]any{"eventType": source.Type, "sequence": strconv.FormatInt(source.Sequence, 10)})
		}
		if emitErr != nil {
			return cp, nil, emitErr
		}
	}
	return cp, frames, nil
}
