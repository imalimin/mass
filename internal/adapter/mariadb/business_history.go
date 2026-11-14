package mariadb

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
)

// The history endpoints and projections share this read model. None of these
// rows are stored as events; each branch reads the entity that owns the data.
const businessHistory = `
WITH scope AS (SELECT ? AS space,? AS session,? AS run),
 session_runs AS (
  SELECT r.* FROM runs r JOIN scope s ON r.space_id=s.space AND r.session_id=s.session AND (s.run='' OR r.id=s.run)
 ), attempts AS (
  SELECT a.*,r.actor_id FROM model_attempts a JOIN session_runs r ON r.space_id=a.space_id AND r.id=a.run_id
 ), history AS (
 SELECT s.created_event_id AS id,'' AS run_id,1 AS seq,'session.created' AS kind,s.created_at AS at,s.actor_id AS actor,
  '' AS operation,'' AS cause,JSON_OBJECT('title',CONVERT(s.title USING utf8mb4) COLLATE utf8mb4_bin) AS data
  FROM sessions s JOIN scope q ON s.space_id=q.space AND s.id=q.session AND q.run=''
 UNION ALL SELECT r.created_event_id,r.id,r.created_sequence,'run.created',r.created_at,r.actor_id,'','',
  IF(r.snapshot IS NULL,'{}',CONCAT('{"snapshot":',r.snapshot,'}')) FROM session_runs r
 UNION ALL SELECT r.started_event_id,r.id,r.started_sequence,'run.started',r.started_at,r.actor_id,'','',
  '{}' FROM session_runs r
 UNION ALL SELECT r.cancel_event_id,r.id,r.cancel_sequence,'run.cancel.requested',r.cancelled_at,r.cancelled_by,'','',
  '{}' FROM session_runs r
 UNION ALL SELECT r.source_event_id,r.id,r.applied_sequence,'run.finished',r.finished_at,r.actor_id,'','',
  JSON_OBJECT('status',CONVERT(r.status USING utf8mb4) COLLATE utf8mb4_bin,'error',CONVERT(r.error_code USING utf8mb4) COLLATE utf8mb4_bin,'snapshot_error',CONVERT(r.snapshot_error USING utf8mb4) COLLATE utf8mb4_bin,
   'final_response_event_id',CONVERT(COALESCE(r.final_response_event_id,'') USING utf8mb4) COLLATE utf8mb4_bin,'failure_detail',JSON_EXTRACT(r.failure_detail,'$'))
  FROM session_runs r WHERE r.finished_at IS NOT NULL
 UNION ALL SELECT m.source_event_id,'',m.sequence,'user.message.accepted',m.occurred_at,m.actor_id,'','',
  JSON_OBJECT('id',CONVERT(m.id USING utf8mb4) COLLATE utf8mb4_bin,'content_id',CONVERT(m.content_id USING utf8mb4) COLLATE utf8mb4_bin,'attachments',JSON_EXTRACT(m.attachments,'$'))
  FROM messages m JOIN scope s ON s.space=m.space_id AND s.session=m.session_id AND s.run='' WHERE m.role='user'
 UNION ALL SELECT p.event_id,p.run_id,p.sequence,'context.projected',p.occurred_at,r.actor_id,'','',p.metadata
  FROM context_projections p JOIN session_runs r ON p.space_id=r.space_id AND p.run_id=r.id
 UNION ALL SELECT a.started_event_id,a.run_id,a.started_sequence,'model.attempt.started',a.started_at,a.actor_id,a.id,a.projection_event_id,
  JSON_OBJECT('id',CONVERT(a.id USING utf8mb4) COLLATE utf8mb4_bin,'retry_of',CONVERT(COALESCE(a.retry_of,'') USING utf8mb4) COLLATE utf8mb4_bin,'message_id',CONVERT(a.message_id USING utf8mb4) COLLATE utf8mb4_bin,'purpose',CONVERT(a.purpose USING utf8mb4) COLLATE utf8mb4_bin,'source',CONVERT(a.source USING utf8mb4) COLLATE utf8mb4_bin,
   'credential_revision',a.credential_revision,'projection_event_id',CONVERT(a.projection_event_id USING utf8mb4) COLLATE utf8mb4_bin)
  FROM attempts a
 UNION ALL SELECT a.finished_event_id,a.run_id,a.finished_sequence,
  IF(a.status='completed','model.response.completed','model.attempt.failed'),a.finished_at,a.actor_id,a.id,a.started_event_id,
  JSON_MERGE_PATCH(JSON_OBJECT('attempt_id',CONVERT(a.id USING utf8mb4) COLLATE utf8mb4_bin,'message_id',CONVERT(a.message_id USING utf8mb4) COLLATE utf8mb4_bin,'preview',CONVERT(COALESCE(a.preview,'') USING utf8mb4) COLLATE utf8mb4_bin,
   'observation',JSON_EXTRACT(a.observation,'$'),'usage',JSON_OBJECT('input_tokens',a.input_tokens,'output_tokens',a.output_tokens,
   'cache_read_tokens',a.cache_read_tokens,'cache_write_tokens',a.cache_write_tokens)),
   IF(a.status='completed',JSON_OBJECT('content_id',CONVERT(a.content_id USING utf8mb4) COLLATE utf8mb4_bin,'finish_reason',CONVERT(a.finish_reason USING utf8mb4) COLLATE utf8mb4_bin,'calls',JSON_EXTRACT(COALESCE((
    SELECT JSON_ARRAYAGG(JSON_OBJECT('id',CONVERT(c.call_id USING utf8mb4) COLLATE utf8mb4_bin,'native_id',CONVERT(COALESCE(c.native_id,'') USING utf8mb4) COLLATE utf8mb4_bin,'name',CONVERT(c.name USING utf8mb4) COLLATE utf8mb4_bin,'arguments',CONVERT(COALESCE(c.arguments,'') USING utf8mb4) COLLATE utf8mb4_bin,
     'response_event_id',CONVERT(c.response_event_id USING utf8mb4) COLLATE utf8mb4_bin,'arguments_sha256',CONVERT(c.arguments_sha256 USING utf8mb4) COLLATE utf8mb4_bin) ORDER BY c.ordinal)
    FROM model_calls c WHERE c.space_id=a.space_id AND c.run_id=a.run_id AND c.response_event_id=a.response_event_id),JSON_ARRAY()),'$')),
    JSON_OBJECT('error',CONVERT(a.error_code USING utf8mb4) COLLATE utf8mb4_bin)))
  FROM attempts a
 UNION ALL SELECT a.summary_event_id,a.run_id,a.summary_sequence,'context.summary.created',a.summary_at,a.actor_id,'',a.response_event_id,
  JSON_OBJECT('content_id',CONVERT(a.summary_ref USING utf8mb4) COLLATE utf8mb4_bin,'response_event_id',CONVERT(a.response_event_id USING utf8mb4) COLLATE utf8mb4_bin,'projection_id',CONVERT(p.id USING utf8mb4) COLLATE utf8mb4_bin,
   'coverage_through',CAST(a.summary_coverage AS CHAR CHARACTER SET utf8mb4) COLLATE utf8mb4_bin,'policy',CONVERT(a.summary_policy USING utf8mb4) COLLATE utf8mb4_bin)
  FROM attempts a JOIN context_projections p ON p.space_id=a.space_id AND p.event_id=a.projection_event_id
  WHERE a.summary_event_id IS NOT NULL
 UNION ALL SELECT p.source_event_id,p.run_id,p.sequence,'plan.updated',p.occurred_at,r.actor_id,p.call_id,'',p.plan
  FROM run_plans p JOIN session_runs r ON r.space_id=p.space_id AND r.id=p.run_id
 UNION ALL SELECT a.source_event_id,a.run_id,a.sequence,'artifact.published',a.occurred_at,r.actor_id,a.call_id,'',a.metadata
  FROM artifacts a JOIN session_runs r ON r.space_id=a.space_id AND r.id=a.run_id
 UNION ALL SELECT w.source_event_id,w.source_run_id,w.sequence,'workspace.snapshot.created',w.occurred_at,r.actor_id,'','',
  JSON_OBJECT('id',CONVERT(w.id USING utf8mb4) COLLATE utf8mb4_bin,'space_id',CONVERT(w.space_id USING utf8mb4) COLLATE utf8mb4_bin,'session_id',CONVERT(w.session_id USING utf8mb4) COLLATE utf8mb4_bin,'source_run_id',CONVERT(w.source_run_id USING utf8mb4) COLLATE utf8mb4_bin,
   'base_snapshot_id',CONVERT(COALESCE(w.base_snapshot_id,'') USING utf8mb4) COLLATE utf8mb4_bin,'object_id',CONVERT(w.object_id USING utf8mb4) COLLATE utf8mb4_bin,'manifest_ref',CONVERT(w.manifest_ref USING utf8mb4) COLLATE utf8mb4_bin,
   'tos_uri',CONVERT(w.tos_uri USING utf8mb4) COLLATE utf8mb4_bin,'archive_sha256',CONVERT(w.archive_sha256 USING utf8mb4) COLLATE utf8mb4_bin,'format_version',w.format_version)
  FROM workspace_snapshots w JOIN session_runs r ON r.space_id=w.space_id AND r.id=w.source_run_id
 UNION ALL SELECT t.restore_event_id,COALESCE(t.restore_run_id,''),t.restore_sequence,'workspace.restored',t.restored_at,t.restored_by,'','',
  JSON_OBJECT('reason',CONVERT(t.restore_reason USING utf8mb4) COLLATE utf8mb4_bin,'snapshot_id',CONVERT(COALESCE(t.restore_snapshot_id,'') USING utf8mb4) COLLATE utf8mb4_bin)
  FROM sessions t JOIN scope s ON s.space=t.space_id AND s.session=t.id AND (s.run='' OR s.run=t.restore_run_id)
 )
 SELECT id,run_id,seq,kind,at,actor,COALESCE(operation,''),COALESCE(cause,''),data
 FROM history WHERE id IS NOT NULL AND seq>? AND seq<=? ORDER BY seq LIMIT ?`

func readEvents(ctx context.Context, tx *sql.Tx, space, session string, after, through int64, limit int, runIDs ...string) ([]journal.Event, error) {
	var runID string
	if len(runIDs) > 0 {
		runID = runIDs[0]
	}
	rows, err := tx.QueryContext(ctx, businessHistory, space, session, runID, after, through, limit)
	if err != nil {
		return nil, err
	}
	events := []journal.Event{}
	for rows.Next() {
		e := journal.Event{SpaceID: space, SessionID: session}
		e.SchemaVersion, e.Actor.Kind = 1, "user"
		if err = rows.Scan(&e.EventID, &e.RunID, &e.Sequence, &e.Type, &e.OccurredAt, &e.Actor.ID, &e.OperationID, &e.CausationID, &e.Data); err != nil {
			rows.Close()
			return nil, err
		}
		e.RecordedAt = e.OccurredAt
		e.Refs = []storage.Ref{}
		events = append(events, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range events {
		if err := businessReferences(ctx, tx, &events[i]); err != nil {
			return nil, err
		}
	}
	return events, nil
}

func businessReferences(ctx context.Context, tx *sql.Tx, e *journal.Event) error {
	add := func(role, id string) error {
		if id == "" {
			return nil
		}
		ref := storage.Ref{Role: role, ObjectID: id}
		if err := tx.QueryRowContext(ctx, "SELECT sha256 FROM content_objects WHERE space_id=? AND id=?", e.SpaceID, id).Scan(&ref.SHA256); err != nil {
			return err
		}
		e.Refs = append(e.Refs, ref)
		return nil
	}
	switch e.Type {
	case "session.created":
		var d journal.SessionData
		var def, version, snapshot, digest, definition sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT s.definition_id,s.version_id,s.source_snapshot_id,o.sha256,s.definition_ref
			FROM sessions s LEFT JOIN content_objects o ON o.space_id=s.space_id AND o.id=s.definition_ref
			WHERE s.space_id=? AND s.id=?`, e.SpaceID, e.SessionID).Scan(&def, &version, &snapshot, &digest, &definition)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(e.Data, &d); err != nil {
			return err
		}
		d.DefinitionID, d.VersionID, d.SourceSnapshotID = def.String, version.String, snapshot.String
		if definition.Valid {
			d.DefinitionRef = &storage.Ref{Role: "definition", ObjectID: definition.String, SHA256: digest.String}
			e.Refs = append(e.Refs, *d.DefinitionRef)
			rows, err := tx.QueryContext(ctx, `SELECT 'agent_version',0,v.content_id,o.sha256
				FROM definition_versions v JOIN content_objects o ON o.space_id=v.space_id AND o.id=v.content_id WHERE v.space_id=? AND v.id=?
				UNION ALL SELECT 'skill_version',d.ordinal,v.content_id,o.sha256 FROM definition_dependencies d
				JOIN skill_versions v ON v.space_id=d.space_id AND v.id=d.skill_version_id
				JOIN content_objects o ON o.space_id=v.space_id AND o.id=v.content_id WHERE d.space_id=? AND d.version_id=? ORDER BY 1,2`, e.SpaceID, d.VersionID, e.SpaceID, d.VersionID)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var ref storage.Ref
				if err := rows.Scan(&ref.Role, &ref.Ordinal, &ref.ObjectID, &ref.SHA256); err != nil {
					return err
				}
				e.Refs = append(e.Refs, ref)
			}
			if err := rows.Err(); err != nil {
				return err
			}
		}
		e.Data, err = json.Marshal(d)
		return err
	case "user.message.accepted":
		var d journal.UserMessageData
		if err := json.Unmarshal(e.Data, &d); err != nil {
			return err
		}
		if err := add("content", d.ContentID); err != nil {
			return err
		}
		for i, a := range d.Attachments {
			ref := a.Ref
			ref.Ordinal = i
			e.Refs = append(e.Refs, ref)
		}
	case "context.projected", "context.summary.created", "model.response.completed":
		var d struct {
			ContentID string `json:"content_id"`
		}
		if err := json.Unmarshal(e.Data, &d); err != nil {
			return err
		}
		role := "response"
		if e.Type == "context.projected" {
			role = "projection"
		} else if e.Type == "context.summary.created" {
			role = "summary"
		}
		return add(role, d.ContentID)
	case "workspace.snapshot.created", "artifact.published":
		var d struct {
			ObjectID    string `json:"object_id"`
			ManifestRef string `json:"manifest_ref"`
		}
		if err := json.Unmarshal(e.Data, &d); err != nil {
			return err
		}
		if e.Type == "artifact.published" {
			return add("artifact", d.ObjectID)
		}
		if err := add("archive", d.ObjectID); err != nil {
			return err
		}
		return add("manifest", d.ManifestRef)
	}
	return nil
}
