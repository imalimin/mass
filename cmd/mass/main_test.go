package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestMigrateUsesOnlyMigrationDSN(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mass.yaml")
	if err := os.WriteFile(path, []byte("database:\n  migration_dsn_env: TEST_MIGRATION_DSN\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_MIGRATION_DSN", "invalid-dsn")
	err := run(context.Background(), []string{"migrate", "--config", path}, io.Discard, io.Discard)
	if err == nil || err.Error() != "invalid database DSN" {
		t.Fatalf("migrate should reach database DSN validation: %v", err)
	}
}

func TestServeLANFlagsRequireOptIn(t *testing.T) {
	for _, flag := range []string{"--port=8080", "--lan-ip=192.168.1.20"} {
		err := run(context.Background(), []string{"serve", flag}, io.Discard, io.Discard)
		if err == nil || err.Error() != "--lan-ip and --port require --lan" {
			t.Fatalf("expected %s to require LAN mode: %v", flag, err)
		}
	}
}
