package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/imalimin/mass/internal/adapter/secrets"
	"github.com/imalimin/mass/internal/app/identity"
	transport "github.com/imalimin/mass/internal/transport/http"
	"github.com/imalimin/mass/tests/testdb"
)

func TestOpenAPISchemaCompiles(t *testing.T) {
	// Validates that all OpenAPI 3.1 schemas (including OpenAPIToken*) compile cleanly.
	_ = contractValidator(t)
}

func TestOpenAPITokensDatabaseAndHTTP(t *testing.T) {
	if os.Getenv("MASS_TEST_ADMIN_DSN") == "" {
		t.Skip("set MASS_TEST_ADMIN_DSN to an isolated MariaDB server")
	}
	ctx := context.Background()
	db, err := testdb.New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Store.CheckSchema(ctx); err != nil {
		t.Fatal(err)
	}

	cipher, err := secrets.New(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := identity.New(db.Store, cipher, time.Hour, []byte("test-key"))
	if err != nil {
		t.Fatal(err)
	}
	owner, err := svc.CreateUser(ctx, "owner", "correct-password-owner")
	if err != nil {
		t.Fatal(err)
	}
	member, err := svc.CreateUser(ctx, "member", "correct-password-member")
	if err != nil {
		t.Fatal(err)
	}

	handler := transport.NewHandler(
		fstest.MapFS{"index.html": {Data: []byte("MASS")}},
		"test",
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		transport.IdentityOptions{Service: svc, Origin: "http://mass.test", Secure: true},
	)
	validateContract := contractValidator(t)
	call := func(method, path, cookie, authHeader string, body any) *httptest.ResponseRecorder {
		var reader io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			reader = bytes.NewReader(b)
		}
		r := httptest.NewRequest(method, "http://mass.test/api/v1"+path, reader)
		r.RemoteAddr = "127.0.0.1:1234"
		if cookie != "" {
			r.Header.Set("Cookie", cookie)
			r.Header.Set("Origin", "http://mass.test")
		}
		if authHeader != "" {
			r.Header.Set("Authorization", authHeader)
		}
		if method != http.MethodGet && method != http.MethodHead {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	api := func(method, path, cookie string, body any, status int) *httptest.ResponseRecorder {
		t.Helper()
		w := call(method, path, cookie, "", body)
		if w.Code != status {
			t.Fatalf("%s %s: want %d got %d: %s", method, path, status, w.Code, w.Body.String())
		}
		validateContract(method, path, w)
		return w
	}
	login := func(username, password string) string {
		r := httptest.NewRequest("POST", "http://mass.test/api/v1/auth/login", strings.NewReader(`{"username":"`+username+`","password":"`+password+`"}`))
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Origin", "http://mass.test")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("login %s: %d %s", username, w.Code, w.Body.String())
		}
		c := w.Result().Cookies()[0]
		return c.Name + "=" + c.Value
	}

	ownerCookie := login("owner", "correct-password-owner")
	memberCookie := login("member", "correct-password-member")

	var spaceA, spaceB identity.Space
	_ = json.Unmarshal(api("POST", "/spaces", ownerCookie, map[string]string{"name": "Space A"}, 201).Body.Bytes(), &spaceA)
	_ = json.Unmarshal(api("POST", "/spaces", ownerCookie, map[string]string{"name": "Space B"}, 201).Body.Bytes(), &spaceB)
	api("POST", "/spaces/"+spaceA.ID+"/members", ownerCookie, map[string]string{"username": "member"}, 204)

	base := "/spaces/" + spaceA.ID + "/openapi/tokens"

	// Create token (default 180 days).
	w := api("POST", base, ownerCookie, map[string]string{"name": "external-app"}, 201)
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("expected Cache-Control: no-store, got %q", w.Header().Get("Cache-Control"))
	}
	var created identity.CreatedOpenAPIToken
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	if len(created.Token) != 43 || created.Status != "active" {
		t.Fatalf("unexpected created token: %+v", created)
	}

	// Verify database stores hash + ciphertext and never plaintext.
	var storedHash, storedCipher []byte
	if err := db.Admin.DB.QueryRow("SELECT token_hash, ciphertext FROM space_openapi_tokens WHERE space_id=? AND id=?", spaceA.ID, created.ID).Scan(&storedHash, &storedCipher); err != nil {
		t.Fatal(err)
	}
	wantHash := sha256.Sum256([]byte(created.Token))
	if !bytes.Equal(storedHash, wantHash[:]) {
		t.Fatal("stored token_hash mismatch")
	}
	if bytes.Contains(storedCipher, []byte(created.Token)) {
		t.Fatal("plaintext token found in ciphertext column")
	}

	// List does not expose token plaintext.
	w = api("GET", base, ownerCookie, nil, 200)
	if strings.Contains(w.Body.String(), created.Token) {
		t.Fatal("list response leaked plaintext token")
	}

	// Copy decrypts and returns exact plaintext.
	w = api("GET", base+"/"+created.ID, ownerCookie, nil, 200)
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("expected Cache-Control: no-store on copy, got %q", w.Header().Get("Cache-Control"))
	}
	var copied identity.OpenAPITokenSecret
	_ = json.Unmarshal(w.Body.Bytes(), &copied)
	if copied.Token != created.Token {
		t.Fatal("copied token mismatch")
	}

	// Member gets 403; Bearer on management endpoint gets 401.
	api("GET", base, memberCookie, nil, 403)
	api("POST", base, memberCookie, map[string]string{"name": "bad"}, 403)
	if w := call("GET", base, ownerCookie, "Bearer "+created.Token, nil); w.Code != 401 {
		t.Fatalf("bearer on management endpoint: want 401 got %d", w.Code)
	}

	// AuthenticateBearer succeeds for spaceA and forbids spaceB.
	p, err := svc.AuthenticateBearer(ctx, created.Token, spaceA.ID)
	if err != nil || p.User.ID != owner.ID {
		t.Fatalf("AuthenticateBearer spaceA: %+v %v", p, err)
	}
	if _, err := svc.AuthenticateBearer(ctx, created.Token, spaceB.ID); err != identity.ErrForbidden {
		t.Fatalf("AuthenticateBearer spaceB: want ErrForbidden got %v", err)
	}

	// Expire token directly in DB, verify copy returns 409, auth returns 401, renew restores active.
	if _, err := db.Admin.DB.Exec("UPDATE space_openapi_tokens SET expires_at=UTC_TIMESTAMP(6) - INTERVAL 1 MINUTE WHERE space_id=? AND id=?", spaceA.ID, created.ID); err != nil {
		t.Fatal(err)
	}
	api("GET", base+"/"+created.ID, ownerCookie, nil, 409)
	if _, err := svc.AuthenticateBearer(ctx, created.Token, spaceA.ID); err != identity.ErrUnauthenticated {
		t.Fatalf("expired token auth: want ErrUnauthenticated got %v", err)
	}
	renewedExpiry := time.Now().UTC().Add(365 * 24 * time.Hour).Truncate(time.Second).Format(time.RFC3339)
	w = api("POST", base+"/"+created.ID+"/renew", ownerCookie, map[string]string{"expires_at": renewedExpiry}, 200)
	var renewed identity.OpenAPIToken
	_ = json.Unmarshal(w.Body.Bytes(), &renewed)
	if renewed.Status != "active" {
		t.Fatalf("renewed status=%q want active", renewed.Status)
	}

	// Revoke token (idempotent second revoke), verify copy/renew 409 and auth 401.
	api("DELETE", base+"/"+created.ID, ownerCookie, nil, 204)
	api("DELETE", base+"/"+created.ID, ownerCookie, nil, 204)
	api("GET", base+"/"+created.ID, ownerCookie, nil, 409)
	api("POST", base+"/"+created.ID+"/renew", ownerCookie, map[string]string{"expires_at": renewedExpiry}, 409)
	if _, err := svc.AuthenticateBearer(ctx, created.Token, spaceA.ID); err != identity.ErrUnauthenticated {
		t.Fatalf("revoked token auth: want ErrUnauthenticated got %v", err)
	}

	// Verify audit_logs contains exactly one row per created/copied/renewed/revoked action for token_id.
	rows, err := db.Admin.DB.Query("SELECT action FROM audit_logs WHERE space_id=? AND target_id=? ORDER BY id", spaceA.ID, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var actions []string
	for rows.Next() {
		var action string
		if err := rows.Scan(&action); err != nil {
			t.Fatal(err)
		}
		actions = append(actions, action)
	}
	wantActions := []string{"openapi_token.created", "openapi_token.copied", "openapi_token.renewed", "openapi_token.revoked"}
	if strings.Join(actions, ",") != strings.Join(wantActions, ",") {
		t.Fatalf("audit actions=%v want %v", actions, wantActions)
	}
	_ = member
}
