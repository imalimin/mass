// Package testdb provisions only randomly named databases on an explicitly
// supplied test server. It never reads MASS_MARIADB_DSN or deployment config.
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"

	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/adapter/mariadb"
)

type Database struct {
	Admin        *mariadb.Store
	Store        *mariadb.Store
	DSN          string
	MigrationDSN string
	Name         string
	cleanup      func()
}

func (d *Database) Close() { d.cleanup() }
func New(ctx context.Context) (*Database, error) {
	source := os.Getenv("MASS_TEST_ADMIN_DSN")
	if source == "" {
		return nil, errors.New("MASS_TEST_ADMIN_DSN must explicitly designate a disposable test server")
	}
	admin, err := mariadb.Open(ctx, source)
	if err != nil {
		return nil, err
	}
	name := "mass_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	user := "mass_test_" + name[len(name)-16:]
	secret := make([]byte, 24)
	if _, err = rand.Read(secret); err != nil {
		admin.DB.Close()
		return nil, err
	}
	password := hex.EncodeToString(secret)
	d := &Database{Name: name}
	d.cleanup = func() {
		if d.Store != nil {
			d.Store.DB.Close()
		}
		if d.Admin != nil {
			d.Admin.DB.Close()
		}
		admin.DB.Exec("DROP DATABASE IF EXISTS `" + name + "`")
		admin.DB.Exec("DROP USER IF EXISTS '" + user + "'@'localhost'")
		admin.DB.Exec("DROP USER IF EXISTS '" + user + "'@'%'")
		admin.DB.Close()
	}
	fail := func(e error) (*Database, error) { d.Close(); return nil, e }
	if _, err = admin.DB.ExecContext(ctx, "CREATE DATABASE `"+name+"` CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"); err != nil {
		return fail(err)
	}
	// Both host variants allow local Unix sockets and isolated CI TCP containers.
	for _, host := range []string{"localhost", "%"} {
		if _, err = admin.DB.ExecContext(ctx, "CREATE USER '"+user+"'@'"+host+"' IDENTIFIED BY '"+password+"'"); err != nil {
			return fail(err)
		}
		if _, err = admin.DB.ExecContext(ctx, "GRANT SELECT, INSERT, UPDATE, DELETE ON `"+name+"`.* TO '"+user+"'@'"+host+"'"); err != nil {
			return fail(err)
		}
	}
	cfg, err := mysql.ParseDSN(source)
	if err != nil {
		return fail(err)
	}
	cfg.DBName = name
	d.MigrationDSN = cfg.FormatDSN()
	d.Admin, err = mariadb.Open(ctx, d.MigrationDSN)
	if err != nil {
		return fail(err)
	}
	if err = d.Admin.Migrate(ctx); err != nil {
		return fail(err)
	}
	if err = d.Admin.Migrate(ctx); err != nil {
		return fail(err)
	}
	cfg.User = user
	cfg.Passwd = password
	d.DSN = cfg.FormatDSN()
	d.Store, err = mariadb.Open(ctx, d.DSN)
	if err != nil {
		return fail(err)
	}
	return d, nil
}
