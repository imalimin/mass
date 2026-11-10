package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/imalimin/mass/db/migrations"
	"github.com/imalimin/mass/tests/testdb"
	"io"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/go-sql-driver/mysql"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "test database fixture:", err)
		os.Exit(1)
	}
}
func run() error {
	version := migrations.CurrentVersion
	if os.Getenv("MASS_TEST_EMPTY_DATABASE") == "1" {
		version = 0
	}
	d, err := testdb.NewVersion(context.Background(), version)
	if err != nil {
		return err
	}
	defer d.Close()
	c, err := mysql.ParseDSN(d.MigrationDSN)
	if err != nil {
		return err
	}
	quote := func(v string) string {
		return "\"" + strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\n", "\\n", "\r", "\\r").Replace(v) + "\""
	}
	options := "[client]\nuser=" + quote(c.User) + "\npassword=" + quote(c.Passwd) + "\n"
	if c.Net == "unix" {
		options += "protocol=socket\nsocket=" + quote(c.Addr) + "\n"
	} else if c.Net == "tcp" {
		host, port, e := net.SplitHostPort(c.Addr)
		if e != nil {
			return e
		}
		if _, e = strconv.Atoi(port); e != nil {
			return e
		}
		options += "protocol=tcp\nhost=" + quote(host) + "\nport=" + port + "\n"
	} else {
		return fmt.Errorf("test client requires unix or tcp DSN")
	}
	if err = json.NewEncoder(os.Stdout).Encode(map[string]string{"dsn": d.DSN, "migration_dsn": d.MigrationDSN, "name": d.Name, "client_options": options}); err != nil {
		return err
	}
	_, err = io.Copy(io.Discard, os.Stdin)
	return err
}
