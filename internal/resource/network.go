package resource

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/imalimin/mass/internal/diagnostic"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
)

type Network struct{ Client *http.Client }

type networkRequestError struct {
	run.NetworkFailure
	err error
}

func (e *networkRequestError) Error() string { return e.Code }
func (e *networkRequestError) Unwrap() error { return e.err }

func sensitiveHeader(name string) bool {
	name = strings.ToLower(name)
	return name == "authorization" || name == "proxy-authorization" || name == "cookie" || name == "set-cookie" || strings.Contains(name, "api-key") || strings.Contains(name, "token") || strings.Contains(name, "secret")
}
func (n *Network) Request(ctx context.Context, args map[string]any, maximum int64, check func(context.Context) error) (map[string]any, string, error) {
	for key, value := range args {
		switch key {
		case "method", "url", "body":
			if _, ok := value.(string); !ok {
				return nil, "none", storage.ErrInvalid
			}
		case "headers":
			if _, ok := value.(map[string]any); !ok {
				return nil, "none", storage.ErrInvalid
			}
		default:
			return nil, "none", storage.ErrInvalid
		}
	}
	method, _ := args["method"].(string)
	if method == "" {
		method = "GET"
	}
	method = strings.ToUpper(method)
	if method != "GET" && method != "POST" && method != "PUT" && method != "PATCH" && method != "DELETE" && method != "HEAD" && method != "OPTIONS" {
		return nil, "none", storage.ErrInvalid
	}
	target, _ := args["url"].(string)
	u, err := url.Parse(target)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || len(target) > 8192 || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, "none", storage.ErrInvalid
	}
	body, _ := args["body"].(string)
	if int64(len(body)) > maximum {
		return nil, "none", run.ToolFailure("network_body_limit", "none")
	}
	request, err := http.NewRequestWithContext(ctx, method, u.String(), strings.NewReader(body))
	if err != nil {
		return nil, "none", storage.ErrInvalid
	}
	if headers, ok := args["headers"].(map[string]any); ok {
		if len(headers) > 64 {
			return nil, "none", storage.ErrInvalid
		}
		for k, v := range headers {
			value, ok := v.(string)
			if !ok || len(k) > 128 || len(value) > 8192 || strings.ContainsAny(k+value, "\r\n\x00") {
				return nil, "none", storage.ErrInvalid
			}
			lower := strings.ToLower(k)
			if lower == "host" || lower == "connection" || lower == "transfer-encoding" || lower == "content-length" || strings.HasPrefix(lower, "proxy-") {
				return nil, "none", storage.ErrInvalid
			}
			request.Header.Set(k, value)
		}
	}
	if err = check(ctx); err != nil {
		return nil, "none", err
	}
	client := http.Client{Timeout: 30 * time.Second}
	if n != nil && n.Client != nil {
		client = *n.Client
	} else {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.DisableKeepAlives = true
		transport.MaxResponseHeaderBytes = 64 << 10
		transport.DialContext = (&net.Dialer{Timeout: 5 * time.Second}).DialContext
		client.Transport = transport
		defer transport.CloseIdleConnections()
	}
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) > 3 {
			return run.ToolFailure("redirect_limit", "unknown")
		}
		if next.URL.User != nil || (next.URL.Scheme != "http" && next.URL.Scheme != "https") {
			return storage.ErrInvalid
		}
		if next.URL.Scheme != via[0].URL.Scheme || next.URL.Host != via[0].URL.Host {
			for k := range next.Header {
				if sensitiveHeader(k) {
					next.Header.Del(k)
				}
			}
		}
		return check(ctx)
	}
	response, err := client.Do(request)
	if err != nil {
		effect := "unknown"
		if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions {
			effect = "none"
		}
		failure := run.NetworkFailure{Method: method, Host: u.Hostname(), Phase: "request", Code: "network_request_failed", Cause: networkCause(err), SideEffect: effect}
		if response != nil {
			failure.Phase, failure.HTTPStatus = "redirect", response.StatusCode
		}
		classified := run.ToolFailure(failure.Code, effect)
		if networkTimedOut(err) {
			failure.Code, classified = run.ErrNetworkTimeout.Error(), run.ErrNetworkTimeout
		}
		return nil, effect, &networkRequestError{NetworkFailure: failure, err: classified}
	}
	defer response.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(storage.ContextReader{Context: ctx, Reader: response.Body}, maximum+1))
	complete := readErr == nil && int64(len(raw)) <= maximum
	if int64(len(raw)) > maximum {
		raw = raw[:maximum]
	}
	headers := map[string]string{}
	for k, values := range response.Header {
		if sensitiveHeader(k) {
			headers[k] = "[REDACTED]"
		} else {
			v := strings.Join(values, ", ")
			if len(v) > 8192 {
				v = v[:8192]
			}
			headers[k] = v
		}
	}
	encoding := "utf-8"
	text := string(raw)
	if !utf8.Valid(raw) {
		encoding = "base64"
		text = base64.StdEncoding.EncodeToString(raw)
	}
	data := map[string]any{"status": response.StatusCode, "headers": headers, "body": text, "encoding": encoding, "complete": complete, "truncated": !complete}
	if networkTimedOut(readErr) {
		return data, "committed", run.ErrNetworkTimeout
	}
	if !complete {
		return data, "committed", run.ToolFailure("network_response_incomplete", "committed")
	}
	if err = check(ctx); err != nil {
		return data, "committed", err
	}
	return data, "committed", nil
}

func networkCause(err error) string {
	var dns *net.DNSError
	var certificate *tls.CertificateVerificationError
	var record tls.RecordHeaderError
	var alert tls.AlertError
	switch {
	case errors.As(err, &dns):
		if dns.IsNotFound {
			return "dns_not_found"
		}
		if dns.Timeout() {
			return "dns_timeout"
		}
		return "dns_error"
	case errors.As(err, &certificate):
		return "tls_certificate_error"
	case errors.As(err, &record), errors.As(err, &alert):
		return "tls_protocol_error"
	case errors.Is(err, io.EOF):
		return "connection_closed"
	default:
		if cause := diagnostic.Cause(err); cause != "unknown" {
			return cause
		}
		return "transport_error"
	}
}

func networkTimedOut(err error) bool {
	var timeout net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout())
}
