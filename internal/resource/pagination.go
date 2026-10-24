package resource

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
	"strconv"
)

type CursorSigner interface {
	SealPage(string, string) string
	OpenPage(string, string) (string, error)
}
type PageInput struct {
	Directory string `json:"directory"`
	Pattern   string `json:"pattern"`
	Mode      string `json:"mode"`
	PathGlob  string `json:"path_glob"`
	Cursor    string `json:"cursor"`
	Limit     int    `json:"limit"`
}
type queryResult struct {
	Space    string            `json:"space"`
	Session  string            `json:"session"`
	Query    string            `json:"query"`
	Items    []json.RawMessage `json:"items"`
	Complete bool              `json:"complete"`
	Scanned  int               `json:"scanned"`
	Bytes    int64             `json:"bytes"`
}
type pageCursor struct {
	Object string `json:"object"`
	Offset int    `json:"offset"`
}

func (f *FileService) page(ctx context.Context, s run.Snapshot, kind string, in PageInput, build func() (queryResult, error)) (run.ToolResult, error) {
	if in.Limit == 0 {
		in.Limit = 50
	}
	if in.Limit < 1 || in.Limit > 100 || len(in.Cursor) > 4096 {
		return run.ToolResult{}, storage.ErrInvalid
	}
	cursor := in.Cursor
	in.Cursor = ""
	limit := in.Limit
	in.Limit = 0
	scope := "tool-page-v1:" + s.ActorID + ":" + s.SpaceID + ":" + s.SessionID + ":" + kind + ":" + journal.Digest(in)
	var q queryResult
	var ref storage.Ref
	offset := 0
	if cursor != "" {
		if f.Cursors == nil {
			return run.ToolResult{}, run.ErrCursor
		}
		payload, err := f.Cursors.OpenPage(scope, cursor)
		if err != nil {
			return run.ToolResult{}, run.ErrCursor
		}
		var c pageCursor
		if json.Unmarshal([]byte(payload), &c) != nil || !storage.ValidID(c.Object) || c.Offset < 1 {
			return run.ToolResult{}, run.ErrCursor
		}
		o, err := f.Content.Repo.Object(ctx, s.ActorID, s.SpaceID, c.Object, true)
		if err != nil || o.Kind != "tool.query" {
			return run.ToolResult{}, run.ErrCursor
		}
		if err = ReadJSON(ctx, f.Content, s.ActorID, s.SpaceID, c.Object, 4<<20, &q); err != nil {
			return run.ToolResult{}, err
		}
		if q.Space != s.SpaceID || q.Session != s.SessionID || q.Query != scope {
			return run.ToolResult{}, run.ErrCursor
		}
		ref = storage.Ref{Role: "query", ObjectID: o.ID, SHA256: o.SHA256}
		offset = c.Offset
	} else {
		var err error
		q, err = build()
		if err != nil {
			return run.ToolResult{}, err
		}
		q.Space = s.SpaceID
		q.Session = s.SessionID
		q.Query = scope
		// Keep every saved query readable under the same object bound on later pages.
		header := q
		header.Items = []json.RawMessage{}
		base, _ := json.Marshal(header)
		remaining := (4 << 20) - len(base) - 128
		for i, item := range q.Items {
			remaining -= len(item) + 1
			if remaining < 0 {
				q.Items = q.Items[:i]
				q.Complete = false
				break
			}
		}
		raw, _ := json.Marshal(q)
		o, err := f.Content.Put(ctx, s.ActorID, s.SpaceID, uuid.NewString(), "tool.query", "application/json", bytesReader(raw))
		if err != nil {
			return run.ToolResult{}, err
		}
		ref = storage.Ref{Role: "query", ObjectID: o.ID, SHA256: o.SHA256}
	}
	if offset > len(q.Items) {
		return run.ToolResult{}, run.ErrCursor
	}
	end := min(offset+limit, len(q.Items))
	var next any
	if end < len(q.Items) {
		if f.Cursors == nil {
			return run.ToolResult{}, run.ErrCursor
		}
		raw, _ := json.Marshal(pageCursor{Object: ref.ObjectID, Offset: end})
		next = f.Cursors.SealPage(scope, string(raw))
	}
	data, _ := json.Marshal(map[string]any{"items": q.Items[offset:end], "next_cursor": next, "query_ref": ref.ObjectID, "complete": q.Complete, "truncated": !q.Complete, "scanned_entries": q.Scanned, "scanned_bytes": q.Bytes, "offset": strconv.Itoa(offset), "parser_version": "mass-tools-v1"})
	return run.ToolResult{Status: "completed", Data: data, SideEffect: "none", Complete: q.Complete, Truncated: !q.Complete, SourceRefs: []storage.Ref{ref}}, nil
}
