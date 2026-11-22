package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	httptransport "github.com/imalimin/mass/internal/transport/http"
	"github.com/imalimin/mass/internal/transport/http/ui"
)

func TestServerManagerUIOnlyLifecycle(t *testing.T) {
	t.Setenv("TMPDIR", os.Getenv("TMPDIR"))
	mgr := newServerManager(nil)
	tmp := t.TempDir()
	req, err := json.Marshal(startRequest{
		Mode:              modeUIOnly,
		Listen:            "127.0.0.1:0",
		WorkspaceTempRoot: filepath.Join(tmp, "workspaces"),
		TmpDir:            filepath.Join(tmp, "tmp"),
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := mgr.Start(string(req))
	var resp statusResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK {
		t.Fatalf("expected OK start, got error: %s", resp.Error)
	}
	defer mgr.Stop()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := json.Unmarshal([]byte(mgr.Status()), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.State == stateRunning && resp.URL != "" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if resp.State != stateRunning {
		t.Fatalf("expected running state, got %+v", resp)
	}

	handler := httptransport.NewHandler(ui.Assets(), version, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	liveRec := httptest.NewRecorder()
	handler.ServeHTTP(liveRec, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	if liveRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on /health/live, got %d", liveRec.Code)
	}

	rootRec := httptest.NewRecorder()
	handler.ServeHTTP(rootRec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rootRec.Code != http.StatusOK || !strings.Contains(rootRec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("expected 200 HTML on /, got status=%d content-type=%q", rootRec.Code, rootRec.Header().Get("Content-Type"))
	}

	stopRaw := mgr.Stop()
	if err := json.Unmarshal([]byte(stopRaw), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.State != stateStopped {
		t.Fatalf("expected stopped state after Stop, got %+v", resp)
	}
}

func TestServerManagerFullModeValidation(t *testing.T) {
	t.Setenv("TMPDIR", os.Getenv("TMPDIR"))
	t.Setenv("MASS_MARIADB_DSN", "")
	mgr := newServerManager(nil)
	tmp := t.TempDir()
	req, err := json.Marshal(startRequest{
		Mode:              modeFull,
		Listen:            "127.0.0.1:0",
		WorkspaceTempRoot: filepath.Join(tmp, "workspaces"),
		TmpDir:            filepath.Join(tmp, "tmp"),
		Env: map[string]string{
			"MASS_MARIADB_DSN": "",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := mgr.Start(string(req))
	var resp statusResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.OK || resp.State != stateError || !strings.Contains(resp.Error, "database.dsn_env") {
		t.Fatalf("expected validation error for missing DSN, got %+v", resp)
	}
}
