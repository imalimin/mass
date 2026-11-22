package archive

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/imalimin/mass/internal/app/catalog"
	"github.com/imalimin/mass/internal/app/identity"
	"github.com/imalimin/mass/tests/fakes"
)

func TestCatalogListMetadataUsesPublishedAndRecordedChanges(t *testing.T) {
	day := func(n int) time.Time { return time.Date(2026, time.November, n, 0, 0, 0, 0, time.UTC) }
	index := catalogIndex{
		"a": {Definition: catalog.Definition{ID: "a", Kind: "agent", DefaultVersionID: "first"}, Versions: []*version{
			{ID: "first", Label: "v1", CreatedAt: day(1)},
			{ID: "latest", Label: "release", CreatedAt: day(2)},
			{ID: "archived", Label: "v3", CreatedAt: day(3), Archived: true},
			{ID: "debug", CreatedAt: day(4), Debug: true},
		}},
		"b": {Definition: catalog.Definition{ID: "b", Kind: "agent"}},
		"c": {Definition: catalog.Definition{ID: "c", Kind: "agent"}},
		"skill": {Definition: catalog.Definition{ID: "skill", Kind: "skill"}, Versions: []*version{
			{ID: "skill-release", Label: "v1", CreatedAt: day(3)},
		}},
	}
	raw, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	blobs := fakes.NewBlobs()
	blobs.Data["space/catalog.json"] = raw
	c := &Catalog{
		Blobs:   blobs,
		Objects: &Objects{Authorize: func(context.Context, string, string, bool) error { return nil }},
		UpdateTimes: func(_ context.Context, _, _ string, ids []string) (map[string]time.Time, error) {
			if slices.Equal(ids, []string{"skill"}) {
				return nil, nil
			}
			if !slices.Equal(ids, []string{"a", "b"}) {
				t.Fatalf("read updates outside the requested page: %v", ids)
			}
			return map[string]time.Time{"a": day(5), "b": day(1)}, nil
		},
	}
	items, err := c.CatalogList(t.Context(), "actor", "space", "agent", identity.Query{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].LatestVersion != "release" || !items[0].UpdatedAt.Equal(day(5)) ||
		items[1].LatestVersion != "" || !items[1].UpdatedAt.Equal(day(1)) {
		t.Fatalf("unexpected list metadata: %+v", items)
	}
	items, err = c.CatalogList(t.Context(), "actor", "space", "skill", identity.Query{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].LatestVersion != "v1" || !items[0].UpdatedAt.Equal(day(3)) {
		t.Fatalf("unexpected Skill list metadata: %+v", items)
	}
}
