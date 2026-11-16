package httptransport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"path"
	"strconv"
	"sync"
	"time"

	"github.com/imalimin/mass/internal/app/catalog"
	"github.com/imalimin/mass/internal/app/identity"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/resource"
)

type endpoint func(http.ResponseWriter, *http.Request, identity.User) error
type IdentityOptions struct {
	Catalog        *catalog.Service
	Runtime        *RuntimeOptions
	Storage        *StorageOptions
	Service        *identity.Service
	Origin         string
	LoopbackOrigin string
	Secure         bool
}
type loginBucket struct {
	start time.Time
	count int
}
type loginLimiter struct {
	mu      sync.Mutex
	buckets map[string]loginBucket
	active  chan struct{}
}

// Bound both retained addresses and concurrent bcrypt work; forwarded headers
// are deliberately ignored until an explicit trusted proxy configuration exists.
func (l *loginLimiter) allow(address string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	for key, b := range l.buckets {
		if now.Sub(b.start) >= time.Minute {
			delete(l.buckets, key)
		}
	}
	b, ok := l.buckets[address]
	if !ok {
		if len(l.buckets) >= 4096 {
			return false
		}
		b.start = now
	}
	if b.count >= 10 {
		return false
	}
	b.count++
	l.buckets[address] = b
	return true
}
func decodeBody(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return identity.ErrInvalid
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return identity.ErrInvalid
	}
	return nil
}
func apiError(w http.ResponseWriter, err error, logger *slog.Logger) {
	status := 500
	code := "internal_error"
	message := "Request could not be completed"
	for _, v := range []struct {
		err     error
		status  int
		message string
	}{
		{catalog.ErrDraft, 409, "Draft changed; compare and reload before saving"}, {catalog.ErrVersion, 409, "Version label already published"}, {catalog.ErrReferenced, 409, "Version is referenced"}, {catalog.ErrConfig, 400, "Invalid definition or operation"},
		{catalog.ErrSkillDocumentMissing, 400, "SKILL.md is required at the ZIP root"},
		{run.ErrState, 409, "Run state conflict"}, {run.ErrCancelled, 409, "Run cancelled"}, {run.ErrBudget, 400, "Run budget exceeded"}, {run.ErrOverflow, 400, "Context capacity exceeded"}, {run.ErrUnavailable, 503, "Runtime unavailable"}, {run.ErrTool, 400, "Tool not available in this stage"}, {run.ErrProtocol, 502, "Model response incomplete or invalid"}, {run.ErrCursor, 400, "Invalid stream cursor"},
		{storage.ErrConflict, 409, "Storage fact conflict"}, {storage.ErrSchema, 400, "Unsupported event schema"}, {storage.ErrInvalid, 400, "Invalid storage request"}, {storage.ErrIntegrity, 503, "Content unavailable"}, {storage.ErrWorkspace, 409, "Workspace unavailable"}, {storage.ErrActive, 409, "Session has an active run"},
		{identity.ErrUnauthenticated, 401, "Please sign in"}, {identity.ErrForbidden, 403, "Access denied"}, {identity.ErrNotFound, 404, "Resource not found"}, {identity.ErrConflict, 409, "Resource changed or already exists"}, {identity.ErrInvalid, 400, "Invalid request"}, {identity.ErrCapability, 400, "Unsupported model capability or endpoint"},
	} {
		if errors.Is(err, v.err) {
			status = v.status
			code = v.err.Error()
			message = v.message
			break
		}
	}
	if status == 500 {
		logger.Error("request failed", "request_id", w.Header().Get("X-Request-ID"))
	}
	writeError(w, status, code, message)
}
func registerIdentity(mux *http.ServeMux, options IdentityOptions, logger *slog.Logger) {
	svc := options.Service
	limiter := &loginLimiter{buckets: map[string]loginBucket{}, active: make(chan struct{}, 4)}
	token := func(r *http.Request) string {
		c, e := r.Cookie("mass_session")
		if e != nil {
			return ""
		}
		return c.Value
	}
	cookie := func(w http.ResponseWriter, value string, expiry time.Time, maxAge int) {
		http.SetCookie(w, &http.Cookie{Name: "mass_session", Value: value, Path: "/", HttpOnly: true, Secure: options.Secure, SameSite: http.SameSiteLaxMode, Expires: expiry, MaxAge: maxAge})
	}
	registerMedia := func(pattern string, auth bool, contentType string, fn endpoint) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			defer cancel()
			r = r.WithContext(ctx)
			if r.Method != "GET" && r.Method != "HEAD" {
				origin := options.Origin
				if options.LoopbackOrigin != "" {
					if local, ok := ctx.Value(http.LocalAddrContextKey).(*net.TCPAddr); ok && local.IP.IsLoopback() {
						origin = options.LoopbackOrigin
					}
				}
				if r.Header.Get("Origin") != origin || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
					apiError(w, identity.ErrForbidden, logger)
					return
				}
				media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
				if err != nil || media != contentType {
					message := "Content-Type must be " + contentType
					if contentType == "application/json" {
						message = "JSON content type is required"
					}
					writeError(w, 415, "unsupported_media_type", message)
					return
				}
			}
			var u identity.User
			var err error
			if auth {
				u, err = svc.Me(ctx, token(r))
				if err != nil {
					apiError(w, err, logger)
					return
				}
			}
			if err = fn(w, r, u); err != nil {
				apiError(w, err, logger)
			}
		})
	}
	register := func(pattern string, auth bool, fn endpoint) {
		registerMedia(pattern, auth, "application/json", fn)
	}
	register("POST /api/v1/auth/login", false, func(w http.ResponseWriter, r *http.Request, _ identity.User) error {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !limiter.allow(host) {
			w.Header().Set("Retry-After", "60")
			writeError(w, 429, "rate_limited", "Try again later")
			return nil
		}
		select {
		case limiter.active <- struct{}{}:
			defer func() { <-limiter.active }()
		default:
			w.Header().Set("Retry-After", "1")
			writeError(w, 429, "rate_limited", "Try again later")
			return nil
		}
		var body struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := decodeBody(w, r, &body); err != nil {
			return err
		}
		u, t, exp, err := svc.Login(r.Context(), body.Username, body.Password)
		if err != nil {
			return err
		}
		// A new login replaces the browser's old server-side session as well.
		if err = svc.Logout(r.Context(), token(r)); err != nil {
			_ = svc.Logout(r.Context(), t)
			return err
		}
		cookie(w, t, exp, int(svc.TTL.Seconds()))
		writeJSON(w, 200, u)
		return nil
	})
	register("GET /api/v1/auth/me", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error { writeJSON(w, 200, u); return nil })
	register("POST /api/v1/auth/logout", false, func(w http.ResponseWriter, r *http.Request, _ identity.User) error {
		if err := svc.Logout(r.Context(), token(r)); err != nil {
			return err
		}
		cookie(w, "", time.Unix(1, 0), -1)
		w.WriteHeader(204)
		return nil
	})
	register("POST /api/v1/spaces", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
		var b struct {
			Name string `json:"name"`
		}
		if err := decodeBody(w, r, &b); err != nil {
			return err
		}
		s, err := svc.CreateSpace(r.Context(), u.ID, b.Name)
		if err != nil {
			return err
		}
		writeJSON(w, 201, s)
		return nil
	})
	query := func(r *http.Request, u identity.User) (identity.Query, int, string, error) {
		limit := 20
		var err error
		if value := r.URL.Query().Get("limit"); value != "" {
			limit, err = strconv.Atoi(value)
			if err != nil {
				return identity.Query{}, 0, "", identity.ErrInvalid
			}
		}
		scope := u.ID + ":" + r.URL.Path + ":" + r.URL.Query().Get("available")
		q, err := svc.Query(scope, r.URL.Query().Get("cursor"), limit)
		return q, limit, scope, err
	}
	registerCatalog(register, registerMedia, options, query)
	registerRuntime(mux, register, options, query, token, logger)
	register("GET /api/v1/spaces", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
		q, n, scope, err := query(r, u)
		if err != nil {
			return err
		}
		items, err := svc.Repo.Spaces(r.Context(), u.ID, q)
		if err != nil {
			return err
		}
		writeJSON(w, 200, identity.Paginate(svc, scope, items, n, func(v identity.Space) string { return v.ID }))
		return nil
	})
	register("GET /api/v1/spaces/{space_id}/members", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
		q, n, scope, err := query(r, u)
		if err != nil {
			return err
		}
		items, err := svc.Repo.Members(r.Context(), u.ID, r.PathValue("space_id"), q)
		if err != nil {
			return err
		}
		writeJSON(w, 200, identity.Paginate(svc, scope, items, n, func(v identity.Member) string { return v.ID }))
		return nil
	})
	register("POST /api/v1/spaces/{space_id}/members", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
		var b struct {
			Username string `json:"username"`
		}
		if err := decodeBody(w, r, &b); err != nil {
			return err
		}
		if err := svc.AddMember(r.Context(), u.ID, r.PathValue("space_id"), b.Username); err != nil {
			return err
		}
		w.WriteHeader(204)
		return nil
	})
	register("DELETE /api/v1/spaces/{space_id}/members/{user_id}", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
		if err := svc.RemoveMember(r.Context(), u.ID, r.PathValue("space_id"), r.PathValue("user_id")); err != nil {
			return err
		}
		w.WriteHeader(204)
		return nil
	})
	register("GET /api/v1/spaces/{space_id}/models", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
		available := r.URL.Query().Get("available")
		if available != "" && available != "true" && available != "false" {
			return identity.ErrInvalid
		}
		q, n, scope, err := query(r, u)
		if err != nil {
			return err
		}
		items, err := svc.Repo.Models(r.Context(), u.ID, r.PathValue("space_id"), q, available == "true")
		if err != nil {
			return err
		}
		writeJSON(w, 200, identity.Paginate(svc, scope, items, n, func(v identity.Model) string { return v.ID }))
		return nil
	})
	register("GET /api/v1/spaces/{space_id}/models/{id}", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
		m, err := svc.Repo.Model(r.Context(), u.ID, r.PathValue("space_id"), r.PathValue("id"))
		if err != nil {
			return err
		}
		writeJSON(w, 200, m)
		return nil
	})
	saveModel := func(w http.ResponseWriter, r *http.Request, u identity.User) error {
		var b identity.Model
		if err := decodeBody(w, r, &b); err != nil {
			return err
		}
		if b.ID != "" {
			return identity.ErrInvalid
		}
		m, err := svc.SaveModel(r.Context(), u.ID, r.PathValue("space_id"), r.PathValue("id"), b)
		if err != nil {
			return err
		}
		status := 200
		if r.Method == "POST" {
			status = 201
		}
		writeJSON(w, status, m)
		return nil
	}
	register("POST /api/v1/spaces/{space_id}/models", true, saveModel)
	register("PUT /api/v1/spaces/{space_id}/models/{id}", true, saveModel)
	register("DELETE /api/v1/spaces/{space_id}/models/{id}", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
		var b struct {
			Revision int64 `json:"revision"`
		}
		if err := decodeBody(w, r, &b); err != nil {
			return err
		}
		if err := svc.DeleteModel(r.Context(), u.ID, r.PathValue("space_id"), r.PathValue("id"), b.Revision); err != nil {
			return err
		}
		w.WriteHeader(204)
		return nil
	})
	register("GET /api/v1/spaces/{space_id}/model-capabilities", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
		if err := svc.CheckAccess(r.Context(), u.ID, r.PathValue("space_id")); err != nil {
			return err
		}
		writeJSON(w, 200, map[string]any{"provider": "ark", "input_types": []string{"text"}, "streaming": true, "tool_calling": true})
		return nil
	})
	if options.Storage != nil {
		storageOptions := options.Storage
		register("GET /api/v1/spaces/{space_id}/sessions/{session_id}/journal", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
			after, through, limit, err := journalQuery(r)
			if err != nil {
				return err
			}
			page, err := storageOptions.Journal.Read(r.Context(), u.ID, r.PathValue("space_id"), r.PathValue("session_id"), after, through, limit)
			if err != nil {
				return err
			}
			if storageOptions.Content.Inline != nil {
				page.Events = publicSessionEvents(page.Events)
			}
			writeJSON(w, 200, page)
			return nil
		})
		register("GET /api/v1/spaces/{space_id}/workspace-snapshots/{id}", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
			if storageOptions.Content.Inline != nil {
				return identity.ErrNotFound
			}
			snapshot, err := storageOptions.Snapshots.Repo.Snapshot(r.Context(), u.ID, r.PathValue("space_id"), r.PathValue("id"))
			if err != nil {
				return err
			}
			writeJSON(w, 200, snapshot)
			return nil
		})
		register("GET /api/v1/spaces/{space_id}/payloads/{id}", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
			ctx, release, err := svc.WatchAccess(r.Context(), u.ID, r.PathValue("space_id"))
			if err != nil {
				return err
			}
			defer release()
			space, id := r.PathValue("space_id"), r.PathValue("id")
			var reader io.Reader
			var size int64
			filename := id
			if entry := r.URL.Query().Get("path"); entry != "" {
				raw, _, err := resource.ReadSkillPackageFile(ctx, storageOptions.Content, u.ID, space, id, entry)
				if err != nil {
					return err
				}
				reader, size, filename = bytes.NewReader(raw), int64(len(raw)), path.Base(entry)
			} else {
				object, file, err := storageOptions.Content.Read(ctx, u.ID, space, id)
				if err != nil {
					return err
				}
				defer file.Close()
				reader, size = file, object.Size
			}
			check := func(ctx context.Context) error {
				if _, err := svc.Me(ctx, token(r)); err != nil {
					return err
				}
				return svc.CheckAccess(ctx, u.ID, r.PathValue("space_id"))
			}
			if err = check(ctx); err != nil {
				return err
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
			w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
			w.WriteHeader(200)
			_ = copyAuthorized(ctx, w, reader, check)
			return nil
		})
	}

}
