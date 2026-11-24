package resource

import (
	"context"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
)

type Edit struct {
	Old string `json:"old_text"`
	New string `json:"new_text"`
}
type EditInput struct {
	Path           string `json:"path"`
	ExpectedSHA256 string `json:"expected_sha256,omitempty"`
	Edits          []Edit `json:"edits"`
}

func (f *FileService) Edit(ctx context.Context, s run.Snapshot, in EditInput) (result run.ToolResult, err error) {
	if _, _, isSkill := splitSkillPath(in.Path); isSkill {
		return result, run.ToolFailure("skill_read_only", "none")
	}
	if !filePath(in.Path) || len(in.Edits) < 1 || len(in.Edits) > 100 {
		return result, storage.ErrInvalid
	}
	err = f.Workspaces.With(ctx, s.SpaceID, s.SessionID, func(dir WorkspaceDir) error {
		if err := f.Check(ctx, s); err != nil {
			return err
		}
		raw, err := f.read(ctx, dir, in.Path)
		if err != nil {
			return err
		}
		if !utf8.Valid(raw) {
			return storage.ErrInvalid
		}
		currentSHA := hashBytes(raw)
		if in.ExpectedSHA256 != "" && currentSHA != in.ExpectedSHA256 {
			return storage.ErrConflict
		}
		type interval struct {
			start, end  int
			replacement string
		}
		edits := []interval{}
		source := string(raw)
		for _, e := range in.Edits {
			if e.Old == "" || !utf8.ValidString(e.Old) || !utf8.ValidString(e.New) {
				return storage.ErrConflict
			}
			start := strings.Index(source, e.Old)
			if start < 0 || strings.Index(source[start+1:], e.Old) >= 0 {
				return storage.ErrConflict
			}
			edits = append(edits, interval{start, start + len(e.Old), e.New})
		}
		sort.Slice(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
		for i := 1; i < len(edits); i++ {
			if edits[i].start < edits[i-1].end {
				return storage.ErrConflict
			}
		}
		for i := len(edits) - 1; i >= 0; i-- {
			e := edits[i]
			source = source[:e.start] + e.replacement + source[e.end:]
			if int64(len(source)) > f.MaxBytes {
				return storage.ErrInvalid
			}
		}
		return f.write(ctx, s, dir, WriteFileInput{Path: in.Path, ExpectedSHA256: currentSHA, Content: source}, &result)
	})
	return
}
