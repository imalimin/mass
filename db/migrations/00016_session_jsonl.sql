-- +goose NO TRANSACTION
-- +goose Up
-- Sessions, runs and the Agent/Skill catalog start empty. TOS objects from
-- the discarded schema are outside the new catalog and session indexes.
SET SESSION FOREIGN_KEY_CHECKS=0;
DROP TABLE IF EXISTS
 artifacts, attachments, content_objects, context_projections,
 definition_dependencies, definition_drafts, definition_versions, definitions,
 journal_appends, messages, model_attempts, model_calls, run_plans,
 runtime_requests, session_active_runs, skill_drafts, skill_versions,
 workspace_snapshots, runs, sessions;
SET SESSION FOREIGN_KEY_CHECKS=1;

CREATE TABLE sessions (
 id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 space_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 agent_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 actor_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 created_at DATETIME(6) NOT NULL,
 request_key VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 request_digest CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 UNIQUE KEY session_request (space_id,actor_id,request_key),
 KEY agent_sessions (space_id,agent_id,id),
 FOREIGN KEY (space_id) REFERENCES spaces(id),
 FOREIGN KEY (actor_id) REFERENCES users(id),
 CHECK (id REGEXP '^[a-z2-7]{16}$'),
 CHECK (agent_id REGEXP '^[a-z2-7]{16}$')
) ENGINE=InnoDB;

CREATE TABLE runs (
 id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 session_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 status ENUM('queued','running','succeeded','failed','cancelled') NOT NULL,
 error_code VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 started_at DATETIME(6) NULL,
 finished_at DATETIME(6) NULL,
 KEY session_runs (session_id,id),
 KEY run_status (status,id),
 FOREIGN KEY (session_id) REFERENCES sessions(id),
 CHECK (id REGEXP '^[a-z2-7]{16}$')
) ENGINE=InnoDB;
