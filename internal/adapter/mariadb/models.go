package mariadb

import (
	"context"
	"database/sql"
	"github.com/imalimin/mass/internal/app/identity"
)

func (s *Store) Credentials(ctx context.Context, actor, space string, q identity.Query) (items []identity.Credential, err error) {
	items = []identity.Credential{}
	err = s.scoped(ctx, actor, space, true, func(tx *sql.Tx) error {
		rows, e := tx.QueryContext(ctx, "SELECT id,name,enabled,revision FROM credentials WHERE space_id=? AND id>? ORDER BY id LIMIT ?", space, q.After, q.Limit)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v identity.Credential
			if e = rows.Scan(&v.ID, &v.Name, &v.Enabled, &v.Revision); e != nil {
				return e
			}
			items = append(items, v)
		}
		return rows.Err()
	})
	return
}
func (s *Store) SaveCredential(ctx context.Context, actor, space string, c identity.Credential, create bool) error {
	return s.scoped(ctx, actor, space, true, func(tx *sql.Tx) error {
		var err error
		if create {
			_, err = tx.ExecContext(ctx, "INSERT INTO credentials(space_id,id,name,key_id,ciphertext,enabled) VALUES(?,?,?,?,?,?)", space, c.ID, c.Name, c.KeyID, c.Ciphertext, c.Enabled)
		} else {
			var result sql.Result
			if len(c.Ciphertext) > 0 {
				result, err = tx.ExecContext(ctx, "UPDATE credentials SET name=?,enabled=?,key_id=?,ciphertext=?,revision=revision+1 WHERE space_id=? AND id=? AND revision=?", c.Name, c.Enabled, c.KeyID, c.Ciphertext, space, c.ID, c.Revision)
			} else {
				result, err = tx.ExecContext(ctx, "UPDATE credentials SET name=?,enabled=?,revision=revision+1 WHERE space_id=? AND id=? AND revision=?", c.Name, c.Enabled, space, c.ID, c.Revision)
			}
			if err == nil {
				if n, _ := result.RowsAffected(); n != 1 {
					return identity.ErrConflict
				}
			}
		}
		if err != nil {
			return err
		}
		return audit(ctx, tx, actor, space, "credential.saved", c.ID)
	})
}

const modelColumns = "m.id,m.name,m.provider,m.model_id,m.endpoint,m.credential_id,m.context_window,m.max_output_tokens,m.temperature,m.enabled,m.revision"

func scanModel(scan func(...any) error) (m identity.Model, err error) {
	err = scan(&m.ID, &m.Name, &m.Provider, &m.ModelID, &m.Endpoint, &m.CredentialID, &m.ContextWindow, &m.MaxOutputTokens, &m.Temperature, &m.Enabled, &m.Revision)
	return
}
func (s *Store) Models(ctx context.Context, actor, space string, q identity.Query, available bool) (items []identity.Model, err error) {
	items = []identity.Model{}
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		query := "SELECT " + modelColumns + " FROM model_configs m JOIN credentials c ON c.space_id=m.space_id AND c.id=m.credential_id WHERE m.space_id=? AND m.id>?"
		if available {
			query += " AND m.enabled=TRUE AND c.enabled=TRUE"
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
		var enabled bool
		if err := tx.QueryRowContext(ctx, "SELECT enabled FROM credentials WHERE space_id=? AND id=?", space, m.CredentialID).Scan(&enabled); err != nil {
			return err
		}
		if m.Enabled && !enabled {
			return identity.ErrInvalid
		}
		var err error
		if create {
			_, err = tx.ExecContext(ctx, "INSERT INTO model_configs(space_id,id,name,provider,model_id,endpoint,credential_id,context_window,max_output_tokens,temperature,enabled) VALUES(?,?,?,?,?,?,?,?,?,?,?)", space, m.ID, m.Name, m.Provider, m.ModelID, m.Endpoint, m.CredentialID, m.ContextWindow, m.MaxOutputTokens, m.Temperature, m.Enabled)
		} else {
			result, e := tx.ExecContext(ctx, "UPDATE model_configs SET name=?,provider=?,model_id=?,endpoint=?,credential_id=?,context_window=?,max_output_tokens=?,temperature=?,enabled=?,revision=revision+1 WHERE space_id=? AND id=? AND revision=?", m.Name, m.Provider, m.ModelID, m.Endpoint, m.CredentialID, m.ContextWindow, m.MaxOutputTokens, m.Temperature, m.Enabled, space, m.ID, m.Revision)
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

// ResolveCredential is a server-only consumption path. It rechecks membership,
// model and credential enablement together immediately before a future call.
func (s *Store) ResolveCredential(ctx context.Context, actor, space, modelID string) (c identity.Credential, err error) {
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT c.id,c.name,c.key_id,c.ciphertext,c.enabled,c.revision FROM credentials c JOIN model_configs m ON m.space_id=c.space_id AND m.credential_id=c.id WHERE m.space_id=? AND m.id=? AND m.enabled=TRUE AND c.enabled=TRUE", space, modelID).Scan(&c.ID, &c.Name, &c.KeyID, &c.Ciphertext, &c.Enabled, &c.Revision)
	})
	return
}

// The run fixes a credential ID; rotation may replace that credential's secret,
// while later edits to model configuration never redirect an existing run.
func (s *Store) ResolveRuntimeCredential(ctx context.Context, actor, space, model, credential string) (c identity.Credential, err error) {
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT c.id,c.name,c.key_id,c.ciphertext,c.enabled,c.revision FROM credentials c JOIN model_configs m ON m.space_id=c.space_id WHERE m.space_id=? AND m.id=? AND c.id=? AND m.enabled=TRUE AND c.enabled=TRUE", space, model, credential).Scan(&c.ID, &c.Name, &c.KeyID, &c.Ciphertext, &c.Enabled, &c.Revision)
	})
	return
}
