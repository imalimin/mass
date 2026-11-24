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
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/imalimin/mass/internal/resource"
)

func Execute(ctx context.Context, workspace, name string, args []string, maxOutput int) (resource.CommandOutput, error) {
	return ExecuteWithOptions(ctx, workspace, name, args, resource.CommandOptions{}, maxOutput)
}

func ExecuteWithEnv(ctx context.Context, workspace, name string, args []string, extraEnv []string, maxOutput int) (resource.CommandOutput, error) {
	return ExecuteWithOptions(ctx, workspace, name, args, resource.CommandOptions{Env: extraEnv}, maxOutput)
}

func ExecuteWithOptions(ctx context.Context, workspace, name string, args []string, opts resource.CommandOptions, maxOutput int) (resource.CommandOutput, error) {
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

	env := append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + tmp, "TMPDIR=" + tmp, "LANG=en_US.UTF-8"}, opts.Env...)
	var readOnlyDirs []string
	if len(opts.SkillDirs) > 0 {
		skillsDir := filepath.Join(tmp, "skills")
		if err := os.Mkdir(skillsDir, 0700); err != nil {
			return out, err
		}
		names := make([]string, 0, len(opts.SkillDirs))
		for skillName := range opts.SkillDirs {
			names = append(names, skillName)
		}
		sort.Strings(names)
		for _, skillName := range names {
			target, err := filepath.EvalSymlinks(opts.SkillDirs[skillName])
			if err != nil || !filepath.IsAbs(target) {
				return out, errors.New("bound skill directory must resolve to an absolute path")
			}
			if err := os.Symlink(target, filepath.Join(skillsDir, skillName)); err != nil {
				return out, err
			}
			readOnlyDirs = append(readOnlyDirs, target)
		}
		env = append(env, resource.SkillsDirEnv+"="+skillsDir)
	}

	pathEnv := os.Getenv("PATH")
	profile, params := sandbox(workspace, tmp, program, pathEnv, readOnlyDirs...)
	argv := append([]string{"-p", profile}, params...)
	argv = append(argv, "--", path)
	cmd := exec.CommandContext(ctx, "/usr/bin/sandbox-exec", append(argv, args...)...)
	cmd.Dir = workspace
	cmd.Env = env
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
    (subpath "/Library/Apple/System/Library") (subpath "/Library/Developer/CommandLineTools") (subpath "/Library/Fonts")
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

func sandbox(workspace, tmp, program, pathEnv string, readOnlyDirs ...string) (string, []string) {
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
	// Auto-detect Go, Python, and Node toolchain roots from PATH without triggering auto-installs.
	for _, root := range toolchainRoots(pathEnv) {
		read(root)
	}
	rule("allow file-read* file-map-executable", "literal", program)
	// The shared workspace parent also holds other sessions and their journals.
	rule("deny file-read* file-map-executable", "subpath", filepath.Dir(workspace))
	for _, dir := range []string{workspace, tmp} {
		rule("allow file-read* file-write* file-map-executable", "subpath", dir)
	}
	for _, dir := range readOnlyDirs {
		rule("allow file-read* file-map-executable", "subpath", dir)
	}
	return profile.String(), params
}

var toolchainCache struct {
	mu    sync.Mutex
	roots map[string][]string
}

func toolchainRoots(pathEnv string) []string {
	home := os.Getenv("HOME")
	key := pathEnv + "\x00" + home
	toolchainCache.mu.Lock()
	if cached, ok := toolchainCache.roots[key]; ok {
		toolchainCache.mu.Unlock()
		return cached
	}
	toolchainCache.mu.Unlock()

	roots := []string{}
	seen := map[string]bool{}
	add := func(p string) {
		p = filepath.Clean(p)
		if !isSafeToolchainRoot(p, home) || seen[p] {
			return
		}
		seen[p] = true
		roots = append(roots, p)
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			resolved = filepath.Clean(resolved)
			if isSafeToolchainRoot(resolved, home) && !seen[resolved] {
				seen[resolved] = true
				roots = append(roots, resolved)
			}
		}
	}
	addBinParent := func(binPath string) {
		for _, p := range []string{binPath, evalSymlinksOrEmpty(binPath)} {
			if p == "" {
				continue
			}
			binDir := filepath.Dir(p)
			if filepath.Base(binDir) == "bin" {
				parent := filepath.Dir(binDir)
				add(parent)
			}
		}
	}

	probeEnv := []string{
		"PATH=" + pathEnv,
		"HOME=" + home,
		"GOTOOLCHAIN=local",
		"GOPROXY=off",
		"GOTELEMETRY=off",
		"MISE_AUTO_INSTALL=0",
		"MISE_NOT_FOUND_AUTO_INSTALL=0",
		"UV_PYTHON_DOWNLOADS=never",
		"PYTHONNOUSERSITE=1",
		"PYTHONDONTWRITEBYTECODE=1",
		"NODE_DISABLE_COLORS=1",
	}

	// Go
	if goBin := findInPath(pathEnv, "go"); goBin != "" {
		for _, line := range probeLines(probeEnv, goBin, "env", "GOROOT") {
			add(line)
		}
		addBinParent(goBin)
	}

	// Python (python3 and python if distinct)
	seenPy := map[string]bool{}
	for _, name := range []string{"python3", "python"} {
		pyBin := findInPath(pathEnv, name)
		if pyBin == "" || isUninstalledApplePythonStub(pyBin) {
			continue
		}
		canonical := pyBin
		if resolved, err := filepath.EvalSymlinks(pyBin); err == nil {
			canonical = resolved
		}
		if seenPy[canonical] {
			continue
		}
		seenPy[canonical] = true
		const pyScript = `import sys; print("\n".join(dict.fromkeys([sys.prefix, sys.base_prefix, sys.exec_prefix, sys.base_exec_prefix])))`
		for _, line := range probeLines(probeEnv, pyBin, "-c", pyScript) {
			add(line)
		}
		addBinParent(pyBin)
	}

	// Node
	if nodeBin := findInPath(pathEnv, "node"); nodeBin != "" {
		for _, line := range probeLines(probeEnv, nodeBin, "-p", "process.execPath") {
			addNodeRoot(line, home, add)
		}
		addNodeRoot(nodeBin, home, add)
	}

	toolchainCache.mu.Lock()
	if toolchainCache.roots == nil {
		toolchainCache.roots = map[string][]string{}
	}
	toolchainCache.roots[key] = roots
	toolchainCache.mu.Unlock()
	return roots
}

func addNodeRoot(execPath, home string, add func(string)) {
	for _, p := range []string{execPath, evalSymlinksOrEmpty(execPath)} {
		if !filepath.IsAbs(p) {
			continue
		}
		binDir := filepath.Dir(p)
		if filepath.Base(binDir) != "bin" {
			continue
		}
		parent := filepath.Dir(binDir)
		if isSafeToolchainRoot(parent, home) {
			add(parent)
			continue
		}
		modules := filepath.Join(parent, "lib", "node_modules")
		if info, err := os.Stat(modules); err == nil && info.IsDir() {
			add(modules)
		}
	}
}

func isUninstalledApplePythonStub(pyBin string) bool {
	if filepath.Clean(pyBin) != "/usr/bin/python3" {
		return false
	}
	if _, err := os.Stat("/Library/Developer/CommandLineTools/usr/bin/python3"); err == nil {
		return false
	}
	if devDir := os.Getenv("DEVELOPER_DIR"); devDir != "" {
		if _, err := os.Stat(filepath.Join(devDir, "usr", "bin", "python3")); err == nil {
			return false
		}
	}
	return true
}

func isSafeToolchainRoot(p, home string) bool {
	p = filepath.Clean(p)
	if !filepath.IsAbs(p) {
		return false
	}
	blocked := []string{
		"/", "/usr", "/usr/local", "/opt", "/opt/homebrew",
		"/System", "/Library", "/Applications", "/Users",
		"/tmp", "/private/tmp", "/var", "/private/var",
	}
	if home != "" {
		homeClean := filepath.Clean(home)
		blocked = append(blocked, homeClean)
		if resolved := evalSymlinksOrEmpty(homeClean); resolved != "" {
			blocked = append(blocked, resolved)
		}
	}
	for _, b := range blocked {
		if p == b {
			return false
		}
	}
	return true
}

func evalSymlinksOrEmpty(p string) string {
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return ""
	}
	return filepath.Clean(resolved)
}

func findInPath(pathEnv, name string) string {
	for _, dir := range filepath.SplitList(pathEnv) {
		if !filepath.IsAbs(dir) {
			continue
		}
		candidate := filepath.Join(dir, name)
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() && info.Mode()&0111 != 0 {
			return candidate
		}
	}
	return ""
}

func probeLines(env []string, bin string, args ...string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	// Run outside any project directory so version managers do not see local version files.
	cmd.Dir = "/"
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil
	}
	var lines []string
	for _, raw := range strings.Split(out.String(), "\n") {
		if trimmed := strings.TrimSpace(raw); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	return lines
}
