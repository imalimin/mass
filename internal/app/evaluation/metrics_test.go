package evaluation

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/imalimin/mass/internal/app/identity"
)

func TestAggregateIncludesRetriesAndDeduplicatesSteps(t *testing.T) {
	raw := []string{
		`{"seq":1,"type":"run_accepted","run_id":"a","accepted_at":"2026-11-19T00:00:00Z"}`,
		`{"seq":2,"type":"model_step","run_id":"a","step":1,"model":{"usage":{"input_tokens":10,"input_cached_tokens":4,"output_tokens":5,"reasoning_tokens":2}}}`,
		`{"seq":3,"type":"model_step","run_id":"a","step":1,"retry_of":"first","model":{"usage":{"input_tokens":20,"output_tokens":8,"reasoning_tokens":3}}}`,
		`{"seq":4,"type":"model_step","run_id":"a","step":2,"purpose":"summary","model":{"usage":null}}`,
		`{"seq":5,"type":"run_finished","run_id":"a","finished_at":"2026-11-19T00:00:01Z"}`,
		`{"seq":6,"type":"run_accepted","run_id":"b","accepted_at":"2026-11-19T00:00:02Z"}`,
		`{"seq":7,"type":"model_step","run_id":"b","step":1,"model":{"usage":{"input_tokens":30,"output_tokens":9}}}`,
		`{"seq":8,"type":"run_finished","run_id":"b","finished_at":"2026-11-19T00:00:04Z"}`,
		`{"seq":9,"type":"model_step","run_id":"other","step":1,"model":{"usage":{"input_tokens":999}}}`,
	}
	records := []json.RawMessage{}
	for _, r := range raw {
		records = append(records, json.RawMessage(r))
	}
	records = append(records, records[1])
	m, err := Aggregate(records, []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if *m.InputTokens != 60 || *m.InputCachedTokens != 4 || *m.OutputTokens != 22 || *m.ReasoningTokens != 5 || *m.StepCount != 3 || *m.DurationMS != 4000 {
		t.Fatalf("wrong aggregate: %+v", m)
	}
	zero, err := Aggregate(nil, nil)
	if err != nil || *zero.InputTokens != 0 || *zero.StepCount != 0 || zero.DurationMS != nil {
		t.Fatalf("wrong empty metrics: %+v %v", zero, err)
	}
}
func TestDatasetValidation(t *testing.T) {
	text := func(value string) Turn { return Turn{Content: []Part{{Type: "text", Text: value}}} }
	valid := DatasetInput{Name: "测试", Cases: []Case{{Turns: []Turn{text("  原文\n"), text("第二轮")}}}}
	if err := ValidateDataset(valid); err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{"image_url", "video_url"} {
		invalid := DatasetInput{Name: "媒体", Cases: []Case{{Turns: []Turn{text("first"), {Content: []Part{{Type: typ}}}}}}}
		if err := ValidateDataset(invalid); !errors.Is(err, identity.ErrCapability) {
			t.Fatalf("media accepted: %s %v", typ, err)
		}
	}
	if err := ValidateDataset(DatasetInput{Name: "空集", Cases: []Case{}}); err != nil {
		t.Fatal(err)
	}
	for _, in := range []DatasetInput{{Name: "x", Cases: nil}, {Name: "x", Cases: []Case{{Turns: nil}}}, {Name: "x", Cases: []Case{{Turns: []Turn{text(" ")}}}}} {
		if ValidateDataset(in) == nil {
			t.Fatalf("invalid dataset accepted: %+v", in)
		}
	}
}
