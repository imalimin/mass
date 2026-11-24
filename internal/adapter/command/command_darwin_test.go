package command

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCommandSeatbelt(t *testing.T) {
	if output, err := exec.Command("/usr/bin/sandbox-exec", "-p", "(version 1) (allow default)", "--", "/usr/bin/true").CombinedOutput(); err != nil {
		t.Skipf("Seatbelt cannot be started in the test environment: %s", output)
	}
	root := t.TempDir()
	workspace := filepath.Join(root, "work")
	bin := t.TempDir()
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	success := func(t *testing.T, name string, args ...string) string {
		t.Helper()
		out, err := Execute(ctx, workspace, name, args, 8192)
		if err != nil {
			t.Fatalf("%s: %v, %s", name, err, out.Stderr)
		}
		return out.Stdout
	}
	t.Run("files and arguments", func(t *testing.T) {
		outside := filepath.Join(root, "private")
		readOnly := filepath.Join(bin, "read-only")
		for _, path := range []string{outside, readOnly} {
			if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
		t.Setenv("MASS_COMMAND_TEST_SECRET", "private")
		if err := os.Symlink(outside, filepath.Join(workspace, "linked")); err != nil {
			t.Fatal(err)
		}
		arg := "hello ; $(pwd)"
		if got := success(t, "sh", "-c", `printf '%s' "$1" > result.txt; cat result.txt`, "mass", arg); got != arg {
			t.Fatalf("argument changed: %q", got)
		}
		if got := success(t, "cat", readOnly); got != "original" {
			t.Fatalf("unexpected PATH file content %q", got)
		}
		for _, args := range [][]string{
			{"cat", outside}, {"cat", "linked"}, {"sh", "-c", `echo changed > linked`},
			{"sh", "-c", `echo changed > "$1"`, "mass", readOnly},
			{"sh", "-c", `ln "$1" hardlink && echo changed > hardlink`, "mass", readOnly},
		} {
			out, err := Execute(ctx, workspace, args[0], args[1:], 2048)
			if err == nil || out.ExitCode == nil || *out.ExitCode == 0 {
				t.Errorf("outside workspace access succeeded: %v, %+v", args, out)
			}
		}
		if got := success(t, "sh", "-c", `printf '%s' "$MASS_COMMAND_TEST_SECRET"`); got != "" {
			t.Fatal("command inherited service environment")
		}
	})
	t.Run("network", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "ok") }))
		defer server.Close()
		if got := success(t, "curl", "--insecure", "--fail", "--silent", "--show-error", server.URL); got != "ok" {
			t.Fatalf("unexpected response %q", got)
		}
	})
	t.Run("installed downloader", func(t *testing.T) {
		if _, err := exec.LookPath("yt-dlp"); err != nil {
			t.Skip(err)
		}
		if got := success(t, "yt-dlp", "--version"); strings.TrimSpace(got) == "" {
			t.Fatal("yt-dlp returned no version")
		}
	})
	t.Run("exit and timeout", func(t *testing.T) {
		out, err := Execute(ctx, workspace, "sh", []string{"-c", "printf 12345; printf detail >&2; exit 7"}, 4)
		if err == nil || out.ExitCode == nil || *out.ExitCode != 7 || out.Stdout != "1234" || out.Stderr != "deta" || !out.Truncated {
			t.Fatalf("unexpected result %+v (%v)", out, err)
		}
		deadline, stop := context.WithTimeout(ctx, 500*time.Millisecond)
		defer stop()
		out, err = Execute(deadline, workspace, "sh", []string{"-c", "sleep 60 & echo $!; wait"}, 2048)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected deadline, got %+v (%v)", out, err)
		}
		var child int
		if _, err := fmt.Sscan(out.Stdout, &child); err != nil {
			t.Fatalf("child PID missing: %+v", out)
		}
		until := time.Now().Add(time.Second)
		for syscall.Kill(child, 0) == nil && time.Now().Before(until) {
			time.Sleep(10 * time.Millisecond)
		}
		if err := syscall.Kill(child, 0); !errors.Is(err, syscall.ESRCH) {
			t.Fatalf("child %d survived the timeout: %v", child, err)
		}
	})
	t.Run("installed video tools", func(t *testing.T) {
		for _, name := range []string{"ffmpeg", "ffprobe"} {
			if _, err := exec.LookPath(name); err != nil {
				t.Skip(err)
			}
		}
		if err := os.WriteFile(filepath.Join(workspace, "zh.srt"), []byte("1\n00:00:00,000 --> 00:00:01,000\n测试\n"), 0600); err != nil {
			t.Fatal(err)
		}
		success(t, "ffmpeg", "-v", "error", "-f", "lavfi", "-i", "color=size=160x90:rate=10", "-t", "1", "-vf", "subtitles=zh.srt", "-c:v", "libx264", "clip.mp4")
		if got := success(t, "ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", "clip.mp4"); strings.TrimSpace(got) != "1.000000" {
			t.Fatalf("unexpected clip duration %q", got)
		}
	})
	t.Run("installed go toolchain", func(t *testing.T) {
		if _, err := exec.LookPath("go"); err != nil {
			t.Skip(err)
		}
		outside := filepath.Join(root, "go-private")
		if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
			t.Fatal(err)
		}
		src := fmt.Sprintf(`package main
import (
	"fmt"
	"os"
)
func main() {
	if _, err := os.ReadFile(%q); err == nil {
		os.Exit(2)
	}
	fmt.Print("go-sandbox-ok")
}
`, outside)
		if err := os.WriteFile(filepath.Join(workspace, "main.go"), []byte(src), 0600); err != nil {
			t.Fatal(err)
		}
		if got := success(t, "sh", "-c", "go run main.go"); got != "go-sandbox-ok" {
			t.Fatalf("go run output = %q, want %q", got, "go-sandbox-ok")
		}
		if got := success(t, "sh", "-c", "CGO_ENABLED=0 go run main.go"); got != "go-sandbox-ok" {
			t.Fatalf("CGO_ENABLED=0 go run output = %q, want %q", got, "go-sandbox-ok")
		}
	})
	t.Run("installed python and node", func(t *testing.T) {
		outside := filepath.Join(root, "script-private")
		if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := exec.LookPath("python3"); err == nil {
			pyCmd := fmt.Sprintf(`python3 -c 'import json, pathlib, sys
try:
    pathlib.Path(%q).read_text()
    sys.exit(2)
except PermissionError:
    pass
print(json.dumps({"ok": True}), end="")'`, outside)
			if got := success(t, "sh", "-c", pyCmd); got != `{"ok": true}` {
				t.Fatalf("python3 output = %q", got)
			}
		}
		if _, err := exec.LookPath("node"); err == nil {
			nodeCmd := fmt.Sprintf(`node -e 'const fs = require("fs");
try { fs.readFileSync(%q); process.exit(2); } catch {}
process.stdout.write(JSON.stringify({ ok: true }));'`, outside)
			if got := success(t, "sh", "-c", nodeCmd); got != `{"ok":true}` {
				t.Fatalf("node output = %q", got)
			}
		}
	})
}

func TestToolchainRootsAutoDetection(t *testing.T) {
	shimDir := t.TempDir()
	goRoot := t.TempDir()
	pyPrefix := t.TempDir()
	pyBase := t.TempDir()
	nodeRoot := t.TempDir()
	nodeBinDir := filepath.Join(nodeRoot, "bin")
	if err := os.MkdirAll(nodeBinDir, 0700); err != nil {
		t.Fatal(err)
	}
	nodeRealBin := filepath.Join(nodeBinDir, "node")
	if err := os.WriteFile(nodeRealBin, []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}

	writeShim := func(name, script string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(shimDir, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	writeShim("go", fmt.Sprintf("#!/bin/sh\n[ \"$GOTOOLCHAIN\" = \"local\" ] || exit 1\n[ \"$GOPROXY\" = \"off\" ] || exit 1\n[ \"$MISE_AUTO_INSTALL\" = \"0\" ] || exit 1\nif [ \"$1\" = \"env\" ] && [ \"$2\" = \"GOROOT\" ]; then printf '%%s\\n' %q; fi\n", goRoot))
	writeShim("python3", fmt.Sprintf("#!/bin/sh\n[ \"$UV_PYTHON_DOWNLOADS\" = \"never\" ] || exit 1\n[ \"$MISE_AUTO_INSTALL\" = \"0\" ] || exit 1\nprintf '%%s\\n%%s\\n' %q %q\n", pyPrefix, pyBase))
	writeShim("node", fmt.Sprintf("#!/bin/sh\n[ \"$MISE_AUTO_INSTALL\" = \"0\" ] || exit 1\nprintf '%%s\\n' %q\n", nodeRealBin))

	roots := toolchainRoots(shimDir)
	hasRoot := func(target string) bool {
		resolved := evalSymlinksOrEmpty(target)
		for _, r := range roots {
			if r == target || (resolved != "" && r == resolved) {
				return true
			}
		}
		return false
	}
	for label, target := range map[string]string{
		"go": goRoot, "python prefix": pyPrefix, "python base": pyBase, "node": nodeRoot,
	} {
		if !hasRoot(target) {
			t.Errorf("missing auto-detected %s root %s in %v", label, target, roots)
		}
	}
	profile, params := sandbox(filepath.Join(t.TempDir(), "work"), t.TempDir(), filepath.Join(shimDir, "go"), shimDir)
	if !strings.Contains(profile, "/Library/Developer/CommandLineTools") {
		t.Fatal("CommandLineTools missing from sandbox policy")
	}
	joined := strings.Join(params, "\n")
	for _, target := range []string{goRoot, pyPrefix, pyBase, nodeRoot} {
		if !strings.Contains(joined, "="+target) {
			t.Fatalf("auto-detected root %s missing from sandbox params:\n%s", target, joined)
		}
	}

	// Absent tools produce no hardcoded roots; broad system/home paths are rejected.
	emptyDir := t.TempDir()
	if got := toolchainRoots(emptyDir); len(got) != 0 {
		t.Fatalf("expected no toolchain roots when none are in PATH, got %v", got)
	}
	home := t.TempDir()
	for _, broad := range []string{"/", "/usr", "/usr/local", "/opt/homebrew", "/Users", home} {
		if isSafeToolchainRoot(broad, home) {
			t.Fatalf("broad root %q was accepted", broad)
		}
	}
}
