package resource_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/imalimin/mass/internal/adapter/javascript"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/resource"
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
	for _, tc := range []struct{ method, effect string }{{"GET", "none"}, {"POST", "unknown"}} {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		_, effect, err = client.Request(ctx, map[string]any{"url": wait.URL, "method": tc.method}, 1024, check)
		cancel()
		if !errors.Is(err, run.ErrNetworkTimeout) || effect != tc.effect {
			t.Fatalf("%s timeout: effect=%q err=%v", tc.method, effect, err)
		}
	}
	partial := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("partial"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer partial.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	value, effect, err = client.Request(ctx, map[string]any{"url": partial.URL, "method": "POST"}, 1024, check)
	if !errors.Is(err, run.ErrNetworkTimeout) || effect != "committed" || value["complete"] != false || value["body"] != "partial" {
		t.Fatal("response body timeout", value, effect, err)
	}
}

func TestCodeReportsHTTPClientTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	files, snapshot, _ := fileFixture(t)
	snapshot.Definition.ToolPolicy = run.ToolPolicy{
		TimeoutMS: 5000, MaxCodeBytes: 1024, MaxInputBytes: 1024, MaxOutputBytes: 1024,
		MaxNetworkRequests: 1, MaxNetworkBodyBytes: 1024,
	}
	code := &resource.CodeService{
		Files: files, Engine: &javascript.Runner{MaxConcurrent: 1},
		Network: &resource.Network{Client: &http.Client{Timeout: 50 * time.Millisecond}},
	}
	for _, tc := range []struct{ name, script, status, effect string }{
		{"GET", "return net.request({url: input});", "failed", "none"},
		{"GET handled", "try {net.request({url: input});} catch (err) {return true;}", "completed", "none"},
		{"POST handled", "try {net.request({url: input, method: 'POST'});} catch (err) {return true;}", "failed", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := run.CodeInput{Language: "javascript", Code: tc.script, Input: []byte("\"" + server.URL + "\"")}
			result, err := code.Run(context.Background(), snapshot, input)
			if result.Status != tc.status || result.SideEffect != tc.effect {
				t.Fatalf("unexpected tool result: status=%q effect=%q error=%v", result.Status, result.SideEffect, err)
			}
			if tc.status == "failed" {
				if !errors.Is(err, run.ErrNetworkTimeout) || result.Error != run.ErrNetworkTimeout.Error() {
					t.Fatalf("unexpected error: result=%q err=%v", result.Error, err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
