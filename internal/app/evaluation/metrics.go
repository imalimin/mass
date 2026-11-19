package evaluation

import (
	"context"
	"encoding/json"
	"time"

	"github.com/imalimin/mass/internal/resource/sessionlog"
)

func number(n int64) *int64 { return &n }
func Aggregate(records []json.RawMessage, ids []string) (Metrics, error) {
	selected := map[string]bool{}
	for _, id := range ids {
		selected[id] = true
	}
	type record struct {
		sessionlog.Header
		Step       int       `json:"step"`
		AcceptedAt time.Time `json:"accepted_at"`
		FinishedAt time.Time `json:"finished_at"`
		Model      struct {
			Usage *struct {
				Input     int64 `json:"input_tokens"`
				Cached    int64 `json:"input_cached_tokens"`
				Output    int64 `json:"output_tokens"`
				Reasoning int64 `json:"reasoning_tokens"`
			} `json:"usage"`
		} `json:"model"`
	}
	var input, cached, output, reasoning int64
	steps := map[string]map[int]bool{}
	seen := map[int64]bool{}
	accepted := map[string]time.Time{}
	finished := map[string]time.Time{}
	for _, raw := range records {
		var r record
		if err := json.Unmarshal(raw, &r); err != nil {
			return Metrics{}, err
		}
		if !selected[r.RunID] || seen[r.Seq] {
			continue
		}
		seen[r.Seq] = true
		switch r.Type {
		case sessionlog.RunAccepted:
			accepted[r.RunID] = r.AcceptedAt
		case sessionlog.RunFinished:
			finished[r.RunID] = r.FinishedAt
		case sessionlog.ModelStep:
			if steps[r.RunID] == nil {
				steps[r.RunID] = map[int]bool{}
			}
			steps[r.RunID][r.Step] = true
			if u := r.Model.Usage; u != nil {
				input += u.Input
				cached += u.Cached
				output += u.Output
				reasoning += u.Reasoning
			}
		}
	}
	var count int64
	for _, set := range steps {
		count += int64(len(set))
	}
	m := Metrics{InputTokens: number(input), InputCachedTokens: number(cached), OutputTokens: number(output), ReasoningTokens: number(reasoning), StepCount: number(count)}
	if len(ids) > 0 {
		first, last := accepted[ids[0]], finished[ids[len(ids)-1]]
		if !first.IsZero() && !last.IsZero() && !last.Before(first) {
			m.DurationMS = number(last.Sub(first).Milliseconds())
		}
	}
	return m, nil
}
func (s *Service) metrics(t *task, ids []string, session string) Metrics {
	ctx, stop := context.WithTimeout(t.ctx, s.callTimeout)
	defer stop()
	records, err := s.Calls.Diagnostics(ctx, t.experiment.SpaceID, session, ids)
	if err == nil {
		var m Metrics
		m, err = Aggregate(records, ids)
		if err == nil {
			return m
		}
	}
	if s.Logger != nil && t.ctx.Err() == nil {
		s.Logger.Warn("evaluation metrics unavailable", "experiment_id", t.experiment.ID, "session_id", session, "error", err)
	}
	return Metrics{}
}
