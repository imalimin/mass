-- +goose NO TRANSACTION
-- +goose Up
-- Reset session/run data, including its derived views. Catalog, users, spaces
-- and immutable content metadata are outside this reset.
SET SESSION FOREIGN_KEY_CHECKS=0;
DELETE FROM context_projections;
DELETE FROM agui_events;
DELETE FROM agui_streams;
DELETE FROM run_plans;
DELETE FROM artifacts;
DELETE FROM tool_executions;
DELETE FROM model_calls;
DELETE FROM model_attempts;
DELETE FROM messages;
DELETE FROM attachments;
DELETE FROM event_objects;
DELETE FROM journal_appends;
DELETE FROM runtime_requests;
DELETE FROM session_active_runs;
DELETE FROM workspace_snapshots;
DELETE FROM runtime_events;
DELETE FROM runs;
DELETE FROM sessions;
SET SESSION FOREIGN_KEY_CHECKS=1;

DROP TABLE IF EXISTS agui_events, agui_streams;
-- Keep the transactional records used by context projection, tool recovery
-- and view rebuilding. This table accepts only the business fact types below.
RENAME TABLE runtime_events TO session_facts;
ALTER TABLE session_facts ADD CONSTRAINT session_fact_type CHECK (event_type IN (
 'session.created','user.message.accepted','run.created','run.started','run.cancel.requested','run.finished',
 'workspace.snapshot.created','workspace.snapshot.failed','workspace.restored','operation.reconciled',
 'context.projected','context.summary.created','model.attempt.started','model.response.completed','model.attempt.failed',
 'tool.execution.started','tool.execution.input_resolved','tool.io.recorded','tool.execution.completed',
 'plan.updated','artifact.published'
));
ALTER TABLE runs
 ADD trace_uploaded_at DATETIME(6) NULL,
 ADD KEY trace_pending(trace_uploaded_at,finished_at);
