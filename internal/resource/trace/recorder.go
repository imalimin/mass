package trace

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/imalimin/mass/internal/domain/run"
)

type Usage struct {
	InputTokens       int64 `json:"input_tokens"`
	InputCachedTokens int64 `json:"input_cached_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
	ReasoningTokens   int64 `json:"reasoning_tokens"`
}

type Model struct {
	ID           string               `json:"id"`
	Status       string               `json:"status"`
	HTTPStatus   int                  `json:"http_status"`
	DurationMS   int64                `json:"duration_ms"`
	FirstTextMS  *int64               `json:"first_text_ms"`
	FinishReason string               `json:"finish_reason"`
	Text         string               `json:"text"`
	Reasoning    *string              `json:"reasoning"`
	ToolCalls    []run.NativeToolCall `json:"tool_calls"`
	Usage        *Usage               `json:"usage"`
	Error        string               `json:"error"`
}

type Tool struct {
	CallID       string          `json:"call_id"`
	NativeCallID string          `json:"native_tool_call_id"`
	Name         string          `json:"name"`
	Arguments    json.RawMessage `json:"arguments"`
	Status       string          `json:"status"`
	DurationMS   *int64          `json:"duration_ms"`
	Result       json.RawMessage `json:"result,omitempty"`
	ResultRef    string          `json:"result_ref,omitempty"`
}

type ModelStep struct {
	Type      string    `json:"type"`
	RunID     string    `json:"run_id"`
	Step      int       `json:"step"`
	AttemptID string    `json:"attempt_id"`
	RetryOf   string    `json:"retry_of,omitempty"`
	Purpose   string    `json:"purpose"`
	StartedAt time.Time `json:"started_at"`
	Model     Model     `json:"model"`
	Tools     []Tool    `json:"tools"`
}

type RunFinished struct {
	Type       string             `json:"type"`
	RunID      string             `json:"run_id"`
	Status     string             `json:"status"`
	StartedAt  *time.Time         `json:"started_at"`
	FinishedAt time.Time          `json:"finished_at"`
	DurationMS *int64             `json:"duration_ms"`
	Error      string             `json:"error"`
	Failure    *run.FailureDetail `json:"failure_detail,omitempty"`
	SnapshotID *string            `json:"snapshot_id"`
}

type Recorder struct {
	mu   sync.Mutex
	runs map[string][]*ModelStep
}

func (r *Recorder) Start(s run.Snapshot, attempt, previous, purpose string, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runs == nil {
		r.runs = make(map[string][]*ModelStep)
	}
	steps := r.runs[s.ID]
	step := 1
	if len(steps) > 0 {
		step = steps[len(steps)-1].Step
		if previous == "" {
			step++
		}
	}
	r.runs[s.ID] = append(steps, &ModelStep{
		Type: "model_step", RunID: s.ID, Step: step, AttemptID: attempt, RetryOf: previous, Purpose: purpose, StartedAt: at,
		Model: Model{ID: s.Definition.Model.ModelID, Status: "running", ToolCalls: []run.NativeToolCall{}}, Tools: []Tool{},
	})
}

func (r *Recorder) ModelDone(id string, reply run.Reply, status, code string, elapsed time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	steps := r.runs[id]
	step := steps[len(steps)-1]
	completeModel(step, reply, status, code, elapsed)
	if status == "succeeded" {
		for _, call := range reply.Message.ToolCalls {
			step.Tools = append(step.Tools, Tool{CallID: call.ID, NativeCallID: call.NativeID, Name: call.Name, Arguments: json.RawMessage(call.Arguments), Status: "not_started"})
		}
	}
}

func completeModel(step *ModelStep, reply run.Reply, status, code string, elapsed time.Duration) {
	m := &step.Model
	m.Status, m.Error, m.DurationMS = status, code, elapsed.Milliseconds()
	m.Text, m.Reasoning, m.FinishReason = reply.Message.Content, reply.Trace.Reasoning, reply.FinishReason
	if reply.Observation != nil {
		m.DurationMS, m.FirstTextMS, m.HTTPStatus = reply.Observation.DurationMS, reply.Observation.FirstTokenMS, reply.Observation.HTTPStatus
	}
	m.ToolCalls = reply.Trace.ToolCalls
	if m.ToolCalls == nil {
		m.ToolCalls = []run.NativeToolCall{}
		for _, call := range reply.Message.ToolCalls {
			id := call.NativeID
			if id == "" {
				id = call.ID
			}
			m.ToolCalls = append(m.ToolCalls, run.NativeToolCall{ID: id, Type: "function", Function: run.NativeFunctionCall{Name: call.Name, Arguments: call.Arguments}})
		}
	}
	u := reply.Usage
	if reply.Trace.UsagePresent || u.Input != nil || u.Output != nil || u.CacheRead != nil || u.CacheWrite != nil || reply.Trace.ReasoningTokens != nil {
		m.Usage = &Usage{value(u.Input), value(u.CacheRead), value(u.Output), value(reply.Trace.ReasoningTokens)}
	}
}

func value(n *int64) int64 {
	if n == nil {
		return 0
	}
	return *n
}

func (r *Recorder) Tool(id, callID, status string, elapsed *int64, result json.RawMessage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	steps := r.runs[id]
	for i := len(steps) - 1; i >= 0; i-- {
		for j := range steps[i].Tools {
			tool := &steps[i].Tools[j]
			if tool.CallID == callID {
				tool.Status, tool.DurationMS, tool.Result = status, elapsed, result
				return
			}
		}
	}
}

// ToolResult returns the validated result bytes shared with the next model call.
func (r *Recorder) ToolResult(runID, callID string) json.RawMessage {
	return r.ToolState(runID, callID).Result
}

func (r *Recorder) ToolState(runID, callID string) Tool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, step := range r.runs[runID] {
		for _, tool := range step.Tools {
			if tool.CallID == callID {
				return tool
			}
		}
	}
	return Tool{}
}

func (r *Recorder) Steps(id string) []ModelStep {
	r.mu.Lock()
	defer r.mu.Unlock()
	steps := make([]ModelStep, 0, len(r.runs[id]))
	for _, step := range r.runs[id] {
		copy := *step
		copy.Tools = append([]Tool{}, step.Tools...)
		steps = append(steps, copy)
	}
	return steps
}

func (r *Recorder) Forget(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.runs, id)
}
