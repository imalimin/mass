// Package sessionlog owns the durable records for a conversation.
package sessionlog

import (
	"encoding/json"
	"time"

	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/resource/trace"
)

const (
	SessionCreated    = "session_created"
	RunAccepted       = "run_accepted"
	ModelStep         = "model_step"
	ContextSummary    = "context_summary"
	RunFinished       = "run_finished"
	AttachmentAdded   = "attachment_added"
	WorkspaceRestored = "workspace_restored"
)

type Header struct {
	Seq   int64  `json:"seq"`
	Type  string `json:"type"`
	Role  string `json:"role"`
	RunID string `json:"run_id,omitempty"`
}

type Created struct {
	Header
	SchemaVersion int        `json:"schema_version"`
	SessionID     string     `json:"session_id"`
	SpaceID       string     `json:"space_id"`
	AgentID       string     `json:"agent_id"`
	VersionID     string     `json:"version_id"`
	ActorID       string     `json:"actor_id"`
	Title         string     `json:"title"`
	EventID       string     `json:"event_id"`
	CreatedAt     time.Time  `json:"created_at"`
	Definition    Definition `json:"definition"`
}

type Definition struct {
	Instructions    string      `json:"instructions"`
	ModelConfigID   string      `json:"model_config_id"`
	Skills          []run.Skill `json:"skills"`
	Tools           []string    `json:"tools"`
	Limits          Limits      `json:"limits"`
	Compression     string      `json:"compression"`
	MaxInputTokens  int         `json:"max_input_tokens"`
	OutputTokens    int         `json:"reserved_output_tokens"`
	Debug           bool        `json:"debug"`
	Platform        string      `json:"platform"`
	PlatformVersion string      `json:"platform_version"`
}

type Limits struct {
	MaxSteps       int `json:"max_steps"`
	TimeoutSeconds int `json:"timeout_seconds"`
}

func FixedDefinition(d run.Definition) Definition {
	tools := make([]string, 0, len(d.Tools))
	for _, t := range d.Tools {
		tools = append(tools, t.Name)
	}
	return Definition{Instructions: d.Instructions, ModelConfigID: d.Model.ID, Skills: d.Skills,
		Tools: tools, Limits: Limits{MaxSteps: d.MaxSteps, TimeoutSeconds: d.TimeoutSeconds},
		Compression: d.Compression, MaxInputTokens: d.MaxInputTokens, OutputTokens: d.OutputTokens,
		Debug: d.Debug, Platform: d.Platform, PlatformVersion: d.PlatformVersion}
}

func (d Definition) Runtime(agent, version string) run.Definition {
	tools := make([]run.ToolSpec, 0, len(d.Tools))
	for _, name := range d.Tools {
		tools = append(tools, run.ToolSpec{Name: name, Version: "1"})
	}
	return run.Definition{AgentID: agent, VersionID: version, Model: run.ModelConfig{ID: d.ModelConfigID},
		Instructions: d.Instructions, Skills: d.Skills, Tools: tools, MaxSteps: d.Limits.MaxSteps,
		TimeoutSeconds: d.Limits.TimeoutSeconds, Compression: d.Compression, MaxInputTokens: d.MaxInputTokens,
		OutputTokens: d.OutputTokens, Debug: d.Debug, Platform: d.Platform, PlatformVersion: d.PlatformVersion}
}

type Message struct {
	Text        string           `json:"text"`
	Attachments []run.Attachment `json:"attachments"`
}

type Attachment struct {
	Header
	Value run.Attachment `json:"attachment"`
}

type Restored struct {
	Header
	EventID    string    `json:"event_id"`
	OccurredAt time.Time `json:"occurred_at"`
	SnapshotID string    `json:"snapshot_id"`
	Reason     string    `json:"reason"`
}

type Accepted struct {
	Header
	AcceptedAt     time.Time `json:"accepted_at"`
	RequestKey     string    `json:"request_key"`
	RequestDigest  string    `json:"request_digest"`
	ActorID        string    `json:"actor_id"`
	MessageID      string    `json:"message_id"`
	UserEventID    string    `json:"user_event_id"`
	EventID        string    `json:"event_id"`
	Execution      Execution `json:"execution"`
	Message        Message   `json:"message"`
	BaseSnapshotID *string   `json:"base_snapshot_id"`

	WorkspaceCleanup bool `json:"workspace_cleanup,omitempty"`
}

type Execution struct {
	Build         string          `json:"build"`
	Model         run.ModelConfig `json:"model"`
	Tools         []run.ToolSpec  `json:"tools"`
	ToolPolicy    run.ToolPolicy  `json:"tool_policy"`
	ModelAttempts int             `json:"model_attempts"`
}

type Step struct {
	Header
	Step              int           `json:"step"`
	AttemptID         string        `json:"attempt_id"`
	RetryOf           string        `json:"retry_of,omitempty"`
	Purpose           string        `json:"purpose"`
	StartedAt         time.Time     `json:"started_at"`
	StartID           string        `json:"start_event_id"`
	EventID           string        `json:"event_id"`
	MessageID         string        `json:"message_id"`
	ProjectionEventID string        `json:"projection_event_id"`
	Component         string        `json:"component,omitempty"`
	Context           *run.Prepared `json:"context,omitempty"`
	Model             trace.Model   `json:"model"`
	Tools             []Tool        `json:"tools"`
}

type Tool struct {
	Role         string          `json:"role"`
	ToolCallID   string          `json:"tool_call_id"`
	CallID       string          `json:"call_id"`
	Name         string          `json:"name"`
	Status       string          `json:"status"`
	DurationMS   *int64          `json:"duration_ms"`
	Result       json.RawMessage `json:"result,omitempty"`
	Artifact     *run.Artifact   `json:"artifact,omitempty"`
	ArtifactSize int64           `json:"artifact_size,omitempty"`
}

type Summary struct {
	Header
	ModelSeq       int64              `json:"model_seq"`
	CoveredThrough int64              `json:"covered_through_seq"`
	EventID        string             `json:"event_id"`
	ContentID      string             `json:"content_id"`
	Metadata       run.ContextSummary `json:"metadata"`
}

type WorkspaceSnapshot struct {
	ID            string           `json:"id"`
	ArchiveSHA256 string           `json:"archive_sha256"`
	ArchiveSize   int64            `json:"archive_size"`
	Manifest      storage.Manifest `json:"manifest"`
}

type Finished struct {
	Header
	Status            string             `json:"status"`
	EventID           string             `json:"event_id"`
	StartedAt         *time.Time         `json:"started_at"`
	FinishedAt        time.Time          `json:"finished_at"`
	DurationMS        *int64             `json:"duration_ms"`
	Error             *string            `json:"error"`
	Failure           *run.FailureDetail `json:"failure_detail,omitempty"`
	SnapshotError     string             `json:"snapshot_error,omitempty"`
	FinalModelSeq     *int64             `json:"final_model_seq"`
	WorkspaceSnapshot *WorkspaceSnapshot `json:"workspace_snapshot"`
}

func (r Header) valid() bool {
	var role string
	switch r.Type {
	case SessionCreated, RunFinished, AttachmentAdded, WorkspaceRestored:
		role = "system"
	case RunAccepted:
		role = "user"
	case ModelStep, ContextSummary:
		role = "assistant"
	default:
		return false
	}
	switch r.Type {
	case SessionCreated, AttachmentAdded:
		return r.Role == role && r.RunID == ""
	case WorkspaceRestored:
		return r.Role == role && (r.RunID == "" || storage.ValidShortID(r.RunID))
	default:
		return r.Role == role && storage.ValidShortID(r.RunID)
	}
}
