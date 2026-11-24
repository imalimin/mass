package mariadb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/imalimin/mass/internal/app/evaluation"
	"github.com/imalimin/mass/internal/app/identity"
	"github.com/imalimin/mass/internal/domain/storage"
)

func (s *Store) SaveEvaluationDataset(ctx context.Context, actor, space, id string, in evaluation.DatasetInput) (v evaluation.Dataset, err error) {
	v = evaluation.Dataset{ID: id, SpaceID: space, Name: in.Name, Cases: in.Cases, CaseCount: len(in.Cases), Version: 1, SavedAt: time.Now().UTC().Truncate(time.Microsecond)}
	fresh := id == ""
	if fresh {
		v.ID = storage.NewShortID()
	}
	raw, err := json.Marshal(in.Cases)
	if err != nil {
		return v, err
	}
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		if !fresh {
			var first int64
			if e := tx.QueryRowContext(ctx, "SELECT version FROM evaluation_datasets WHERE space_id=? AND id=? AND version=1 FOR UPDATE", space, id).Scan(&first); e != nil {
				return e
			}
			if e := tx.QueryRowContext(ctx, "SELECT version FROM evaluation_datasets WHERE space_id=? AND id=? ORDER BY version DESC LIMIT 1 FOR UPDATE", space, id).Scan(&v.Version); e != nil {
				return e
			}
			v.Version++
		}
		_, e := tx.ExecContext(ctx, "INSERT INTO evaluation_datasets(space_id,id,version,name,saved_at,cases) VALUES(?,?,?,?,?,?)", space, v.ID, v.Version, v.Name, v.SavedAt, raw)
		return e
	})
	return
}
func datasetRow(scan func(...any) error) (v evaluation.Dataset, err error) {
	var raw []byte
	err = scan(&v.SpaceID, &v.ID, &v.Version, &v.Name, &v.SavedAt, &raw)
	if err == nil {
		err = json.Unmarshal(raw, &v.Cases)
		v.CaseCount = len(v.Cases)
	}
	return
}

const datasetColumns = "space_id,id,version,name,saved_at,cases"

func (s *Store) EvaluationDataset(ctx context.Context, actor, space, id string, version int64) (v evaluation.Dataset, err error) {
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		query := "SELECT " + datasetColumns + " FROM evaluation_datasets WHERE space_id=? AND id=?"
		args := []any{space, id}
		if version > 0 {
			query += " AND version=?"
			args = append(args, version)
		}
		var e error
		v, e = datasetRow(tx.QueryRowContext(ctx, query+" ORDER BY version DESC LIMIT 1", args...).Scan)
		return e
	})
	return
}
func (s *Store) EvaluationDatasets(ctx context.Context, actor, space string, q identity.Query) (items []evaluation.Dataset, err error) {
	items = []evaluation.Dataset{}
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		rows, e := tx.QueryContext(ctx, "SELECT d.space_id,d.id,d.version,d.name,d.saved_at,JSON_LENGTH(d.cases) FROM evaluation_datasets d JOIN (SELECT id,MAX(version) AS version FROM evaluation_datasets WHERE space_id=? AND id>? GROUP BY id ORDER BY id LIMIT ?) latest ON latest.id=d.id AND latest.version=d.version WHERE d.space_id=? ORDER BY d.id", space, q.After, q.Limit, space)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v evaluation.Dataset
			if e = rows.Scan(&v.SpaceID, &v.ID, &v.Version, &v.Name, &v.SavedAt, &v.CaseCount); e != nil {
				return e
			}
			items = append(items, v)
		}
		return rows.Err()
	})
	return
}

const experimentColumns = "e.id,e.space_id,e.actor_id,e.created_at,e.dataset_id,d.name,e.dataset_version,e.agent_id,e.agent_version_id,e.concurrency,e.idempotency_key,e.request_digest,e.result,e.completed_at,JSON_LENGTH(d.cases)"
const experimentFrom = " FROM evaluation_experiments e JOIN evaluation_datasets d ON d.space_id=e.space_id AND d.id=e.dataset_id AND d.version=e.dataset_version"

func experimentRow(scan func(...any) error) (v evaluation.Experiment, err error) {
	var raw []byte
	err = scan(&v.ID, &v.SpaceID, &v.ActorID, &v.CreatedAt, &v.DatasetID, &v.DatasetName, &v.DatasetVersion, &v.AgentID, &v.AgentVersionID, &v.Concurrency, &v.Key, &v.Digest, &raw, &v.CompletedAt, &v.CaseCount)
	if err == nil && raw != nil {
		err = json.Unmarshal(raw, &v.Result)
	}
	return
}
func (s *Store) FindEvaluationRequest(ctx context.Context, actor, space, key, digest string) (v evaluation.Experiment, found bool, err error) {
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		var e error
		v, e = experimentRow(tx.QueryRowContext(ctx, "SELECT "+experimentColumns+experimentFrom+" WHERE e.space_id=? AND e.actor_id=? AND e.idempotency_key=?", space, actor, key).Scan)
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		if v.Digest != digest {
			return identity.ErrConflict
		}
		found = true
		return nil
	})
	return
}
func (s *Store) CreateEvaluationExperiment(ctx context.Context, in evaluation.Experiment) (v evaluation.Experiment, err error) {
	v = in
	err = s.scoped(ctx, in.ActorID, in.SpaceID, false, func(tx *sql.Tx) error {
		previous, e := experimentRow(tx.QueryRowContext(ctx, "SELECT "+experimentColumns+experimentFrom+" WHERE e.space_id=? AND e.actor_id=? AND e.idempotency_key=?", in.SpaceID, in.ActorID, in.Key).Scan)
		if e == nil {
			if previous.Digest != in.Digest {
				return identity.ErrConflict
			}
			v = previous
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		d, e := datasetRow(tx.QueryRowContext(ctx, "SELECT "+datasetColumns+" FROM evaluation_datasets WHERE space_id=? AND id=? ORDER BY version DESC LIMIT 1", in.SpaceID, in.DatasetID).Scan)
		if e != nil {
			return e
		}
		if len(d.Cases) == 0 {
			return identity.ErrInvalid
		}
		if e = evaluation.ValidateDataset(evaluation.DatasetInput{Name: d.Name, Cases: d.Cases}); e != nil {
			return e
		}
		v.DatasetName, v.DatasetVersion, v.CaseCount = d.Name, d.Version, len(d.Cases)
		_, e = tx.ExecContext(ctx, "INSERT INTO evaluation_experiments(id,space_id,actor_id,created_at,dataset_id,dataset_version,agent_id,agent_version_id,concurrency,idempotency_key,request_digest) VALUES(?,?,?,?,?,?,?,?,?,?,?)", v.ID, v.SpaceID, v.ActorID, v.CreatedAt, v.DatasetID, v.DatasetVersion, v.AgentID, v.AgentVersionID, v.Concurrency, v.Key, v.Digest)
		return e
	})
	return
}
func (s *Store) EvaluationExperiment(ctx context.Context, actor, space, id string) (v evaluation.Experiment, err error) {
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		var e error
		v, e = experimentRow(tx.QueryRowContext(ctx, "SELECT "+experimentColumns+experimentFrom+" WHERE e.space_id=? AND e.id=?", space, id).Scan)
		return e
	})
	return
}
func (s *Store) EvaluationExperiments(ctx context.Context, actor, space string, q identity.Query) ([]evaluation.Experiment, error) {
	return s.evaluationExperimentRows(ctx, actor, space, "e.space_id=? AND e.id>? ORDER BY e.id LIMIT ?", space, q.After, q.Limit)
}

func (s *Store) CompletedEvaluationExperiments(ctx context.Context, actor, space string, ids []string) ([]evaluation.Experiment, error) {
	placeholders := make([]string, len(ids))
	args := make([]any, 1, len(ids)+1)
	args[0] = space
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	filter := "e.space_id=? AND e.completed_at IS NOT NULL AND e.id IN (" + strings.Join(placeholders, ",") + ")"
	return s.evaluationExperimentRows(ctx, actor, space, filter, args...)
}

func (s *Store) evaluationExperimentRows(ctx context.Context, actor, space, filter string, args ...any) (items []evaluation.Experiment, err error) {
	items = []evaluation.Experiment{}
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		rows, e := tx.QueryContext(ctx, "SELECT "+experimentColumns+experimentFrom+" WHERE "+filter, args...)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			v, e := experimentRow(rows.Scan)
			if e != nil {
				return e
			}
			items = append(items, v)
		}
		return rows.Err()
	})
	return
}

// FinishEvaluationExperiment records the outcome of an already accepted task.
// The actor identifies the original submission, even if membership was revoked.
func (s *Store) FinishEvaluationExperiment(ctx context.Context, actor, space, id string, result map[string]evaluation.Progress, at time.Time) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	res, err := s.DB.ExecContext(ctx, "UPDATE evaluation_experiments SET result=?,completed_at=? WHERE space_id=? AND id=? AND actor_id=? AND completed_at IS NULL", raw, at, space, id, actor)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n != 1 {
		return identity.ErrConflict
	}
	return err
}
