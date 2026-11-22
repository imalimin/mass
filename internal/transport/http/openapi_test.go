package httptransport

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/adapter/secrets"
	"github.com/imalimin/mass/internal/app/identity"
	sessionapp "github.com/imalimin/mass/internal/app/session"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
	"github.com/imalimin/mass/internal/transport/agui"
)

type openapiTestRepo struct {
	identity.Repository
	mu       sync.Mutex
	sessions map[[32]byte]identity.User
	users    map[string]identity.User
	owners   map[string]map[string]bool
	members  map[string]map[string]bool
	tokens   map[string]identity.OpenAPIToken
}

func newOpenAPITestRepo() *openapiTestRepo {
	return &openapiTestRepo{
		sessions: map[[32]byte]identity.User{},
		users:    map[string]identity.User{},
		owners:   map[string]map[string]bool{},
		members:  map[string]map[string]bool{},
		tokens:   map[string]identity.OpenAPIToken{},
	}
}

func (r *openapiTestRepo) addCookieSession(u identity.User) string {
	raw := bytes.Repeat([]byte{byte(len(r.sessions) + 1)}, 32)
	tok := hex.EncodeToString(raw)
	sum := sha256.Sum256(raw)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions[sum] = u
	r.users[u.ID] = u
	return "mass_session=" + tok
}

func (r *openapiTestRepo) Session(_ context.Context, hash []byte) (identity.User, error) {
	var key [32]byte
	copy(key[:], hash)
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.sessions[key]
	if !ok {
		return identity.User{}, identity.ErrUnauthenticated
	}
	return u, nil
}

func (r *openapiTestRepo) CheckAccess(_ context.Context, actor, space string, owner bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.members[space][actor] {
		return identity.ErrForbidden
	}
	if owner && !r.owners[space][actor] {
		return identity.ErrForbidden
	}
	return nil
}

func (r *openapiTestRepo) Spaces(_ context.Context, actor string, _ identity.Query) ([]identity.Space, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []identity.Space
	for space, members := range r.members {
		if members[actor] {
			out = append(out, identity.Space{ID: space, Name: space, IsOwner: r.owners[space][actor], CreatedAt: time.Now().UTC()})
		}
	}
	return out, nil
}

func (r *openapiTestRepo) OpenAPITokens(_ context.Context, actor, space string, q identity.Query) ([]identity.OpenAPIToken, error) {
	if err := r.CheckAccess(context.Background(), actor, space, true); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().UTC()
	var out []identity.OpenAPIToken
	for _, tok := range r.tokens {
		if tok.SpaceID == space && tok.ID > q.After {
			tok.Status = identity.OpenAPITokenStatus(tok.ExpiresAt, tok.RevokedAt, now)
			out = append(out, tok)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

func (r *openapiTestRepo) CreateOpenAPIToken(_ context.Context, actor, space string, tok identity.OpenAPIToken) error {
	if err := r.CheckAccess(context.Background(), actor, space, true); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tokens[tok.ID] = tok
	return nil
}

func (r *openapiTestRepo) OpenAPITokenSecret(_ context.Context, actor, space, id string) (identity.OpenAPIToken, error) {
	if err := r.CheckAccess(context.Background(), actor, space, true); err != nil {
		return identity.OpenAPIToken{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	tok, ok := r.tokens[id]
	if !ok || tok.SpaceID != space {
		return identity.OpenAPIToken{}, identity.ErrNotFound
	}
	if tok.RevokedAt != nil || !tok.ExpiresAt.After(time.Now().UTC()) {
		return identity.OpenAPIToken{}, identity.ErrConflict
	}
	return tok, nil
}

func (r *openapiTestRepo) RenewOpenAPIToken(_ context.Context, actor, space, id string, expiresAt time.Time) (identity.OpenAPIToken, error) {
	if err := r.CheckAccess(context.Background(), actor, space, true); err != nil {
		return identity.OpenAPIToken{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	tok, ok := r.tokens[id]
	if !ok || tok.SpaceID != space {
		return identity.OpenAPIToken{}, identity.ErrNotFound
	}
	if tok.RevokedAt != nil {
		return identity.OpenAPIToken{}, identity.ErrConflict
	}
	tok.ExpiresAt = expiresAt
	r.tokens[id] = tok
	return tok, nil
}

func (r *openapiTestRepo) RevokeOpenAPIToken(_ context.Context, actor, space, id string, revokedAt time.Time) error {
	if err := r.CheckAccess(context.Background(), actor, space, true); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	tok, ok := r.tokens[id]
	if !ok || tok.SpaceID != space {
		return identity.ErrNotFound
	}
	if tok.RevokedAt != nil {
		return nil
	}
	t := revokedAt
	tok.RevokedAt = &t
	r.tokens[id] = tok
	return nil
}

func (r *openapiTestRepo) AuthenticateBearerToken(_ context.Context, hash []byte) (identity.BearerPrincipal, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().UTC()
	for _, tok := range r.tokens {
		if bytes.Equal(tok.TokenHash, hash) {
			if tok.RevokedAt != nil || !tok.ExpiresAt.After(now) {
				return identity.BearerPrincipal{}, identity.ErrUnauthenticated
			}
			return identity.BearerPrincipal{User: r.users[tok.IssuerID], SpaceID: tok.SpaceID, TokenID: tok.ID}, nil
		}
	}
	return identity.BearerPrincipal{}, identity.ErrUnauthenticated
}

type stubRuntimeRepo struct {
	RuntimeRepository
}

func (stubRuntimeRepo) RuntimeMessages(_ context.Context, _, _, _ string, _ int64, _ int) ([]run.MessageView, error) {
	return []run.MessageView{}, nil
}

func (stubRuntimeRepo) RuntimeSession(_ context.Context, _, space, id string) (run.Session, error) {
	return run.Session{ID: id, SpaceID: space}, nil
}

func (stubRuntimeRepo) RuntimeSessions(_ context.Context, _, _, _ string, _ identity.Query) ([]run.Session, error) {
	return []run.Session{}, nil
}

func (stubRuntimeRepo) RuntimeRuns(_ context.Context, _, _, _ string, _ identity.Query) ([]run.View, error) {
	return []run.View{}, nil
}

func (stubRuntimeRepo) CancelRuntimeRun(_ context.Context, _, space, id string) (run.View, error) {
	return run.View{Snapshot: run.Snapshot{ID: id, SpaceID: space}, CancelRequested: true}, nil
}

type stubAGUIRepo struct {
	frames []agui.Frame
	closed bool
}

func (s *stubAGUIRepo) LoadStream(_ context.Context, _, _, _ string) (agui.Checkpoint, error) {
	return agui.Checkpoint{
		StreamID: uuid.NewString(),
		Profile:  agui.Profile,
		Mapper:   1,
		Parser:   1,
		Cursor:   int64(len(s.frames)),
		Closed:   s.closed,
	}, nil
}

func (s *stubAGUIRepo) CommitStream(_ context.Context, _, _, _ string, _, _ agui.Checkpoint, _ []agui.Frame) error {
	return nil
}

func (s *stubAGUIRepo) ReadEvents(_ context.Context, _, _, _ string, _ int64, _ *int64, _ int) (journal.Page, error) {
	return journal.Page{}, nil
}

func (s *stubAGUIRepo) ReadFrames(_ context.Context, _, _, _ string, after int64, _ int) ([]agui.Frame, bool, error) {
	var out []agui.Frame
	for _, f := range s.frames {
		if f.Sequence > after {
			out = append(out, f)
		}
	}
	return out, s.closed, nil
}

func TestOpenAPIManagementAndBearerHTTP(t *testing.T) {
	repo := newOpenAPITestRepo()
	owner := identity.User{ID: uuid.NewString(), Username: "owner"}
	member := identity.User{ID: uuid.NewString(), Username: "member"}
	spaceA := storage.NewShortID()
	spaceB := storage.NewShortID()
	repo.owners[spaceA] = map[string]bool{owner.ID: true}
	repo.members[spaceA] = map[string]bool{owner.ID: true, member.ID: true}
	repo.owners[spaceB] = map[string]bool{owner.ID: true}
	repo.members[spaceB] = map[string]bool{owner.ID: true}
	ownerCookie := repo.addCookieSession(owner)
	memberCookie := repo.addCookieSession(member)

	cipher, err := secrets.New(bytes.Repeat([]byte{4}, 32))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := identity.New(repo, cipher, time.Hour, []byte("http-test-cursor"))
	if err != nil {
		t.Fatal(err)
	}
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	aguiRepo := &stubAGUIRepo{
		frames: []agui.Frame{{Sequence: 1, JSON: json.RawMessage(`{"type":"RUN_STARTED"}`)}},
		closed: false,
	}
	rtRepo := stubRuntimeRepo{}
	handler := NewHandler(fstest.MapFS{"index.html": {Data: []byte("MASS")}}, "test", logger, IdentityOptions{
		Service: svc,
		Origin:  "http://mass.test",
		Runtime: &RuntimeOptions{
			Repo:    rtRepo,
			Service: &sessionapp.Service{Repo: rtRepo, Identity: svc},
			UI:      &agui.Projector{Repo: aguiRepo, Live: &agui.Live{}},
		},
	})

	do := func(method, path string, headers map[string]string, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://mass.test"+path, strings.NewReader(body))
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	// Owner creates token via Cookie.
	w := do("POST", "/api/v1/spaces/"+spaceA+"/openapi/tokens", map[string]string{
		"Cookie":       ownerCookie,
		"Origin":       "http://mass.test",
		"Content-Type": "application/json",
	}, `{"name":"ci-client"}`)
	if w.Code != 201 {
		t.Fatalf("create token: got %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("missing no-store on create: %q", w.Header().Get("Cache-Control"))
	}
	var created identity.CreatedOpenAPIToken
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if len(created.Token) != 43 || created.Status != "active" {
		t.Fatalf("unexpected created payload: %+v", created)
	}

	// List never returns raw token.
	w = do("GET", "/api/v1/spaces/"+spaceA+"/openapi/tokens", map[string]string{"Cookie": ownerCookie}, "")
	if w.Code != 200 || strings.Contains(w.Body.String(), created.Token) {
		t.Fatalf("list failed or leaked token: %d %s", w.Code, w.Body.String())
	}

	// Copy returns plaintext with Cache-Control: no-store.
	w = do("GET", "/api/v1/spaces/"+spaceA+"/openapi/tokens/"+created.ID, map[string]string{"Cookie": ownerCookie}, "")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("copy failed: %d %s", w.Code, w.Body.String())
	}
	var secret identity.OpenAPITokenSecret
	if err := json.Unmarshal(w.Body.Bytes(), &secret); err != nil || secret.Token != created.Token {
		t.Fatalf("copy payload mismatch: %+v %v", secret, err)
	}

	// Member cannot access management endpoints.
	for _, tc := range []struct {
		method, path, body string
	}{
		{"GET", "/api/v1/spaces/" + spaceA + "/openapi/tokens", ""},
		{"POST", "/api/v1/spaces/" + spaceA + "/openapi/tokens", `{"name":"x"}`},
		{"GET", "/api/v1/spaces/" + spaceA + "/openapi/tokens/" + created.ID, ""},
		{"POST", "/api/v1/spaces/" + spaceA + "/openapi/tokens/" + created.ID + "/renew", `{"expires_at":"2028-01-01T00:00:00Z"}`},
		{"DELETE", "/api/v1/spaces/" + spaceA + "/openapi/tokens/" + created.ID, ""},
	} {
		w = do(tc.method, tc.path, map[string]string{
			"Cookie":       memberCookie,
			"Origin":       "http://mass.test",
			"Content-Type": "application/json",
		}, tc.body)
		if w.Code != 403 {
			t.Fatalf("member %s %s: got %d want 403", tc.method, tc.path, w.Code)
		}
	}

	// Bearer rejected with 401 on non-whitelisted endpoints even with valid Cookie and without Origin.
	for _, tc := range []struct {
		method, path, body string
	}{
		{"GET", "/api/v1/spaces", ""},
		{"GET", "/api/v1/spaces/" + spaceA + "/openapi/tokens", ""},
		{"POST", "/api/v1/spaces/" + spaceA + "/openapi/tokens", `{"name":"via-bearer"}`},
	} {
		w = do(tc.method, tc.path, map[string]string{
			"Authorization": "Bearer " + created.Token,
			"Cookie":        ownerCookie,
			"Content-Type":  "application/json",
		}, tc.body)
		if w.Code != 401 {
			t.Fatalf("bearer on non-whitelisted %s %s: got %d want 401", tc.method, tc.path, w.Code)
		}
	}

	// OpenAPI context resolves space_id for Bearer token and rejects Cookie-only requests.
	w = do("GET", "/api/v1/openapi/context", map[string]string{"Cookie": ownerCookie}, "")
	if w.Code != 401 {
		t.Fatalf("openapi context with cookie only: got %d want 401", w.Code)
	}
	w = do("GET", "/api/v1/openapi/context", map[string]string{"Authorization": "Bearer " + created.Token}, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), spaceA) {
		t.Fatalf("openapi context with bearer: got %d %s", w.Code, w.Body.String())
	}

	// Whitelisted session endpoints accept Bearer without Origin.
	sessionID := storage.NewShortID()
	for _, p := range []string{
		"/api/v1/spaces/" + spaceA + "/sessions",
		"/api/v1/spaces/" + spaceA + "/sessions/" + sessionID,
		"/api/v1/spaces/" + spaceA + "/sessions/" + sessionID + "/runs",
		"/api/v1/spaces/" + spaceA + "/sessions/" + sessionID + "/messages",
	} {
		w = do("GET", p, map[string]string{
			"Authorization": "bearer " + created.Token,
		}, "")
		if w.Code != 200 {
			t.Fatalf("bearer GET %s: got %d %s", p, w.Code, w.Body.String())
		}
	}
	w = do("POST", "/api/v1/spaces/"+spaceA+"/runs/"+storage.NewShortID()+"/cancel", map[string]string{
		"Authorization": "Bearer " + created.Token,
		"Content-Type":  "application/json",
	}, `{}`)
	if w.Code != 200 {
		t.Fatalf("bearer POST cancel: got %d %s", w.Code, w.Body.String())
	}

	// Cross-space Bearer returns 403.
	w = do("GET", "/api/v1/spaces/"+spaceB+"/sessions/"+sessionID+"/messages", map[string]string{
		"Authorization": "Bearer " + created.Token,
	}, "")
	if w.Code != 403 {
		t.Fatalf("cross-space bearer: got %d want 403", w.Code)
	}

	// Invalid Bearer returns 401 even when valid Cookie is also sent, and even when Content-Type is wrong.
	for _, authHeader := range []string{"", "Basic abc", "Bearer", "Bearer invalid-token", "Bearer " + created.Token + " extra"} {
		w = do("POST", "/api/v1/spaces/"+spaceA+"/sessions", map[string]string{
			"Authorization": authHeader,
			"Cookie":        ownerCookie,
			"Origin":        "http://mass.test",
			"Content-Type":  "text/plain",
		}, `{}`)
		if w.Code != 401 {
			t.Fatalf("bad Authorization %q with valid cookie: got %d want 401", authHeader, w.Code)
		}
	}

	// Multiple Authorization headers return 401.
	{
		r := httptest.NewRequest("GET", "http://mass.test/api/v1/spaces/"+spaceA+"/sessions/"+sessionID+"/messages", nil)
		r.Header.Add("Authorization", "Bearer "+created.Token)
		r.Header.Add("Authorization", "Bearer "+created.Token)
		r.Header.Set("Cookie", ownerCookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		if rec.Code != 401 {
			t.Fatalf("multiple Authorization headers: got %d want 401", rec.Code)
		}
	}

	// Valid Bearer write without Origin passes auth and checks Content-Type (415) and reaches handler (400 on bad body).
	w = do("POST", "/api/v1/spaces/"+spaceA+"/sessions", map[string]string{
		"Authorization": "Bearer " + created.Token,
		"Content-Type":  "text/plain",
	}, `{}`)
	if w.Code != 415 {
		t.Fatalf("valid bearer wrong content-type: got %d want 415", w.Code)
	}
	w = do("POST", "/api/v1/spaces/"+spaceA+"/sessions/"+sessionID+"/attachments", map[string]string{
		"Authorization": "Bearer " + created.Token,
		"Content-Type":  "application/json",
	}, `{"name":"a.txt","media_type":"text/plain","content_base64":"!!!"}`)
	if w.Code != 400 {
		t.Fatalf("valid bearer attachment reached handler: got %d want 400 (%s)", w.Code, w.Body.String())
	}
	w = do("POST", "/api/v1/spaces/"+spaceA+"/sessions/"+sessionID+"/runs", map[string]string{
		"Authorization": "Bearer " + created.Token,
		"Content-Type":  "application/json",
	}, `{"idempotency_key":"k1","text":"   "}`)
	if w.Code != 400 {
		t.Fatalf("valid bearer submit run reached handler: got %d want 400 (%s)", w.Code, w.Body.String())
	}

	// Cookie fallback still works when Authorization header is omitted.
	w = do("GET", "/api/v1/spaces/"+spaceA+"/sessions/"+sessionID+"/messages", map[string]string{
		"Cookie": ownerCookie,
	}, "")
	if w.Code != 200 {
		t.Fatalf("cookie fallback GET messages: got %d %s", w.Code, w.Body.String())
	}

	// SSE with Bearer streams initial frame and closes immediately upon revocation.
	pr, pw := io.Pipe()
	sseWriter := &pipeResponseWriter{header: make(http.Header), pipe: pw, statusCh: make(chan int, 1)}
	sseDone := make(chan struct{})
	go func() {
		defer close(sseDone)
		defer pw.Close()
		sseReq := httptest.NewRequest("GET", "http://mass.test/api/v1/spaces/"+spaceA+"/runs/"+storage.NewShortID()+"/events", nil)
		sseReq.Header.Set("Authorization", "Bearer "+created.Token)
		handler.ServeHTTP(sseWriter, sseReq)
	}()
	reader := bufio.NewReader(pr)
	line, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "id: 1") {
		t.Fatalf("expected first SSE frame, got %q err=%v", line, err)
	}
	_, _ = reader.ReadString('\n') // data line
	_, _ = reader.ReadString('\n') // empty separator

	// Revoke token via Cookie and verify SSE closes promptly.
	w = do("DELETE", "/api/v1/spaces/"+spaceA+"/openapi/tokens/"+created.ID, map[string]string{
		"Cookie":       ownerCookie,
		"Origin":       "http://mass.test",
		"Content-Type": "application/json",
	}, "")
	if w.Code != 204 {
		t.Fatalf("revoke failed: %d %s", w.Code, w.Body.String())
	}
	select {
	case <-sseDone:
	case <-time.After(2 * time.Second):
		t.Fatal("SSE connection did not close after token revocation")
	}
	_ = pr.Close()

	// Idempotent revoke returns 204; revoked token cannot copy/renew (409) or authenticate (401).
	w = do("DELETE", "/api/v1/spaces/"+spaceA+"/openapi/tokens/"+created.ID, map[string]string{
		"Cookie":       ownerCookie,
		"Origin":       "http://mass.test",
		"Content-Type": "application/json",
	}, "")
	if w.Code != 204 {
		t.Fatalf("idempotent revoke: got %d want 204", w.Code)
	}
	w = do("GET", "/api/v1/spaces/"+spaceA+"/openapi/tokens/"+created.ID, map[string]string{"Cookie": ownerCookie}, "")
	if w.Code != 409 {
		t.Fatalf("copy revoked token: got %d want 409", w.Code)
	}
	w = do("POST", "/api/v1/spaces/"+spaceA+"/openapi/tokens/"+created.ID+"/renew", map[string]string{
		"Cookie":       ownerCookie,
		"Origin":       "http://mass.test",
		"Content-Type": "application/json",
	}, `{"expires_at":"2028-01-01T00:00:00Z"}`)
	if w.Code != 409 {
		t.Fatalf("renew revoked token: got %d want 409", w.Code)
	}
	w = do("GET", "/api/v1/spaces/"+spaceA+"/sessions/"+sessionID+"/messages", map[string]string{
		"Authorization": "Bearer " + created.Token,
		"Cookie":        ownerCookie,
	}, "")
	if w.Code != 401 {
		t.Fatalf("revoked bearer with valid cookie: got %d want 401", w.Code)
	}

	// Ensure logs never contain the raw token.
	if strings.Contains(logBuf.String(), created.Token) {
		t.Fatal("raw token leaked into server logs")
	}
	// Ensure base64 decoding matches 32 bytes.
	if raw, err := base64.RawURLEncoding.DecodeString(created.Token); err != nil || len(raw) != 32 {
		t.Fatalf("unexpected raw token bytes: %v %d", err, len(raw))
	}
}

type pipeResponseWriter struct {
	header   http.Header
	pipe     *io.PipeWriter
	status   int
	statusCh chan int
}

func (p *pipeResponseWriter) Header() http.Header { return p.header }
func (p *pipeResponseWriter) WriteHeader(code int) {
	if p.status == 0 {
		p.status = code
		select {
		case p.statusCh <- code:
		default:
		}
	}
}
func (p *pipeResponseWriter) Write(b []byte) (int, error) {
	if p.status == 0 {
		p.WriteHeader(http.StatusOK)
	}
	return p.pipe.Write(b)
}
func (p *pipeResponseWriter) Flush() {}
func (p *pipeResponseWriter) SetWriteDeadline(time.Time) error {
	return nil
}
