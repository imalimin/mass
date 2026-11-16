package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/imalimin/mass/db/migrations"
	"github.com/imalimin/mass/internal/adapter/mariadb"
	"github.com/imalimin/mass/internal/app/identity"
	"github.com/imalimin/mass/internal/bootstrap"
)

var version = "dev"
var revision = "unknown"
var buildTime = "unknown"

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
		return errors.New("usage: mass <serve|migrate|restore-storage|user create|member add|member remove|check-config|version|build-info> [--config mass.yaml]")
	}
	if args[0] == "build-info" {
		if len(args) != 1 {
			return errors.New("build-info takes no arguments")
		}
		return json.NewEncoder(out).Encode(map[string]any{"version": version, "revision": revision, "build_time": buildTime, "go_version": runtime.Version(), "platform": runtime.GOOS + "/" + runtime.GOARCH, "schema": migrations.CurrentVersion})
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
	if args[0] == "member" {
		if len(args) < 2 || (args[1] != "add" && args[1] != "remove") {
			return errors.New("usage: mass member <add|remove> --actor OWNER_ID --space SPACE_ID --username NAME (add) | --user USER_ID (remove)")
		}
		args = append([]string{"member-" + args[1]}, args[2:]...)
	}
	member := args[0] == "member-add" || args[0] == "member-remove"
	if args[0] != "serve" && args[0] != "check-config" && args[0] != "migrate" && args[0] != "restore-storage" && args[0] != "user-create" && !member {
		return errors.New("unknown command; available: serve, migrate, restore-storage, user create, member add/remove, check-config, version, build-info")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "mass.yaml", "single service configuration file")
	var username, passwordEnv *string
	var actor, space, memberName, user *string
	var sourceConfig, expectedDatabase *string
	var lan *bool
	var lanIP *string
	var port *int
	if args[0] == "serve" {
		lan = flags.Bool("lan", false, "serve HTTP on a private LAN address")
		lanIP = flags.String("lan-ip", "", "local private IP address to use with --lan")
		port = flags.Int("port", 8080, "TCP port to use with --lan")
	}
	if args[0] == "restore-storage" {
		sourceConfig = flags.String("source-config", "", "original configuration for retained source TOS objects")
		expectedDatabase = flags.String("expect-database", "", "exact isolated restored database name")
	}
	if member {
		actor = flags.String("actor", "", "existing space owner ID; command requires database operator access")
		space = flags.String("space", "", "space ID")
		if args[0] == "member-add" {
			memberName = flags.String("username", "", "existing username")
		} else {
			user = flags.String("user", "", "existing user ID")
		}
	}
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
	if lan != nil && !*lan {
		hasLANOptions := false
		flags.Visit(func(f *flag.Flag) { hasLANOptions = hasLANOptions || f.Name == "lan-ip" || f.Name == "port" })
		if hasLANOptions {
			return errors.New("--lan-ip and --port require --lan")
		}
	}
	c, err := bootstrap.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	if lan != nil && *lan {
		if err := c.EnableLAN(*lanIP, *port); err != nil {
			return err
		}
	}
	databaseCommand := args[0] == "migrate" || args[0] == "user-create" || member
	if !databaseCommand {
		if err := c.ValidateService(os.Getenv); err != nil {
			return err
		}
	}
	if args[0] == "check-config" {
		_, err = fmt.Fprintln(out, "configuration valid")
		return err
	}
	if args[0] == "restore-storage" {
		if *sourceConfig == "" || *expectedDatabase == "" {
			return errors.New("restore-storage requires --source-config and --expect-database")
		}
		source, err := bootstrap.LoadConfig(*sourceConfig)
		if err != nil {
			return err
		}
		if err := source.ValidateService(os.Getenv); err != nil {
			return err
		}
		result, err := bootstrap.RestoreStorage(ctx, source, c, *expectedDatabase)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "verified and relocated %d objects; preserved %d unreferenced uploading intents (not copied)\n", result.Relocated, result.UnreferencedUploads)
		return err
	}
	if databaseCommand {
		field, env := "database.dsn_env", c.Database.DSNEnv
		if args[0] == "migrate" {
			field, env = "database.migration_dsn_env", c.Database.MigrationDSNEnv
		}
		dsn := os.Getenv(env)
		if strings.TrimSpace(dsn) == "" {
			return fmt.Errorf("%s requires a nonempty environment value", field)
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
		if member {
			lease, err := store.OpenRuntime(ctx)
			if err != nil {
				return errors.New("member CLI requires the service to be stopped; use the authenticated member API while running")
			}
			defer lease.Close()
			if *actor == "" || *space == "" {
				return errors.New("member requires --actor and --space")
			}
			svc, err := identity.New(store, nil, time.Hour, nil)
			if err != nil {
				return err
			}
			if args[0] == "member-add" {
				err = svc.AddMember(ctx, *actor, *space, *memberName)
			} else {
				err = svc.RemoveMember(ctx, *actor, *space, *user)
			}
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(out, "membership updated")
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
