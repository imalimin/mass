-- +goose NO TRANSACTION
-- +goose Up
CREATE TABLE context_projections (
 space_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 session_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 run_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 event_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 content_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 through_sequence BIGINT NOT NULL,
 purpose VARCHAR(16) NOT NULL,
 epoch INT NOT NULL,
 metadata JSON NOT NULL,
 PRIMARY KEY(space_id,id),
 KEY context_checkpoint(space_id,session_id,purpose,through_sequence),
 FOREIGN KEY(space_id,session_id) REFERENCES sessions(space_id,id),
 FOREIGN KEY(space_id,run_id) REFERENCES runs(space_id,id),
 FOREIGN KEY(space_id,event_id) REFERENCES runtime_events(space_id,event_id),
 FOREIGN KEY(space_id,content_id) REFERENCES content_objects(space_id,id)
) ENGINE=InnoDB;
-- Existing D04/D05 projection payloads remain immutable and readable.
INSERT INTO context_projections(space_id,session_id,run_id,id,event_id,content_id,through_sequence,purpose,epoch,metadata)
SELECT space_id,session_id,run_id,JSON_UNQUOTE(JSON_EXTRACT(data,'$.id')),event_id,
 JSON_UNQUOTE(JSON_EXTRACT(data,'$.content_id')),
 CAST(JSON_UNQUOTE(JSON_EXTRACT(data,'$.through_sequence')) AS UNSIGNED),'task',1,data
FROM runtime_events WHERE event_type='context.projected';
