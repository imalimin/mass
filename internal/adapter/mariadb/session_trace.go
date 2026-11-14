package mariadb

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/imalimin/mass/internal/domain/run"
)

type TraceJob struct {
	Run         run.View
	Failure     *run.FailureDetail
	HasPrevious bool
}

func (l *RuntimeLease) PendingTraces(ctx context.Context) ([]TraceJob, error) {
	if err := l.Check(ctx); err != nil {
		return nil, err
	}
	rows, err := l.store.DB.QueryContext(ctx, `SELECT r.snapshot,r.status,r.error_code,r.started_at,r.finished_at,
		COALESCE(r.workspace_snapshot_id,''), r.failure_detail,
		EXISTS(SELECT 1 FROM runs p WHERE p.space_id=r.space_id AND p.session_id=r.session_id AND p.trace_uploaded_at IS NOT NULL)
		FROM runs r
		WHERE r.trace_uploaded_at IS NULL AND r.finished_at IS NOT NULL AND r.snapshot IS NOT NULL
		ORDER BY r.finished_at,r.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []TraceJob
	for rows.Next() {
		var job TraceJob
		var snapshot, finish []byte
		v := &job.Run
		if err := rows.Scan(&snapshot, &v.Status, &v.Error, &v.StartedAt, &v.FinishedAt, &v.WorkspaceSnapshotID, &finish, &job.HasPrevious); err != nil {
			return nil, err
		}
		if len(finish) > 0 {
			if err := json.Unmarshal(finish, &job.Failure); err != nil {
				return nil, err
			}
		}
		if err := json.Unmarshal(snapshot, &v.Snapshot); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (l *RuntimeLease) MarkTraceUploaded(ctx context.Context, runs []run.View) error {
	if err := l.Check(ctx); err != nil {
		return err
	}
	return l.store.transaction(ctx, func(tx *sql.Tx) error {
		for _, v := range runs {
			if _, err := tx.ExecContext(ctx, "UPDATE runs SET trace_uploaded_at=UTC_TIMESTAMP(6) WHERE space_id=? AND id=? AND trace_uploaded_at IS NULL", v.SpaceID, v.ID); err != nil {
				return err
			}
		}
		return nil
	})
}
