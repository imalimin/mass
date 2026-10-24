package mariadb

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
)

func (s *Store) RegisterAttachment(ctx context.Context, actor, space string, a run.Attachment) error {
	return s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		var id string
		if err := tx.QueryRowContext(ctx, "SELECT id FROM sessions WHERE space_id=? AND id=? FOR UPDATE", space, a.SessionID).Scan(&id); err != nil {
			return err
		}
		o, err := scanObject(tx.QueryRowContext(ctx, "SELECT "+objectColumns+" FROM content_objects WHERE space_id=? AND id=? AND state='ready' AND kind='attachment'", space, a.Ref.ObjectID).Scan)
		if err != nil {
			return err
		}
		if o.Size != a.Size || o.SHA256 != a.Ref.SHA256 || o.MediaType != a.MediaType {
			return storage.ErrIntegrity
		}
		raw, _ := json.Marshal(a)
		_, err = tx.ExecContext(ctx, "INSERT INTO attachments(space_id,session_id,id,object_id,metadata) VALUES(?,?,?,?,?)", space, a.SessionID, a.ID, a.Ref.ObjectID, raw)
		return err
	})
}
func attachmentInTx(ctx context.Context, tx *sql.Tx, space, session, id string) (a run.Attachment, err error) {
	var raw []byte
	err = tx.QueryRowContext(ctx, "SELECT metadata FROM attachments WHERE space_id=? AND session_id=? AND id=?", space, session, id).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &a)
	}
	return
}
func (s *Store) RuntimeAttachment(ctx context.Context, actor, space, session, id string) (a run.Attachment, err error) {
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error { var e error; a, e = attachmentInTx(ctx, tx, space, session, id); return e })
	return
}
func (s *Store) RuntimePlan(ctx context.Context, actor, space, id string) (plan run.Plan, err error) {
	plan = run.Plan{Steps: []run.PlanStep{}}
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		if _, e := runInTx(ctx, tx, space, id); e != nil {
			return e
		}
		var raw []byte
		e := tx.QueryRowContext(ctx, "SELECT plan FROM run_plans WHERE space_id=? AND run_id=?", space, id).Scan(&raw)
		if e == sql.ErrNoRows {
			return nil
		}
		if e != nil {
			return e
		}
		return json.Unmarshal(raw, &plan)
	})
	return
}
