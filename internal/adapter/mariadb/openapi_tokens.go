package mariadb

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/imalimin/mass/internal/app/identity"
)

func (s *Store) OpenAPITokens(ctx context.Context, actor, space string, q identity.Query) (items []identity.OpenAPIToken, err error) {
	items = []identity.OpenAPIToken{}
	err = s.scoped(ctx, actor, space, true, func(tx *sql.Tx) error {
		var dbNow time.Time
		if e := tx.QueryRowContext(ctx, "SELECT UTC_TIMESTAMP(6)").Scan(&dbNow); e != nil {
			return e
		}
		rows, e := tx.QueryContext(ctx, "SELECT id,space_id,issuer_id,name,created_at,expires_at,revoked_at FROM space_openapi_tokens WHERE space_id=? AND id>? ORDER BY id LIMIT ?", space, q.After, q.Limit)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v identity.OpenAPIToken
			var revoked sql.NullTime
			if e = rows.Scan(&v.ID, &v.SpaceID, &v.IssuerID, &v.Name, &v.CreatedAt, &v.ExpiresAt, &revoked); e != nil {
				return e
			}
			if revoked.Valid {
				t := revoked.Time.UTC()
				v.RevokedAt = &t
			}
			v.CreatedAt = v.CreatedAt.UTC()
			v.ExpiresAt = v.ExpiresAt.UTC()
			v.Status = identity.OpenAPITokenStatus(v.ExpiresAt, v.RevokedAt, dbNow.UTC())
			items = append(items, v)
		}
		return rows.Err()
	})
	return
}

func (s *Store) CreateOpenAPIToken(ctx context.Context, actor, space string, tok identity.OpenAPIToken) error {
	return s.scoped(ctx, actor, space, true, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO space_openapi_tokens(id,space_id,issuer_id,name,token_hash,key_id,ciphertext,created_at,expires_at,revoked_at) VALUES(?,?,?,?,?,?,?,?,?,NULL)",
			tok.ID, space, tok.IssuerID, tok.Name, tok.TokenHash, tok.KeyID, tok.Ciphertext, tok.CreatedAt, tok.ExpiresAt,
		); err != nil {
			return err
		}
		return audit(ctx, tx, actor, space, "openapi_token.created", tok.ID)
	})
}

func (s *Store) OpenAPITokenSecret(ctx context.Context, actor, space, id string) (tok identity.OpenAPIToken, err error) {
	err = s.scoped(ctx, actor, space, true, func(tx *sql.Tx) error {
		var revoked sql.NullTime
		var dbNow time.Time
		if e := tx.QueryRowContext(ctx,
			"SELECT id,space_id,issuer_id,name,key_id,ciphertext,created_at,expires_at,revoked_at,UTC_TIMESTAMP(6) FROM space_openapi_tokens WHERE space_id=? AND id=? FOR UPDATE",
			space, id,
		).Scan(&tok.ID, &tok.SpaceID, &tok.IssuerID, &tok.Name, &tok.KeyID, &tok.Ciphertext, &tok.CreatedAt, &tok.ExpiresAt, &revoked, &dbNow); e != nil {
			return e
		}
		tok.CreatedAt = tok.CreatedAt.UTC()
		tok.ExpiresAt = tok.ExpiresAt.UTC()
		if revoked.Valid {
			t := revoked.Time.UTC()
			tok.RevokedAt = &t
			return identity.ErrConflict
		}
		if !tok.ExpiresAt.After(dbNow.UTC()) {
			return identity.ErrConflict
		}
		tok.Status = identity.OpenAPITokenActive
		return audit(ctx, tx, actor, space, "openapi_token.copied", tok.ID)
	})
	return
}

func (s *Store) RenewOpenAPIToken(ctx context.Context, actor, space, id string, expiresAt time.Time) (tok identity.OpenAPIToken, err error) {
	err = s.scoped(ctx, actor, space, true, func(tx *sql.Tx) error {
		var revoked sql.NullTime
		if e := tx.QueryRowContext(ctx,
			"SELECT id,space_id,issuer_id,name,created_at,expires_at,revoked_at FROM space_openapi_tokens WHERE space_id=? AND id=? FOR UPDATE",
			space, id,
		).Scan(&tok.ID, &tok.SpaceID, &tok.IssuerID, &tok.Name, &tok.CreatedAt, &tok.ExpiresAt, &revoked); e != nil {
			return e
		}
		if revoked.Valid {
			return identity.ErrConflict
		}
		if _, e := tx.ExecContext(ctx, "UPDATE space_openapi_tokens SET expires_at=? WHERE space_id=? AND id=?", expiresAt, space, id); e != nil {
			return e
		}
		tok.CreatedAt = tok.CreatedAt.UTC()
		tok.ExpiresAt = expiresAt.UTC()
		tok.RevokedAt = nil
		return audit(ctx, tx, actor, space, "openapi_token.renewed", tok.ID)
	})
	return
}

func (s *Store) RevokeOpenAPIToken(ctx context.Context, actor, space, id string, revokedAt time.Time) error {
	return s.scoped(ctx, actor, space, true, func(tx *sql.Tx) error {
		var existing sql.NullTime
		if err := tx.QueryRowContext(ctx, "SELECT revoked_at FROM space_openapi_tokens WHERE space_id=? AND id=? FOR UPDATE", space, id).Scan(&existing); err != nil {
			return err
		}
		if existing.Valid {
			return nil
		}
		if _, err := tx.ExecContext(ctx, "UPDATE space_openapi_tokens SET revoked_at=? WHERE space_id=? AND id=?", revokedAt, space, id); err != nil {
			return err
		}
		return audit(ctx, tx, actor, space, "openapi_token.revoked", id)
	})
}

func (s *Store) AuthenticateBearerToken(ctx context.Context, hash []byte) (p identity.BearerPrincipal, err error) {
	err = s.DB.QueryRowContext(ctx,
		"SELECT t.id,t.space_id,u.id,u.username FROM space_openapi_tokens t JOIN users u ON u.id=t.issuer_id WHERE t.token_hash=? AND t.revoked_at IS NULL AND t.expires_at>UTC_TIMESTAMP(6)",
		hash,
	).Scan(&p.TokenID, &p.SpaceID, &p.User.ID, &p.User.Username)
	if errors.Is(err, sql.ErrNoRows) {
		err = identity.ErrUnauthenticated
	}
	return
}
