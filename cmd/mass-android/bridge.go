package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/imalimin/mass/internal/bootstrap"
	httptransport "github.com/imalimin/mass/internal/transport/http"
	"github.com/imalimin/mass/internal/transport/http/ui"
)

var version = "android-dev"

const (
	modeFull   = "full"
	modeUIOnly = "ui_only"

	stateStopped  = "stopped"
	stateStarting = "starting"
	stateRunning  = "running"
	stateStopping = "stopping"
	stateError    = "error"

	maxLogEntries = 200
)

type startRequest struct {
	Mode              string            `json:"mode"`
	Listen            string            `json:"listen"`
	PublicURL         string            `json:"public_url"`
	WorkspaceTempRoot string            `json:"workspace_temp_root"`
	TmpDir            string            `json:"tmp_dir"`
	TOSPrefix         string            `json:"tos_prefix"`
	ConfigYAML        string            `json:"config_yaml"`
	Env               map[string]string `json:"env"`
}

type statusResponse struct {
	OK      bool     `json:"ok"`
	State   string   `json:"state"`
	Mode    string   `json:"mode"`
	URL     string   `json:"url"`
	Error   string   `json:"error,omitempty"`
	Version string   `json:"version"`
	Logs    []string `json:"logs"`
}

type serverManager struct {
	mu          sync.Mutex
	state       string
	mode        string
	url         string
	lastError   string
	cancel      context.CancelFunc
	done        chan struct{}
	appliedEnvs []string
	logs        []string
	sink        func(string)
}

var defaultManager = newServerManager(nil)

func newServerManager(sink func(string)) *serverManager {
	return &serverManager{
		state: stateStopped,
		mode:  modeFull,
		sink:  sink,
	}
}

func (m *serverManager) appendLog(line string) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return
	}
	m.mu.Lock()
	m.logs = append(m.logs, trimmed)
	if len(m.logs) > maxLogEntries {
		m.logs = append([]string(nil), m.logs[len(m.logs)-maxLogEntries:]...)
	}
	sink := m.sink
	m.mu.Unlock()
	if sink != nil {
		sink(trimmed)
	}
}

func (m *serverManager) snapshot(ok bool) statusResponse {
	m.mu.Lock()
	defer m.mu.Unlock()
	copiedLogs := make([]string, len(m.logs))
	copy(copiedLogs, m.logs)
	return statusResponse{
		OK:      ok,
		State:   m.state,
		Mode:    m.mode,
		URL:     m.url,
		Error:   m.lastError,
		Version: version,
		Logs:    copiedLogs,
	}
}

func (m *serverManager) statusJSON(ok bool) string {
	raw, err := json.Marshal(m.snapshot(ok))
	if err != nil {
		return `{"ok":false,"state":"error","error":"marshal status failed"}`
	}
	return string(raw)
}

func (m *serverManager) Start(rawJSON string) string {
	var req startRequest
	if err := json.Unmarshal([]byte(rawJSON), &req); err != nil {
		m.setError(modeFull, "", fmt.Errorf("invalid start request JSON: %w", err))
		return m.statusJSON(false)
	}

	if req.Mode == "" {
		req.Mode = modeFull
	}
	if req.Mode != modeFull && req.Mode != modeUIOnly {
		m.setError(req.Mode, "", fmt.Errorf("unsupported mode %q", req.Mode))
		return m.statusJSON(false)
	}
	if strings.TrimSpace(req.Listen) == "" {
		req.Listen = "127.0.0.1:15336"
	}
	if strings.TrimSpace(req.PublicURL) == "" {
		req.PublicURL = "http://" + req.Listen
	}
	if strings.TrimSpace(req.TOSPrefix) == "" {
		req.TOSPrefix = "mass/"
	}

	// Ensure any previous instance is cleanly stopped before starting a new one.
	m.stopInternal(12 * time.Second)

	if err := m.prepareDirectoriesAndEnv(req); err != nil {
		m.setError(req.Mode, req.PublicURL, err)
		return m.statusJSON(false)
	}

	cfg, err := buildConfig(req)
	if err != nil {
		m.setError(req.Mode, req.PublicURL, err)
		return m.statusJSON(false)
	}
	if req.Mode == modeFull {
		if err := cfg.ValidateService(os.Getenv); err != nil {
			m.setError(req.Mode, cfg.HTTP.PublicURL, err)
			return m.statusJSON(false)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	m.mu.Lock()
	m.state = stateStarting
	m.mode = req.Mode
	m.url = cfg.HTTP.PublicURL
	m.lastError = ""
	m.cancel = cancel
	m.done = done
	m.mu.Unlock()

	m.appendLog(fmt.Sprintf("starting MASS android server mode=%s listen=%s", req.Mode, cfg.HTTP.Listen))

	logger := slog.New(slog.NewJSONHandler(&logWriter{
		onLine: func(line string) {
			m.appendLog(line)
			if strings.Contains(line, `"msg":"MASS listening"`) {
				m.mu.Lock()
				if m.done == done && m.state == stateStarting {
					m.state = stateRunning
				}
				m.mu.Unlock()
			}
		},
	}, nil))

	go func() {
		defer close(done)
		var runErr error
		if req.Mode == modeUIOnly {
			runErr = m.serveUIOnly(ctx, cfg, logger, done)
		} else {
			runErr = bootstrap.Serve(ctx, cfg, version, logger)
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.done != done {
			return
		}
		m.cancel = nil
		m.done = nil
		if runErr != nil && ctx.Err() == nil {
			m.state = stateError
			m.lastError = runErr.Error()
			go m.appendLog("server exited with error: " + runErr.Error())
		} else {
			m.state = stateStopped
			m.lastError = ""
			go m.appendLog("server stopped cleanly")
		}
	}()

	return m.statusJSON(true)
}

func (m *serverManager) Stop() string {
	m.stopInternal(12 * time.Second)
	return m.statusJSON(true)
}

func (m *serverManager) Status() string {
	return m.statusJSON(true)
}

func (m *serverManager) stopInternal(timeout time.Duration) {
	m.mu.Lock()
	cancel := m.cancel
	done := m.done
	if cancel != nil {
		m.state = stateStopping
	}
	m.mu.Unlock()

	if cancel == nil || done == nil {
		return
	}
	m.appendLog("stopping MASS android server")
	cancel()
	select {
	case <-done:
	case <-time.After(timeout):
		m.mu.Lock()
		if m.done == done {
			m.state = stateError
			m.lastError = "timed out waiting for server shutdown"
		}
		m.mu.Unlock()
	}
}

func (m *serverManager) setError(mode, url string, err error) {
	m.mu.Lock()
	m.state = stateError
	if mode != "" {
		m.mode = mode
	}
	if url != "" {
		m.url = url
	}
	m.lastError = err.Error()
	m.mu.Unlock()
	m.appendLog("error: " + err.Error())
}

func (m *serverManager) prepareDirectoriesAndEnv(req startRequest) error {
	if req.TmpDir != "" {
		if err := os.MkdirAll(req.TmpDir, 0o700); err != nil {
			return fmt.Errorf("create tmp dir: %w", err)
		}
		if err := os.Setenv("TMPDIR", req.TmpDir); err != nil {
			return fmt.Errorf("set TMPDIR: %w", err)
		}
	}
	if req.WorkspaceTempRoot != "" {
		if err := os.MkdirAll(req.WorkspaceTempRoot, 0o700); err != nil {
			return fmt.Errorf("create workspace root: %w", err)
		}
	}

	m.mu.Lock()
	prev := m.appliedEnvs
	m.appliedEnvs = nil
	m.mu.Unlock()

	for _, k := range prev {
		_ = os.Unsetenv(k)
	}

	applied := make([]string, 0, len(req.Env))
	for k, v := range req.Env {
		key := strings.TrimSpace(k)
		if key == "" {
			continue
		}
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			_ = os.Unsetenv(key)
			continue
		}
		if err := os.Setenv(key, trimmed); err != nil {
			return fmt.Errorf("set env %s: %w", key, err)
		}
		applied = append(applied, key)
	}

	m.mu.Lock()
	m.appliedEnvs = applied
	m.mu.Unlock()
	return nil
}

func buildConfig(req startRequest) (bootstrap.Config, error) {
	var cfg bootstrap.Config
	if strings.TrimSpace(req.ConfigYAML) != "" {
		var err error
		cfg, err = bootstrap.ParseConfig([]byte(req.ConfigYAML))
		if err != nil {
			return bootstrap.Config{}, err
		}
	} else {
		rawYAML := defaultAndroidConfigYAML(req)
		var err error
		cfg, err = bootstrap.ParseConfig([]byte(rawYAML))
		if err != nil {
			return bootstrap.Config{}, err
		}
	}
	if strings.TrimSpace(req.Listen) != "" {
		cfg.HTTP.Listen = strings.TrimSpace(req.Listen)
	}
	if strings.TrimSpace(req.PublicURL) != "" {
		cfg.HTTP.PublicURL = strings.TrimSpace(req.PublicURL)
	}
	if strings.TrimSpace(req.WorkspaceTempRoot) != "" {
		cfg.Runtime.WorkspaceTempRoot = filepath.Clean(req.WorkspaceTempRoot)
	}
	if strings.TrimSpace(req.TOSPrefix) != "" {
		cfg.TOS.Prefix = strings.TrimSpace(req.TOSPrefix)
	}
	return cfg, nil
}

func defaultAndroidConfigYAML(req startRequest) string {
	workspaceRoot := req.WorkspaceTempRoot
	if workspaceRoot == "" {
		workspaceRoot = filepath.Join(os.TempDir(), "mass-workspaces")
	}
	prefix := req.TOSPrefix
	if prefix == "" {
		prefix = "mass/"
	}
	return fmt.Sprintf(`http:
  listen: %q
  public_url: %q
  shutdown_timeout: "10s"
database:
  dsn_env: "MASS_MARIADB_DSN"
  migration_dsn_env: "MASS_MARIADB_MIGRATION_DSN"
tos:
  endpoint_env: "MASS_TOS_ENDPOINT"
  region_env: "MASS_TOS_REGION"
  bucket_env: "MASS_TOS_BUCKET"
  prefix: %q
  access_key_env: "MASS_TOS_ACCESS_KEY"
  secret_key_env: "MASS_TOS_SECRET_KEY"
storage:
  max_object_bytes: 2097152
  max_workspace_bytes: 134217728
auth:
  session_ttl: "24h"
  cookie_secure: false
llm:
  max_attempts: 2
  timeout: "30m"
  framework: "eino"
  provider: "ark"
secrets:
  master_key_env: "MASS_MASTER_KEY"
runtime:
  workspace_temp_root: %q
  snapshot_timeout: "60s"
tools:
  code:
    timeout: "10m"
    max_concurrent: 1
    max_source_bytes: 65536
    max_input_bytes: 2097152
    max_output_bytes: 2097152
    max_log_bytes: 65536
    max_file_operations: 100
    max_network_requests: 10
    max_network_body_bytes: 2097152
`, req.Listen, req.PublicURL, prefix, workspaceRoot)
}

func (m *serverManager) serveUIOnly(ctx context.Context, cfg bootstrap.Config, logger *slog.Logger, done chan struct{}) error {
	listener, err := net.Listen("tcp", cfg.HTTP.Listen)
	if err != nil {
		return err
	}
	defer listener.Close()

	actualURL := "http://" + listener.Addr().String()
	m.mu.Lock()
	if m.done == done {
		m.url = actualURL
		m.state = stateRunning
	}
	m.mu.Unlock()

	srv := &http.Server{
		Handler:           httptransport.NewHandler(ui.Assets(), version, logger),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(listener) }()

	logger.Info("MASS listening", "address", listener.Addr().String(), "public_url", actualURL, "mode", modeUIOnly)

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := srv.Shutdown(shutdownCtx)
		if err != nil {
			_ = srv.Close()
		}
		serveErr := <-errCh
		if err != nil {
			return err
		}
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return serveErr
		}
		return nil
	}
}

type logWriter struct {
	mu     sync.Mutex
	buf    strings.Builder
	onLine func(string)
}

func (w *logWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	for {
		s := w.buf.String()
		idx := strings.IndexByte(s, '\n')
		if idx < 0 {
			break
		}
		line := s[:idx]
		rest := s[idx+1:]
		w.buf.Reset()
		w.buf.WriteString(rest)
		if w.onLine != nil {
			w.onLine(line)
		}
	}
	return len(p), nil
}

var _ io.Writer = (*logWriter)(nil)
