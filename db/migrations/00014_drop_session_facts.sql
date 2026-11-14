-- +goose NO TRANSACTION
-- +goose Up
ALTER TABLE sessions
 ADD created_event_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
 ADD source_snapshot_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
 ADD restore_event_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
 ADD restore_sequence BIGINT NULL,
 ADD restored_at DATETIME(6) NULL,
 ADD restored_by CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
 ADD restore_run_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
 ADD restore_snapshot_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
 ADD restore_reason VARCHAR(64) CHARACTER SET ascii NULL;
ALTER TABLE runs
 ADD created_event_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
 ADD created_sequence BIGINT NULL,
 ADD started_event_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
 ADD started_sequence BIGINT NULL,
 ADD cancel_event_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
 ADD cancel_sequence BIGINT NULL,
 ADD cancelled_at DATETIME(6) NULL,
 ADD cancelled_by CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
 ADD failure_detail JSON NULL;
ALTER TABLE messages
 ADD actor_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
 ADD attachments JSON NULL,
 ADD occurred_at DATETIME(6) NULL;
ALTER TABLE model_attempts
 ADD started_event_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
 ADD started_sequence BIGINT NULL,
 ADD started_at DATETIME(6) NULL,
 ADD retry_of CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
 ADD credential_revision BIGINT NOT NULL DEFAULT 0,
 ADD finished_event_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
 ADD finished_sequence BIGINT NULL,
 ADD finished_at DATETIME(6) NULL,
 ADD content_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
 ADD finish_reason VARCHAR(16) CHARACTER SET ascii NULL,
 ADD observation JSON NULL,
 ADD preview TEXT NULL,
 ADD error_code VARCHAR(64) CHARACTER SET ascii NULL,
 ADD summary_event_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
 ADD summary_sequence BIGINT NULL,
 ADD summary_at DATETIME(6) NULL,
 ADD summary_ref CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
 ADD summary_coverage BIGINT NULL,
 ADD summary_policy VARCHAR(64) CHARACTER SET ascii NULL,
 ADD FOREIGN KEY(space_id,content_id) REFERENCES content_objects(space_id,id),
 ADD FOREIGN KEY(space_id,summary_ref) REFERENCES content_objects(space_id,id);
ALTER TABLE model_calls
 ADD native_id VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
 ADD arguments TEXT NULL,
 ADD ordinal INT NULL;
ALTER TABLE context_projections ADD sequence BIGINT NULL, ADD occurred_at DATETIME(6) NULL;
ALTER TABLE artifacts ADD sequence BIGINT NULL, ADD occurred_at DATETIME(6) NULL;
ALTER TABLE run_plans ADD sequence BIGINT NULL, ADD occurred_at DATETIME(6) NULL, ADD call_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NULL;
ALTER TABLE workspace_snapshots ADD sequence BIGINT NULL, ADD occurred_at DATETIME(6) NULL, ADD UNIQUE KEY snapshot_event(space_id,source_event_id);

UPDATE sessions s JOIN session_facts f ON f.space_id=s.space_id AND f.session_id=s.id AND f.event_type='session.created'
 SET s.created_event_id=f.event_id,s.source_snapshot_id=JSON_VALUE(f.data,'$.source_snapshot_id');
UPDATE sessions s JOIN session_facts f ON f.space_id=s.space_id AND f.session_id=s.id AND f.event_type='workspace.restored'
 AND f.sequence=(SELECT MAX(e.sequence) FROM session_facts e WHERE e.space_id=s.space_id AND e.session_id=s.id AND e.event_type='workspace.restored')
 SET s.restore_event_id=f.event_id,s.restore_sequence=f.sequence,s.restored_at=f.occurred_at,s.restored_by=f.actor_id,
 s.restore_run_id=f.run_id,s.restore_snapshot_id=JSON_VALUE(f.data,'$.snapshot_id'),s.restore_reason=JSON_VALUE(f.data,'$.reason');
UPDATE runs r JOIN session_facts f ON f.space_id=r.space_id AND f.run_id=r.id AND f.event_type='run.created'
 SET r.created_event_id=f.event_id,r.created_sequence=f.sequence;
UPDATE runs r JOIN session_facts f ON f.space_id=r.space_id AND f.run_id=r.id AND f.event_type='run.started'
 SET r.started_event_id=f.event_id,r.started_sequence=f.sequence;
UPDATE runs r JOIN session_facts f ON f.space_id=r.space_id AND f.run_id=r.id AND f.event_type='run.cancel.requested'
 SET r.cancel_event_id=f.event_id,r.cancel_sequence=f.sequence,r.cancelled_at=f.occurred_at,r.cancelled_by=f.actor_id;
UPDATE runs r JOIN session_facts f ON f.space_id=r.space_id AND f.event_id=r.source_event_id AND f.event_type='run.finished'
 SET r.failure_detail=JSON_EXTRACT(f.data,'$.failure_detail');
UPDATE messages m JOIN session_facts f ON f.space_id=m.space_id AND f.event_id=m.source_event_id
 SET m.actor_id=f.actor_id,m.occurred_at=f.occurred_at,m.attachments=JSON_EXTRACT(f.data,'$.attachments');
UPDATE context_projections p JOIN session_facts f ON f.space_id=p.space_id AND f.event_id=p.event_id
 SET p.sequence=f.sequence,p.occurred_at=f.occurred_at;
UPDATE artifacts a JOIN session_facts f ON f.space_id=a.space_id AND f.event_id=a.source_event_id
 SET a.sequence=f.sequence,a.occurred_at=f.occurred_at;
UPDATE run_plans p JOIN session_facts f ON f.space_id=p.space_id AND f.event_id=p.source_event_id
 SET p.sequence=f.sequence,p.occurred_at=f.occurred_at,p.call_id=f.operation_id;
UPDATE workspace_snapshots w JOIN session_facts f ON f.space_id=w.space_id AND f.event_id=w.source_event_id
 SET w.sequence=f.sequence,w.occurred_at=f.occurred_at;
UPDATE model_attempts a JOIN session_facts f ON f.space_id=a.space_id AND f.run_id=a.run_id AND f.event_type='model.attempt.started'
 AND a.id=JSON_VALUE(f.data,'$.id')
 SET a.started_event_id=f.event_id,a.started_sequence=f.sequence,a.started_at=f.occurred_at,
 a.retry_of=JSON_VALUE(f.data,'$.retry_of'),a.credential_revision=COALESCE(JSON_VALUE(f.data,'$.credential_revision'),0);
UPDATE model_attempts a JOIN session_facts f ON f.space_id=a.space_id AND f.run_id=a.run_id
 AND f.event_type IN ('model.response.completed','model.attempt.failed') AND a.id=JSON_VALUE(f.data,'$.attempt_id')
 SET a.finished_event_id=f.event_id,a.finished_sequence=f.sequence,a.finished_at=f.occurred_at,
 a.content_id=JSON_VALUE(f.data,'$.content_id'),a.finish_reason=JSON_VALUE(f.data,'$.finish_reason'),
 a.error_code=JSON_VALUE(f.data,'$.error'),a.preview=JSON_VALUE(f.data,'$.preview'),a.observation=JSON_EXTRACT(f.data,'$.observation');
UPDATE model_attempts a JOIN session_facts f ON f.space_id=a.space_id AND f.run_id=a.run_id
 AND f.event_type='context.summary.created' AND a.response_event_id=JSON_VALUE(f.data,'$.response_event_id')
 SET a.summary_event_id=f.event_id,a.summary_sequence=f.sequence,a.summary_at=f.occurred_at,
 a.summary_ref=JSON_VALUE(f.data,'$.content_id'),a.summary_coverage=JSON_VALUE(f.data,'$.coverage_through'),a.summary_policy=JSON_VALUE(f.data,'$.policy');
UPDATE model_calls c JOIN session_facts f ON f.space_id=c.space_id AND f.event_id=c.response_event_id
 JOIN JSON_TABLE(f.data,'$.calls[*]' COLUMNS (
   ordinal FOR ORDINALITY, call_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin PATH '$.id',
   native_id VARCHAR(128) PATH '$.native_id', arguments TEXT PATH '$.arguments'
 )) j ON j.call_id=c.call_id
 SET c.native_id=j.native_id,c.arguments=j.arguments,c.ordinal=j.ordinal;

DROP TABLE tool_executions;
DROP TABLE event_objects;
ALTER TABLE runs DROP FOREIGN KEY runs_ibfk_3;
ALTER TABLE messages DROP FOREIGN KEY messages_ibfk_3;
ALTER TABLE workspace_snapshots DROP FOREIGN KEY workspace_snapshots_ibfk_5;
ALTER TABLE model_attempts DROP FOREIGN KEY model_attempts_ibfk_2, DROP FOREIGN KEY model_attempts_ibfk_3;
ALTER TABLE model_calls DROP FOREIGN KEY model_calls_ibfk_2;
ALTER TABLE artifacts DROP FOREIGN KEY artifacts_ibfk_4;
ALTER TABLE run_plans DROP FOREIGN KEY run_plans_ibfk_2;
ALTER TABLE context_projections DROP FOREIGN KEY context_projections_ibfk_3;

-- Command receipts contain digests and sequence ranges, never event payloads.
ALTER TABLE journal_appends ADD first_event_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
 ADD UNIQUE KEY command_event(space_id,first_event_id);
UPDATE journal_appends j JOIN session_facts f ON f.space_id=j.space_id AND f.session_id=j.session_id AND f.sequence=j.first_sequence
 SET j.first_event_id=f.event_id
 WHERE j.append_key=(SELECT MIN(a.append_key) FROM journal_appends a WHERE a.space_id=j.space_id AND a.session_id=j.session_id AND a.first_sequence=j.first_sequence);
DROP TABLE session_facts;
