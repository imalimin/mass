package mariadb

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
)

func hasRef(e journal.Proposed, role, id string) bool {
	for _, r := range e.Refs {
		if r.Role == role && r.ObjectID == id {
			return true
		}
	}
	return false
}
func attemptCheck(ctx context.Context, tx *sql.Tx, space, runID, id string) error {
	var status string
	if err := tx.QueryRowContext(ctx, "SELECT status FROM model_attempts WHERE space_id=? AND run_id=? AND id=?", space, runID, id).Scan(&status); err != nil {
		return err
	}
	if status != "started" {
		return run.ErrState
	}
	return nil
}
func reduceRuntime(ctx context.Context, tx *sql.Tx, space, session string, e journal.Proposed, seq int64) error {
	if strings.HasPrefix(e.Type, "model.") || strings.HasPrefix(e.Type, "context.") || e.Type == "artifact.published" || e.Type == "plan.updated" {
		var status string
		var cancelled bool
		if err := tx.QueryRowContext(ctx, "SELECT status,cancel_requested FROM runs WHERE space_id=? AND id=?", space, e.RunID).Scan(&status, &cancelled); err != nil {
			return err
		}
		if status != "running" {
			return run.ErrState
		}
		if cancelled && (e.Type == "model.attempt.started" || e.Type == "context.projected") {
			return run.ErrCancelled
		}
	}
	switch e.Type {
	case "plan.updated":
		var p run.Plan
		json.Unmarshal(e.Data, &p)
		var name string
		if err := tx.QueryRowContext(ctx, "SELECT name FROM model_calls WHERE space_id=? AND run_id=? AND call_id=?", space, e.RunID, e.OperationID).Scan(&name); err != nil {
			return err
		}
		if name != "update_plan" {
			return run.ErrState
		}
		var current int64
		err := tx.QueryRowContext(ctx, "SELECT revision FROM run_plans WHERE space_id=? AND run_id=?", space, e.RunID).Scan(&current)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if current+1 != p.Revision {
			return storage.ErrConflict
		}
		raw, _ := json.Marshal(p)
		_, err = tx.ExecContext(ctx, "INSERT INTO run_plans(space_id,run_id,revision,source_event_id,plan,sequence,occurred_at,call_id) VALUES(?,?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE revision=VALUES(revision),source_event_id=VALUES(source_event_id),plan=VALUES(plan),sequence=VALUES(sequence),occurred_at=VALUES(occurred_at),call_id=VALUES(call_id)", space, e.RunID, p.Revision, e.EventID, raw, seq, e.OccurredAt, e.OperationID)
		return err
	case "user.message.accepted":
		var d journal.UserMessageData
		json.Unmarshal(e.Data, &d)
		if !hasRef(e, "content", d.ContentID) {
			return storage.ErrInvalid
		}
		for _, a := range d.Attachments {
			actual, err := attachmentInTx(ctx, tx, space, session, a.ID)
			if err != nil {
				return err
			}
			if journal.Digest(actual) != journal.Digest(a) || !hasRef(e, "attachment", a.Ref.ObjectID) {
				return storage.ErrInvalid
			}
		}
		attachments, _ := json.Marshal(d.Attachments)
		_, err := tx.ExecContext(ctx, "INSERT INTO messages(space_id,session_id,id,role,content_id,source_event_id,sequence,complete,actor_id,attachments,occurred_at) VALUES(?,?,?,'user',?,?,?,TRUE,?,?,?)", space, session, d.ID, d.ContentID, e.EventID, seq, e.Actor.ID, attachments, e.OccurredAt)
		return err
	case "run.cancel.requested":
		var status string
		var cancel bool
		if err := tx.QueryRowContext(ctx, "SELECT status,cancel_requested FROM runs WHERE space_id=? AND id=?", space, e.RunID).Scan(&status, &cancel); err != nil {
			return err
		}
		if err := run.CheckTransition(status, cancel, e.Type, "", false); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE runs SET cancel_requested=TRUE,cancel_event_id=?,cancel_sequence=?,cancelled_at=?,cancelled_by=? WHERE space_id=? AND id=?", e.EventID, seq, e.OccurredAt, e.Actor.ID, space, e.RunID)
		return err
	case "context.summary.created":
		var d journal.SummaryData
		json.Unmarshal(e.Data, &d)
		if d.Through >= seq || !hasRef(e, "summary", d.ContentID) {
			return storage.ErrInvalid
		}
		var purpose, status, projectionID string
		err := tx.QueryRowContext(ctx, "SELECT a.purpose,a.status,p.id FROM model_attempts a JOIN context_projections p ON p.space_id=a.space_id AND p.event_id=a.projection_event_id WHERE a.space_id=? AND a.run_id=? AND a.response_event_id=?", space, e.RunID, d.ResponseEventID).Scan(&purpose, &status, &projectionID)
		if err != nil {
			return err
		}
		if purpose != "summary" || status != "completed" || projectionID != d.ProjectionID {
			return storage.ErrInvalid
		}
		_, err = tx.ExecContext(ctx, "UPDATE model_attempts SET summary_event_id=?,summary_sequence=?,summary_at=?,summary_ref=?,summary_coverage=?,summary_policy=? WHERE space_id=? AND run_id=? AND response_event_id=?", e.EventID, seq, e.OccurredAt, d.ContentID, d.Through, d.Policy, space, e.RunID, d.ResponseEventID)
		return err
	case "context.projected":
		var d journal.ProjectionData
		json.Unmarshal(e.Data, &d)
		if d.Through >= seq || !hasRef(e, "projection", d.ContentID) {
			return storage.ErrInvalid
		}
		purpose := d.Purpose
		if purpose == "" {
			purpose = "task"
		}
		if d.ParentID != "" {
			var parentPurpose string
			if err := tx.QueryRowContext(ctx, "SELECT purpose FROM context_projections WHERE space_id=? AND session_id=? AND id=? AND through_sequence<?", space, session, d.ParentID, d.Through).Scan(&parentPurpose); err != nil {
				return err
			}
			if parentPurpose != purpose {
				return storage.ErrInvalid
			}
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO context_projections(space_id,session_id,run_id,id,event_id,content_id,through_sequence,purpose,epoch,metadata,sequence,occurred_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)", space, session, e.RunID, d.ID, e.EventID, d.ContentID, d.Through, purpose, d.Epoch, e.Data, seq, e.OccurredAt)
		return err
	case "model.attempt.started":
		var d journal.AttemptData
		json.Unmarshal(e.Data, &d)
		var purpose string
		if err := tx.QueryRowContext(ctx, "SELECT purpose FROM context_projections WHERE space_id=? AND session_id=? AND run_id=? AND event_id=?", space, session, e.RunID, d.ProjectionEventID).Scan(&purpose); err != nil {
			return err
		}
		if purpose != d.Purpose {
			return storage.ErrInvalid
		}

		if d.RetryOf != "" {
			var status, projection, priorPurpose string
			if err := tx.QueryRowContext(ctx, "SELECT status,projection_event_id,purpose FROM model_attempts WHERE space_id=? AND run_id=? AND id=?", space, e.RunID, d.RetryOf).Scan(&status, &projection, &priorPurpose); err != nil {
				return err
			}
			if status != "failed" || projection != d.ProjectionEventID || priorPurpose != d.Purpose {
				return storage.ErrInvalid
			}
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO model_attempts(space_id,session_id,run_id,id,projection_event_id,message_id,purpose,source,status,started_event_id,started_sequence,started_at,retry_of,credential_revision) VALUES(?,?,?,?,?,?,?,?,'started',?,?,?,?,?)", space, session, e.RunID, d.ID, d.ProjectionEventID, d.MessageID, d.Purpose, d.Source, e.EventID, seq, e.OccurredAt, nullable(d.RetryOf), d.CredentialRevision)
		return err
	case "model.response.completed":
		var d journal.ResponseData
		json.Unmarshal(e.Data, &d)
		if err := attemptCheck(ctx, tx, space, e.RunID, d.AttemptID); err != nil {
			return err
		}
		if !hasRef(e, "response", d.ContentID) {
			return storage.ErrInvalid
		}
		var messageID string
		if err := tx.QueryRowContext(ctx, "SELECT message_id FROM model_attempts WHERE space_id=? AND id=?", space, d.AttemptID).Scan(&messageID); err != nil {
			return err
		}
		if messageID != d.MessageID {
			return storage.ErrInvalid
		}
		observation, _ := json.Marshal(d.Observation)
		if _, err := tx.ExecContext(ctx, "UPDATE model_attempts SET status='completed',response_event_id=?,input_tokens=?,output_tokens=?,cache_read_tokens=?,cache_write_tokens=?,finished_event_id=?,finished_sequence=?,finished_at=?,content_id=?,finish_reason=?,observation=?,preview=? WHERE space_id=? AND id=?", e.EventID, d.Usage.Input, d.Usage.Output, d.Usage.CacheRead, d.Usage.CacheWrite, e.EventID, seq, e.OccurredAt, d.ContentID, d.FinishReason, observation, d.Preview, space, d.AttemptID); err != nil {
			return err
		}
		var purpose string
		if err := tx.QueryRowContext(ctx, "SELECT purpose FROM model_attempts WHERE space_id=? AND id=?", space, d.AttemptID).Scan(&purpose); err != nil {
			return err
		}
		if purpose == "summary" {
			if len(d.Calls) > 0 {
				return storage.ErrInvalid
			}
			return nil
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO messages(space_id,session_id,id,run_id,role,content_id,source_event_id,sequence,complete,actor_id,occurred_at) VALUES(?,?,?,?,'assistant',?,?,?,TRUE,?,?)", space, session, d.MessageID, e.RunID, d.ContentID, e.EventID, seq, e.Actor.ID, e.OccurredAt); err != nil {
			return err
		}
		for i, c := range d.Calls {
			if c.ResponseEventID != e.EventID {
				return storage.ErrInvalid
			}
			digest := c.ArgumentsSHA256
			if digest == "" {
				digest = journal.Digest(c.Arguments)
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO model_calls(space_id,run_id,response_event_id,call_id,name,arguments_sha256,native_id,arguments,ordinal) VALUES(?,?,?,?,?,?,?,?,?)", space, e.RunID, e.EventID, c.ID, c.Name, digest, nullable(c.NativeID), c.Arguments, i+1); err != nil {
				return err
			}
		}
		return nil
	case "model.attempt.failed":
		var d journal.AttemptFailedData
		json.Unmarshal(e.Data, &d)
		if err := attemptCheck(ctx, tx, space, e.RunID, d.AttemptID); err != nil {
			return err
		}
		observation, _ := json.Marshal(d.Observation)
		_, err := tx.ExecContext(ctx, "UPDATE model_attempts SET status='failed',input_tokens=?,output_tokens=?,cache_read_tokens=?,cache_write_tokens=?,finished_event_id=?,finished_sequence=?,finished_at=?,observation=?,preview=?,error_code=? WHERE space_id=? AND id=?", d.Usage.Input, d.Usage.Output, d.Usage.CacheRead, d.Usage.CacheWrite, e.EventID, seq, e.OccurredAt, observation, d.Preview, d.Error, space, d.AttemptID)
		return err
	case "artifact.published":
		var d run.Artifact
		json.Unmarshal(e.Data, &d)
		if d.SpaceID != space || d.SessionID != session || !hasRef(e, "artifact", d.ObjectID) {
			return storage.ErrInvalid
		}
		var name string
		if err := tx.QueryRowContext(ctx, "SELECT name FROM model_calls WHERE space_id=? AND run_id=? AND call_id=?", space, e.RunID, d.CallID).Scan(&name); err != nil {
			return err
		}
		if name != "publish_artifact" {
			return run.ErrState
		}
		raw, _ := json.Marshal(d)
		_, err := tx.ExecContext(ctx, "INSERT INTO artifacts(space_id,session_id,run_id,id,call_id,object_id,source_event_id,metadata,sequence,occurred_at) VALUES(?,?,?,?,?,?,?,?,?,?)", space, session, e.RunID, d.ID, d.CallID, d.ObjectID, e.EventID, raw, seq, e.OccurredAt)
		return err
	default:
		return storage.ErrSchema
	}
}
