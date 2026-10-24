package resource

import (
	"context"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
	"regexp"
	"strings"
	"unicode/utf8"
)

var stepID = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

func ValidatePlan(p run.Plan) bool {
	if p.Revision < 1 || p.Revision > 1000000 || len(p.Steps) > 100 {
		return false
	}
	seen := map[string]bool{}
	active := 0
	for _, step := range p.Steps {
		if !stepID.MatchString(step.ID) || seen[step.ID] || len(step.Title) > 256 || strings.TrimSpace(step.Title) == "" || !utf8.ValidString(step.Title) {
			return false
		}
		seen[step.ID] = true
		switch step.Status {
		case "pending", "completed", "skipped":
		case "in_progress":
			active++
		default:
			return false
		}
	}
	return active <= 1
}
func PreparePlan(ctx context.Context, j *journal.Journal, s run.Snapshot, expected int64, steps []run.PlanStep) (run.ToolResult, error) {
	p := run.Plan{Revision: expected + 1, Steps: steps}
	if !ValidatePlan(p) {
		return run.ToolResult{}, storage.ErrInvalid
	}
	history, err := RunHistory(ctx, j, s.ActorID, s.SpaceID, s.SessionID, s.ID, "plan.updated")
	if err != nil {
		return run.ToolResult{}, err
	}
	current := run.Plan{}
	for _, event := range history {
		if event.RunID == s.ID && event.Type == "plan.updated" {
			journal.Decode(event.Data, &current)
		}
	}
	if current.Revision != expected {
		return run.ToolResult{}, storage.ErrConflict
	}
	for _, old := range current.Steps {
		for _, step := range steps {
			if old.ID == step.ID && old.Title != step.Title {
				return run.ToolResult{}, storage.ErrConflict
			}
		}
	}
	result := dataResult(p, "committed")
	result.Plan = &p
	return result, nil
}
