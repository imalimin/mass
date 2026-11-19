-- +goose NO TRANSACTION
-- +goose Up
ALTER TABLE sessions
 ADD COLUMN hide BOOLEAN NOT NULL DEFAULT FALSE,
 ADD CONSTRAINT session_hide CHECK (hide IN (FALSE,TRUE)),
 ADD KEY visible_sessions (space_id,hide,id),
 DROP INDEX agent_sessions,
 ADD KEY agent_sessions (space_id,agent_id,hide,id);

CREATE TABLE evaluation_datasets (
 space_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 version BIGINT UNSIGNED NOT NULL,
 name VARCHAR(100) NOT NULL,
 saved_at DATETIME(6) NOT NULL,
 cases JSON NOT NULL,
 PRIMARY KEY(space_id,id,version),
 FOREIGN KEY(space_id) REFERENCES spaces(id),
 CHECK(id REGEXP '^[a-z2-7]{16}$'),
 CHECK(version>=1)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE evaluation_experiments (
 id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 space_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 actor_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 created_at DATETIME(6) NOT NULL,
 dataset_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 dataset_version BIGINT UNSIGNED NOT NULL,
 agent_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 agent_version_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 concurrency SMALLINT UNSIGNED NOT NULL,
 idempotency_key VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 request_digest CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 result JSON NULL,
 completed_at DATETIME(6) NULL,
 UNIQUE KEY experiment_request(space_id,actor_id,idempotency_key),
 KEY space_experiments(space_id,id),
 KEY experiment_dataset(space_id,dataset_id,dataset_version),
 FOREIGN KEY(space_id,dataset_id,dataset_version) REFERENCES evaluation_datasets(space_id,id,version),
 FOREIGN KEY(actor_id) REFERENCES users(id),
 CHECK(id REGEXP '^[a-z2-7]{16}$'),
 CHECK(concurrency>=1),
 CHECK((result IS NULL AND completed_at IS NULL) OR (result IS NOT NULL AND completed_at IS NOT NULL))
) ENGINE=InnoDB;
