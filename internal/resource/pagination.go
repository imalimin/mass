package resource

import (
	"encoding/json"
	"strconv"

	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
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
	Items    []json.RawMessage `json:"items"`
	Complete bool              `json:"complete"`
	Scanned  int               `json:"scanned"`
	Bytes    int64             `json:"bytes"`
}
type pageCursor struct {
	Object string `json:"object"`
	Offset int    `json:"offset"`
}

func (f *FileService) EnableRunPagination() {
	f.queries = make(map[string]queryResult)
}

func (f *FileService) page(s run.Snapshot, kind string, in PageInput, build func() (queryResult, error)) (run.ToolResult, error) {
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
	scope := "tool-page-v2:" + s.SpaceID + ":" + s.SessionID + ":" + s.ActorID + ":" + s.ID + ":" + kind + ":" + journal.Digest(in)
	var q queryResult
	var queryID string
	offset := 0
	if cursor != "" {
		if f.Cursors == nil || f.queries == nil {
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
		var exists bool
		q, exists = f.queries[c.Object]
		if !exists {
			return run.ToolResult{}, run.ErrCursor
		}
		queryID, offset = c.Object, c.Offset
	} else {
		var err error
		q, err = build()
		if err != nil {
			return run.ToolResult{}, err
		}
		if len(q.Items) > limit {
			queryID = uuid.NewString()
		}
	}
	if offset > len(q.Items) {
		return run.ToolResult{}, run.ErrCursor
	}
	end := min(offset+limit, len(q.Items))
	var next any
	if end < len(q.Items) {
		if f.Cursors == nil || f.queries == nil {
			return run.ToolResult{}, run.ErrCursor
		}
		f.queries[queryID] = q
		raw, _ := json.Marshal(pageCursor{Object: queryID, Offset: end})
		next = f.Cursors.SealPage(scope, string(raw))
	}
	data, _ := json.Marshal(map[string]any{"items": q.Items[offset:end], "next_cursor": next, "complete": q.Complete, "truncated": !q.Complete, "scanned_entries": q.Scanned, "scanned_bytes": q.Bytes, "offset": strconv.Itoa(offset), "parser_version": "mass-tools-v1"})
	return run.ToolResult{Status: "completed", Data: data, SideEffect: "none", Complete: q.Complete, Truncated: !q.Complete, SourceRefs: []storage.Ref{}}, nil
}
