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
}
