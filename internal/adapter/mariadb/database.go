package mariadb

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/imalimin/mass/db/migrations"
	"github.com/imalimin/mass/internal/app/identity"
	"github.com/pressly/goose/v3"
)

type Store struct{ DB *sql.DB }

func Open(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, errors.New("invalid database DSN")
	}
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	cfg.MultiStatements = false
	cfg.Timeout = 5 * time.Second
	cfg.ReadTimeout = 10 * time.Second
	cfg.WriteTimeout = 10 * time.Second
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	cfg.Params["time_zone"] = "'+00:00'"
	delete(cfg.Params, "charset")
	if err = cfg.Apply(mysql.Charset("utf8mb4", "utf8mb4_bin")); err != nil {
		return nil, err
	}
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, errors.New("invalid database configuration")
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(5 * time.Minute)
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		return nil, errors.New("database connection failed")
	}
	return &Store{db}, nil
}
func (s *Store) Migrate(ctx context.Context) error {
	p, err := goose.NewProvider(goose.DialectMySQL, s.DB, migrations.Files)
	if err != nil {
		return err
	}
	_, err = p.Up(ctx)
	return err
}
func (s *Store) CheckSchema(ctx context.Context) error {
	var version int64
	if err := s.DB.QueryRowContext(ctx, "SELECT MAX(version_id) FROM goose_db_version WHERE is_applied=1").Scan(&version); err != nil {
		return errors.New("database schema unavailable; run mass migrate")
	}
	if version != 3 {
		return errors.New("database schema mismatch; run mass migrate with the matching binary")
	}
	return nil
}
func dbError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return identity.ErrNotFound
	}
	var e *mysql.MySQLError
	if errors.As(err, &e) && e.Number == 1062 {
		return identity.ErrConflict
	}
	return err
}
func (s *Store) transaction(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		return dbError(err)
	}
	return dbError(tx.Commit())
}

// Lock order: space, membership, resource. This serializes revocation with
// resource reads/writes. The callback cannot execute model or other network I/O.
func authorize(ctx context.Context, tx *sql.Tx, actor, space string, owner bool) error {
	var id string
	if err := tx.QueryRowContext(ctx, "SELECT id FROM spaces WHERE id=? FOR UPDATE", space).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return identity.ErrForbidden
		}
		return err
	}
	var isOwner bool
	if err := tx.QueryRowContext(ctx, "SELECT is_owner FROM space_members WHERE space_id=? AND user_id=?", space, actor).Scan(&isOwner); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return identity.ErrForbidden
		}
		return err
	}
	if owner && !isOwner {
		return identity.ErrForbidden
	}
	return nil
}
func (s *Store) scoped(ctx context.Context, actor, space string, owner bool, fn func(*sql.Tx) error) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if err := authorize(ctx, tx, actor, space, owner); err != nil {
			return err
		}
		return fn(tx)
	})
}
func audit(ctx context.Context, tx *sql.Tx, actor, space, action, target string) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO audit_logs(space_id,actor_id,action,target_id) VALUES(?,?,?,?)", space, actor, action, target)
	return err
}
func (s *Store) CheckAccess(ctx context.Context, actor, space string, owner bool) error {
	return s.scoped(ctx, actor, space, owner, func(*sql.Tx) error { return nil })
}
