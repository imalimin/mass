// Package evaluation orchestrates versioned test inputs through the ordinary session API.
package evaluation

import (
	"context"
	"encoding/json"
	"time"

	"github.com/imalimin/mass/internal/app/identity"
	sessionapp "github.com/imalimin/mass/internal/app/session"
	"github.com/imalimin/mass/internal/domain/run"
)

type URL struct {
	URL string `json:"url"`
}
type Part struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL *URL   `json:"image_url,omitempty"`
	VideoURL *URL   `json:"video_url,omitempty"`
}
type Turn struct {
	Content []Part `json:"content"`
}
type Case struct {
	Turns []Turn `json:"turns"`
}
type DatasetInput struct {
	Name  string `json:"name"`
	Cases []Case `json:"cases"`
}
type Dataset struct {
	ID        string    `json:"id"`
	SpaceID   string    `json:"space_id"`
	Version   int64     `json:"version"`
	Name      string    `json:"name"`
	SavedAt   time.Time `json:"saved_at"`
	CaseCount int       `json:"case_count"`
	Cases     []Case    `json:"cases,omitempty"`
}
type ExperimentInput struct {
	DatasetID      string `json:"dataset_id"`
	AgentID        string `json:"agent_id"`
	AgentVersionID string `json:"agent_version_id"`
	Concurrency    int    `json:"concurrency"`
	Key            string `json:"idempotency_key"`
}
type Metrics struct {
	InputTokens       *int64 `json:"input_tokens"`
	InputCachedTokens *int64 `json:"input_cached_tokens"`
	OutputTokens      *int64 `json:"output_tokens"`
	ReasoningTokens   *int64 `json:"reasoning_tokens"`
	DurationMS        *int64 `json:"duration_ms"`
	StepCount         *int64 `json:"step_count"`
}
type Progress struct {
	Status       string   `json:"status"`
	SessionID    string   `json:"session_id,omitempty"`
	RunIDs       []string `json:"run_ids"`
	FailureStage string   `json:"failure_stage,omitempty"`
	FailureCode  string   `json:"failure_code,omitempty"`
	Metrics
}
type Experiment struct {
	ID             string              `json:"id"`
	SpaceID        string              `json:"space_id"`
	ActorID        string              `json:"actor_id"`
	CreatedAt      time.Time           `json:"created_at"`
	DatasetID      string              `json:"dataset_id"`
	DatasetVersion int64               `json:"dataset_version"`
	AgentID        string              `json:"agent_id"`
	AgentVersionID string              `json:"agent_version_id"`
	Concurrency    int                 `json:"concurrency"`
	Key            string              `json:"-"`
	Digest         string              `json:"-"`
	Result         map[string]Progress `json:"result"`
	CompletedAt    *time.Time          `json:"completed_at"`
	Status         string              `json:"status"`
	CaseCount      int                 `json:"case_count"`
	Succeeded      int                 `json:"succeeded"`
	Failed         int                 `json:"failed"`
	CaseProgress   map[string]Progress `json:"case_progress,omitempty"`
}
type CaseView struct {
	Ordinal int    `json:"ordinal"`
	Turns   []Turn `json:"turns"`
	Progress
}
type Repository interface {
	SaveEvaluationDataset(context.Context, string, string, string, DatasetInput) (Dataset, error)
	EvaluationDataset(context.Context, string, string, string, int64) (Dataset, error)
	EvaluationDatasets(context.Context, string, string, identity.Query) ([]Dataset, error)
	FindEvaluationRequest(context.Context, string, string, string, string) (Experiment, bool, error)
	CreateEvaluationExperiment(context.Context, Experiment) (Experiment, error)
	EvaluationExperiment(context.Context, string, string, string) (Experiment, error)
	EvaluationExperiments(context.Context, string, string, identity.Query) ([]Experiment, error)
	CompletedEvaluationExperiments(context.Context, string, string, []string) ([]Experiment, error)
	FinishEvaluationExperiment(context.Context, string, string, string, map[string]Progress, time.Time) error
}

// Calls are the same Go entry points used by the HTTP handlers. No runtime implementation is imported.
type Calls struct {
	CheckAgent  func(context.Context, string, string, string, string) error
	Create      func(context.Context, string, string, sessionapp.CreateInput) (run.Session, error)
	Submit      func(context.Context, string, string, string, sessionapp.SubmitInput) (run.View, error)
	Run         func(context.Context, string, string, string) (run.View, error)
	Diagnostics func(context.Context, string, string, string) ([]json.RawMessage, error)
}
