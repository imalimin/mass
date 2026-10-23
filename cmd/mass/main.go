package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/imalimin/mass/internal/adapter/mariadb"
	"github.com/imalimin/mass/internal/app/identity"
	"github.com/imalimin/mass/internal/bootstrap"
	"time"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: mass <serve|migrate|rebuild-views|user create|check-config|version> [--config mass.yaml]")
	}
	if args[0] == "version" {
		if len(args) != 1 {
			return errors.New("version takes no arguments")
		}
		_, err := fmt.Fprintln(out, version)
		return err
	}
	if args[0] == "user" {
		if len(args) < 2 || args[1] != "create" {
			return errors.New("usage: mass user create --username NAME --password-env ENV --config mass.yaml")
		}
		args = append([]string{"user-create"}, args[2:]...)
	}
	if args[0] != "serve" && args[0] != "check-config" && args[0] != "migrate" && args[0] != "rebuild-views" && args[0] != "user-create" {
		return errors.New("unknown command; available: serve, migrate, rebuild-views, user create, check-config, version")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "mass.yaml", "single service configuration file")
	var username, passwordEnv *string
	if args[0] == "user-create" {
		username = flags.String("username", "", "existing account name")
		passwordEnv = flags.String("password-env", "", "environment variable holding the password (12-72 bytes)")
	}
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	c, err := bootstrap.LoadConfig(*configPath, os.Getenv)
	if err != nil {
		return err
	}
	if args[0] == "check-config" {
		_, err = fmt.Fprintln(out, "configuration valid")
		return err
	}
	if args[0] == "rebuild-views" {
		if err := bootstrap.RebuildViews(ctx, c); err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, "views rebuilt from journal")
		return err
	}
	if args[0] == "migrate" || args[0] == "user-create" {
		dsn := os.Getenv(c.Database.DSNEnv)
		if args[0] == "migrate" {
			if c.Database.MigrationDSNEnv == "" || os.Getenv(c.Database.MigrationDSNEnv) == "" {
				return errors.New("database.migration_dsn_env requires a nonempty environment value")
			}
			dsn = os.Getenv(c.Database.MigrationDSNEnv)
		}
		store, err := mariadb.Open(ctx, dsn)
		if err != nil {
			return err
		}
		defer store.DB.Close()
		if args[0] == "migrate" {
			if err = store.Migrate(ctx); err != nil {
				return errors.New("migration failed; inspect database schema before retrying")
			}
			_, err = fmt.Fprintln(out, "migrations applied")
			return err
		}
		if err = store.CheckSchema(ctx); err != nil {
			return err
		}
		if *passwordEnv == "" {
			return errors.New("--password-env is required")
		}
		svc, err := identity.New(store, nil, time.Hour, nil)
		if err != nil {
			return err
		}
		u, err := svc.CreateUser(ctx, *username, os.Getenv(*passwordEnv))
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, u.ID)
		return err
	}
	return bootstrap.Serve(ctx, c, version, slog.New(slog.NewJSONHandler(out, nil)))
}
