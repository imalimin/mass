package dependencies_test

import (
	"database/sql"
	"testing"
	"testing/fstest"

	"github.com/bmatcuk/doublestar/v4"
	_ "github.com/go-sql-driver/mysql"
	"github.com/pressly/goose/v3"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/volcengine/ve-tos-golang-sdk/v2/tos"
)

// Compile and constructor checks only. These do not claim MariaDB/TOS
// integration success and never connect to external storage.
func TestStorageDependencyContracts(t *testing.T) {
	db, err := sql.Open("mysql", "fixture:fixture@tcp(127.0.0.1:1)/mass_test")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fixtureMigrations := fstest.MapFS{"00001_fixture.sql": {Data: []byte("-- +goose NO TRANSACTION\n-- +goose Up\nCREATE TABLE fixture (id BIGINT PRIMARY KEY);\n-- +goose Down\nDROP TABLE fixture;\n")}}
	_, err = goose.NewProvider(goose.DialectMySQL, db, fixtureMigrations, goose.WithDisableGlobalRegistry(true))
	if err != nil {
		t.Fatal(err)
	}
	_ = tos.PutObjectV2Input{PutObjectBasicInput: tos.PutObjectBasicInput{Bucket: "test-only", Key: "unique-object", ForbidOverwrite: true, ContentLength: 3}}
	if ok, err := doublestar.Match("reports/**/*.csv", "reports/2026/q1.csv"); err != nil || !ok {
		t.Fatalf("glob: %v %v", ok, err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("urn:mass:tool", map[string]any{"type": "object", "required": []any{"path"}}); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("urn:mass:tool")
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(map[string]any{}); err == nil {
		t.Fatal("schema accepted missing required field")
	}
}
