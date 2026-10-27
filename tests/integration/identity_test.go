package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/imalimin/mass/internal/adapter/secrets"
	"github.com/imalimin/mass/internal/app/identity"
	transport "github.com/imalimin/mass/internal/transport/http"
	"github.com/imalimin/mass/tests/testdb"
)

func TestIdentityDatabaseAndHTTP(t *testing.T) {
	if os.Getenv("MASS_TEST_ADMIN_DSN") == "" {
		t.Skip("set MASS_TEST_ADMIN_DSN to an isolated MariaDB server")
	}
	ctx := context.Background()
	db, err := testdb.New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version string
	if err = db.Admin.DB.QueryRow("SELECT VERSION()").Scan(&version); err != nil {
		t.Fatal(err)
	}
	t.Log("MariaDB", version)
	if err = db.Store.CheckSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Store.DB.Exec("CREATE TABLE forbidden_ddl(id INT)"); err == nil {
		t.Fatal("application account has DDL privileges")
	}
	cipher, _ := secrets.New(bytes.Repeat([]byte{9}, 32))
	svc, _ := identity.New(db.Store, cipher, time.Hour, []byte("test-key"))
	alice, err := svc.CreateUser(ctx, "alice", "correct-password-alice")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := svc.CreateUser(ctx, "bob", "correct-password-bob")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.CreateUser(ctx, "alice", "correct-password-alice"); !errors.Is(err, identity.ErrConflict) {
		t.Fatal("duplicate account", err)
	}
	if _, err = svc.CreateUser(ctx, "weak", "short"); !errors.Is(err, identity.ErrInvalid) {
		t.Fatal("weak password", err)
	}
	var stored []byte
	db.Admin.DB.QueryRow("SELECT password_hash FROM users WHERE id=?", alice.ID).Scan(&stored)
	if bytes.Contains(stored, []byte("correct-password")) {
		t.Fatal("password persisted in plaintext")
	}
	handler := transport.NewHandler(fstest.MapFS{"index.html": {Data: []byte("MASS")}}, "test", slog.New(slog.NewTextHandler(io.Discard, nil)), transport.IdentityOptions{Service: svc, Origin: "http://mass.test", Secure: true})
	call := func(method, path, cookie, origin, content string, body any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "http://mass.test/api/v1"+path, bytes.NewReader(b))
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", content)
		if cookie != "" {
			r.Header.Set("Cookie", cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	validateContract := contractValidator(t)
	api := func(method, path, cookie string, body any, status int) *httptest.ResponseRecorder {
		t.Helper()
		w := call(method, path, cookie, "http://mass.test", "application/json", body)
		if w.Code != status {
			t.Fatalf("%s %s: want %d got %d: %s", method, path, status, w.Code, w.Body.String())
		}
		validateContract(method, path, w)
		return w
	}
	api("GET", "/auth/me", "", nil, 401)
	api("POST", "/auth/login", "", map[string]string{"username": "alice", "password": "wrong"}, 401)
	api("POST", "/auth/login", "", map[string]string{"username": "missing", "password": "wrong"}, 401)
	for _, origin := range []string{"", "null", "https://evil.test"} {
		if w := call("POST", "/auth/login", "", origin, "application/json", map[string]string{"username": "alice", "password": "correct-password-alice"}); w.Code != 403 {
			t.Fatal("origin bypass", w.Code)
		}
	}
	if w := call("POST", "/auth/login", "", "http://mass.test", "text/plain", nil); w.Code != 415 {
		t.Fatal("content type bypass")
	}
	login := func(name, password string) string {
		w := api("POST", "/auth/login", "", map[string]string{"username": name, "password": password}, 200)
		cookies := w.Result().Cookies()
		if len(cookies) != 1 {
			t.Fatal("cookie missing")
		}
		c := cookies[0]
		if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.MaxAge != 3600 {
			t.Fatal("cookie attributes", c)
		}
		return c.Name + "=" + c.Value
	}
	aCookie, bCookie := login("alice", "correct-password-alice"), login("bob", "correct-password-bob")
	space := func(cookie, name string) identity.Space {
		w := api("POST", "/spaces", cookie, map[string]string{"name": name}, 201)
		var s identity.Space
		json.Unmarshal(w.Body.Bytes(), &s)
		if s.CreatedAt.IsZero() {
			t.Fatal("space creation timestamp missing")
		}
		return s
	}
	a, b := space(aCookie, "Alice space"), space(bCookie, "Bob space")
	aBase, bBase := "/spaces/"+a.ID, "/spaces/"+b.ID
	var listed identity.Page[identity.Space]
	json.Unmarshal(api("GET", "/spaces", aCookie, nil, 200).Body.Bytes(), &listed)
	if len(listed.Items) != 1 || listed.Items[0].ID != a.ID || !listed.Items[0].CreatedAt.Equal(a.CreatedAt) {
		t.Fatal("space creation time changed between create and list", listed.Items)
	}
	api("GET", aBase+"/members", bCookie, nil, 403)
	api("POST", aBase+"/members", bCookie, map[string]string{"username": "bob"}, 403)
	api("POST", aBase+"/members", aCookie, map[string]string{"username": "bob"}, 204)
	api("POST", aBase+"/members", aCookie, map[string]string{"username": "bob"}, 204)
	api("GET", aBase+"/members", bCookie, nil, 200)
	api("DELETE", aBase+"/members/"+alice.ID, aCookie, nil, 403)
	api("DELETE", aBase+"/members/"+alice.ID, bCookie, nil, 403)
	api("POST", aBase+"/members", aCookie, map[string]string{"username": "missing"}, 404)
	// Pagination tokens cannot be transplanted across actors or list scopes.
	w := api("GET", "/spaces?limit=1", bCookie, nil, 200)
	var page identity.Page[identity.Space]
	json.Unmarshal(w.Body.Bytes(), &page)
	if page.NextCursor == nil {
		t.Fatal("pagination missing")
	}
	api("GET", "/spaces?limit=1&cursor="+*page.NextCursor, bCookie, nil, 200)
	api("GET", "/spaces?cursor="+*page.NextCursor, aCookie, nil, 400)
	var cred identity.Credential
	w = api("POST", aBase+"/credentials", aCookie, identity.CredentialInput{Name: "Ark", Secret: "very-private-provider-key", Enabled: true}, 201)
	json.Unmarshal(w.Body.Bytes(), &cred)
	for _, path := range []string{aBase + "/credentials", bBase + "/credentials"} {
		api("GET", path, bCookie, nil, map[bool]int{true: 403, false: 200}[strings.HasPrefix(path, aBase)])
	}
	if strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "ciphertext") {
		t.Fatal("secret leaked")
	}
	var encrypted []byte
	db.Admin.DB.QueryRow("SELECT ciphertext FROM credentials WHERE space_id=? AND id=?", a.ID, cred.ID).Scan(&encrypted)
	if bytes.Contains(encrypted, []byte("very-private")) {
		t.Fatal("plaintext credential")
	}
	model := identity.Model{Name: "Ark model", Provider: "ark", ModelID: "ep-test", Endpoint: identity.ArkEndpoint, CredentialID: cred.ID, ContextWindow: 32768, MaxOutputTokens: 4096, Temperature: .7, Enabled: true}
	api("POST", bBase+"/models", bCookie, model, 404)
	bad := model
	bad.Endpoint = "http://127.0.0.1:9999"
	api("POST", aBase+"/models", aCookie, bad, 400)
	api("POST", aBase+"/models", bCookie, model, 403)
	w = api("POST", aBase+"/models", aCookie, model, 201)
	var saved identity.Model
	json.Unmarshal(w.Body.Bytes(), &saved)
	api("GET", bBase+"/models/"+saved.ID, bCookie, nil, 404)
	secret, err := svc.ModelSecret(ctx, bob.ID, a.ID, saved.ID)
	if err != nil || string(secret) != "very-private-provider-key" {
		t.Fatal("credential resolution", err)
	}
	// A failed audit statement rolls back the business mutation as well.
	_, err = db.Admin.DB.Exec("CREATE TRIGGER reject_audit BEFORE INSERT ON audit_logs FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test audit failure'")
	if err != nil {
		t.Fatal(err)
	}
	api("DELETE", aBase+"/members/"+bob.ID, aCookie, nil, 500)
	if err = svc.CheckAccess(ctx, bob.ID, a.ID); err != nil {
		t.Fatal("membership not rolled back", err)
	}
	db.Admin.DB.Exec("DROP TRIGGER reject_audit")
	watching, release, err := svc.WatchAccess(ctx, bob.ID, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	// Exercise the same space lock from multiple requests while revoking.
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := db.Store.Models(ctx, bob.ID, a.ID, identity.Query{Limit: 10}, true)
			if e != nil && !errors.Is(e, identity.ErrForbidden) {
				t.Errorf("concurrent read: %v", e)
			}
		}()
	}
	api("DELETE", aBase+"/members/"+bob.ID, aCookie, nil, 204)
	wg.Wait()
	select {
	case <-watching.Done():
	case <-time.After(time.Second):
		t.Fatal("revocation notification missing")
	}
	api("GET", aBase+"/models", bCookie, nil, 403)
	if _, err = svc.ModelSecret(ctx, bob.ID, a.ID, saved.ID); !errors.Is(err, identity.ErrForbidden) {
		t.Fatal("revoked actor resolved secret", err)
	}
	if _, _, err = svc.WatchAccess(ctx, bob.ID, a.ID); !errors.Is(err, identity.ErrForbidden) {
		t.Fatal("revoked watch subscribed", err)
	}
	api("PUT", aBase+"/credentials/"+cred.ID, aCookie, identity.CredentialInput{Name: cred.Name, Enabled: false, Revision: cred.Revision}, 200)
	api("PUT", aBase+"/credentials/"+cred.ID, aCookie, identity.CredentialInput{Name: cred.Name, Enabled: true, Revision: cred.Revision}, 409)
	w = api("GET", aBase+"/models?available=true", aCookie, nil, 200)
	if !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatal("disabled credential model available")
	}
	if _, err = svc.ModelSecret(ctx, alice.ID, a.ID, saved.ID); !errors.Is(err, identity.ErrNotFound) {
		t.Fatal("disabled credential resolved", err)
	}
	api("PUT", aBase+"/credentials/"+cred.ID, aCookie, identity.CredentialInput{Name: cred.Name, Secret: "rotated-secret", Enabled: true, Revision: 2}, 200)
	secret, err = svc.ModelSecret(ctx, alice.ID, a.ID, saved.ID)
	if err != nil || string(secret) != "rotated-secret" {
		t.Fatal("rotation", err)
	}
	api("DELETE", aBase+"/models/"+saved.ID, aCookie, map[string]int{"revision": 1}, 204)
	api("GET", aBase+"/models/"+saved.ID, aCookie, nil, 200)
	api("POST", aBase+"/members", aCookie, map[string]string{"username": "bob"}, 204)
	api("DELETE", aBase+"/members/"+bob.ID, bCookie, nil, 204) // self leave
	api("POST", "/auth/logout", aCookie, nil, 204)
	api("GET", "/auth/me", aCookie, nil, 401)
	db.Admin.DB.Exec("UPDATE auth_sessions SET expires_at=UTC_TIMESTAMP(6)-INTERVAL 1 SECOND WHERE user_id=?", bob.ID)
	api("GET", "/auth/me", bCookie, nil, 401)
	// Rate limiting is tested on a separate remote address, independent of logins.
	for i := 0; i < 11; i++ {
		r := httptest.NewRequest("POST", "http://mass.test/api/v1/auth/login", strings.NewReader(`{"username":"missing","password":"invalid"}`))
		r.RemoteAddr = "127.0.0.2:44"
		r.Header.Set("Origin", "http://mass.test")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		expected := 401
		if i == 10 {
			expected = 429
		}
		if w.Code != expected {
			t.Fatalf("rate limit %d: %d", i, w.Code)
		}
	}
	var count int
	db.Admin.DB.QueryRow("SELECT COUNT(*) FROM audit_logs WHERE space_id=? AND action='member.added'", a.ID).Scan(&count)
	if count != 2 {
		t.Fatalf("idempotent add audit count %d", count)
	}
}
