// Package journal owns immutable event contracts and the single append entrance.
package journal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/imalimin/mass/internal/domain/storage"
)

type Actor struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}
type Proposed struct {
	EventID       string          `json:"event_id"`
	RunID         string          `json:"run_id,omitempty"`
	Type          string          `json:"event_type"`
	SchemaVersion int             `json:"schema_version"`
	OperationID   string          `json:"operation_id,omitempty"`
	CausationID   string          `json:"causation_event_id,omitempty"`
	CorrelationID string          `json:"correlation_id,omitempty"`
	Actor         Actor           `json:"actor"`
	OccurredAt    time.Time       `json:"occurred_at"`
	Data          json.RawMessage `json:"data"`
	Refs          []storage.Ref   `json:"content_refs"`
}
type Event struct {
	Proposed
	SpaceID    string    `json:"space_id"`
	SessionID  string    `json:"session_id"`
	Sequence   int64     `json:"sequence,string"`
	RecordedAt time.Time `json:"recorded_at"`
}
type Request struct {
	SpaceID, SessionID, AppendKey string
	ExpectedSequence              *int64
	Events                        []Proposed
}
type Batch struct {
	Request
	Digests []string
	Digest  string
}
type Page struct {
	Events  []Event `json:"events"`
	Through int64   `json:"through_sequence,string"`
	Next    int64   `json:"next_after,string"`
	HasMore bool    `json:"has_more"`
}
type Repository interface {
	AppendEvents(context.Context, string, Batch) ([]Event, error)
	ReadEvents(context.Context, string, string, string, int64, *int64, int) (Page, error)
}
type Journal struct{ Repo Repository }

// RunReader is an optional indexed read capability. It retains Session sequence
// numbers; omitted records from other runs are not gaps in this filtered view.
type RunReader interface {
	ReadRunEvents(context.Context, string, string, string, string, int64, *int64, int) (Page, error)
}

func (j *Journal) ReadRun(ctx context.Context, actor, space, session, runID string, after int64, through *int64, limit int) (Page, error) {
	if !storage.ValidID(runID) || !storage.ValidID(space) || !storage.ValidID(session) || after < 0 || limit < 1 || limit > 100 || through != nil && *through < after {
		return Page{}, storage.ErrInvalid
	}
	var page Page
	var err error
	if reader, ok := j.Repo.(RunReader); ok {
		page, err = reader.ReadRunEvents(ctx, actor, space, session, runID, after, through, limit)
	} else {
		page, err = j.Read(ctx, actor, space, session, after, through, limit)
		items := []Event{}
		for _, e := range page.Events {
			if e.RunID == runID {
				items = append(items, e)
			}
		}
		page.Events = items
	}
	if err != nil {
		return Page{}, err
	}
	for _, e := range page.Events {
		if err = Validate(e.Proposed); err != nil {
			return Page{}, err
		}
	}
	return page, nil
}

type SessionData struct {
	SourceSnapshotID string       `json:"source_snapshot_id,omitempty"`
	DefinitionRef    *storage.Ref `json:"definition_ref,omitempty"`
	DefinitionID     string       `json:"definition_id,omitempty"`
	VersionID        string       `json:"version_id,omitempty"`
	Title            string       `json:"title,omitempty"`
}
type FinishData struct {
	Status               string `json:"status"`
	Error                string `json:"error,omitempty"`
	FinalResponseEventID string `json:"final_response_event_id,omitempty"`
	SnapshotError        string `json:"snapshot_error,omitempty"`
}
type RestoreData struct {
	SnapshotID string `json:"snapshot_id,omitempty"`
	Reason     string `json:"reason"`
}
type FailureData struct {
	Reason string `json:"reason"`
}
type ReconcileData struct {
	Outcome string `json:"outcome"`
}

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
var rolePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func Decode(data []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return storage.ErrSchema
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return storage.ErrSchema
	}
	return nil
}
func Validate(e Proposed) error {
	if !storage.ValidID(e.EventID) || (e.RunID != "" && !storage.ValidID(e.RunID)) || (e.CausationID != "" && !storage.ValidID(e.CausationID)) || e.SchemaVersion != 1 || (e.Actor.Kind != "user" && e.Actor.Kind != "system") || !storage.ValidID(e.Actor.ID) || e.OccurredAt.IsZero() || e.OccurredAt.Year() < 1000 || e.OccurredAt.Year() > 9999 {
		return storage.ErrSchema
	}
	for _, id := range []string{e.OperationID, e.CorrelationID} {
		if id != "" && !keyPattern.MatchString(id) {
			return storage.ErrSchema
		}
	}
	if len(e.Data) > 65536 || len(e.Data) == 0 || e.Data[0] != '{' || len(e.Refs) > 100 {
		return storage.ErrSchema
	}
	var err error
	switch e.Type {
	case "session.created":
		var d SessionData
		err = Decode(e.Data, &d)
		if d.SourceSnapshotID != "" && !storage.ValidID(d.SourceSnapshotID) {
			return storage.ErrSchema
		}
		if e.RunID != "" || len(d.Title) > 200 {
			return storage.ErrSchema
		}
		if d.DefinitionRef != nil && (!storage.ValidID(d.DefinitionID) || !storage.ValidID(d.VersionID) || !storage.ValidID(d.DefinitionRef.ObjectID) || !hashPattern.MatchString(d.DefinitionRef.SHA256)) {
			return storage.ErrSchema
		}
	case "run.created":
		var d RunData
		err = Decode(e.Data, &d)
		if d.Snapshot != nil && (d.Snapshot.ID != e.RunID || !storage.ValidID(d.Snapshot.UserEventID) || d.Snapshot.Profile != "mass-agui-v1" || d.Snapshot.MapperVersion != 1 || d.Snapshot.ParserVersion != 1) {
			return storage.ErrSchema
		}
		if e.RunID == "" {
			return storage.ErrSchema
		}
	case "run.started":
		var d StartData
		err = Decode(e.Data, &d)
		if d.OwnerID != "" && !storage.ValidID(d.OwnerID) {
			return storage.ErrSchema
		}
		if e.RunID == "" {
			return storage.ErrSchema
		}
	case "run.finished":
		var d FinishData
		err = Decode(e.Data, &d)
		if e.RunID == "" || (d.Status != "succeeded" && d.Status != "failed" && d.Status != "cancelled") || len(d.Error) > 64 || len(d.SnapshotError) > 64 || (d.FinalResponseEventID != "" && !storage.ValidID(d.FinalResponseEventID)) {
			return storage.ErrSchema
		}
	case "workspace.snapshot.created":
		var d storage.Snapshot
		err = Decode(e.Data, &d)
		if !storage.ValidID(d.ID) || !storage.ValidID(d.ObjectID) || !storage.ValidID(d.ManifestRef) || d.SourceRunID != e.RunID || e.RunID == "" || d.FormatVersion != 1 || !hashPattern.MatchString(d.SHA256) {
			return storage.ErrSchema
		}
	case "workspace.snapshot.failed":
		var d FailureData
		err = Decode(e.Data, &d)
		if e.RunID == "" || d.Reason == "" || len(d.Reason) > 64 {
			return storage.ErrSchema
		}
	case "workspace.restored":
		var d RestoreData
		err = Decode(e.Data, &d)
		if d.Reason == "" || len(d.Reason) > 64 || (d.SnapshotID != "" && !storage.ValidID(d.SnapshotID)) {
			return storage.ErrSchema
		}
	case "operation.reconciled":
		var d ReconcileData
		err = Decode(e.Data, &d)
		if e.RunID == "" || e.OperationID == "" || (d.Outcome != "unknown" && d.Outcome != "completed" && d.Outcome != "failed") {
			return storage.ErrSchema
		}
	default:
		known, e2 := validateRuntime(e)
		if !known {
			return storage.ErrSchema
		}
		err = e2
	}
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, r := range e.Refs {
		key := r.Role + ":" + strconv.Itoa(r.Ordinal)
		if !rolePattern.MatchString(r.Role) || r.Ordinal < 0 || r.Ordinal > 10000 || !storage.ValidID(r.ObjectID) || !hashPattern.MatchString(r.SHA256) || seen[key] {
			return storage.ErrSchema
		}
		seen[key] = true
	}
	return nil
}
func Digest(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func Prepare(actor string, r Request) (Batch, error) {
	if !storage.ValidID(r.SpaceID) || !storage.ValidID(r.SessionID) || !keyPattern.MatchString(r.AppendKey) || len(r.Events) == 0 || len(r.Events) > 100 || (r.ExpectedSequence != nil && *r.ExpectedSequence < 0) {
		return Batch{}, storage.ErrInvalid
	}
	// Canonicalize without float64 conversion: equivalent key order is a retry,
	// while integer precision and original object bytes remain intact.
	batch := Batch{Request: r}
	batch.Events = append([]Proposed(nil), r.Events...)
	seen := map[string]bool{}
	for i, e := range batch.Events {
		if err := Validate(e); err != nil {
			return Batch{}, err
		}
		if e.Actor.ID != actor || seen[e.EventID] {
			return Batch{}, storage.ErrInvalid
		}
		seen[e.EventID] = true
		var data any
		d := json.NewDecoder(bytes.NewReader(e.Data))
		d.UseNumber()
		if err := d.Decode(&data); err != nil {
			return Batch{}, storage.ErrSchema
		}
		e.Data, _ = json.Marshal(data)
		e.OccurredAt = e.OccurredAt.UTC().Truncate(time.Microsecond)
		if e.Refs == nil {
			e.Refs = []storage.Ref{}
		}
		e.Refs = append([]storage.Ref{}, e.Refs...)
		sort.Slice(e.Refs, func(i, j int) bool {
			if e.Refs[i].Role != e.Refs[j].Role {
				return e.Refs[i].Role < e.Refs[j].Role
			}
			return e.Refs[i].Ordinal < e.Refs[j].Ordinal
		})
		batch.Events[i] = e
		batch.Digests = append(batch.Digests, Digest(e))
	}
	batch.Digest = Digest(batch.Digests)
	return batch, nil
}
func (j *Journal) Append(ctx context.Context, actor string, r Request) ([]Event, error) {
	b, err := Prepare(actor, r)
	if err != nil {
		return nil, err
	}
	return j.Repo.AppendEvents(ctx, actor, b)
}
func (j *Journal) Read(ctx context.Context, actor, space, session string, after int64, through *int64, limit int) (Page, error) {
	if !storage.ValidID(space) || !storage.ValidID(session) || after < 0 || limit < 1 || limit > 100 || (through != nil && (*through < after || *through < 0)) {
		return Page{}, storage.ErrInvalid
	}
	page, err := j.Repo.ReadEvents(ctx, actor, space, session, after, through, limit)
	if err != nil {
		return page, err
	}
	for _, e := range page.Events {
		if err := Validate(e.Proposed); err != nil {
			return Page{}, err
		}
	}
	return page, nil
}
