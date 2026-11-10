package identity

import (
	"net"
	"net/url"
	"strings"
)

// NormalizeBaseURL keeps credentials out of URLs and preserves the deployment's
// HTTPS policy; HTTP is only permitted for loopback development and fixtures.
func NormalizeBaseURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 2048 {
		return "", ErrInvalid
	}
	u, err := url.Parse(value)
	if err != nil || u.Host == "" || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || strings.Contains(value, "#") || strings.ContainsAny(value, "\x00\r\n") {
		return "", ErrInvalid
	}
	u.Scheme = strings.ToLower(u.Scheme)
	ip := net.ParseIP(u.Hostname())
	loopback := strings.EqualFold(u.Hostname(), "localhost") || ip != nil && ip.IsLoopback()
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return "", ErrInvalid
	}
	normalized := strings.TrimRight(u.String(), "/")
	if len(normalized) > 2048 {
		return "", ErrInvalid
	}
	return normalized, nil
}
