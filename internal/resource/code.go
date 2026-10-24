package resource

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
	"io/fs"
	"math"
	"strings"
	"time"
)

type CodeEngine interface {
	Execute(context.Context, run.CodeInput, run.CodeHost, run.ToolPolicy) (run.CodeOutput, error)
}
type CodeService struct {
	Engine  CodeEngine
	Files   *FileService
	Network *Network
	Journal *journal.Journal
	Content *ContentStore
}
type codeHost struct {
	service                   *CodeService
	ctx                       context.Context
	s                         run.Snapshot
	call                      string
	sequence, files, requests int
	inputs, outputs           int64
	effect                    string
	broken                    bool
}

func (c *CodeService) Run(ctx context.Context, s run.Snapshot, call string, input run.CodeInput) (run.ToolResult, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(s.Definition.ToolPolicy.TimeoutMS)*time.Millisecond)
	defer cancel()
	if err := c.Files.Check(ctx, s); err != nil {
		return run.ToolResult{}, err
	}
	if int64(len(input.Input)+len(input.Code)) > s.Definition.ToolPolicy.MaxInputBytes {
		return run.ToolResult{}, run.ToolFailure("code_input_limit", "none")
	}
	raw, _ := json.Marshal(map[string]any{"input": input, "session_id": s.SessionID, "base_workspace_snapshot_id": s.BaseSnapshotID, "engine": "goja-e6862644b38f", "policy": s.Definition.ToolPolicy})
	o, err := c.Content.Put(ctx, s.ActorID, s.SpaceID, uuid.NewString(), "tool.input", "application/json", bytesReader(raw))
	if err != nil {
		return run.ToolResult{}, err
	}
	e := journal.New(s.ActorID, "tool.execution.input_resolved", s.ID, journal.ResolvedData{CallID: call, ContentID: o.ID, Engine: "goja-e6862644b38f", Policy: "mass-tools-v1"}, storage.Ref{Role: "io", ObjectID: o.ID, SHA256: o.SHA256})
	e.OperationID = call
	if _, err = AppendRun(ctx, c.Journal, s, e); err != nil {
		return run.ToolResult{}, err
	}
	host := &codeHost{service: c, ctx: ctx, s: s, call: call, effect: "none", inputs: int64(len(input.Input) + len(input.Code))}
	output, actionErr := c.Engine.Execute(ctx, input, host, s.Definition.ToolPolicy)
	if output.Value == nil {
		output.Value = json.RawMessage(`null`)
	}
	result := dataResult(map[string]any{"value": output.Value, "logs": output.Logs, "logs_complete": output.LogsComplete, "error": output.Error, "error_detail": output.Detail, "io_count": host.sequence, "io_complete": !host.broken, "engine_version": "goja-e6862644b38f"}, host.effect, storage.Ref{Role: "input", ObjectID: o.ID, SHA256: o.SHA256})
	if output.Error != "" || actionErr != nil || host.broken {
		result.Status = "failed"
		result.Error = output.Error
		if result.Error == "" {
			result.Error = "code_failed"
		}
	}
	if host.broken {
		result.SideEffect = "unknown"
		result.Complete = false
	}
	return result, actionErr
}
func (h *codeHost) record(phase, name string, payload any, effect string, refs []storage.Ref) error {
	raw, _ := json.Marshal(payload)
	ctx := h.ctx
	var cancel context.CancelFunc
	if ctx.Err() != nil {
		ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
	}
	o, err := h.service.Content.Put(ctx, h.s.ActorID, h.s.SpaceID, uuid.NewString(), "tool.io", "application/json", bytesReader(raw))
	if err != nil {
		return err
	}
	all := []storage.Ref{{Role: "io", ObjectID: o.ID, SHA256: o.SHA256}}
	for i, r := range refs {
		r.Role = "source"
		r.Ordinal = i
		all = append(all, r)
	}
	complete := true
	if fields, ok := payload.(map[string]any); ok {
		if value, exists := fields["complete"].(bool); exists {
			complete = value
		}
	}
	e := journal.New(h.s.ActorID, "tool.io.recorded", h.s.ID, journal.IOData{CallID: h.call, Sequence: h.sequence, Phase: phase, Name: name, ContentID: o.ID, SideEffect: effect, Complete: complete}, all...)
	e.OperationID = h.call
	_, err = AppendRun(ctx, h.service.Journal, h.s, e)
	return err
}
func (h *codeHost) Invoke(name string, arguments json.RawMessage) (json.RawMessage, error) {
	if h.broken {
		return nil, run.ToolFailure("io_journal_incomplete", "unknown")
	}
	if err := h.service.Files.Check(h.ctx, h.s); err != nil {
		return nil, err
	}
	policy := h.s.Definition.ToolPolicy
	h.inputs += int64(len(arguments))
	if h.inputs > policy.MaxInputBytes {
		return nil, run.ToolFailure("code_input_limit", h.effect)
	}
	if strings.HasPrefix(name, "fs.") {
		h.files++
		if h.files > policy.MaxFileOperations {
			return nil, run.ToolFailure("file_operation_limit", h.effect)
		}
	}
	if name == "net.request" {
		h.requests++
		if h.requests > policy.MaxNetworkRequests {
			return nil, run.ToolFailure("network_operation_limit", h.effect)
		}
	}
	var args any
	if json.Unmarshal(arguments, &args) != nil {
		return nil, storage.ErrInvalid
	}
	safe := args
	if name == "net.request" {
		m, ok := args.(map[string]any)
		if !ok {
			return nil, storage.ErrInvalid
		}
		safe = redactedRequest(m)
	}
	h.sequence++
	if err := h.record("prepared", name, map[string]any{"arguments": safe}, "none", nil); err != nil {
		h.broken = true
		return nil, err
	}
	result, err := h.perform(name, args)
	if result.SideEffect == "unknown" {
		h.effect = "unknown"
	} else if result.SideEffect == "committed" && h.effect != "unknown" {
		h.effect = "committed"
	}
	if err != nil && result.SideEffect == "" {
		var failure *run.ToolError
		if errors.As(err, &failure) {
			result.SideEffect = failure.Effect
		} else if errors.Is(err, storage.ErrInvalid) || errors.Is(err, storage.ErrConflict) || errors.Is(err, fs.ErrNotExist) {
			result.SideEffect = "none"
		} else {
			result.SideEffect = "unknown"
		}
		if result.SideEffect == "unknown" {
			h.effect = "unknown"
		}
	}
	if result.Data == nil {
		result.Data = json.RawMessage(`null`)
	}
	h.outputs += int64(len(result.Data))
	if h.outputs > policy.MaxOutputBytes && err == nil {
		err = run.ToolFailure("code_output_limit", result.SideEffect)
	}
	phase := "completed"
	var errorCode any
	if err != nil {
		phase = "failed"
		errorCode = "host_operation_failed"
		var detail *run.ToolError
		if errors.As(err, &detail) {
			errorCode = detail.Code
		}
	}
	if saveErr := h.record(phase, name, map[string]any{"result": result.Data, "error": errorCode, "side_effect": result.SideEffect, "complete": result.Complete, "truncated": result.Truncated}, result.SideEffect, result.SourceRefs); saveErr != nil {
		h.broken = true
		h.effect = "unknown"
		return nil, saveErr
	}
	if err != nil {
		return nil, err
	}
	return result.Data, nil
}
func (h *codeHost) perform(name string, args any) (result run.ToolResult, err error) {
	files := *h.service.Files
	files.ReadBudget = func(size int64) error {
		if size > h.s.Definition.ToolPolicy.MaxInputBytes-h.inputs {
			return run.ToolFailure("code_input_limit", "none")
		}
		h.inputs += size
		return nil
	}
	f, s, ctx := &files, h.s, h.ctx
	if name == "net.request" {
		m, ok := args.(map[string]any)
		if !ok {
			return result, storage.ErrInvalid
		}
		maximum := min(s.Definition.ToolPolicy.MaxNetworkBodyBytes, s.Definition.ToolPolicy.MaxOutputBytes-h.outputs)
		if maximum <= 0 {
			return result, run.ToolFailure("code_output_limit", "none")
		}
		data, effect, e := h.service.Network.Request(ctx, m, maximum, func(ctx context.Context) error { return f.Check(ctx, s) })
		result := dataResult(data, effect)
		if data != nil {
			result.Complete, _ = data["complete"].(bool)
			result.Truncated = !result.Complete
		}
		return result, e
	}
	if name == "csv.parse" {
		text, ok := args.(string)
		if !ok {
			return result, storage.ErrInvalid
		}
		rows, e := parseCSV(ctx, text, 10000)
		return dataResult(rows, "none"), e
	}
	if name == "csv.stringify" {
		rows, ok := args.([]any)
		if !ok || len(rows) > 10000 {
			return result, storage.ErrInvalid
		}
		var buffer strings.Builder
		writer := csv.NewWriter(&buffer)
		for _, v := range rows {
			if err = ctx.Err(); err != nil {
				return result, err
			}
			row, ok := v.([]any)
			if !ok {
				return result, storage.ErrInvalid
			}
			values := []string{}
			for _, cell := range row {
				text, ok := cell.(string)
				if !ok {
					return result, storage.ErrInvalid
				}
				values = append(values, text)
			}
			if err = writer.Write(values); err != nil {
				return result, err
			}
			writer.Flush()
			if int64(buffer.Len()) > s.Definition.ToolPolicy.MaxOutputBytes {
				return result, run.ToolFailure("csv_output_limit", "none")
			}
		}
		return dataResult(buffer.String(), "none"), writer.Error()
	}
	wrapper, ok := args.(map[string]any)
	if !ok {
		return result, storage.ErrInvalid
	}
	values, ok := wrapper["arguments"].([]any)
	if !ok {
		return result, storage.ErrInvalid
	}
	counts := map[string][2]int{"fs.list": {0, 2}, "fs.readFile": {1, 2}, "fs.mkdir": {1, 1}, "fs.writeFile": {3, 3}, "fs.editFile": {3, 3}, "fs.rename": {3, 3}, "fs.remove": {2, 2}}
	count, known := counts[name]
	if !known || len(values) < count[0] || len(values) > count[1] {
		return result, storage.ErrInvalid
	}
	if len(values) > 0 {
		if _, ok := values[0].(string); !ok {
			return result, storage.ErrInvalid
		}
	}
	if name == "fs.writeFile" || name == "fs.rename" {
		if _, ok := values[1].(string); !ok {
			return result, storage.ErrInvalid
		}
	}
	optionIndex := 1
	if name == "fs.writeFile" || name == "fs.rename" || name == "fs.editFile" {
		optionIndex = 2
	}
	if name == "fs.mkdir" {
		optionIndex = -1
	}
	if optionIndex >= 0 && len(values) > optionIndex {
		options, ok := values[optionIndex].(map[string]any)
		if !ok {
			return result, storage.ErrInvalid
		}
		for key, value := range options {
			allowed := map[string]map[string]bool{
				"fs.list":      {"cursor": true, "limit": true},
				"fs.readFile":  {"expected_sha256": true, "range": true},
				"fs.writeFile": {"expected_sha256": true, "media_type": true},
				"fs.editFile":  {"expected_sha256": true}, "fs.rename": {"expected_sha256": true}, "fs.remove": {"expected_sha256": true},
			}
			if !allowed[name][key] {
				return result, storage.ErrInvalid
			}
			switch key {
			case "expected_sha256", "media_type", "cursor":
				if _, ok := value.(string); !ok {
					return result, storage.ErrInvalid
				}
			case "limit":
				if n, ok := value.(float64); !ok || math.Trunc(n) != n || n < 1 || n > 100 {
					return result, storage.ErrInvalid
				}
			case "range":
				span, ok := value.(map[string]any)
				if !ok {
					return result, storage.ErrInvalid
				}
				for k, v := range span {
					if k != "offset" && k != "length" && k != "start_line" && k != "end_line" {
						return result, storage.ErrInvalid
					}
					n, ok := v.(float64)
					if !ok || n < 0 || n > 1<<30 || math.Trunc(n) != n {
						return result, storage.ErrInvalid
					}
				}
			default:
				return result, storage.ErrInvalid
			}
		}
	}
	text := func(i int) string {
		if i < len(values) {
			v, _ := values[i].(string)
			return v
		}
		return ""
	}
	options := func(i int) map[string]any {
		if i < len(values) {
			m, _ := values[i].(map[string]any)
			if m != nil {
				return m
			}
		}
		return map[string]any{}
	}
	switch name {
	case "fs.list":
		limit := 100
		if n, ok := options(1)["limit"].(float64); ok {
			limit = int(n)
		}
		cursor, _ := options(1)["cursor"].(string)
		return f.Query(ctx, s, "list_files", PageInput{Directory: text(0), Limit: limit, Cursor: cursor})
	case "fs.readFile":
		o := options(1)
		sha, _ := o["expected_sha256"].(string)
		var span *run.Range
		if m, ok := o["range"].(map[string]any); ok {
			span = &run.Range{}
			if n, ok := m["offset"].(float64); ok {
				v := int64(n)
				span.Offset = &v
			}
			if n, ok := m["length"].(float64); ok {
				v := int64(n)
				span.Length = &v
			}
			if n, ok := m["start_line"].(float64); ok {
				v := int(n)
				span.StartLine = &v
			}
			if n, ok := m["end_line"].(float64); ok {
				v := int(n)
				span.EndLine = &v
			}
		}
		return f.Read(ctx, s, ReadFileInput{Path: text(0), ExpectedSHA256: sha, Range: span})
	case "fs.writeFile":
		o := options(2)
		sha, _ := o["expected_sha256"].(string)
		media, _ := o["media_type"].(string)
		if media == "" {
			media = "text/plain"
		}
		return f.Write(ctx, s, WriteFileInput{Path: text(0), Content: text(1), ExpectedSHA256: sha, MediaType: media})
	case "fs.editFile":
		sha, _ := options(2)["expected_sha256"].(string)
		raw, ok := func() (any, bool) {
			if len(values) > 1 {
				return values[1], true
			}
			return nil, false
		}()
		list, ok := raw.([]any)
		if !ok {
			return result, storage.ErrInvalid
		}
		edits := []Edit{}
		for _, v := range list {
			m, ok := v.(map[string]any)
			if !ok || len(m) != 2 {
				return result, storage.ErrInvalid
			}
			old, oldOK := m["old_text"].(string)
			next, nextOK := m["new_text"].(string)
			if !oldOK || !nextOK {
				return result, storage.ErrInvalid
			}
			edits = append(edits, Edit{Old: old, New: next})
		}
		return f.Edit(ctx, s, EditInput{Path: text(0), ExpectedSHA256: sha, Edits: edits})
	case "fs.rename":
		sha, _ := options(2)["expected_sha256"].(string)
		return f.Move(ctx, s, MoveInput{Source: text(0), Target: text(1), ExpectedSHA256: sha})
	case "fs.remove":
		sha, _ := options(1)["expected_sha256"].(string)
		return f.Delete(ctx, s, ReadFileInput{Path: text(0), ExpectedSHA256: sha})
	case "fs.mkdir":
		return f.Mkdir(ctx, s, text(0))
	}
	return result, storage.ErrInvalid
}
