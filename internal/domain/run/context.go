package run

import "github.com/imalimin/mass/internal/domain/storage"

// ContextBlock binds each model-visible block to immutable facts. Indices are
// half-open message offsets; tool groups always occupy a single block.
type ContextBlock struct {
	Start    int           `json:"start"`
	End      int           `json:"end"`
	EventIDs []string      `json:"event_ids"`
	Refs     []storage.Ref `json:"content_refs"`
	Digest   string        `json:"digest"`
	Reason   string        `json:"reason"`
	RunID    string        `json:"run_id,omitempty"`
	Sequence int64         `json:"sequence,string"`
}
type ContextSummary struct {
	Text            string         `json:"text"`
	SourceEventIDs  []string       `json:"source_event_ids"`
	Blocks          []ContextBlock `json:"ordered_source_blocks"`
	PreviousRef     *storage.Ref   `json:"previous_summary_ref,omitempty"`
	Through         int64          `json:"coverage_through,string"`
	ResponseEventID string         `json:"response_event_id"`
	ProjectionID    string         `json:"projection_id"`
	Policy          string         `json:"policy"`
}

type ContextOmission struct {
	EventID string `json:"event_id"`
	Reason  string `json:"reason"`
}
