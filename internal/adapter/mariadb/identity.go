package mariadb

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/imalimin/mass/internal/app/identity"
)

func (s *Store) CreateUser(ctx context.Context, u identity.User, hash []byte) error {
	_, err := s.DB.ExecContext(ctx, "INSERT INTO users(id,username,password_hash) VALUES(?,?,?)", u.ID, u.Username, hash)
	return dbError(err)
}
func (s *Store) Password(ctx context.Context, name string) (u identity.User, hash []byte, err error) {
	err = s.DB.QueryRowContext(ctx, "SELECT id,username,password_hash FROM users WHERE username=?", name).Scan(&u.ID, &u.Username, &hash)
	return u, hash, dbError(err)
}
func (s *Store) SaveSession(ctx context.Context, hash []byte, user string, expiry time.Time) error {
	_, err := s.DB.ExecContext(ctx, "INSERT INTO auth_sessions(token_hash,user_id,expires_at) VALUES(?,?,?)", hash, user, expiry)
	return err
}
func (s *Store) Session(ctx context.Context, hash []byte) (u identity.User, err error) {
	err = s.DB.QueryRowContext(ctx, "SELECT u.id,u.username FROM auth_sessions a JOIN users u ON u.id=a.user_id WHERE a.token_hash=? AND a.expires_at>UTC_TIMESTAMP(6)", hash).Scan(&u.ID, &u.Username)
	if errors.Is(err, sql.ErrNoRows) {
		err = identity.ErrUnauthenticated
	}
	return
}
func (s *Store) DeleteSession(ctx context.Context, hash []byte) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM auth_sessions WHERE token_hash=?", hash)
	return err
}
func (s *Store) CreateSpace(ctx context.Context, actor string, space identity.Space) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO spaces(id,name,created_at) VALUES(?,?,?)", space.ID, space.Name, space.CreatedAt); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO space_members(space_id,user_id,is_owner) VALUES(?,?,TRUE)", space.ID, actor); err != nil {
			return err
		}
		return audit(ctx, tx, actor, space.ID, "space.created", space.ID)
	})
}
func (s *Store) Spaces(ctx context.Context, actor string, q identity.Query) ([]identity.Space, error) {
	items := []identity.Space{}
	rows, err := s.DB.QueryContext(ctx, "SELECT s.id,s.name,m.is_owner,s.created_at FROM spaces s JOIN space_members m ON m.space_id=s.id WHERE m.user_id=? AND s.id>? ORDER BY s.id LIMIT ?", actor, q.After, q.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v identity.Space
		if err = rows.Scan(&v.ID, &v.Name, &v.IsOwner, &v.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, v)
	}
	return items, rows.Err()
}
func (s *Store) Members(ctx context.Context, actor, space string, q identity.Query) (items []identity.Member, err error) {
	items = []identity.Member{}
	err = s.scoped(ctx, actor, space, false, func(tx *sql.Tx) error {
		rows, e := tx.QueryContext(ctx, "SELECT u.id,u.username,m.is_owner FROM space_members m JOIN users u ON u.id=m.user_id WHERE m.space_id=? AND u.id>? ORDER BY u.id LIMIT ?", space, q.After, q.Limit)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v identity.Member
			if e = rows.Scan(&v.ID, &v.Username, &v.IsOwner); e != nil {
				return e
			}
			items = append(items, v)
		}
		return rows.Err()
	})
	return
}
func (s *Store) AddMember(ctx context.Context, actor, space, name string) error {
	return s.scoped(ctx, actor, space, true, func(tx *sql.Tx) error {
		var user string
		if err := tx.QueryRowContext(ctx, "SELECT id FROM users WHERE username=?", name).Scan(&user); err != nil {
			return err
		}
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM space_members WHERE space_id=? AND user_id=?", space, user).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			return nil
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO space_members(space_id,user_id,is_owner) VALUES(?,?,FALSE)", space, user); err != nil {
			return err
		}
		return audit(ctx, tx, actor, space, "member.added", user)
	})
}
func (s *Store) RemoveMember(ctx context.Context, actor, space, user string) error {
	return s.scoped(ctx, actor, space, actor != user, func(tx *sql.Tx) error {
		var owner bool
		err := tx.QueryRowContext(ctx, "SELECT is_owner FROM space_members WHERE space_id=? AND user_id=?", space, user).Scan(&owner)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if owner {
			return identity.ErrForbidden
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM space_members WHERE space_id=? AND user_id=?", space, user); err != nil {
			return err
		}
		return audit(ctx, tx, actor, space, "member.removed", user)
	})
}
