-- +goose NO TRANSACTION
-- +goose Up
-- Discard old spaces and their dependent rows. Keep users and login sessions.
SET SESSION FOREIGN_KEY_CHECKS=0;
START TRANSACTION;
DELETE FROM context_projections;
DELETE FROM run_plans;
DELETE FROM attachments;
DELETE FROM artifacts;
DELETE FROM model_calls;
DELETE FROM model_attempts;
DELETE FROM messages;
DELETE FROM runtime_requests;
DELETE FROM workspace_snapshots;
DELETE FROM journal_appends;
DELETE FROM session_active_runs;
DELETE FROM runs;
DELETE FROM sessions;
DELETE FROM definition_dependencies;
DELETE FROM skill_versions;
DELETE FROM skill_drafts;
DELETE FROM definition_versions;
DELETE FROM definition_drafts;
DELETE FROM definitions;
DELETE FROM content_objects;
DELETE FROM model_configs;
DELETE FROM audit_logs;
DELETE FROM space_members;
DELETE FROM spaces;
COMMIT;

-- InnoDB requires removing these foreign keys before changing the column widths.
ALTER TABLE artifacts
 DROP FOREIGN KEY IF EXISTS artifacts_ibfk_1,
 DROP FOREIGN KEY IF EXISTS artifacts_ibfk_2,
 DROP FOREIGN KEY IF EXISTS artifacts_ibfk_3;
ALTER TABLE attachments
 DROP FOREIGN KEY IF EXISTS attachments_ibfk_1,
 DROP FOREIGN KEY IF EXISTS attachments_ibfk_2;
ALTER TABLE audit_logs
 DROP FOREIGN KEY IF EXISTS audit_logs_ibfk_1;
ALTER TABLE content_objects
 DROP FOREIGN KEY IF EXISTS content_objects_ibfk_1;
ALTER TABLE context_projections
 DROP FOREIGN KEY IF EXISTS context_projections_ibfk_1,
 DROP FOREIGN KEY IF EXISTS context_projections_ibfk_2,
 DROP FOREIGN KEY IF EXISTS context_projections_ibfk_4;
ALTER TABLE definitions
 DROP FOREIGN KEY IF EXISTS definitions_ibfk_1,
 DROP FOREIGN KEY IF EXISTS definitions_ibfk_2,
 DROP CONSTRAINT IF EXISTS definitions_agent_short_id;
ALTER TABLE definition_dependencies
 DROP FOREIGN KEY IF EXISTS definition_dependencies_ibfk_1,
 DROP FOREIGN KEY IF EXISTS definition_dependencies_ibfk_2;
ALTER TABLE definition_drafts
 DROP FOREIGN KEY IF EXISTS definition_drafts_ibfk_1;
ALTER TABLE definition_versions
 DROP FOREIGN KEY IF EXISTS definition_versions_ibfk_1,
 DROP FOREIGN KEY IF EXISTS definition_versions_ibfk_2;
ALTER TABLE journal_appends
 DROP FOREIGN KEY IF EXISTS journal_appends_ibfk_1;
ALTER TABLE messages
 DROP FOREIGN KEY IF EXISTS messages_ibfk_1,
 DROP FOREIGN KEY IF EXISTS messages_ibfk_2,
 DROP FOREIGN KEY IF EXISTS messages_ibfk_4;
ALTER TABLE model_attempts
 DROP FOREIGN KEY IF EXISTS model_attempts_ibfk_1,
 DROP FOREIGN KEY IF EXISTS model_attempts_ibfk_4,
 DROP FOREIGN KEY IF EXISTS model_attempts_ibfk_5;
ALTER TABLE model_calls
 DROP FOREIGN KEY IF EXISTS model_calls_ibfk_1;
ALTER TABLE model_configs
 DROP FOREIGN KEY IF EXISTS model_configs_ibfk_1;
ALTER TABLE runs
 DROP FOREIGN KEY IF EXISTS runs_ibfk_1,
 DROP FOREIGN KEY IF EXISTS run_snapshot_fk;
ALTER TABLE runtime_requests
 DROP FOREIGN KEY IF EXISTS runtime_requests_ibfk_1,
 DROP FOREIGN KEY IF EXISTS runtime_requests_ibfk_2;
ALTER TABLE run_plans
 DROP FOREIGN KEY IF EXISTS run_plans_ibfk_1;
ALTER TABLE sessions
 DROP FOREIGN KEY IF EXISTS latest_snapshot_fk,
 DROP FOREIGN KEY IF EXISTS sessions_ibfk_1,
 DROP FOREIGN KEY IF EXISTS sessions_ibfk_3,
 DROP FOREIGN KEY IF EXISTS sessions_ibfk_4,
 DROP CONSTRAINT IF EXISTS sessions_short_id;
ALTER TABLE session_active_runs
 DROP FOREIGN KEY IF EXISTS session_active_runs_ibfk_1;
ALTER TABLE skill_drafts
 DROP FOREIGN KEY IF EXISTS skill_drafts_ibfk_1,
 DROP FOREIGN KEY IF EXISTS skill_drafts_ibfk_2;
ALTER TABLE skill_versions
 DROP FOREIGN KEY IF EXISTS skill_versions_ibfk_1,
 DROP FOREIGN KEY IF EXISTS skill_versions_ibfk_2;
ALTER TABLE space_members
 DROP FOREIGN KEY IF EXISTS space_members_ibfk_1;
ALTER TABLE workspace_snapshots
 DROP FOREIGN KEY IF EXISTS workspace_snapshots_ibfk_1,
 DROP FOREIGN KEY IF EXISTS workspace_snapshots_ibfk_2,
 DROP FOREIGN KEY IF EXISTS workspace_snapshots_ibfk_3,
 DROP FOREIGN KEY IF EXISTS workspace_snapshots_ibfk_4;
ALTER TABLE spaces
 DROP CONSTRAINT IF EXISTS spaces_short_id;

ALTER TABLE spaces
 MODIFY id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 ADD CONSTRAINT spaces_short_id CHECK (id REGEXP '^[a-z2-7]{16}$');
ALTER TABLE space_members MODIFY space_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL;
ALTER TABLE audit_logs MODIFY space_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL;
ALTER TABLE model_configs MODIFY space_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL;
ALTER TABLE content_objects MODIFY space_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL;
ALTER TABLE definitions
 MODIFY space_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 ADD CONSTRAINT definitions_agent_short_id CHECK (kind <> 'agent' OR id REGEXP '^[a-z2-7]{16}$');
ALTER TABLE definition_drafts
 MODIFY space_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 MODIFY definition_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL;
ALTER TABLE definition_versions
 MODIFY space_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 MODIFY definition_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL;
ALTER TABLE skill_drafts MODIFY space_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL;
ALTER TABLE skill_versions MODIFY space_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL;
ALTER TABLE definition_dependencies MODIFY space_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL;
ALTER TABLE sessions
 MODIFY space_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 MODIFY id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 MODIFY definition_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NULL,
 ADD CONSTRAINT sessions_short_id CHECK (id REGEXP '^[a-z2-7]{16}$');
ALTER TABLE runs
 MODIFY space_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 MODIFY session_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL;
ALTER TABLE session_active_runs
 MODIFY space_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 MODIFY session_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL;
ALTER TABLE journal_appends
 MODIFY space_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 MODIFY session_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL;
ALTER TABLE workspace_snapshots
 MODIFY space_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 MODIFY session_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL;
ALTER TABLE runtime_requests
 MODIFY space_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 MODIFY session_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL;
ALTER TABLE messages
 MODIFY space_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 MODIFY session_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL;
ALTER TABLE model_attempts
 MODIFY space_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 MODIFY session_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL;
ALTER TABLE model_calls MODIFY space_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL;
ALTER TABLE artifacts
 MODIFY space_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 MODIFY session_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL;
ALTER TABLE attachments
 MODIFY space_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 MODIFY session_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL;
ALTER TABLE run_plans MODIFY space_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL;
ALTER TABLE context_projections
 MODIFY space_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 MODIFY session_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL;

-- Keep the existing relationships, including Skill references to the shared definitions table.
ALTER TABLE artifacts
 ADD CONSTRAINT artifacts_ibfk_1 FOREIGN KEY(space_id,run_id,call_id) REFERENCES model_calls(space_id,run_id,call_id),
 ADD CONSTRAINT artifacts_ibfk_2 FOREIGN KEY(space_id,session_id) REFERENCES sessions(space_id,id),
 ADD CONSTRAINT artifacts_ibfk_3 FOREIGN KEY(space_id,object_id) REFERENCES content_objects(space_id,id);
ALTER TABLE attachments
 ADD CONSTRAINT attachments_ibfk_1 FOREIGN KEY(space_id,session_id) REFERENCES sessions(space_id,id),
 ADD CONSTRAINT attachments_ibfk_2 FOREIGN KEY(space_id,object_id) REFERENCES content_objects(space_id,id);
ALTER TABLE audit_logs
 ADD CONSTRAINT audit_logs_ibfk_1 FOREIGN KEY(space_id) REFERENCES spaces(id);
ALTER TABLE content_objects
 ADD CONSTRAINT content_objects_ibfk_1 FOREIGN KEY(space_id) REFERENCES spaces(id);
ALTER TABLE context_projections
 ADD CONSTRAINT context_projections_ibfk_1 FOREIGN KEY(space_id,session_id) REFERENCES sessions(space_id,id),
 ADD CONSTRAINT context_projections_ibfk_2 FOREIGN KEY(space_id,run_id) REFERENCES runs(space_id,id),
 ADD CONSTRAINT context_projections_ibfk_4 FOREIGN KEY(space_id,content_id) REFERENCES content_objects(space_id,id);
ALTER TABLE definitions
 ADD CONSTRAINT definitions_ibfk_1 FOREIGN KEY(space_id) REFERENCES spaces(id),
 ADD CONSTRAINT definitions_ibfk_2 FOREIGN KEY(space_id,id,default_version_id) REFERENCES definition_versions(space_id,definition_id,id);
ALTER TABLE definition_dependencies
 ADD CONSTRAINT definition_dependencies_ibfk_1 FOREIGN KEY(space_id,version_id) REFERENCES definition_versions(space_id,id),
 ADD CONSTRAINT definition_dependencies_ibfk_2 FOREIGN KEY(space_id,skill_id,skill_version_id) REFERENCES skill_versions(space_id,definition_id,id);
ALTER TABLE definition_drafts
 ADD CONSTRAINT definition_drafts_ibfk_1 FOREIGN KEY(space_id,definition_id) REFERENCES definitions(space_id,id);
ALTER TABLE definition_versions
 ADD CONSTRAINT definition_versions_ibfk_1 FOREIGN KEY(space_id,definition_id) REFERENCES definitions(space_id,id),
 ADD CONSTRAINT definition_versions_ibfk_2 FOREIGN KEY(space_id,content_id) REFERENCES content_objects(space_id,id);
ALTER TABLE journal_appends
 ADD CONSTRAINT journal_appends_ibfk_1 FOREIGN KEY(space_id,session_id) REFERENCES sessions(space_id,id);
ALTER TABLE messages
 ADD CONSTRAINT messages_ibfk_1 FOREIGN KEY(space_id,session_id) REFERENCES sessions(space_id,id),
 ADD CONSTRAINT messages_ibfk_2 FOREIGN KEY(space_id,session_id,run_id) REFERENCES runs(space_id,session_id,id),
 ADD CONSTRAINT messages_ibfk_4 FOREIGN KEY(space_id,content_id) REFERENCES content_objects(space_id,id);
ALTER TABLE model_attempts
 ADD CONSTRAINT model_attempts_ibfk_1 FOREIGN KEY(space_id,session_id,run_id) REFERENCES runs(space_id,session_id,id),
 ADD CONSTRAINT model_attempts_ibfk_4 FOREIGN KEY(space_id,content_id) REFERENCES content_objects(space_id,id),
 ADD CONSTRAINT model_attempts_ibfk_5 FOREIGN KEY(space_id,summary_ref) REFERENCES content_objects(space_id,id);
ALTER TABLE model_calls
 ADD CONSTRAINT model_calls_ibfk_1 FOREIGN KEY(space_id,run_id) REFERENCES runs(space_id,id);
ALTER TABLE model_configs
 ADD CONSTRAINT model_configs_ibfk_1 FOREIGN KEY(space_id) REFERENCES spaces(id);
ALTER TABLE runs
 ADD CONSTRAINT runs_ibfk_1 FOREIGN KEY(space_id,session_id) REFERENCES sessions(space_id,id),
 ADD CONSTRAINT run_snapshot_fk FOREIGN KEY(space_id,session_id,workspace_snapshot_id) REFERENCES workspace_snapshots(space_id,session_id,id);
ALTER TABLE runtime_requests
 ADD CONSTRAINT runtime_requests_ibfk_1 FOREIGN KEY(space_id,session_id) REFERENCES sessions(space_id,id),
 ADD CONSTRAINT runtime_requests_ibfk_2 FOREIGN KEY(space_id,session_id,run_id) REFERENCES runs(space_id,session_id,id);
ALTER TABLE run_plans
 ADD CONSTRAINT run_plans_ibfk_1 FOREIGN KEY(space_id,run_id) REFERENCES runs(space_id,id);
ALTER TABLE sessions
 ADD CONSTRAINT latest_snapshot_fk FOREIGN KEY(space_id,latest_workspace_snapshot_id) REFERENCES workspace_snapshots(space_id,id),
 ADD CONSTRAINT sessions_ibfk_1 FOREIGN KEY(space_id) REFERENCES spaces(id),
 ADD CONSTRAINT sessions_ibfk_3 FOREIGN KEY(space_id,definition_id,version_id) REFERENCES definition_versions(space_id,definition_id,id),
 ADD CONSTRAINT sessions_ibfk_4 FOREIGN KEY(space_id,definition_ref) REFERENCES content_objects(space_id,id);
ALTER TABLE session_active_runs
 ADD CONSTRAINT session_active_runs_ibfk_1 FOREIGN KEY(space_id,session_id,run_id) REFERENCES runs(space_id,session_id,id);
ALTER TABLE skill_drafts
 ADD CONSTRAINT skill_drafts_ibfk_1 FOREIGN KEY(space_id,definition_id) REFERENCES definitions(space_id,id),
 ADD CONSTRAINT skill_drafts_ibfk_2 FOREIGN KEY(space_id,content_id) REFERENCES content_objects(space_id,id);
ALTER TABLE skill_versions
 ADD CONSTRAINT skill_versions_ibfk_1 FOREIGN KEY(space_id,definition_id) REFERENCES definitions(space_id,id),
 ADD CONSTRAINT skill_versions_ibfk_2 FOREIGN KEY(space_id,content_id) REFERENCES content_objects(space_id,id);
ALTER TABLE space_members
 ADD CONSTRAINT space_members_ibfk_1 FOREIGN KEY(space_id) REFERENCES spaces(id);
ALTER TABLE workspace_snapshots
 ADD CONSTRAINT workspace_snapshots_ibfk_1 FOREIGN KEY(space_id,session_id,source_run_id) REFERENCES runs(space_id,session_id,id),
 ADD CONSTRAINT workspace_snapshots_ibfk_2 FOREIGN KEY(space_id,base_snapshot_id) REFERENCES workspace_snapshots(space_id,id),
 ADD CONSTRAINT workspace_snapshots_ibfk_3 FOREIGN KEY(space_id,object_id) REFERENCES content_objects(space_id,id),
 ADD CONSTRAINT workspace_snapshots_ibfk_4 FOREIGN KEY(space_id,manifest_ref) REFERENCES content_objects(space_id,id);
SET SESSION FOREIGN_KEY_CHECKS=1;
