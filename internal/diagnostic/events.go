package diagnostic

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"regexp"
	"syscall"

	"github.com/go-sql-driver/mysql"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
)

type scopeKey struct{}
type attemptKey struct{}

type scope struct {
	store                 *Store
	space, session, runID string
}

func WithRun(ctx context.Context, store *Store, s run.Snapshot) context.Context {
	if store == nil {
		return ctx
	}
	return context.WithValue(ctx, scopeKey{}, scope{store, s.SpaceID, s.SessionID, s.ID})
}

func WithAttempt(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, attemptKey{}, id)
}

func Attempt(ctx context.Context) string {
	id, _ := ctx.Value(attemptKey{}).(string)
	return id
}

func ModelFailed(ctx context.Context, err error, stage, purpose string, status int, requestID, underlying string) error {
	if err == nil || ctx.Err() != nil || Reported(err) {
		return err
	}
	msg := fmt.Sprintf("attempt_id=%s purpose=%s stage=%s", Attempt(ctx), purpose, stage)
	if status != 0 {
		msg += fmt.Sprintf(" http_status=%d", status)
	}
	if requestID = RequestID(requestID); requestID != "" {
		msg += " request_id=" + requestID
	}
	if underlying != "" && underlying != "unknown" {
		msg += " underlying=" + underlying
	}
	return Failure(ctx, "model.attempt_failed", err, "%s", msg)
}

func Record(ctx context.Context, level, event, msg string, args ...any) {
	if value, ok := ctx.Value(scopeKey{}).(scope); ok {
		value.store.Write(value.space, value.session, value.runID, level, event, fmt.Sprintf(msg, args...))
	}
}

type reported struct{ error }

func (r reported) Unwrap() error { return r.error }

func Reported(err error) bool {
	var logged reported
	return errors.As(err, &logged)
}

func ReportedAs(err error) error { return reported{err} }

// Failure associates the original error with the log already written by its owner.
func Failure(ctx context.Context, event string, err error, msg string, args ...any) error {
	if ctx == nil || err == nil || Reported(err) {
		return err
	}
	if _, ok := ctx.Value(scopeKey{}).(scope); !ok {
		return err
	}
	message := fmt.Sprintf(msg, args...)
	if cause := Cause(err); cause != "unknown" {
		message += " cause=" + cause
	}
	Record(ctx, "warn", event, "%s", message)
	return reported{err}
}

var safeCode = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
var safeRequestID = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

func RequestID(value string) string {
	if safeRequestID.MatchString(value) {
		return value
	}
	return ""
}

// Cause returns operational codes only. Provider errors, filesystem paths and
// arbitrary error messages can contain user data or credentials.
func Cause(err error) string {
	if err == nil {
		return "unknown"
	}
	var probe *run.CheckError
	if errors.As(err, &probe) && probe.MySQLCode != 0 {
		return fmt.Sprintf("mysql_%d", probe.MySQLCode)
	}
	var sqlErr *mysql.MySQLError
	if errors.As(err, &sqlErr) {
		return fmt.Sprintf("mysql_%d", sqlErr.Number)
	}
	var httpErr interface{ HTTPStatus() int }
	if errors.As(err, &httpErr) && httpErr.HTTPStatus() != 0 {
		return fmt.Sprintf("http_%d", httpErr.HTTPStatus())
	}
	var model *run.ModelFailure
	if errors.As(err, &model) && model.Status != 0 {
		return fmt.Sprintf("http_%d", model.Status)
	}
	for _, known := range []struct {
		err  error
		code string
	}{
		{context.DeadlineExceeded, "deadline_exceeded"}, {context.Canceled, "cancelled"},
		{fs.ErrNotExist, "file_not_found"}, {fs.ErrPermission, "permission_denied"},
		{syscall.ENOSPC, "no_space_left"}, {syscall.ENOTDIR, "not_a_directory"},
		{syscall.ECONNREFUSED, "connection_refused"}, {syscall.ECONNRESET, "connection_reset"},
		{io.ErrUnexpectedEOF, "unexpected_eof"}, {storage.ErrWorkspace, "workspace_unavailable"},
		{storage.ErrIntegrity, "content_unavailable"}, {storage.ErrConflict, "conflict"},
		{storage.ErrBlobNotFound, "object_not_found"}, {run.ErrOverflow, "context_overflow"},
		{run.ErrBudget, "budget_exceeded"}, {run.ErrProtocol, "model_protocol_error"},
		{run.ErrPermission, "permission_revoked"}, {run.ErrCancelled, "run_cancelled"},
		{run.ErrSummary, "context_summary_failed"}, {run.ErrTool, "unsupported_tool"},
		{run.ErrUnavailable, "runtime_unavailable"}, {run.ErrState, "run_state_conflict"},
		{run.ErrCodeTimeout, "code_timeout"}, {run.ErrNetworkTimeout, "network_timeout"},
		{run.ErrUnknownEffect, "tool_effect_unknown"}, {storage.ErrInvalid, "invalid_request"},
	} {
		if errors.Is(err, known.err) {
			return known.code
		}
	}
	var tool *run.ToolError
	if errors.As(err, &tool) && safeCode.MatchString(tool.Code) {
		return tool.Code
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "network_timeout"
	}
	switch err.Error() {
	case "forbidden", "not_found", "unauthenticated", "invalid_request", "unsupported_capability", "runtime_unavailable",
		"model_transport_failed", "model_stream_failed":
		return err.Error()
	case "model request exceeds size limit":
		return "model_request_bytes_exceeded"
	case "model response exceeds size limit":
		return "model_wire_response_bytes_exceeded"
	default:
		return "unknown"
	}
}
