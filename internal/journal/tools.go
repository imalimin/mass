package journal

import (
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
)

type IOData struct {
	CallID     string `json:"call_id"`
	Sequence   int    `json:"io_sequence"`
	Phase      string `json:"phase"`
	Name       string `json:"name"`
	ContentID  string `json:"content_id"`
	SideEffect string `json:"side_effect"`
	Complete   bool   `json:"complete"`
}
type ResolvedData struct {
	CallID    string `json:"call_id"`
	ContentID string `json:"content_id"`
	Engine    string `json:"engine"`
	Policy    string `json:"policy"`
}

func validateTools(e Proposed) (bool, error) {
	switch e.Type {
	case "plan.updated":
		var p run.Plan
		if Decode(e.Data, &p) != nil || p.Revision < 1 || p.Revision > 1000000 || len(p.Steps) > 100 || !keyPattern.MatchString(e.OperationID) || e.RunID == "" {
			return true, storage.ErrSchema
		}
		seen := map[string]bool{}
		active := 0
		for _, s := range p.Steps {
			if !keyPattern.MatchString(s.ID) || len(s.ID) > 64 || len(s.Title) < 1 || len(s.Title) > 256 || seen[s.ID] {
				return true, storage.ErrSchema
			}
			seen[s.ID] = true
			switch s.Status {
			case "pending", "completed", "skipped":
			case "in_progress":
				active++
			default:
				return true, storage.ErrSchema
			}
		}
		if active > 1 {
			return true, storage.ErrSchema
		}
	case "tool.execution.input_resolved":
		var d ResolvedData
		if Decode(e.Data, &d) != nil || !keyPattern.MatchString(d.CallID) || !storage.ValidID(d.ContentID) || d.Engine == "" || d.Policy == "" || e.RunID == "" || e.OperationID != d.CallID {
			return true, storage.ErrSchema
		}
	case "tool.io.recorded":
		var d IOData
		if Decode(e.Data, &d) != nil || !keyPattern.MatchString(d.CallID) || e.OperationID != d.CallID || d.Sequence < 1 || d.Sequence > 10000 || (d.Phase != "prepared" && d.Phase != "completed" && d.Phase != "failed") || len(d.Name) < 1 || len(d.Name) > 64 || !storage.ValidID(d.ContentID) || e.RunID == "" || (d.SideEffect != "none" && d.SideEffect != "committed" && d.SideEffect != "unknown") {
			return true, storage.ErrSchema
		}
	default:
		return false, nil
	}
	return true, nil
}
