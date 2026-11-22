// Package command runs installed programs in an OS sandbox.
package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/imalimin/mass/internal/resource"
)

func Execute(ctx context.Context, workspace, name string, args []string, maxOutput int) (resource.CommandOutput, error) {
	return ExecuteWithEnv(ctx, workspace, name, args, nil, maxOutput)
}

func ExecuteWithEnv(ctx context.Context, workspace, name string, args []string, extraEnv []string, maxOutput int) (resource.CommandOutput, error) {
	var out resource.CommandOutput
	path, err := exec.LookPath(name)
	if err != nil {
		return out, err
	}
	program, err := filepath.EvalSymlinks(path)
	if err != nil {
		return out, err
	}
	workspace, err = filepath.EvalSymlinks(workspace)
	if err != nil {
		return out, err
	}
	if !filepath.IsAbs(path) || !filepath.IsAbs(workspace) {
		return out, errors.New("command and workspace must resolve to absolute paths")
	}
	tmp, err := os.MkdirTemp("", "mass-command-")
	if err != nil {
		return out, err
	}
	defer os.RemoveAll(tmp)
	tmp, err = filepath.EvalSymlinks(tmp)
	if err != nil {
		return out, err
	}

	pathEnv := os.Getenv("PATH")
	profile, params := sandbox(workspace, tmp, program, pathEnv)
	argv := append([]string{"-p", profile}, params...)
	argv = append(argv, "--", path)
	cmd := exec.CommandContext(ctx, "/usr/bin/sandbox-exec", append(argv, args...)...)
	cmd.Dir = workspace
	cmd.Env = append([]string{"PATH=" + pathEnv, "HOME=" + tmp, "TMPDIR=" + tmp, "LANG=en_US.UTF-8"}, extraEnv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killGroup(cmd.Process.Pid) }
	// A child that keeps an output descriptor open must not hold up Wait.
	cmd.WaitDelay = time.Second
	stdout, stderr := &outputBuffer{limit: maxOutput}, &outputBuffer{limit: maxOutput}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil {
		return out, err
	}
	out.Started = true
	err = cmd.Wait()
	_ = killGroup(cmd.Process.Pid)
	out.Stdout, out.Stderr = stdout.buffer.String(), stderr.buffer.String()
	out.Truncated = stdout.truncated || stderr.truncated || errors.Is(err, exec.ErrWaitDelay)
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	if code := cmd.ProcessState.ExitCode(); code >= 0 {
		out.ExitCode = &code
	}
	return out, err
}

func killGroup(pid int) error {
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != syscall.ESRCH {
		return err
	}
	return os.ErrProcessDone
}

type outputBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (b *outputBuffer) Write(p []byte) (int, error) {
	n := min(len(p), max(0, b.limit-b.buffer.Len()))
	b.truncated = b.truncated || n < len(p)
	_, _ = b.buffer.Write(p[:n])
	return len(p), nil
}

const basePolicy = `
(version 1)
(deny default)
(allow process-exec process-fork)
(allow signal (target same-sandbox))
(allow process-info* (target same-sandbox))
(allow file-read-metadata)
; The system loader and getcwd need to read the root directory itself.
(allow file-read* (literal "/"))
(allow file-read* file-map-executable (subpath "/System/Library") (subpath "/usr/lib") (subpath "/usr/share")
    (subpath "/Library/Apple/System/Library") (subpath "/Library/Fonts")
    (literal "/dev/null") (literal "/dev/urandom") (literal "/dev/random"))
(allow file-read* (subpath "/private/etc/ssl"))
(allow file-write-data (literal "/dev/null"))
(allow system-mac-syscall (require-all (mac-policy-name "Sandbox") (mac-syscall-number 67)))
(allow sysctl-read (sysctl-name-prefix "hw.") (sysctl-name-prefix "net.routetable.")
    (sysctl-name "kern.argmax") (sysctl-name "kern.hostname") (sysctl-name "kern.maxfilesperproc")
    (sysctl-name "kern.ostype") (sysctl-name "kern.osrelease") (sysctl-name "kern.version")
    (sysctl-name "kern.osversion") (sysctl-name "kern.osproductversion")
    (sysctl-name "sysctl.proc_cputype"))
(allow system-socket (socket-domain AF_INET) (socket-domain AF_INET6))
(allow network-outbound network-inbound (remote ip))
(allow network-bind (local ip))
; getaddrinfo uses the system DNS resolver's local socket.
(allow system-socket (socket-domain AF_UNIX))
(allow network-outbound (remote unix-socket (literal "/private/var/run/mDNSResponder")))
(allow system-socket (require-all (socket-domain AF_SYSTEM) (socket-protocol 2)))
(allow mach-lookup (global-name "com.apple.system.opendirectoryd.membership")
    (global-name "com.apple.SystemConfiguration.DNSConfiguration")
    (global-name "com.apple.SystemConfiguration.configd")
    (global-name "com.apple.networkd") (global-name "com.apple.SecurityServer")
    (global-name "com.apple.trustd.agent") (global-name "com.apple.TrustEvaluationAgent")
    (global-name "com.apple.ocspd"))
`

func sandbox(workspace, tmp, program, pathEnv string) (string, []string) {
	var profile strings.Builder
	profile.WriteString(basePolicy)
	var params []string
	rule := func(operation, match, path string) {
		key := "P" + strconv.Itoa(len(params)/2)
		params = append(params, "-D", key+"="+path)
		fmt.Fprintf(&profile, "(%s (%s (param %q)))\n", operation, match, key)
	}
	seen := map[string]bool{}
	read := func(path string) {
		if !seen[path] {
			seen[path] = true
			rule("allow file-read* file-map-executable", "subpath", path)
		}
	}
	for _, dir := range filepath.SplitList(pathEnv) {
		if filepath.IsAbs(dir) && filepath.Clean(dir) != "/" {
			read(filepath.Clean(dir))
			if resolved, err := filepath.EvalSymlinks(dir); err == nil && resolved != "/" {
				read(resolved)
			}
		}
	}
	// Homebrew links executables and libraries across formula directories.
	for _, prefix := range []string{"/opt/homebrew", "/usr/local"} {
		for _, dir := range []string{"Cellar", "opt", "lib", "etc/ca-certificates", "etc/fonts", "share/fonts"} {
			read(filepath.Join(prefix, dir))
		}
	}
	rule("allow file-read* file-map-executable", "literal", program)
	// The shared workspace parent also holds other sessions and their journals.
	rule("deny file-read* file-map-executable", "subpath", filepath.Dir(workspace))
	for _, dir := range []string{workspace, tmp} {
		rule("allow file-read* file-write* file-map-executable", "subpath", dir)
	}
	return profile.String(), params
}
