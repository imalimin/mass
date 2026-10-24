package httptransport

import (
	"context"
	"sort"

	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/journal"
)

type metricGroup struct {
	Purpose             string         `json:"purpose"`
	Phase               string         `json:"phase"`
	Cache               run.CacheStats `json:"cache"`
	AverageMS           *float64       `json:"average_ms"`
	AverageFirstTokenMS *float64       `json:"average_first_token_ms"`
}

func modelMetrics(ctx context.Context, repo RuntimeRepository, actor, space, id string) ([]metricGroup, int64, error) {
	phases := map[string]string{}
	attempts := map[string]string{}
	usages := map[string][]run.Usage{}
	durations := map[string][]int64{}
	first := map[string][]int64{}
	groups := map[string]metricGroup{}
	var through *int64
	var after int64
	for {
		page, err := repo.RuntimeTrace(ctx, actor, space, id, after, through, 100)
		if err != nil {
			return nil, 0, err
		}
		if through == nil {
			n := page.Through
			through = &n
		}
		if page.Through != *through || page.HasMore && page.Next <= after {
			return nil, 0, run.ErrProtocol
		}
		for _, e := range page.Events {
			if err := journal.Validate(e.Proposed); err != nil {
				return nil, 0, err
			}
			switch e.Type {
			case "context.projected":
				var d journal.ProjectionData
				journal.Decode(e.Data, &d)
				phase := d.PrefixChangeReason
				if phase == "" {
					phase = "legacy"
				}
				phases[e.EventID] = phase
			case "model.attempt.started":
				var d journal.AttemptData
				journal.Decode(e.Data, &d)
				phase := phases[d.ProjectionEventID]
				key := d.Purpose + "/" + phase
				attempts[d.ID] = key
				groups[key] = metricGroup{Purpose: d.Purpose, Phase: phase}
			case "model.response.completed", "model.attempt.failed":
				var usage run.Usage
				var observation *run.ModelObservation
				var attempt string
				if e.Type == "model.response.completed" {
					var d journal.ResponseData
					journal.Decode(e.Data, &d)
					usage = d.Usage
					observation = d.Observation
					attempt = d.AttemptID
				} else {
					var d journal.AttemptFailedData
					journal.Decode(e.Data, &d)
					usage = d.Usage
					observation = d.Observation
					attempt = d.AttemptID
				}
				key := attempts[attempt]
				if key == "" {
					return nil, 0, run.ErrProtocol
				}
				usages[key] = append(usages[key], usage)
				if observation != nil {
					durations[key] = append(durations[key], observation.DurationMS)
					if observation.FirstTokenMS != nil {
						first[key] = append(first[key], *observation.FirstTokenMS)
					}
				}
			}
		}
		if !page.HasMore {
			break
		}
		after = page.Next
	}
	average := func(values []int64) *float64 {
		if len(values) == 0 {
			return nil
		}
		var sum int64
		for _, v := range values {
			sum += v
		}
		n := float64(sum) / float64(len(values))
		return &n
	}
	keys := []string{}
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	result := []metricGroup{}
	for _, key := range keys {
		g := groups[key]
		g.Cache = run.MeasureCache(usages[key])
		g.AverageMS = average(durations[key])
		g.AverageFirstTokenMS = average(first[key])
		result = append(result, g)
	}
	return result, *through, nil
}
