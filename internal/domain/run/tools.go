package run

import (
	"encoding/json"
	"github.com/imalimin/mass/internal/domain/storage"
)

type Range struct {
	Offset    *int64 `json:"offset,omitempty"`
	Length    *int64 `json:"length,omitempty"`
	StartLine *int   `json:"start_line,omitempty"`
	EndLine   *int   `json:"end_line,omitempty"`
}
type SkillResource struct {
	Path      string `json:"path"`
	ObjectID  string `json:"object_id"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	MediaType string `json:"media_type"`
}
type Attachment struct {
	ID        string      `json:"id"`
	SessionID string      `json:"session_id"`
	Name      string      `json:"name"`
	MediaType string      `json:"media_type"`
	Size      int64       `json:"size"`
	Ref       storage.Ref `json:"ref"`
}
type PlanStep struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}
type Plan struct {
	Revision int64      `json:"revision"`
	Steps    []PlanStep `json:"steps"`
}
type ToolPolicy struct {
	Version             string `json:"version"`
	MaxCodeBytes        int    `json:"max_code_bytes"`
	TimeoutMS           int64  `json:"timeout_ms"`
	MaxInputBytes       int64  `json:"max_input_bytes"`
	MaxOutputBytes      int64  `json:"max_output_bytes"`
	MaxFileOperations   int    `json:"max_file_operations"`
	MaxNetworkRequests  int    `json:"max_network_requests"`
	MaxNetworkBodyBytes int64  `json:"max_network_body_bytes"`
	MaxLogBytes         int    `json:"max_log_bytes"`
}
type CodeInput struct {
	Language string          `json:"language"`
	Code     string          `json:"code"`
	Input    json.RawMessage `json:"input"`
}
type CodeHost interface {
	Invoke(name string, arguments json.RawMessage) (json.RawMessage, error)
}
type CodeOutput struct {
	Value        json.RawMessage   `json:"value"`
	Logs         []json.RawMessage `json:"logs"`
	Detail       string            `json:"error_detail,omitempty"`
	Error        string            `json:"error,omitempty"`
	LogsComplete bool              `json:"logs_complete"`
}
type ToolError struct {
	Code   string
	Effect string
}

func (e *ToolError) Error() string          { return e.Code }
func ToolFailure(code, effect string) error { return &ToolError{Code: code, Effect: effect} }
