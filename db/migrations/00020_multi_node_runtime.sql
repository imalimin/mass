-- +goose NO TRANSACTION
-- +goose Up
CREATE TABLE runtime_nodes (
 node_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 peer_addr VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 started_at DATETIME(6) NOT NULL,
 heartbeat_at DATETIME(6) NOT NULL,
 KEY node_heartbeat (heartbeat_at)
) ENGINE=InnoDB;

ALTER TABLE sessions
 ADD COLUMN log_seq BIGINT UNSIGNED NOT NULL DEFAULT 0;

ALTER TABLE runs
 ADD COLUMN owner_node_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
 ADD COLUMN lease_expires_at DATETIME(6) NULL,
 ADD KEY run_lease (status,lease_expires_at,id);

ALTER TABLE evaluation_experiments
 ADD COLUMN owner_node_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
 ADD COLUMN lease_expires_at DATETIME(6) NULL,
 ADD KEY experiment_lease (completed_at,lease_expires_at,id);
