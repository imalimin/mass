package mariadb

import (
	"context"
	"database/sql"
	"github.com/imalimin/mass/internal/app/identity"
)

// Public reads deliberately exclude encryption metadata and ciphertext.
const modelColumns = "m.id,m.name,m.provider,m.model_id,m.base_url,m.context_window,m.max_output_tokens,m.temperature,m.enabled,m.revision"

func scanModel(scan func(...any) error) (m identity.Model, err error) {
	err = scan(&m.ID, &m.Name, &m.Provider, &m.ModelID, &m.BaseURL, &m.ContextWindow, &m.MaxOutputTokens, &m.Temperature, &m.Enabled, &m.Revision)
	return
}
func (s *Store) Models(ctx context.Context, actor, space string, q identity.Query, available bool) (items []identity.Model, err error) {
	items = []identity.Model{}
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		query := "SELECT " + modelColumns + " FROM model_configs m WHERE m.space_id=? AND m.id>?"
		if available {
			query += " AND m.enabled=TRUE"
		}
		query += " ORDER BY m.id LIMIT ?"
		rows, e := tx.QueryContext(ctx, query, space, q.After, q.Limit)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			v, e := scanModel(rows.Scan)
			if e != nil {
				return e
			}
			items = append(items, v)
		}
		return rows.Err()
	})
	return
}
func (s *Store) Model(ctx context.Context, actor, space, id string) (m identity.Model, err error) {
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		var e error
		m, e = scanModel(tx.QueryRowContext(ctx, "SELECT "+modelColumns+" FROM model_configs m WHERE m.space_id=? AND m.id=?", space, id).Scan)
		return e
	})
	return
}
func (s *Store) SaveModel(ctx context.Context, actor, space string, m identity.Model, create bool) error {
	return s.scoped(ctx, actor, space, true, func(tx *sql.Tx) error {
		var err error
		if create {
			_, err = tx.ExecContext(ctx, "INSERT INTO model_configs(space_id,id,name,provider,model_id,base_url,secret_binding_id,key_id,ciphertext,context_window,max_output_tokens,temperature,enabled) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)", space, m.ID, m.Name, m.Provider, m.ModelID, m.BaseURL, m.SecretBindingID, m.KeyID, m.Ciphertext, m.ContextWindow, m.MaxOutputTokens, m.Temperature, m.Enabled)
		} else {
			query := "UPDATE model_configs SET name=?,provider=?,model_id=?,base_url=?,context_window=?,max_output_tokens=?,temperature=?,enabled=?,revision=revision+1"
			args := []any{m.Name, m.Provider, m.ModelID, m.BaseURL, m.ContextWindow, m.MaxOutputTokens, m.Temperature, m.Enabled}
			if len(m.Ciphertext) > 0 {
				query += ",secret_binding_id=?,key_id=?,ciphertext=?"
				args = append(args, m.SecretBindingID, m.KeyID, m.Ciphertext)
			}
			query += " WHERE space_id=? AND id=? AND revision=?"
			args = append(args, space, m.ID, m.Revision)
			result, e := tx.ExecContext(ctx, query, args...)
			err = e
			if err == nil {
				if n, _ := result.RowsAffected(); n != 1 {
					return identity.ErrConflict
				}
			}
		}
		if err != nil {
			return err
		}
		return audit(ctx, tx, actor, space, "model.saved", m.ID)
	})
}

// URL and encrypted key come from one authorized read of the run's fixed model ID.
// secret_binding_id preserves the authenticated encryption context of migrated keys.
func (s *Store) ResolveModelSecret(ctx context.Context, actor, space, model string) (m identity.Model, err error) {
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT id,base_url,secret_binding_id,key_id,ciphertext,revision FROM model_configs WHERE space_id=? AND id=? AND enabled=TRUE", space, model).Scan(&m.ID, &m.BaseURL, &m.SecretBindingID, &m.KeyID, &m.Ciphertext, &m.Revision)
	})
	return
}

func (s *Store) SharedRunModel(ctx context.Context, actor, space, source, model string) (m identity.Model, err error) {
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT "+modelColumns+",m.secret_binding_id,m.key_id,m.ciphertext FROM model_configs m WHERE m.space_id=? AND m.id=? AND m.enabled=TRUE", source, model).Scan(
			&m.ID, &m.Name, &m.Provider, &m.ModelID, &m.BaseURL, &m.ContextWindow, &m.MaxOutputTokens, &m.Temperature, &m.Enabled, &m.Revision,
			&m.SecretBindingID, &m.KeyID, &m.Ciphertext)
	})
	return
}
