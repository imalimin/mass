-- +goose NO TRANSACTION
-- +goose Up
ALTER TABLE sessions
 ADD workspace_maintenance_actor CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
 ADD workspace_maintenance BOOLEAN NOT NULL DEFAULT FALSE,
 ADD title VARCHAR(200) NOT NULL DEFAULT '',
 ADD definition_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
 ADD version_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
 ADD definition_ref CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
 ADD created_at DATETIME(6) NOT NULL DEFAULT UTC_TIMESTAMP(6),
 ADD FOREIGN KEY(space_id,definition_id,version_id) REFERENCES definition_versions(space_id,definition_id,id),
 ADD FOREIGN KEY(space_id,definition_ref) REFERENCES content_objects(space_id,id);
ALTER TABLE runs
 ADD snapshot JSON NULL,
 ADD cancel_requested BOOLEAN NOT NULL DEFAULT FALSE,
 ADD owner_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
 ADD created_at DATETIME(6) NOT NULL DEFAULT UTC_TIMESTAMP(6),
 ADD started_at DATETIME(6) NULL,
 ADD heartbeat_at DATETIME(6) NULL,
 ADD finished_at DATETIME(6) NULL,
 ADD final_response_event_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
 ADD error_code VARCHAR(64) CHARACTER SET ascii NOT NULL DEFAULT '',
 ADD snapshot_error VARCHAR(64) CHARACTER SET ascii NOT NULL DEFAULT '',
 ADD cleanup_plan JSON NULL,
 ADD KEY runtime_queue(status,created_at,id),
 ADD FOREIGN KEY(space_id,session_id,final_response_event_id) REFERENCES runtime_events(space_id,session_id,event_id);
CREATE TABLE runtime_requests (
 space_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 actor_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 operation VARCHAR(32) CHARACTER SET ascii NOT NULL,
 request_key VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 request_digest CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 session_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 run_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
 PRIMARY KEY(space_id,actor_id,operation,request_key),
 FOREIGN KEY(space_id,session_id) REFERENCES sessions(space_id,id),
 FOREIGN KEY(space_id,session_id,run_id) REFERENCES runs(space_id,session_id,id),
 FOREIGN KEY(actor_id) REFERENCES users(id)
) ENGINE=InnoDB;
CREATE TABLE messages (
 space_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 session_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 run_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
 role VARCHAR(16) CHARACTER SET ascii NOT NULL,
 content_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 source_event_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 sequence BIGINT NOT NULL, complete BOOLEAN NOT NULL,
 PRIMARY KEY(space_id,id), UNIQUE(space_id,source_event_id), KEY session_messages(space_id,session_id,sequence),
 FOREIGN KEY(space_id,session_id) REFERENCES sessions(space_id,id),
 FOREIGN KEY(space_id,session_id,run_id) REFERENCES runs(space_id,session_id,id),
 FOREIGN KEY(space_id,session_id,source_event_id) REFERENCES runtime_events(space_id,session_id,event_id),
 FOREIGN KEY(space_id,content_id) REFERENCES content_objects(space_id,id)
) ENGINE=InnoDB;
CREATE TABLE model_attempts (
 space_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 session_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 run_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 projection_event_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 message_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 purpose VARCHAR(16) CHARACTER SET ascii NOT NULL,
 source VARCHAR(16) CHARACTER SET ascii NOT NULL,
 status VARCHAR(16) CHARACTER SET ascii NOT NULL,
 response_event_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
 input_tokens BIGINT NULL, output_tokens BIGINT NULL, cache_read_tokens BIGINT NULL, cache_write_tokens BIGINT NULL,
 PRIMARY KEY(space_id,id), UNIQUE(space_id,response_event_id),
 FOREIGN KEY(space_id,session_id,run_id) REFERENCES runs(space_id,session_id,id),
 FOREIGN KEY(space_id,session_id,projection_event_id) REFERENCES runtime_events(space_id,session_id,event_id),
 FOREIGN KEY(space_id,session_id,response_event_id) REFERENCES runtime_events(space_id,session_id,event_id)
) ENGINE=InnoDB;
CREATE TABLE model_calls (
 space_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 run_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 response_event_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 call_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 name VARCHAR(64) CHARACTER SET ascii NOT NULL,
 arguments_sha256 CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 PRIMARY KEY(space_id,run_id,call_id),
 FOREIGN KEY(space_id,run_id) REFERENCES runs(space_id,id),
 FOREIGN KEY(space_id,response_event_id) REFERENCES runtime_events(space_id,event_id)
) ENGINE=InnoDB;
CREATE TABLE tool_executions (
 space_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 run_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 call_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 source_event_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 status VARCHAR(16) CHARACTER SET ascii NOT NULL,
 result_ref CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
 side_effect VARCHAR(16) CHARACTER SET ascii NOT NULL DEFAULT 'unknown',
 PRIMARY KEY(space_id,run_id,call_id),
 FOREIGN KEY(space_id,run_id,call_id) REFERENCES model_calls(space_id,run_id,call_id),
 FOREIGN KEY(space_id,source_event_id) REFERENCES runtime_events(space_id,event_id),
 FOREIGN KEY(space_id,result_ref) REFERENCES content_objects(space_id,id)
) ENGINE=InnoDB;
CREATE TABLE artifacts (
 space_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 session_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 run_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 call_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 object_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 source_event_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 metadata JSON NOT NULL,
 PRIMARY KEY(space_id,id), UNIQUE(space_id,run_id,call_id),
 FOREIGN KEY(space_id,run_id,call_id) REFERENCES model_calls(space_id,run_id,call_id),
 FOREIGN KEY(space_id,session_id) REFERENCES sessions(space_id,id),
 FOREIGN KEY(space_id,object_id) REFERENCES content_objects(space_id,id),
 FOREIGN KEY(space_id,source_event_id) REFERENCES runtime_events(space_id,event_id)
) ENGINE=InnoDB;
CREATE TABLE agui_streams (
 space_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 session_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 run_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 protocol_profile VARCHAR(32) CHARACTER SET ascii NOT NULL,
 mapper_version INT NOT NULL, parser_version INT NOT NULL,
 source_sequence BIGINT NOT NULL DEFAULT 0, last_ui_sequence BIGINT NOT NULL DEFAULT 0,
 state JSON NOT NULL, closed BOOLEAN NOT NULL DEFAULT FALSE,
 PRIMARY KEY(space_id,run_id), FOREIGN KEY(space_id,session_id,run_id) REFERENCES runs(space_id,session_id,id)
) ENGINE=InnoDB;
CREATE TABLE agui_events (
 space_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 run_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 ui_sequence BIGINT NOT NULL,
 source_event_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 source_ordinal INT NOT NULL, event_json JSON NOT NULL,
 PRIMARY KEY(space_id,run_id,ui_sequence), UNIQUE(space_id,run_id,source_event_id,source_ordinal),
 FOREIGN KEY(space_id,run_id) REFERENCES agui_streams(space_id,run_id),
 FOREIGN KEY(space_id,source_event_id) REFERENCES runtime_events(space_id,event_id)
) ENGINE=InnoDB;
