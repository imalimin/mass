-- +goose NO TRANSACTION
-- +goose Up
CREATE TABLE content_objects (
 space_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 kind VARCHAR(64) CHARACTER SET ascii NOT NULL,
 media_type VARCHAR(128) CHARACTER SET ascii NOT NULL,
 size BIGINT NOT NULL,
 sha256 CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 object_key VARCHAR(768) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 tos_uri VARCHAR(1024) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 etag VARCHAR(256) NOT NULL DEFAULT '',
 version_id VARCHAR(256) NOT NULL DEFAULT '',
 state VARCHAR(16) CHARACTER SET ascii NOT NULL DEFAULT 'uploading',
 created_at DATETIME(6) NOT NULL DEFAULT UTC_TIMESTAMP(6),
 PRIMARY KEY(space_id,id), UNIQUE KEY object_location(object_key),
 FOREIGN KEY(space_id) REFERENCES spaces(id),
 CHECK(size>=0), CHECK(state IN ('uploading','ready'))
) ENGINE=InnoDB;
CREATE TABLE sessions (
 space_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 actor_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 last_sequence BIGINT NOT NULL DEFAULT 0,
 latest_workspace_snapshot_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
 workspace_error VARCHAR(64) CHARACTER SET ascii NOT NULL DEFAULT '',
 PRIMARY KEY(space_id,id), FOREIGN KEY(space_id) REFERENCES spaces(id),
 FOREIGN KEY(actor_id) REFERENCES users(id), CHECK(last_sequence>=0)
) ENGINE=InnoDB;
CREATE TABLE runs (
 space_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 session_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 actor_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 status VARCHAR(16) CHARACTER SET ascii NOT NULL,
 source_event_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 applied_sequence BIGINT NOT NULL,
 workspace_snapshot_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
 PRIMARY KEY(space_id,id), UNIQUE KEY session_run(space_id,session_id,id),
 FOREIGN KEY(space_id,session_id) REFERENCES sessions(space_id,id),
 FOREIGN KEY(actor_id) REFERENCES users(id),
 CHECK(status IN ('queued','running','succeeded','failed','cancelled'))
) ENGINE=InnoDB;
CREATE TABLE session_active_runs (
 space_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 session_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 run_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 PRIMARY KEY(space_id,session_id), UNIQUE KEY active_run(space_id,run_id),
 FOREIGN KEY(space_id,session_id,run_id) REFERENCES runs(space_id,session_id,id)
) ENGINE=InnoDB;
CREATE TABLE journal_appends (
 space_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 session_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 append_key VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 digest CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 first_sequence BIGINT NOT NULL,
 event_count INT NOT NULL,
 PRIMARY KEY(space_id,session_id,append_key),
 FOREIGN KEY(space_id,session_id) REFERENCES sessions(space_id,id)
) ENGINE=InnoDB;
CREATE TABLE runtime_events (
 space_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 session_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 event_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 sequence BIGINT NOT NULL,
 run_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
 event_type VARCHAR(64) CHARACTER SET ascii NOT NULL,
 schema_version INT NOT NULL,
 operation_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NULL,
 causation_event_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
 correlation_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NULL,
 actor_kind VARCHAR(16) CHARACTER SET ascii NOT NULL,
 actor_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 occurred_at DATETIME(6) NOT NULL,
 recorded_at DATETIME(6) NOT NULL DEFAULT UTC_TIMESTAMP(6),
 data JSON NOT NULL,
 event_digest CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 PRIMARY KEY(space_id,event_id), UNIQUE KEY session_sequence(space_id,session_id,sequence),
 UNIQUE KEY session_event(space_id,session_id,event_id), KEY run_sequence(space_id,run_id,sequence),
 FOREIGN KEY(space_id,session_id) REFERENCES sessions(space_id,id),
 FOREIGN KEY(space_id,session_id,run_id) REFERENCES runs(space_id,session_id,id),
 FOREIGN KEY(space_id,session_id,causation_event_id) REFERENCES runtime_events(space_id,session_id,event_id),
 CHECK(sequence>0), CHECK(schema_version>0)
) ENGINE=InnoDB;
CREATE TABLE event_objects (
 space_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 event_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 object_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 role VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 ordinal INT NOT NULL,
 PRIMARY KEY(space_id,event_id,role,ordinal),
 FOREIGN KEY(space_id,event_id) REFERENCES runtime_events(space_id,event_id),
 FOREIGN KEY(space_id,object_id) REFERENCES content_objects(space_id,id), CHECK(ordinal>=0)
) ENGINE=InnoDB;
CREATE TABLE workspace_snapshots (
 space_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 session_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 source_run_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 base_snapshot_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
 object_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 manifest_ref CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 tos_uri VARCHAR(1024) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 archive_sha256 CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 format_version INT NOT NULL,
 source_event_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 created_at DATETIME(6) NOT NULL DEFAULT UTC_TIMESTAMP(6),
 PRIMARY KEY(space_id,id), UNIQUE KEY snapshot_session(space_id,session_id,id),
 FOREIGN KEY(space_id,session_id,source_run_id) REFERENCES runs(space_id,session_id,id),
 FOREIGN KEY(space_id,base_snapshot_id) REFERENCES workspace_snapshots(space_id,id),
 FOREIGN KEY(space_id,object_id) REFERENCES content_objects(space_id,id),
 FOREIGN KEY(space_id,manifest_ref) REFERENCES content_objects(space_id,id),
 FOREIGN KEY(space_id,source_event_id) REFERENCES runtime_events(space_id,event_id), CHECK(format_version=1)
) ENGINE=InnoDB;
ALTER TABLE sessions ADD CONSTRAINT latest_snapshot_fk FOREIGN KEY(space_id,latest_workspace_snapshot_id) REFERENCES workspace_snapshots(space_id,id);
ALTER TABLE runs ADD CONSTRAINT run_snapshot_fk FOREIGN KEY(space_id,session_id,workspace_snapshot_id) REFERENCES workspace_snapshots(space_id,session_id,id);
