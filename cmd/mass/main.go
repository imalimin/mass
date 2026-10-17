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

	"github.com/imalimin/mass/internal/bootstrap"
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
		return errors.New("usage: mass <serve|check-config|version> [--config mass.yaml]")
	}
	if args[0] == "version" {
		if len(args) != 1 {
			return errors.New("version takes no arguments")
		}
		_, err := fmt.Fprintln(out, version)
		return err
	}
	if args[0] != "serve" && args[0] != "check-config" {
		return errors.New("unknown command; available: serve, check-config, version")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "mass.yaml", "single service configuration file")
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
	return bootstrap.Serve(ctx, c, version, slog.New(slog.NewJSONHandler(out, nil)))
}
