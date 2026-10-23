// Package projector deterministically reads committed facts and immutable bytes.
package projector

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/journal"
	"github.com/imalimin/mass/internal/resource"
)

const Counter = "utf8-byte-upper-bound-v1"
const Renderer = "mass-context-v1"

type Projector struct{ Content *resource.ContentStore }

func (p *Projector) Project(ctx context.Context, s run.Snapshot, events []journal.Event, through int64, tools []run.ToolSpec) (run.Prepared, error) {
	var definition run.Definition
	if err := resource.ReadJSON(ctx, p.Content, s.ActorID, s.SpaceID, s.DefinitionRef.ObjectID, 16<<20, &definition); err != nil {
		return run.Prepared{}, err
	}
	result := run.Prepared{Spec: run.ProjectionSpec{Purpose: "task", Through: through, Counter: Counter, Renderer: Renderer, Parser: 1, OutputTokens: s.Definition.OutputTokens, DefinitionSHA256: s.DefinitionRef.SHA256, ModelSHA256: journal.Digest(s.Definition.Model), SourceEventIDs: []string{}}, Tools: tools, Messages: []run.Message{{Role: "system", Content: definition.Platform + "\n\n" + definition.Instructions}}}
	for _, skill := range definition.Skills {
		result.Messages[0].Content += fmt.Sprintf("\n\n<skill id=%q version=%q>\n%s\n</skill>", skill.ID, skill.Version, skill.Markdown)
	}
	statuses := map[string]string{}
	for _, e := range events {
		if e.Sequence > through {
			break
		}
		if e.Type == "run.finished" {
			var d journal.FinishData
			journal.Decode(e.Data, &d)
			statuses[e.RunID] = d.Status
		}
	}
	calls := map[string]bool{}
	for _, e := range events {
		if e.Sequence > through {
			break
		}
		if err := journal.Validate(e.Proposed); err != nil {
			return run.Prepared{}, err
		}
		include := e.RunID == s.ID || statuses[e.RunID] == "succeeded"
		switch e.Type {
		case "user.message.accepted":
			var d journal.UserMessageData
			journal.Decode(e.Data, &d)
			raw, err := resource.ReadBytes(ctx, p.Content, s.ActorID, s.SpaceID, d.ContentID, 256<<10)
			if err != nil {
				return run.Prepared{}, err
			}
			result.Messages = append(result.Messages, run.Message{Role: "user", Content: string(raw)})
		case "model.response.completed":
			if !include {
				continue
			}
			var d journal.ResponseData
			journal.Decode(e.Data, &d)
			var reply run.Reply
			if err := resource.ReadJSON(ctx, p.Content, s.ActorID, s.SpaceID, d.ContentID, 4<<20, &reply); err != nil {
				return run.Prepared{}, err
			}
			result.Messages = append(result.Messages, reply.Message)
			for _, c := range reply.Message.ToolCalls {
				if calls[c.ID] {
					return run.Prepared{}, run.ErrProtocol
				}
				calls[c.ID] = true
			}
		case "tool.execution.completed":
			var d journal.ToolData
			journal.Decode(e.Data, &d)
			if !include {
				if d.SideEffect == "committed" || d.SideEffect == "unknown" {
					result.Messages = append(result.Messages, run.Message{Role: "user", Content: "[MASS recorded a tool action in a failed/cancelled run: " + d.Name + "; side_effect=" + d.SideEffect + ". Its original result is retained in the trace.]"})
				}
				continue
			}
			if !calls[d.CallID] {
				return run.Prepared{}, run.ErrProtocol
			}
			delete(calls, d.CallID)
			raw, err := resource.ReadBytes(ctx, p.Content, s.ActorID, s.SpaceID, d.ContentID, 4<<20)
			if err != nil {
				return run.Prepared{}, err
			}
			result.Messages = append(result.Messages, run.Message{Role: "tool", Content: string(raw), ToolCallID: d.CallID})
		case "operation.reconciled":
			var d journal.ReconcileData
			journal.Decode(e.Data, &d)
			if d.Outcome != "unknown" {
				continue
			}
			result.Messages = append(result.Messages, run.Message{Role: "user", Content: "[MASS interrupted operation " + e.OperationID + "; outcome unknown. Verify its effects before attempting it again.]"})
		case "workspace.restored":
			var d journal.RestoreData
			journal.Decode(e.Data, &d)
			result.Messages = append(result.Messages, run.Message{Role: "user", Content: "[MASS workspace restored from snapshot " + d.SnapshotID + "; reason=" + d.Reason + ". Unsaved changes from an interrupted run may be lost.]"})
		case "run.finished":
			if statuses[e.RunID] != "succeeded" {
				var d journal.FinishData
				journal.Decode(e.Data, &d)
				result.Messages = append(result.Messages, run.Message{Role: "user", Content: "[MASS previous run " + d.Status + "; reason=" + d.Error + ".]"})
			}
		default:
			continue
		}
		result.Spec.SourceEventIDs = append(result.Spec.SourceEventIDs, e.EventID)
	}
	if len(calls) > 0 {
		return run.Prepared{}, run.ErrProtocol
	}
	raw, _ := json.Marshal(struct {
		Messages []run.Message
		Tools    []run.ToolSpec
	}{result.Messages, tools})
	// This conservative, versioned upper bound never truncates or calls a model.
	result.InputTokens = len(raw) + len(result.Messages)*32 + 128
	if result.InputTokens > s.Definition.MaxInputTokens || result.InputTokens+s.Definition.OutputTokens > s.Definition.Model.ContextWindow {
		return run.Prepared{}, run.ErrOverflow
	}
	result.Fingerprint = journal.Digest(struct {
		Spec     run.ProjectionSpec
		Messages []run.Message
		Tools    []run.ToolSpec
	}{result.Spec, result.Messages, tools})
	return result, nil
}
