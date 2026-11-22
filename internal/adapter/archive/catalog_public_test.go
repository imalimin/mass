package archive

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/imalimin/mass/internal/app/catalog"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/tests/fakes"
)

func TestPublicAgentVersionSelectsLatestActiveFormalVersion(t *testing.T) {
	day := func(n int) time.Time { return time.Date(2026, time.November, n, 0, 0, 0, 0, time.UTC) }
	ref := func(name string) Ref {
		return Reference(storage.Object{ID: "obj-" + name, Key: "space/obj-" + name, SHA256: "sha-" + name})
	}
	index := catalogIndex{
		"public-agent": {
			Definition: catalog.Definition{
				ID:               "public-agent",
				Kind:             "agent",
				Name:             "agent_creator",
				Visibility:       catalog.VisibilityPublic,
				DefaultVersionID: "v1",
			},
			Versions: []*version{
				{ID: "v1", Label: "v1", CreatedAt: day(1), Object: ref("v1")},
				{ID: "v2", Label: "v2", CreatedAt: day(2), Object: ref("v2")},
				{ID: "v3", Label: "v3", CreatedAt: day(3), Archived: true, Object: ref("v3")},
				{ID: "debug", CreatedAt: day(4), Debug: true, Object: ref("debug")},
			},
		},
		"all-archived": {
			Definition: catalog.Definition{
				ID:               "all-archived",
				Kind:             "agent",
				Name:             "archived_versions",
				Visibility:       catalog.VisibilityPublic,
				DefaultVersionID: "old",
			},
			Versions: []*version{
				{ID: "old", Label: "v1", CreatedAt: day(1), Archived: true, Object: ref("old")},
				{ID: "dbg", CreatedAt: day(2), Debug: true, Object: ref("dbg")},
			},
		},
		"private-agent": {
			Definition: catalog.Definition{
				ID:               "private-agent",
				Kind:             "agent",
				Name:             "private_creator",
				Visibility:       catalog.VisibilitySpace,
				DefaultVersionID: "pv1",
			},
			Versions: []*version{
				{ID: "pv1", Label: "v1", CreatedAt: day(1), Object: ref("pv1")},
			},
		},
	}
	raw, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	blobs := fakes.NewBlobs()
	blobs.Data["space/catalog.json"] = raw
	c := &Catalog{Blobs: blobs}

	def, ver, obj, err := c.PublicAgentVersion(t.Context(), "space", "public-agent", "")
	if err != nil {
		t.Fatalf("PublicAgentVersion default lookup failed: %v", err)
	}
	if def.ID != "public-agent" || ver.ID != "v2" || obj.ID != "obj-v2" {
		t.Fatalf("expected latest active formal version v2, got def=%+v ver=%+v obj=%+v", def, ver, obj)
	}

	_, pinned, pinnedObj, err := c.PublicAgentVersion(t.Context(), "space", "public-agent", "v1")
	if err != nil {
		t.Fatalf("PublicAgentVersion pinned lookup failed: %v", err)
	}
	if pinned.ID != "v1" || pinnedObj.ID != "obj-v1" {
		t.Fatalf("expected pinned version v1, got ver=%+v obj=%+v", pinned, pinnedObj)
	}

	for _, rejected := range []string{"v3", "debug", "missing"} {
		if _, _, _, err := c.PublicAgentVersion(t.Context(), "space", "public-agent", rejected); !errors.Is(err, catalog.ErrAgentNotPublic) {
			t.Fatalf("version %q: expected ErrAgentNotPublic, got %v", rejected, err)
		}
	}
	if _, _, _, err := c.PublicAgentVersion(t.Context(), "space", "all-archived", ""); !errors.Is(err, catalog.ErrAgentNotPublic) {
		t.Fatalf("all-archived agent: expected ErrAgentNotPublic, got %v", err)
	}
	if _, _, _, err := c.PublicAgentVersion(t.Context(), "space", "private-agent", ""); !errors.Is(err, catalog.ErrAgentNotPublic) {
		t.Fatalf("private agent: expected ErrAgentNotPublic, got %v", err)
	}
}
