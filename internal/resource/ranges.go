package resource

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"unicode/utf8"
)

const PreviewBytes = 64 << 10

func bytesReader(raw []byte) *bytes.Reader { return bytes.NewReader(raw) }

// Ranges describe complete source bytes; a bounded view never masquerades as the full source.
func rangeData(raw []byte, r *run.Range) (map[string]any, error) {
	start, end := int64(0), int64(len(raw))
	text := utf8.Valid(raw)
	if r != nil {
		byteRange := r.Offset != nil || r.Length != nil
		lineRange := r.StartLine != nil || r.EndLine != nil
		if byteRange == lineRange {
			return nil, storage.ErrInvalid
		}
		if byteRange {
			if r.Offset == nil || r.Length == nil || *r.Offset < 0 || *r.Length < 1 || *r.Length > PreviewBytes || *r.Offset > int64(len(raw)) {
				return nil, storage.ErrInvalid
			}
			start = *r.Offset
			end = start + min(*r.Length, int64(len(raw))-start)
		}
		if lineRange {
			if !text || r.StartLine == nil || r.EndLine == nil || *r.StartLine < 1 || *r.EndLine < *r.StartLine || *r.EndLine-*r.StartLine > 1000 {
				return nil, storage.ErrInvalid
			}
			line := 1
			start = -1
			end = int64(len(raw))
			for i := 0; i <= len(raw); i++ {
				if line == *r.StartLine && start < 0 {
					start = int64(i)
				}
				if i == len(raw) {
					break
				}
				if raw[i] == '\n' {
					if line == *r.EndLine {
						end = int64(i + 1)
						break
					}
					line++
				}
			}
			if start < 0 {
				return nil, storage.ErrInvalid
			}
		}
	}
	capped := end-start > PreviewBytes
	if capped {
		end = start + PreviewBytes
	}
	part := raw[start:end]
	encoding := "utf-8"
	if text {
		// A byte range cutting a code point is represented explicitly as bytes.
		if !utf8.Valid(part) {
			if capped && (start == int64(len(raw)) || utf8.RuneStart(raw[start])) {
				for len(part) > 0 && !utf8.Valid(part) {
					part = part[:len(part)-1]
					end--
				}
			} else {
				encoding = "base64"
			}
		}
	} else {
		encoding = "base64"
	}
	content := string(part)
	if encoding == "base64" {
		content = base64.StdEncoding.EncodeToString(part)
	}
	complete := start == 0 && end == int64(len(raw))
	return map[string]any{"content": content, "encoding": encoding, "offset": start, "length": end - start, "size": len(raw), "sha256": hashBytes(raw), "truncated": !complete, "complete": complete, "parser_version": "mass-range-v1"}, nil
}
func dataResult(data any, effect string, refs ...storage.Ref) run.ToolResult {
	raw, _ := json.Marshal(data)
	if refs == nil {
		refs = []storage.Ref{}
	}
	return run.ToolResult{Status: "completed", Data: raw, SideEffect: effect, Complete: true, SourceRefs: refs}
}
