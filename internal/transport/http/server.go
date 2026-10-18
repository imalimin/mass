package httptransport

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
)

type ErrorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

// NewHandler retains D02 management routes and optionally installs D01 storage.
func NewHandler(assets fs.FS, version string, logger *slog.Logger, options ...IdentityOptions) http.Handler {
	mux := http.NewServeMux()
	stage := "D00"
	if len(options) > 0 {
		registerIdentity(mux, options[0], logger)
		stage = "D02"
		if options[0].Catalog != nil {
			stage = "D03"
		}
	}
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "alive"}) })
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		if len(options) == 0 || options[0].Storage == nil || options[0].Storage.Check == nil {
			writeError(w, 503, "dependencies_not_initialized", "Storage services are unavailable")
			return
		}
		if err := options[0].Storage.Check(r.Context()); err != nil {
			writeError(w, 503, "storage_unavailable", "Database schema or TOS is unavailable")
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("GET /api/v1/system", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"name": "MASS", "version": version, "stage": stage, "protocol_profile": "mass-agui-v1"})
	})
	// Reserve both roots and descendants; API/health misses never become HTML.
	for _, prefix := range []string{"/api", "/api/", "/health", "/health/"} {
		mux.HandleFunc(prefix, func(w http.ResponseWriter, r *http.Request) { writeError(w, 404, "not_found", "Endpoint not found") })
	}
	files := http.FileServer(http.FS(assets))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, 405, "method_not_allowed", "Method not allowed")
			return
		}
		if r.URL.Path == "/" || r.URL.Path == "/spaces" || r.URL.Path == "/login" {
			index, err := fs.ReadFile(assets, "index.html")
			if err != nil {
				writeError(w, 503, "ui_unavailable", "UI build is unavailable")
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			if r.Method != http.MethodHead {
				_, _ = w.Write(index)
			}
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		info, err := fs.Stat(assets, name)
		if err != nil || info.IsDir() {
			writeError(w, 404, "not_found", "Resource not found")
			return
		}
		if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			http.Error(w, "request identity unavailable", 503)
			return
		}
		w.Header().Set("X-Request-ID", hex.EncodeToString(id[:]))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		defer func() {
			if recover() != nil {
				logger.Error("HTTP handler panic", "request_id", w.Header().Get("X-Request-ID"))
				writeError(w, 500, "internal_error", "Internal server error")
			}
		}()
		mux.ServeHTTP(w, r)
	})
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, ErrorBody{code, message, w.Header().Get("X-Request-ID")})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
