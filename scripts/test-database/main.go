package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/imalimin/mass/tests/testdb"
	"io"
	"os"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "test database fixture:", err)
		os.Exit(1)
	}
}
func run() error {
	d, err := testdb.New(context.Background())
	if err != nil {
		return err
	}
	defer d.Close()
	if err = json.NewEncoder(os.Stdout).Encode(map[string]string{"dsn": d.DSN, "migration_dsn": d.MigrationDSN}); err != nil {
		return err
	}
	_, err = io.Copy(io.Discard, os.Stdin)
	return err
}
