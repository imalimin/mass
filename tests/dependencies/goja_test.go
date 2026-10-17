package dependencies_test

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dop251/goja"
	"github.com/dop251/goja/parser"
)

// probeVM is a dependency experiment, not the production D05 CodeRunner. The
// fixture host deliberately permits only files under t.TempDir and one local URL.
func probeVM(ctx context.Context, directory, endpoint, source string, sourceMapLoads *atomic.Int32) (goja.Value, error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	vm := goja.New()
	vm.SetParserOptions(parser.WithDisableSourceMaps, parser.WithSourceMapLoader(func(string) ([]byte, error) {
		sourceMapLoads.Add(1)
		return nil, errors.New("external source maps forbidden")
	}))
	check := func(name string) {
		if err := ctx.Err(); err != nil {
			panic(vm.NewGoError(err))
		}
		if !fs.ValidPath(name) || name == "." {
			panic(vm.NewTypeError("invalid path"))
		}
		for p := name; p != "."; p = filepath.Dir(p) {
			if info, e := root.Lstat(p); e == nil && (info.Mode()&os.ModeSymlink != 0) {
				panic(vm.NewTypeError("links forbidden"))
			}
		}
	}
	fileObject := vm.NewObject()
	if err := fileObject.Set("readFile", func(name string) string {
		check(name)
		f, e := root.Open(name)
		if e != nil {
			panic(vm.NewGoError(e))
		}
		defer f.Close()
		b, e := io.ReadAll(io.LimitReader(f, 1025))
		if e != nil {
			panic(vm.NewGoError(e))
		}
		if len(b) > 1024 {
			panic(vm.NewTypeError("file too large"))
		}
		return string(b)
	}); err != nil {
		return nil, err
	}
	if err := fileObject.Set("writeFile", func(name, content string) {
		check(name)
		if len(content) > 1024 {
			panic(vm.NewTypeError("file too large"))
		}
		if e := root.WriteFile(name, []byte(content), 0600); e != nil {
			panic(vm.NewGoError(e))
		}
	}); err != nil {
		return nil, err
	}
	if err := vm.Set("fs", fileObject); err != nil {
		return nil, err
	}
	netObject := vm.NewObject()
	if err := netObject.Set("request", func(target string) string {
		if target != endpoint {
			panic(vm.NewTypeError("unapproved fixture URL"))
		}
		req, e := http.NewRequestWithContext(ctx, "GET", target, nil)
		if e != nil {
			panic(vm.NewGoError(e))
		}
		client := http.Client{Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		res, e := client.Do(req)
		if e != nil {
			panic(vm.NewGoError(e))
		}
		defer res.Body.Close()
		b, e := io.ReadAll(io.LimitReader(res.Body, 1025))
		if e != nil {
			panic(vm.NewGoError(e))
		}
		if len(b) > 1024 {
			panic(vm.NewTypeError("response too large"))
		}
		return string(b)
	}); err != nil {
		return nil, err
	}
	if err := vm.Set("net", netObject); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		select {
		case <-ctx.Done():
			vm.Interrupt(ctx.Err())
		case <-done:
		}
	}()
	defer func() { close(done); <-exited }()
	return vm.RunScript("probe.js", source)
}

func TestGojaHostCapabilities(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "input.txt"), []byte("abc"), 0600); err != nil {
		t.Fatal(err)
	}
	var requests, loads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, "local-response")
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	source := `let result = ""; for(let i=0;i<3;i++) result += fs.readFile("input.txt"); fs.writeFile("output.txt",result); net.request("` + server.URL + `"); result;` + "\n//# sourceMappingURL=" + server.URL + "/map"
	value, err := probeVM(ctx, dir, server.URL, source, &loads)
	if err != nil || value.String() != "abcabcabc" {
		t.Fatalf("value=%v err=%v", value, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "output.txt"))
	if err != nil || string(data) != "abcabcabc" {
		t.Fatalf("output=%q err=%v", data, err)
	}
	if requests.Load() != 1 || loads.Load() != 0 {
		t.Fatalf("requests=%d sourceMaps=%d", requests.Load(), loads.Load())
	}
	_, err = probeVM(ctx, dir, server.URL, `fs.writeFile("committed.txt","kept"); throw new Error("after write")`, &loads)
	if err == nil {
		t.Fatal("expected error")
	}
	if data, e := os.ReadFile(filepath.Join(dir, "committed.txt")); e != nil || string(data) != "kept" {
		t.Fatal("side effect was lost")
	}
	for _, source := range []string{`fs.readFile("../outside")`, `fs.readFile("/etc/passwd")`, `require("fs")`, `process.env`, `result`} {
		if _, err := probeVM(ctx, dir, server.URL, source, &loads); err == nil {
			t.Fatalf("unexpected capability: %s", source)
		}
	}
	_, err = probeVM(ctx, dir, server.URL, `eval("1\n//# sourceMappingURL=`+server.URL+`/eval"); new Function("return 1;\n//# sourceMappingURL=`+server.URL+`/function")();`, &loads)
	if err != nil || loads.Load() != 0 || requests.Load() != 1 {
		t.Fatalf("dynamic source map: %v loads=%d requests=%d", err, loads.Load(), requests.Load())
	}
}

func TestGojaCancellation(t *testing.T) {
	for _, kind := range []string{"loop", "network"} {
		t.Run(kind, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			source := `while(true){}`
			if kind == "network" {
				source = `net.request("` + server.URL + `")`
			}
			var loads atomic.Int32
			start := time.Now()
			_, err := probeVM(ctx, t.TempDir(), server.URL, source, &loads)
			if err == nil || time.Since(start) > 2*time.Second || !strings.Contains(err.Error(), "deadline") {
				t.Fatalf("cancellation not propagated: %v", err)
			}
		})
	}
}
