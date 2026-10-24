package resource_test

import (
	"context"
	"errors"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/resource"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNetworkStatusLimitsRedirectAndAuthorization(t *testing.T) {
	count := atomic.Int32{}
	var forwardedAuth atomic.Bool
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-Api-Key") != "" {
			forwardedAuth.Store(true)
		}
		w.Header().Set("Set-Cookie", "secret-cookie")
		if r.URL.Path == "/large" {
			w.Write([]byte(strings.Repeat("x", 40)))
			return
		}
		w.WriteHeader(409)
		w.Write([]byte("conflict"))
	}))
	defer receiver.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, receiver.URL+"/target", http.StatusFound)
	}))
	defer redirect.Close()
	check := func(context.Context) error { return nil }
	client := &resource.Network{}
	value, effect, err := client.Request(context.Background(), map[string]any{"url": redirect.URL, "headers": map[string]any{"Authorization": "explicit-secret", "X-Api-Key": "explicit-key"}}, 1024, check)
	if err != nil || effect != "committed" || value["status"] != 409 || value["body"] != "conflict" || forwardedAuth.Load() || count.Load() != 1 {
		t.Fatal(value, effect, err, forwardedAuth.Load(), count.Load())
	}
	if value["headers"].(map[string]string)["Set-Cookie"] != "[REDACTED]" {
		t.Fatal("response auth header leaked")
	}
	value, effect, err = client.Request(context.Background(), map[string]any{"url": receiver.URL + "/large"}, 8, check)
	if err == nil || effect != "committed" || value["complete"] != false || value["body"] != "xxxxxxxx" {
		t.Fatal("incomplete response hidden", value, effect, err)
	}
	before := count.Load()
	_, effect, err = client.Request(context.Background(), map[string]any{"url": receiver.URL}, 1024, func(context.Context) error { return run.ErrPermission })
	if !errors.Is(err, run.ErrPermission) || effect != "none" || count.Load() != before {
		t.Fatal("request after revocation", effect, err)
	}
	for _, target := range []string{"ftp://host/file", "http://user:password@host/path", "file:///etc/passwd", receiver.URL + "/#fragment"} {
		if _, _, err = client.Request(context.Background(), map[string]any{"url": target}, 1024, check); err == nil {
			t.Fatal("unsupported URL", target)
		}
	}
	wait := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer wait.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, effect, err = client.Request(ctx, map[string]any{"url": wait.URL}, 1024, check)
	if err == nil || effect != "unknown" || time.Since(start) > time.Second {
		t.Fatal("HTTP cancellation", effect, err)
	}
}
