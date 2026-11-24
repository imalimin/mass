package run

import (
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
	Package   bool   `json:"package,omitempty"`
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
	MaxCodeBytes        int    `json:"max_code_bytes,omitempty"`
	TimeoutMS           int64  `json:"timeout_ms"`
	MaxInputBytes       int64  `json:"max_input_bytes,omitempty"`
	MaxOutputBytes      int64  `json:"max_output_bytes,omitempty"`
	MaxFileOperations   int    `json:"max_file_operations,omitempty"`
	MaxNetworkRequests  int    `json:"max_network_requests,omitempty"`
	MaxNetworkBodyBytes int64  `json:"max_network_body_bytes,omitempty"`
	MaxLogBytes         int    `json:"max_log_bytes"`
}

const CommandTool = "run_command"

// NetworkFailure retains historical code-tool network failure fields when replaying archived tool results.
type NetworkFailure struct {
	RequestNo  int    `json:"request_no"`
	Method     string `json:"method"`
	Host       string `json:"host"`
	Phase      string `json:"phase"`
	Code       string `json:"code"`
	Cause      string `json:"cause"`
	SideEffect string `json:"side_effect"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

type ToolError struct {
	Code   string
	Effect string
}

func (e *ToolError) Error() string          { return e.Code }
func ToolFailure(code, effect string) error { return &ToolError{Code: code, Effect: effect} }
