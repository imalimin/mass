package bootstrap

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	einoadapter "github.com/imalimin/mass/internal/adapter/eino"
	"go.yaml.in/yaml/v4"
)

// Config is the single deployment configuration tree. Secrets are resolved only
// from named environment variables and must never be serialized to clients.
type Config struct {
	HTTP struct {
		Listen          string        `yaml:"listen"`
		PublicURL       string        `yaml:"public_url"`
		ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
	} `yaml:"http"`
	Database struct {
		DSNEnv          string `yaml:"dsn_env"`
		MigrationDSNEnv string `yaml:"migration_dsn_env"`
	} `yaml:"database"`
	TOS struct {
		EndpointEnv  string `yaml:"endpoint_env"`
		RegionEnv    string `yaml:"region_env"`
		BucketEnv    string `yaml:"bucket_env"`
		Prefix       string `yaml:"prefix"`
		AccessKeyEnv string `yaml:"access_key_env"`
		SecretKeyEnv string `yaml:"secret_key_env"`
	} `yaml:"tos"`
	Storage struct {
		MaxObjectBytes    int64 `yaml:"max_object_bytes"`
		MaxWorkspaceBytes int64 `yaml:"max_workspace_bytes"`
	} `yaml:"storage"`
	Auth struct {
		SessionTTL   time.Duration `yaml:"session_ttl"`
		CookieSecure bool          `yaml:"cookie_secure"`
	} `yaml:"auth"`
	LLM struct {
		MaxAttempts int           `yaml:"max_attempts"`
		Timeout     time.Duration `yaml:"timeout"`
		Framework   string        `yaml:"framework"`
		Provider    string        `yaml:"provider"`
		// Accepted for older configuration files. Model settings now live in the database.
		ModelEnv  string `yaml:"model_env"`
		APIKeyEnv string `yaml:"api_key_env"`
	} `yaml:"llm"`
	Secrets struct {
		MasterKeyEnv string `yaml:"master_key_env"`
	} `yaml:"secrets"`
	Runtime struct {
		MaxConcurrentRuns int           `yaml:"max_concurrent_runs"`
		WorkspaceTempRoot string        `yaml:"workspace_temp_root"`
		SnapshotTimeout   time.Duration `yaml:"snapshot_timeout"`
	} `yaml:"runtime"`
	Tools struct {
		Code struct {
			Timeout             time.Duration `yaml:"timeout"`
			MaxConcurrent       int           `yaml:"max_concurrent"`
			MaxSourceBytes      int           `yaml:"max_source_bytes"`
			MaxInputBytes       int           `yaml:"max_input_bytes"`
			MaxOutputBytes      int           `yaml:"max_output_bytes"`
			MaxLogBytes         int           `yaml:"max_log_bytes"`
			MaxFileOperations   int           `yaml:"max_file_operations"`
			MaxNetworkRequests  int           `yaml:"max_network_requests"`
			MaxNetworkBodyBytes int           `yaml:"max_network_body_bytes"`
		} `yaml:"code"`
	} `yaml:"tools"`
}

func LoadConfig(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open configuration: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return Config{}, errors.New("read configuration failed")
	}
	if len(data) > 1<<20 {
		return Config{}, errors.New("configuration exceeds 1 MiB")
	}
	return ParseConfig(data)
}

func ParseConfig(data []byte) (Config, error) {
	var c Config
	c.LLM.Timeout = einoadapter.DefaultModelTimeout
	if len(data) > 1<<20 {
		return c, errors.New("configuration exceeds 1 MiB")
	}
	// Inspect structure before decoding. Aliases can hide unbounded expansion;
	// source values are never included in parse errors (they may be secrets).
	var tree yaml.Node
	d := yaml.NewDecoder(bytes.NewReader(data))
	if err := d.Decode(&tree); err != nil {
		return c, errors.New("invalid YAML configuration")
	}
	if err := validateNode(&tree, 0); err != nil {
		return c, err
	}
	var extra yaml.Node
	if err := d.Decode(&extra); err != io.EOF {
		return c, errors.New("exactly one YAML document is required")
	}
	d = yaml.NewDecoder(bytes.NewReader(data))
	d.KnownFields(true)
	if err := d.Decode(&c); err != nil {
		return c, errors.New("invalid configuration fields or types")
	}
	if c.Storage.MaxObjectBytes == 0 {
		c.Storage.MaxObjectBytes = 2 << 20
	}
	if c.Storage.MaxWorkspaceBytes == 0 {
		c.Storage.MaxWorkspaceBytes = 128 << 20
	}
	return c, nil
}

func validateNode(n *yaml.Node, depth int) error {
	if depth > 20 || n.Kind == yaml.AliasNode {
		return errors.New("configuration nesting/alias is not allowed")
	}
	for _, child := range n.Content {
		if err := validateNode(child, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func (c Config) ValidateService(getenv func(string) string) error {
	if _, _, err := net.SplitHostPort(c.HTTP.Listen); err != nil {
		return errors.New("http.listen must be host:port")
	}
	u, err := url.Parse(c.HTTP.PublicURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("http.public_url must be an HTTP(S) origin")
	}
	if !c.Auth.CookieSecure && u.Hostname() != "localhost" && !isLoopback(u.Hostname()) {
		return errors.New("insecure cookies require a loopback public_url")
	}
	if c.Auth.CookieSecure && u.Scheme != "https" {
		return errors.New("secure cookies require HTTPS public_url")
	}
	if c.HTTP.ShutdownTimeout <= 0 || c.Auth.SessionTTL < time.Second || c.Runtime.SnapshotTimeout <= 0 || c.Runtime.MaxConcurrentRuns < 1 {
		return errors.New("timeouts and concurrency limits must be positive")
	}
	if !filepath.IsAbs(c.Runtime.WorkspaceTempRoot) || filepath.Clean(c.Runtime.WorkspaceTempRoot) == string(filepath.Separator) {
		return errors.New("workspace_temp_root must be an absolute dedicated directory")
	}
	if c.LLM.MaxAttempts < 0 || c.LLM.MaxAttempts > 3 {
		return errors.New("llm.max_attempts must be between 1 and 3 (0 uses 1)")
	}
	if c.LLM.Timeout <= 0 {
		return errors.New("llm.timeout must be positive")
	}
	if c.LLM.Framework != "eino" || c.LLM.Provider != "ark" {
		return errors.New("only the verified eino/ark provider is supported")
	}
	if c.TOS.Prefix == "" || strings.HasPrefix(c.TOS.Prefix, "/") || strings.Contains(c.TOS.Prefix, "..") {
		return errors.New("tos.prefix must be a relative object prefix")
	}
	if c.Storage.MaxObjectBytes < 1 || c.Storage.MaxObjectBytes > c.Storage.MaxWorkspaceBytes {
		return errors.New("invalid storage limits")
	}
	code := c.Tools.Code
	if code.Timeout <= 0 || code.MaxConcurrent < 1 || code.MaxSourceBytes < 1 || code.MaxInputBytes < 1 || code.MaxOutputBytes < 1 || code.MaxLogBytes < 1 || code.MaxFileOperations < 1 || code.MaxNetworkRequests < 1 || code.MaxNetworkBodyBytes < 1 {
		return errors.New("tools.code limits must be positive")
	}
	refs := []struct{ field, name string }{
		{"database.dsn_env", c.Database.DSNEnv}, {"tos.endpoint_env", c.TOS.EndpointEnv}, {"tos.region_env", c.TOS.RegionEnv}, {"tos.bucket_env", c.TOS.BucketEnv}, {"tos.access_key_env", c.TOS.AccessKeyEnv}, {"tos.secret_key_env", c.TOS.SecretKeyEnv}, {"secrets.master_key_env", c.Secrets.MasterKeyEnv},
	}
	for _, ref := range refs {
		if ref.name == "" || getenv == nil || strings.TrimSpace(getenv(ref.name)) == "" {
			return fmt.Errorf("%s requires a nonempty environment value", ref.field)
		}
	}
	key, err := base64.StdEncoding.DecodeString(getenv(c.Secrets.MasterKeyEnv))
	if err != nil || len(key) != 32 {
		return errors.New("secrets.master_key_env must resolve to a base64 encoded 32-byte key")
	}
	return nil
}

func isLoopback(host string) bool { ip := net.ParseIP(host); return ip != nil && ip.IsLoopback() }
