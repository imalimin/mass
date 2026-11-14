package run

// ModelFailure contains only bounded public diagnostics, never provider text or
// authorization headers. Retrying is an explicit platform policy decision.
type ModelFailure struct {
	Code      string
	Status    int
	Retryable bool
}

func (e *ModelFailure) Error() string { return e.Code }

type ModelObservation struct {
	DurationMS   int64  `json:"duration_ms"`
	FirstTokenMS *int64 `json:"first_token_ms"`
	HTTPStatus   int    `json:"http_status"`
	Component    string `json:"component"`
}

// ModelTrace is the merged provider output retained for diagnostics during a run.
type ModelTrace struct {
	Reasoning       *string
	ToolCalls       []NativeToolCall
	UsagePresent    bool
	ReasoningTokens *int64
}

type NativeToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function NativeFunctionCall `json:"function"`
}

type NativeFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}
