package httptransport

import (
	"context"
	"encoding/json"
	sessionapp "github.com/imalimin/mass/internal/app/session"
	"github.com/imalimin/mass/internal/domain/run"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestReadinessTracksRuntimeFaultWithoutStorageCache(t *testing.T) {
	fault := false
	h := NewHandler(fstest.MapFS{"index.html": {Data: []byte("MASS")}}, "test", slog.New(slog.NewTextHandler(io.Discard, nil)), IdentityOptions{
		Storage: &StorageOptions{Check: CachedReadiness(func(context.Context) error { return nil })},
		Runtime: &RuntimeOptions{Service: &sessionapp.Service{Ready: func(context.Context) error {
			if fault {
				return run.ErrUnavailable
			}
			return nil
		}}},
	})
	for _, status := range []int{200, 503} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/health/ready", nil))
		if w.Code != status {
			t.Fatal(w.Code, w.Body.String())
		}
		fault = true
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/health/live", nil))
	if w.Code != 200 {
		t.Fatal("runtime fault is not process death")
	}
}

func TestRoutingBoundaries(t *testing.T) {
	assets := fstest.MapFS{"index.html": {Data: []byte("<html>MASS</html>")}, "assets/app.js": {Data: []byte("export {}")}}
	handler := NewHandler(assets, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, tc := range []struct {
		method, path string
		status       int
		contentType  string
	}{
		{"GET", "/", 200, "text/html"}, {"GET", "/spaces", 200, "text/html"}, {"GET", "/assets/app.js", 200, "javascript"},
		{"GET", "/spaces/example", 200, "text/html"},
		{"GET", "/spaces/example/members", 200, "text/html"}, {"GET", "/spaces/example/models", 200, "text/html"}, {"GET", "/spaces/example/credentials", 200, "text/html"},
		{"GET", "/spaces/example/agents", 200, "text/html"}, {"GET", "/spaces/example/agents/definition", 200, "text/html"},
		{"GET", "/spaces/example/skills/definition", 200, "text/html"}, {"GET", "/spaces/example/sessions/session?run=run", 200, "text/html"},
		{"HEAD", "/spaces/example/sessions/session", 200, "text/html"}, {"GET", "/spaces/example/unknown", 404, "application/json"},
		{"GET", "/spaces/example/agents/id/missing.js", 404, "application/json"}, {"POST", "/spaces/example/members", 405, "application/json"},
		{"GET", "/assets/missing.js", 404, "application/json"}, {"GET", "/unknown", 404, "application/json"}, {"GET", "/api/v1/missing", 404, "application/json"}, {"GET", "/health/missing", 404, "application/json"}, {"POST", "/spaces", 405, "application/json"},
		{"GET", "/health/live", 200, "application/json"}, {"GET", "/health/ready", 503, "application/json"}, {"GET", "/api/v1/system", 200, "application/json"},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.status || !strings.Contains(w.Header().Get("Content-Type"), tc.contentType) {
				t.Fatalf("status=%d type=%s body=%s", w.Code, w.Header().Get("Content-Type"), w.Body.String())
			}
			if len(w.Header().Get("X-Request-ID")) != 32 {
				t.Fatal("missing request ID")
			}
			if tc.status >= 400 {
				var body ErrorBody
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Code == "" || body.RequestID != w.Header().Get("X-Request-ID") {
					t.Fatalf("invalid error envelope: %+v %v", body, err)
				}
			}
		})
	}
}
