package bootstrap

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"
	"time"
)

func fixtureEnv(name string) string {
	if name == "MASS_MASTER_KEY" {
		return base64.StdEncoding.EncodeToString(make([]byte, 32))
	}
	return "offline-fixture"
}
func validateServiceConfig(data []byte, getenv func(string) string) error {
	c, err := ParseConfig(data)
	if err != nil {
		return err
	}
	return c.ValidateService(getenv)
}

func TestConfig(t *testing.T) {
	data, err := os.ReadFile("../../configs/mass.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateServiceConfig(data, fixtureEnv); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		data string
		env  func(string) string
	}{
		{"missing_secret", string(data), func(string) string { return "" }},
		{"unknown", string(data) + "unexpected: true\n", fixtureEnv},
		{"global_model_url_removed", strings.Replace(string(data), "  framework: \"eino\"", "  base_url_env: MODEL_URL\n  framework: \"eino\"", 1), fixtureEnv},
		{"multiple_documents", string(data) + "---\nhttp: {}\n", fixtureEnv},
		{"duplicate", string(data) + "http: {}\n", fixtureEnv},
		{"nonpositive_limit", strings.Replace(string(data), "max_concurrent_runs: 4", "max_concurrent_runs: 0", 1), fixtureEnv},
		{"nonpositive_model_timeout", strings.Replace(string(data), `timeout: "30m"`, `timeout: "0s"`, 1), fixtureEnv},
		{"insecure_remote_cookie", strings.Replace(string(data), "http://127.0.0.1:8080", "http://example.com", 1), fixtureEnv},
		{"relative_workspace", strings.Replace(string(data), "/tmp/mass-workspaces", "relative", 1), fixtureEnv},
		{"alias", string(data) + "unexpected: &a {x: *a}\n", fixtureEnv},
		{"invalid_key", string(data), func(string) string { return "not-base64" }},
		{"oversize", strings.Repeat("#", (1<<20)+1), fixtureEnv},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateServiceConfig([]byte(tc.data), tc.env); err == nil {
				t.Fatal("accepted invalid configuration")
			} else if strings.Contains(err.Error(), "not-base64") {
				t.Fatal("secret disclosed in error")
			}
		})
	}
}

func TestModelTimeoutConfiguration(t *testing.T) {
	for _, tc := range []struct {
		yaml string
		want time.Duration
	}{
		{"llm:\n  provider: ark\n", 30 * time.Minute},
		{"llm:\n  timeout: 45m\n", 45 * time.Minute},
	} {
		config, err := ParseConfig([]byte(tc.yaml))
		if err != nil || config.LLM.Timeout != tc.want {
			t.Fatalf("model timeout = %s, want %s, err = %v", config.LLM.Timeout, tc.want, err)
		}
	}
}

func TestD02ConfigurationDefaults(t *testing.T) {
	data, err := os.ReadFile("../../configs/mass.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	start := strings.Index(source, "storage:")
	end := strings.Index(source[start:], "auth:") + start
	source = source[:start] + source[end:]
	config, err := ParseConfig([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	if config.Storage.MaxObjectBytes != 256<<20 || config.Storage.MaxWorkspaceBytes != 128<<20 || config.Storage.MaxFileBytes != 32<<20 || config.Storage.MaxEntries != 10000 {
		t.Fatal("incompatible legacy defaults")
	}
	if err = validateServiceConfig([]byte(strings.Replace(string(data), "max_object_bytes: 268435456", "max_object_bytes: -1", 1)), fixtureEnv); err == nil {
		t.Fatal("negative storage limit accepted")
	}
}
