package bootstrap

import (
	"encoding/base64"
	"net/netip"
	"os"
	"strconv"
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

func TestCreativityConfig(t *testing.T) {
	config, err := ParseConfig([]byte("creativity:\n  agent_id: author\n  agent_workspace_id: origin\n"))
	if err != nil || config.Creativity.AgentID != "author" || config.Creativity.AgentWorkspaceID != "origin" {
		t.Fatalf("unexpected creativity config: %+v, %v", config.Creativity, err)
	}
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
		{"run_concurrency_removed", strings.Replace(string(data), "runtime:\n", "runtime:\n  max_concurrent_runs: 4\n", 1), fixtureEnv},
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

func TestStorageConfigurationDefaults(t *testing.T) {
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
	if config.Storage.MaxObjectBytes != 2<<20 || config.Storage.MaxWorkspaceBytes != 128<<20 {
		t.Fatal("unexpected snapshot limits")
	}
	if err = validateServiceConfig([]byte(strings.Replace(string(data), "max_object_bytes: 2097152", "max_object_bytes: -1", 1)), fixtureEnv); err == nil {
		t.Fatal("negative storage limit accepted")
	}
	if err = validateServiceConfig([]byte(strings.Replace(string(data), "max_workspace_bytes: 134217728", "max_workspace_bytes: 536870912", 1)), fixtureEnv); err != nil {
		t.Fatal("workspace budget was capped by the content object limit:", err)
	}
}

func TestLANConfiguration(t *testing.T) {
	data, err := os.ReadFile("../../configs/mass.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	base, err := ParseConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	v6 := netip.MustParseAddr("fd00::12")
	v4 := netip.MustParseAddr("192.168.1.20")
	for _, entry := range []struct {
		name      string
		requested netip.Addr
		port      uint16
		listen    string
		publicURL string
	}{
		{"automatic", netip.Addr{}, 8080, "192.168.1.20:8080", "http://192.168.1.20:8080"},
		{"explicit IPv6", v6, 8443, "[fd00::12]:8443", "http://[fd00::12]:8443"},
		{"default HTTP port", v4, 80, "192.168.1.20:80", "http://192.168.1.20"},
	} {
		t.Run(entry.name, func(t *testing.T) {
			c := base
			c.Auth.CookieSecure = true
			if err := c.configureLAN([]netip.Addr{v6, v4}, entry.requested, entry.port); err != nil {
				t.Fatal(err)
			}
			if c.HTTP.Listen != entry.listen || c.HTTP.PublicURL != entry.publicURL || c.Auth.CookieSecure {
				t.Fatalf("wrong LAN settings: %q, %q, secure=%v", c.HTTP.Listen, c.HTTP.PublicURL, c.Auth.CookieSecure)
			}
			if err := c.ValidateService(fixtureEnv); err != nil {
				t.Fatal(err)
			}
			if c.lanLoopback != "127.0.0.1:"+strconv.Itoa(int(entry.port)) {
				t.Fatalf("wrong local listener: %q", c.lanLoopback)
			}
			c.lanLoopback = ""
			if err := c.ValidateService(fixtureEnv); err == nil {
				t.Fatal("plain HTTP on a private address was accepted without LAN mode")
			}
		})
	}
	if err := base.configureLAN([]netip.Addr{v4}, netip.MustParseAddr("192.168.1.21"), 8080); err == nil {
		t.Fatal("unassigned address was accepted")
	}
	if err := base.configureLAN(nil, netip.Addr{}, 8080); err == nil {
		t.Fatal("accepted LAN mode without a private address")
	}
	for _, ip := range []string{"127.0.0.1", "0.0.0.0", "203.0.113.10", "example.com"} {
		if err := base.EnableLAN(ip, 8080); err == nil {
			t.Fatalf("accepted invalid LAN address %q", ip)
		}
	}
	for _, port := range []int{0, 65536} {
		if err := base.EnableLAN("", port); err == nil {
			t.Fatalf("accepted invalid LAN port %d", port)
		}
	}
}
