package bootstrap

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"
)

func fixtureEnv(name string) string {
	if name == "MASS_MASTER_KEY" {
		return base64.StdEncoding.EncodeToString(make([]byte, 32))
	}
	return "offline-fixture"
}
func TestConfig(t *testing.T) {
	data, err := os.ReadFile("../../configs/mass.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseConfig(data, fixtureEnv); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		data string
		env  func(string) string
	}{
		{"missing_secret", string(data), func(string) string { return "" }},
		{"unknown", string(data) + "unexpected: true\n", fixtureEnv},
		{"multiple_documents", string(data) + "---\nhttp: {}\n", fixtureEnv},
		{"duplicate", string(data) + "http: {}\n", fixtureEnv},
		{"nonpositive_limit", strings.Replace(string(data), "max_concurrent_runs: 4", "max_concurrent_runs: 0", 1), fixtureEnv},
		{"insecure_remote_cookie", strings.Replace(string(data), "http://127.0.0.1:8080", "http://example.com", 1), fixtureEnv},
		{"relative_workspace", strings.Replace(string(data), "/tmp/mass-workspaces", "relative", 1), fixtureEnv},
		{"alias", string(data) + "unexpected: &a {x: *a}\n", fixtureEnv},
		{"invalid_key", string(data), func(string) string { return "not-base64" }},
		{"oversize", strings.Repeat("#", (1<<20)+1), fixtureEnv},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseConfig([]byte(tc.data), tc.env); err == nil {
				t.Fatal("accepted invalid configuration")
			} else if strings.Contains(err.Error(), "not-base64") {
				t.Fatal("secret disclosed in error")
			}
		})
	}
}
