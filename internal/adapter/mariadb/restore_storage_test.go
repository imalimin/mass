package mariadb

import (
	"io/fs"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/imalimin/mass/db/migrations"
)

// Adding a content FK must also extend restore preflight, even when no Journal
// event refers to it (draft resources and unaccepted attachments are examples).
func TestRestoreCoversEveryContentForeignKey(t *testing.T) {
	files, err := fs.Glob(migrations.Files, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	tablePattern := regexp.MustCompile(`(?i)(?:CREATE|ALTER) TABLE (?:IF NOT EXISTS )?(\w+)`)
	dropPattern := regexp.MustCompile(`(?i)DROP TABLE (?:IF EXISTS )?(\w+)`)
	fkPattern := regexp.MustCompile(`(?i)FOREIGN KEY\(([^)]+)\) REFERENCES content_objects\(([^)]+)\)`)
	want := map[string]bool{}
	for _, name := range files {
		raw, err := migrations.Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, statement := range strings.Split(string(raw), ";") {
			if dropped := dropPattern.FindStringSubmatch(statement); dropped != nil {
				for ref := range want {
					if strings.HasPrefix(ref, dropped[1]+".") {
						delete(want, ref)
					}
				}
				continue
			}
			table := tablePattern.FindStringSubmatch(statement)
			for _, fk := range fkPattern.FindAllStringSubmatch(statement, -1) {
				if len(table) != 2 {
					t.Fatal("unrecognized content reference", statement)
				}
				local, remote := strings.Split(fk[1], ","), strings.Split(fk[2], ",")
				for i, column := range remote {
					if strings.TrimSpace(column) == "id" {
						want[table[1]+"."+strings.TrimSpace(local[i])] = true
					}
				}
			}
		}
	}
	got := map[string]bool{}
	for _, ref := range regexp.MustCompile(`SELECT space_id,(\w+) FROM (\w+)`).FindAllStringSubmatch(restoredInvalidObjects, -1) {
		got[ref[2]+"."+ref[1]] = true
	}
	if len(want) == 0 || !reflect.DeepEqual(got, want) {
		t.Fatalf("restore reference coverage = %v; schema requires %v", got, want)
	}
}
