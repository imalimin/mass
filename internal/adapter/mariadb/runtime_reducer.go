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
func eventType(ctx context.Context, tx *sql.Tx, space, session, id, runID, kind string) error {
	var n int
	err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM runtime_events WHERE space_id=? AND session_id=? AND event_id=? AND run_id=? AND event_type=?", space, session, id, runID, kind).Scan(&n)
	if err != nil {
		return err
	}
	if n != 1 {
		return storage.ErrInvalid
	}
	return nil
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
	if strings.HasPrefix(e.Type, "model.") || strings.HasPrefix(e.Type, "tool.") || e.Type == "context.projected" || e.Type == "artifact.published" || e.Type == "plan.updated" {
		var status string
		var cancelled bool
		if err := tx.QueryRowContext(ctx, "SELECT status,cancel_requested FROM runs WHERE space_id=? AND id=?", space, e.RunID).Scan(&status, &cancelled); err != nil {
			return err
		}
		if status != "running" {
			return run.ErrState
		}
		if cancelled && (e.Type == "model.attempt.started" || e.Type == "tool.execution.started" || e.Type == "context.projected") {
			return run.ErrCancelled
		}
	}
	switch e.Type {
	case "plan.updated":
		var p run.Plan
		json.Unmarshal(e.Data, &p)
		var status, name string
		if err := tx.QueryRowContext(ctx, "SELECT t.status,c.name FROM tool_executions t JOIN model_calls c ON c.space_id=t.space_id AND c.run_id=t.run_id AND c.call_id=t.call_id WHERE t.space_id=? AND t.run_id=? AND t.call_id=?", space, e.RunID, e.OperationID).Scan(&status, &name); err != nil {
			return err
		}
		if status != "started" || name != "update_plan" {
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
		_, err = tx.ExecContext(ctx, "INSERT INTO run_plans(space_id,run_id,revision,source_event_id,plan) VALUES(?,?,?,?,?) ON DUPLICATE KEY UPDATE revision=VALUES(revision),source_event_id=VALUES(source_event_id),plan=VALUES(plan)", space, e.RunID, p.Revision, e.EventID, raw)
		return err
	case "tool.execution.input_resolved", "tool.io.recorded":
		var callID, content string
		if e.Type == "tool.io.recorded" {
			var d journal.IOData
			json.Unmarshal(e.Data, &d)
			callID = d.CallID
			content = d.ContentID
			var prepared, terminal int
			var preparedName string
			if err := tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(JSON_UNQUOTE(JSON_EXTRACT(data,'$.phase'))='prepared'),0),COALESCE(SUM(JSON_UNQUOTE(JSON_EXTRACT(data,'$.phase')) IN ('completed','failed')),0),COALESCE(MAX(JSON_UNQUOTE(JSON_EXTRACT(data,'$.name'))),'') FROM runtime_events WHERE space_id=? AND run_id=? AND operation_id=? AND event_type='tool.io.recorded' AND JSON_EXTRACT(data,'$.io_sequence')=? AND sequence<?", space, e.RunID, callID, d.Sequence, seq).Scan(&prepared, &terminal, &preparedName); err != nil {
				return err
			}
			if d.Phase == "prepared" && (prepared != 0 || terminal != 0) || d.Phase != "prepared" && (prepared != 1 || terminal != 0 || preparedName != d.Name) {
				return run.ErrState
			}
			if d.Phase == "prepared" {
				var previous int
				if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(CAST(JSON_EXTRACT(data,'$.io_sequence') AS UNSIGNED)),0) FROM runtime_events WHERE space_id=? AND run_id=? AND operation_id=? AND event_type='tool.io.recorded' AND JSON_UNQUOTE(JSON_EXTRACT(data,'$.phase')) IN ('completed','failed') AND sequence<?", space, e.RunID, callID, seq).Scan(&previous); err != nil {
					return err
				}
				if previous+1 != d.Sequence {
					return run.ErrState
				}
			}
		} else {
			var d journal.ResolvedData
			json.Unmarshal(e.Data, &d)
			callID = d.CallID
			content = d.ContentID
		}
		var status, name string
		if err := tx.QueryRowContext(ctx, "SELECT t.status,c.name FROM tool_executions t JOIN model_calls c ON c.space_id=t.space_id AND c.run_id=t.run_id AND c.call_id=t.call_id WHERE t.space_id=? AND t.run_id=? AND t.call_id=?", space, e.RunID, callID).Scan(&status, &name); err != nil {
			return err
		}
		if status != "started" || name != "run_code" || !hasRef(e, "io", content) {
			return storage.ErrInvalid
		}

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
		_, err := tx.ExecContext(ctx, "INSERT INTO messages(space_id,session_id,id,role,content_id,source_event_id,sequence,complete) VALUES(?,?,?,'user',?,?,?,TRUE)", space, session, d.ID, d.ContentID, e.EventID, seq)
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
		_, err := tx.ExecContext(ctx, "UPDATE runs SET cancel_requested=TRUE WHERE space_id=? AND id=?", space, e.RunID)
		return err
	case "context.projected":
		var d journal.ProjectionData
		json.Unmarshal(e.Data, &d)
		if d.Through >= seq || !hasRef(e, "projection", d.ContentID) {
			return storage.ErrInvalid
		}
	case "model.attempt.started":
		var d journal.AttemptData
		json.Unmarshal(e.Data, &d)
		if err := eventType(ctx, tx, space, session, d.ProjectionEventID, e.RunID, "context.projected"); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO model_attempts(space_id,session_id,run_id,id,projection_event_id,message_id,purpose,source,status) VALUES(?,?,?,?,?,?,?,?,'started')", space, session, e.RunID, d.ID, d.ProjectionEventID, d.MessageID, d.Purpose, d.Source)
		return err
	case "model.request.recorded", "model.response.recorded":
		var d journal.WireData
		json.Unmarshal(e.Data, &d)
		if err := attemptCheck(ctx, tx, space, e.RunID, d.AttemptID); err != nil {
			return err
		}
		if !hasRef(e, "wire", d.ContentID) {
			return storage.ErrInvalid
		}
	case "model.response.delta":
		var d journal.DeltaData
		json.Unmarshal(e.Data, &d)
		if err := attemptCheck(ctx, tx, space, e.RunID, d.AttemptID); err != nil {
			return err
		}
		var messageID string
		if err := tx.QueryRowContext(ctx, "SELECT message_id FROM model_attempts WHERE space_id=? AND run_id=? AND id=?", space, e.RunID, d.AttemptID).Scan(&messageID); err != nil {
			return err
		}
		if messageID != d.MessageID {
			return storage.ErrInvalid
		}
		return nil
	case "operation.reconciled":
		var d journal.ReconcileData
		json.Unmarshal(e.Data, &d)
		if d.Outcome == "unknown" {
			_, err := tx.ExecContext(ctx, "UPDATE tool_executions SET status='unknown',side_effect='unknown',source_event_id=? WHERE space_id=? AND run_id=? AND call_id=? AND status='started'", e.EventID, space, e.RunID, e.OperationID)
			return err
		}
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
		var requests, responses int
		if err := tx.QueryRowContext(ctx, "SELECT message_id FROM model_attempts WHERE space_id=? AND id=?", space, d.AttemptID).Scan(&messageID); err != nil {
			return err
		}
		if messageID != d.MessageID {
			return storage.ErrInvalid
		}
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(event_type='model.request.recorded'),0),COALESCE(SUM(event_type='model.response.recorded'),0) FROM runtime_events WHERE space_id=? AND run_id=? AND JSON_UNQUOTE(JSON_EXTRACT(data,'$.attempt_id'))=?", space, e.RunID, d.AttemptID).Scan(&requests, &responses); err != nil {
			return err
		}
		if requests < 1 || responses < 1 {
			return storage.ErrIntegrity
		}
		if _, err := tx.ExecContext(ctx, "UPDATE model_attempts SET status='completed',response_event_id=?,input_tokens=?,output_tokens=?,cache_read_tokens=?,cache_write_tokens=? WHERE space_id=? AND id=?", e.EventID, d.Usage.Input, d.Usage.Output, d.Usage.CacheRead, d.Usage.CacheWrite, space, d.AttemptID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO messages(space_id,session_id,id,run_id,role,content_id,source_event_id,sequence,complete) VALUES(?,?,?,?,'assistant',?,?,?,TRUE)", space, session, d.MessageID, e.RunID, d.ContentID, e.EventID, seq); err != nil {
			return err
		}
		for _, c := range d.Calls {
			if c.ResponseEventID != e.EventID {
				return storage.ErrInvalid
			}
			digest := c.ArgumentsSHA256
			if digest == "" {
				digest = journal.Digest(c.Arguments)
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO model_calls(space_id,run_id,response_event_id,call_id,name,arguments_sha256) VALUES(?,?,?,?,?,?)", space, e.RunID, e.EventID, c.ID, c.Name, digest); err != nil {
				return err
			}
		}
	case "model.attempt.failed":
		var d journal.AttemptFailedData
		json.Unmarshal(e.Data, &d)
		if err := attemptCheck(ctx, tx, space, e.RunID, d.AttemptID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE model_attempts SET status='failed',input_tokens=?,output_tokens=?,cache_read_tokens=?,cache_write_tokens=? WHERE space_id=? AND id=?", d.Usage.Input, d.Usage.Output, d.Usage.CacheRead, d.Usage.CacheWrite, space, d.AttemptID)
		return err
	case "tool.execution.started", "tool.execution.completed":
		var d journal.ToolData
		json.Unmarshal(e.Data, &d)
		var name, sha string
		if err := tx.QueryRowContext(ctx, "SELECT name,arguments_sha256 FROM model_calls WHERE space_id=? AND run_id=? AND response_event_id=? AND call_id=?", space, e.RunID, d.ResponseEventID, d.CallID).Scan(&name, &sha); err != nil {
			return err
		}
		if name != d.Name || sha != d.ArgumentsSHA256 {
			return storage.ErrInvalid
		}
		if e.Type == "tool.execution.started" {
			_, err := tx.ExecContext(ctx, "INSERT INTO tool_executions(space_id,run_id,call_id,source_event_id,status) VALUES(?,?,?,?,'started')", space, e.RunID, d.CallID, e.EventID)
			return err
		}
		if !hasRef(e, "result", d.ContentID) {
			return storage.ErrInvalid
		}
		result, err := tx.ExecContext(ctx, "UPDATE tool_executions SET status=?,result_ref=?,side_effect=?,source_event_id=? WHERE space_id=? AND run_id=? AND call_id=? AND status='started'", d.Status, d.ContentID, d.SideEffect, e.EventID, space, e.RunID, d.CallID)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return run.ErrState
		}
	case "artifact.published":
		var d run.Artifact
		json.Unmarshal(e.Data, &d)
		if d.SpaceID != space || d.SessionID != session || !hasRef(e, "artifact", d.ObjectID) {
			return storage.ErrInvalid
		}
		var status, name string
		if err := tx.QueryRowContext(ctx, "SELECT t.status,c.name FROM tool_executions t JOIN model_calls c ON c.space_id=t.space_id AND c.run_id=t.run_id AND c.call_id=t.call_id WHERE t.space_id=? AND t.run_id=? AND t.call_id=?", space, e.RunID, d.CallID).Scan(&status, &name); err != nil {
			return err
		}
		if status != "started" || name != "publish_artifact" {
			return run.ErrState
		}
		raw, _ := json.Marshal(d)
		_, err := tx.ExecContext(ctx, "INSERT INTO artifacts(space_id,session_id,run_id,id,call_id,object_id,source_event_id,metadata) VALUES(?,?,?,?,?,?,?,?)", space, session, e.RunID, d.ID, d.CallID, d.ObjectID, e.EventID, raw)
		return err
	}
	return nil
}
