package httptransport

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type stubPeerLocator struct {
	nodeID    string
	peerAddr  string
	remote    bool
	err       error
	expAddr   string
	expRemote bool
	expErr    error
}

func (s stubPeerLocator) NodeID() string { return s.nodeID }
func (s stubPeerLocator) RunPeer(context.Context, string, string) (string, bool, error) {
	return s.peerAddr, s.remote, s.err
}
func (s stubPeerLocator) ExperimentPeer(context.Context, string, string) (string, bool, error) {
	return s.expAddr, s.expRemote, s.expErr
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type streamingRecorder struct {
	header http.Header
	code   int
	pw     *io.PipeWriter
	flushed chan struct{}
}

func (s *streamingRecorder) Header() http.Header         { return s.header }
func (s *streamingRecorder) WriteHeader(statusCode int)  { s.code = statusCode }
func (s *streamingRecorder) Write(p []byte) (int, error) { return s.pw.Write(p) }
func (s *streamingRecorder) Flush() {
	select {
	case s.flushed <- struct{}{}:
	default:
	}
}

func TestPeerProxyStreamsSSEAndPreservesHeaders(t *testing.T) {
	releaseSecond := make(chan struct{})
	var gotURL, gotHost, gotCookie, gotAuth, gotLastID, gotHop, gotSig, gotEnc string

	masterKey := []byte("0123456789abcdef0123456789abcdef")
	proxy := NewPeerProxy(stubPeerLocator{
		nodeID:   "node-b",
		peerAddr: "http://10.0.1.12:8080",
		remote:   true,
	}, masterKey, nil)
	proxy.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotURL = r.URL.String()
		gotHost = r.Host
		gotCookie = r.Header.Get("Cookie")
		gotAuth = r.Header.Get("Authorization")
		gotLastID = r.Header.Get("Last-Event-ID")
		gotHop = r.Header.Get(peerHopHeader)
		gotSig = r.Header.Get(peerSigHeader)
		gotEnc = r.Header.Get("Accept-Encoding")

		pr, pw := io.Pipe()
		go func() {
			defer pw.Close()
			_, _ = fmt.Fprint(pw, "id: 1\ndata: {\"type\":\"TEXT_MESSAGE_CONTENT\",\"delta\":\"hi\"}\n\n")
			<-releaseSecond
			_, _ = fmt.Fprint(pw, "id: 2\ndata: {\"type\":\"RUN_FINISHED\"}\n\n")
		}()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type":      []string{"text/event-stream"},
				"X-Accel-Buffering": []string{"no"},
			},
			Body: pr,
		}, nil
	})

	req := httptest.NewRequest("GET", "http://public.example/api/v1/spaces/space1/runs/run1/events?after=0&stream_id=s1", nil)
	req.Header.Set("Cookie", "mass_session=secret")
	req.Header.Set("Authorization", "Bearer tok_123")
	req.Header.Set("Last-Event-ID", "0")
	// Spoofed external hop header without valid signature must NOT block forwarding.
	req.Header.Set(peerHopHeader, "spoofed-node")

	pr, pw := io.Pipe()
	rec := &streamingRecorder{header: http.Header{}, pw: pw, flushed: make(chan struct{}, 4)}
	done := make(chan bool, 1)
	go func() {
		handled := proxy.ForwardRun(rec, req, "space1", "run1")
		_ = pw.Close()
		done <- handled
	}()

	reader := bufio.NewReader(pr)
	// Must receive the first SSE frame immediately before upstream sends frame 2.
	line1, err := reader.ReadString('\n')
	if err != nil || strings.TrimSpace(line1) != "id: 1" {
		t.Fatalf("first SSE line = %q, err=%v", line1, err)
	}
	line2, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(line2, `"delta":"hi"`) {
		t.Fatalf("second SSE line = %q, err=%v", line2, err)
	}
	_, _ = reader.ReadString('\n')
	<-rec.flushed

	close(releaseSecond)
	line3, err := reader.ReadString('\n')
	if err != nil || strings.TrimSpace(line3) != "id: 2" {
		t.Fatalf("third SSE line = %q, err=%v", line3, err)
	}
	_, _ = io.Copy(io.Discard, reader)
	if !<-done {
		t.Fatal("expected ForwardRun to handle remote request")
	}
	if rec.code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.code)
	}
	if gotURL != "http://10.0.1.12:8080/api/v1/spaces/space1/runs/run1/events?after=0&stream_id=s1" || gotHost != "10.0.1.12:8080" {
		t.Fatalf("forwarded target wrong: url=%q host=%q", gotURL, gotHost)
	}
	if gotCookie != "mass_session=secret" || gotAuth != "Bearer tok_123" || gotLastID != "0" {
		t.Fatalf("lost auth/SSE headers: cookie=%q auth=%q lastID=%q", gotCookie, gotAuth, gotLastID)
	}
	if gotHop != "node-b" || gotSig == "" || gotEnc != "identity" {
		t.Fatalf("proxy headers wrong: hop=%q sig=%q enc=%q", gotHop, gotSig, gotEnc)
	}
}

func TestPeerProxyPreventsForwardingLoopAndHandlesLocal(t *testing.T) {
	masterKey := []byte("0123456789abcdef0123456789abcdef")

	t.Run("local ownership does not forward", func(t *testing.T) {
		proxy := NewPeerProxy(stubPeerLocator{nodeID: "node-a", remote: false}, masterKey, nil)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/v1/spaces/space1/runs/run1/events", nil)
		if proxy.ForwardRun(rec, req, "space1", "run1") {
			t.Fatal("should not forward local run")
		}
	})

	t.Run("verified hop loop returns 503", func(t *testing.T) {
		proxy := NewPeerProxy(stubPeerLocator{nodeID: "node-a", peerAddr: "http://10.0.0.2:8080", remote: true}, masterKey, nil)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/v1/spaces/space1/runs/run1/events?after=1", nil)
		req.Header.Set(peerHopHeader, "node-b")
		req.Header.Set(peerSigHeader, proxy.sign("node-b", req.Method, req.URL.RequestURI()))
		if !proxy.ForwardRun(rec, req, "space1", "run1") {
			t.Fatal("expected ForwardRun to handle loop")
		}
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rec.Code)
		}
	})

	t.Run("missing peer address returns 503", func(t *testing.T) {
		proxy := NewPeerProxy(stubPeerLocator{nodeID: "node-a", peerAddr: "", remote: true}, masterKey, nil)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/v1/spaces/space1/runs/run1/cancel", nil)
		if !proxy.ForwardRun(rec, req, "space1", "run1") {
			t.Fatal("expected ForwardRun to handle dead remote owner")
		}
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rec.Code)
		}
	})
}
