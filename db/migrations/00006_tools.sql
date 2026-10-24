-- +goose NO TRANSACTION
-- +goose Up
CREATE TABLE attachments (
 space_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 session_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 object_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 metadata JSON NOT NULL,
 PRIMARY KEY(space_id,id),
 FOREIGN KEY(space_id,session_id) REFERENCES sessions(space_id,id),
 FOREIGN KEY(space_id,object_id) REFERENCES content_objects(space_id,id)
) ENGINE=InnoDB;
CREATE TABLE run_plans (
 space_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 run_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 revision BIGINT NOT NULL,
 source_event_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 plan JSON NOT NULL,
 PRIMARY KEY(space_id,run_id),
 FOREIGN KEY(space_id,run_id) REFERENCES runs(space_id,id),
 FOREIGN KEY(space_id,source_event_id) REFERENCES runtime_events(space_id,event_id)
) ENGINE=InnoDB;
