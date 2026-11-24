package resource

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
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

func PreparePlan(ctx context.Context, j *journal.Journal, s run.Snapshot, steps []run.PlanStep) (run.ToolResult, error) {
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
	used := map[string]bool{}
	for _, step := range steps {
		if step.ID != "" {
			used[step.ID] = true
		}
	}
	normalized := make([]run.PlanStep, len(steps))
	nextNum := 1
	for i, step := range steps {
		if step.ID == "" {
			for {
				candidate := fmt.Sprintf("step-%d", nextNum)
				nextNum++
				if !used[candidate] {
					step.ID = candidate
					used[candidate] = true
					break
				}
			}
		}
		normalized[i] = step
	}
	p := run.Plan{Revision: current.Revision + 1, Steps: normalized}
	if !ValidatePlan(p) {
		return run.ToolResult{}, storage.ErrInvalid
	}
	result := dataResult(p, "committed")
	result.Plan = &p
	return result, nil
}
