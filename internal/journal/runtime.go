package journal

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
)

type UserMessageData struct {
	ID          string           `json:"id"`
	ContentID   string           `json:"content_id"`
	Attachments []run.Attachment `json:"attachments,omitempty"`
}
type RunData struct {
	Snapshot *run.Snapshot `json:"snapshot,omitempty"`
}
type StartData struct {
	OwnerID string `json:"owner_id,omitempty"`
}
type SummaryData struct {
	ContentID       string `json:"content_id"`
	ResponseEventID string `json:"response_event_id"`
	ProjectionID    string `json:"projection_id"`
	Through         int64  `json:"coverage_through,string"`
	Policy          string `json:"policy"`
}
type ProjectionData struct {
	Purpose            string `json:"purpose,omitempty"`
	ParentID           string `json:"parent_projection_id,omitempty"`
	PrefixChangeReason string `json:"prefix_change_reason,omitempty"`

	ID          string `json:"id"`
	ContentID   string `json:"content_id"`
	Fingerprint string `json:"fingerprint"`
	Through     int64  `json:"through_sequence,string"`
	Counter     string `json:"counter"`
	Epoch       int    `json:"epoch"`
}
type AttemptData struct {
	RetryOf            string `json:"retry_of,omitempty"`
	ID                 string `json:"id"`
	ProjectionEventID  string `json:"projection_event_id"`
	MessageID          string `json:"message_id"`
	Purpose            string `json:"purpose"`
	Source             string `json:"source"`
	CredentialRevision int64  `json:"credential_revision"`
}
type WireData struct {
	AttemptID string `json:"attempt_id"`
	Ordinal   int    `json:"ordinal"`
	Status    int    `json:"http_status"`
	ContentID string `json:"content_id"`
}
type DeltaData struct {
	AttemptID string `json:"attempt_id"`
	MessageID string `json:"message_id"`
	Text      string `json:"text"`
}
type ResponseData struct {
	Observation  *run.ModelObservation `json:"observation,omitempty"`
	AttemptID    string                `json:"attempt_id"`
	MessageID    string                `json:"message_id"`
	ContentID    string                `json:"content_id"`
	FinishReason string                `json:"finish_reason"`
	Usage        run.Usage             `json:"usage"`
	Calls        []run.Call            `json:"calls"`
	Preview      string                `json:"preview"`
}
type AttemptFailedData struct {
	Observation *run.ModelObservation `json:"observation,omitempty"`
	AttemptID   string                `json:"attempt_id"`
	MessageID   string                `json:"message_id"`
	Error       string                `json:"error"`
	Usage       run.Usage             `json:"usage"`
}
type ToolData struct {
	CallID          string `json:"call_id"`
	ResponseEventID string `json:"response_event_id"`
	Name            string `json:"name"`
	Version         string `json:"version"`
	ArgumentsSHA256 string `json:"arguments_sha256"`
	ContentID       string `json:"content_id,omitempty"`
	Status          string `json:"status,omitempty"`
	SideEffect      string `json:"side_effect,omitempty"`
	Preview         string `json:"preview,omitempty"`
}

func New(actor, kind, runID string, data any, refs ...storage.Ref) Proposed {
	raw, _ := json.Marshal(data)
	return Proposed{EventID: uuid.NewString(), RunID: runID, Type: kind, SchemaVersion: 1, Actor: Actor{Kind: "user", ID: actor}, OccurredAt: time.Now().UTC(), Data: raw, Refs: refs}
}
func validateRuntime(e Proposed) (bool, error) {
	if known, err := validateTools(e); known {
		return true, err
	}
	bad := func() (bool, error) { return true, storage.ErrSchema }
	requiredRun := true
	switch e.Type {
	case "user.message.accepted":
		var d UserMessageData
		if Decode(e.Data, &d) != nil || !storage.ValidID(d.ID) || !storage.ValidID(d.ContentID) || e.RunID != "" {
			return bad()
		}
		if len(d.Attachments) > 16 {
			return bad()
		}
		seen := map[string]bool{}
		for _, a := range d.Attachments {
			if !storage.ValidID(a.ID) || !storage.ValidID(a.Ref.ObjectID) || !hashPattern.MatchString(a.Ref.SHA256) || seen[a.ID] {
				return bad()
			}
			seen[a.ID] = true
		}
		requiredRun = false
	case "run.cancel.requested":
		if Decode(e.Data, &struct{}{}) != nil {
			return bad()
		}
	case "context.summary.created":
		var d SummaryData
		if Decode(e.Data, &d) != nil || !storage.ValidID(d.ContentID) || !storage.ValidID(d.ResponseEventID) || !storage.ValidID(d.ProjectionID) || d.Through < 1 || d.Policy == "" {
			return bad()
		}
	case "context.projected":
		var d ProjectionData
		if Decode(e.Data, &d) != nil || !storage.ValidID(d.ID) || !storage.ValidID(d.ContentID) || !hashPattern.MatchString(d.Fingerprint) || d.Through < 1 || d.Counter == "" || d.Epoch < 1 || (d.Purpose != "" && d.Purpose != "task" && d.Purpose != "summary") || (d.ParentID != "" && !storage.ValidID(d.ParentID)) {
			return bad()
		}
	case "model.attempt.started":
		var d AttemptData
		if Decode(e.Data, &d) != nil || (d.RetryOf != "" && !storage.ValidID(d.RetryOf)) || !storage.ValidID(d.ID) || !storage.ValidID(d.ProjectionEventID) || !storage.ValidID(d.MessageID) || (d.Purpose != "task" && d.Purpose != "summary") || (d.Source != "ark" && d.Source != "fake") || d.CredentialRevision < 0 {
			return bad()
		}
	case "model.request.recorded", "model.response.recorded":
		var d WireData
		if Decode(e.Data, &d) != nil || !storage.ValidID(d.AttemptID) || !storage.ValidID(d.ContentID) || d.Ordinal < 0 || d.Status < 0 || d.Status > 599 {
			return bad()
		}
	case "model.response.delta":
		var d DeltaData
		if Decode(e.Data, &d) != nil || !storage.ValidID(d.AttemptID) || !storage.ValidID(d.MessageID) || len(d.Text) == 0 || len(d.Text) > 8192 {
			return bad()
		}
	case "model.response.completed":
		var d ResponseData
		if Decode(e.Data, &d) != nil || !storage.ValidID(d.AttemptID) || !storage.ValidID(d.MessageID) || !storage.ValidID(d.ContentID) || (d.FinishReason != "stop" && d.FinishReason != "tool_calls") || len(d.Calls) > 16 || len(d.Preview) > 8192 {
			return bad()
		}
		if (d.FinishReason == "tool_calls") != (len(d.Calls) > 0) {
			return bad()
		}
		seen := map[string]bool{}
		for _, c := range d.Calls {
			if !keyPattern.MatchString(c.ID) || seen[c.ID] || !rolePattern.MatchString(c.Name) || len(c.Arguments) > 2<<20 {
				return bad()
			}
			if c.Arguments == "" {
				if !hashPattern.MatchString(c.ArgumentsSHA256) {
					return bad()
				}
			} else if !json.Valid([]byte(c.Arguments)) || c.ArgumentsSHA256 != "" && c.ArgumentsSHA256 != Digest(c.Arguments) {
				return bad()
			}
			seen[c.ID] = true
		}
		if !validUsage(d.Usage) || !validObservation(d.Observation) {
			return bad()
		}
	case "model.attempt.failed":
		var d AttemptFailedData
		if Decode(e.Data, &d) != nil || !storage.ValidID(d.AttemptID) || !storage.ValidID(d.MessageID) || d.Error == "" || len(d.Error) > 64 || !validUsage(d.Usage) || !validObservation(d.Observation) {
			return bad()
		}
	case "tool.execution.started", "tool.execution.completed":
		var d ToolData
		if Decode(e.Data, &d) != nil || !keyPattern.MatchString(d.CallID) || !storage.ValidID(d.ResponseEventID) || !rolePattern.MatchString(d.Name) || d.Version != "1" || !hashPattern.MatchString(d.ArgumentsSHA256) {
			return bad()
		}
		if e.Type == "tool.execution.completed" && (!storage.ValidID(d.ContentID) || (d.Status != "completed" && d.Status != "failed") || (d.SideEffect != "none" && d.SideEffect != "committed" && d.SideEffect != "unknown") || len(d.Preview) > 8192) {
			return bad()
		}
	case "artifact.published":
		var d run.Artifact
		if Decode(e.Data, &d) != nil || !storage.ValidID(d.ID) || !storage.ValidID(d.ObjectID) || d.RunID != e.RunID || !keyPattern.MatchString(d.CallID) || !hashPattern.MatchString(d.SHA256) || len(d.Name) == 0 || len(d.Name) > 200 || len(d.Path) > 1024 {
			return bad()
		}
	default:
		return false, nil
	}
	if requiredRun && e.RunID == "" {
		return bad()
	}
	return true, nil
}
func validUsage(u run.Usage) bool {
	for _, n := range []*int64{u.Input, u.Output, u.CacheRead, u.CacheWrite} {
		if n != nil && (*n < 0 || *n > 1000000000) {
			return false
		}
	}
	return true
}

func validObservation(o *run.ModelObservation) bool {
	if o == nil {
		return true
	}
	return o.DurationMS >= 0 && o.HTTPStatus >= 0 && o.HTTPStatus <= 599 && len(o.Component) > 0 && len(o.Component) <= 64 && (o.FirstTokenMS == nil || *o.FirstTokenMS >= 0 && *o.FirstTokenMS <= o.DurationMS)
}
