package resource

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"mime"
	"strings"
	"unicode/utf8"

	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
)

type Sources struct {
	Files   *FileService
	Journal *journal.Journal
}

type SourceInput struct {
	RecordOffset int        `json:"record_offset"`
	RecordLimit  int        `json:"record_limit"`
	AttachmentID string     `json:"attachment_id"`
	Format       string     `json:"format"`
	Range        *run.Range `json:"range"`
}

func (r *Sources) ReadAttachment(ctx context.Context, s run.Snapshot, in SourceInput) (run.ToolResult, error) {
	if err := r.Files.Check(ctx, s); err != nil {
		return run.ToolResult{}, err
	}
	var a *run.Attachment
	reached := false
	_, err := WalkHistory(ctx, r.Journal, s.ActorID, s.SpaceID, s.SessionID, func(e journal.Event) error {
		if e.Type == "user.message.accepted" {
			var d journal.UserMessageData
			journal.Decode(e.Data, &d)
			for _, candidate := range d.Attachments {
				if candidate.ID == in.AttachmentID {
					value := candidate
					a = &value
				}
			}
		}
		if e.EventID == s.UserEventID {
			reached = true
			return io.EOF
		}
		return nil
	})
	if err != nil {
		return run.ToolResult{}, err
	}
	if !reached {
		return run.ToolResult{}, storage.ErrIntegrity
	}
	if a == nil || a.SessionID != s.SessionID {
		return run.ToolResult{}, run.ToolFailure("attachment_not_accepted", "none")
	}
	raw, err := ReadBytes(ctx, r.Files.Content, s.ActorID, s.SpaceID, a.Ref.ObjectID, 8<<20)
	if err != nil {
		return run.ToolResult{}, err
	}
	if hashBytes(raw) != a.Ref.SHA256 {
		return run.ToolResult{}, storage.ErrIntegrity
	}
	media, _, err := mime.ParseMediaType(a.MediaType)
	if err != nil {
		return run.ToolResult{}, storage.ErrInvalid
	}
	if !strings.HasPrefix(media, "text/") && media != "application/json" && media != "application/csv" {
		return run.ToolResult{}, run.ToolFailure("unsupported_media_type", "none")
	}
	if !utf8.Valid(raw) {
		return run.ToolResult{}, run.ToolFailure("invalid_encoding", "none")
	}
	format := in.Format
	if format == "" {
		format = "text"
	}
	var data map[string]any
	switch format {
	case "text":
		data, err = rangeData(raw, in.Range)
	case "json", "csv":
		if in.Range != nil {
			return run.ToolResult{}, run.ToolFailure("structured_range_not_supported", "none")
		}
		var value any
		if format == "json" {
			if err = json.Unmarshal(raw, &value); err != nil {
				return run.ToolResult{}, run.ToolFailure("invalid_json", "none")
			}
		} else {
			value, err = parseCSV(ctx, string(raw), 10000)
			if err != nil {
				return run.ToolResult{}, err
			}
		}
		offset, limit := in.RecordOffset, in.RecordLimit
		if limit == 0 {
			limit = 100
		}
		if offset < 0 || limit < 1 || limit > 1000 {
			return run.ToolResult{}, storage.ErrInvalid
		}
		var rows []any
		switch values := value.(type) {
		case []any:
			rows = values
		case [][]string:
			for _, row := range values {
				rows = append(rows, row)
			}
		}
		complete := true
		var next any
		if rows != nil {
			if offset > len(rows) {
				return run.ToolResult{}, storage.ErrInvalid
			}
			selected := []any{}
			size := 2
			end := offset
			for end < len(rows) && len(selected) < limit {
				encoded, _ := json.Marshal(rows[end])
				if size+len(encoded)+1 > PreviewBytes {
					break
				}
				selected = append(selected, rows[end])
				size += len(encoded) + 1
				end++
			}
			value = selected
			complete = offset == 0 && end == len(rows)
			if end < len(rows) && end > offset {
				next = end
			}
		} else {
			if offset != 0 {
				return run.ToolResult{}, storage.ErrInvalid
			}
			encoded, _ := json.Marshal(value)
			complete = len(encoded) <= PreviewBytes
			if !complete {
				value = nil
			}
		}
		data = map[string]any{"value": value, "format": format, "parser_version": "mass-attachment-v1", "parser_complete": true, "complete": complete, "truncated": !complete, "record_offset": offset, "next_record_offset": next, "size": len(raw), "sha256": a.Ref.SHA256}
	default:
		return run.ToolResult{}, run.ToolFailure("unsupported_format", "none")
	}
	if err != nil {
		return run.ToolResult{}, err
	}
	data["attachment_id"] = a.ID
	result := dataResult(data, "none", a.Ref)
	result.Complete = data["complete"].(bool)
	result.Truncated = !result.Complete
	return result, nil
}

func parseCSV(ctx context.Context, text string, maxRows int) ([][]string, error) {
	reader := csv.NewReader(strings.NewReader(text))
	rows := [][]string{}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, run.ToolFailure("invalid_csv", "none")
		}
		if len(rows) >= maxRows {
			return nil, run.ToolFailure("csv_limit_exceeded", "none")
		}
		rows = append(rows, row)
	}
	return rows, nil
}
