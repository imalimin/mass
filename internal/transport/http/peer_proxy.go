package httptransport

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/imalimin/mass/internal/domain/run"
)

const (
	peerHopHeader = "X-Mass-Peer-Hop"
	peerSigHeader = "X-Mass-Peer-Sig"
)

// PeerLocator resolves which node owns an active Run or evaluation experiment.
type PeerLocator interface {
	NodeID() string
	RunPeer(ctx context.Context, space, runID string) (peerAddr string, remote bool, err error)
	ExperimentPeer(ctx context.Context, space, experimentID string) (peerAddr string, remote bool, err error)
}

// PeerProxy transparently forwards live Run (including SSE) and evaluation
// requests to the node currently owning execution.
type PeerProxy struct {
	Locator   PeerLocator
	Secret    []byte
	Transport http.RoundTripper
	Logger    *slog.Logger
}

func NewPeerProxy(locator PeerLocator, masterKey []byte, logger *slog.Logger) *PeerProxy {
	if locator == nil {
		return nil
	}
	var secret []byte
	if len(masterKey) > 0 {
		sum := sha256.Sum256(append([]byte("mass-peer-proxy-v1:"), masterKey...))
		secret = sum[:]
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
	}
	return &PeerProxy{
		Locator:   locator,
		Secret:    secret,
		Transport: transport,
		Logger:    logger,
	}
}

func (p *PeerProxy) sign(nodeID, method, requestURI string) string {
	if len(p.Secret) == 0 {
		return ""
	}
	mac := hmac.New(sha256.New, p.Secret)
	mac.Write([]byte(nodeID))
	mac.Write([]byte{0})
	mac.Write([]byte(method))
	mac.Write([]byte{0})
	mac.Write([]byte(requestURI))
	return hex.EncodeToString(mac.Sum(nil))
}

func (p *PeerProxy) isVerifiedHop(r *http.Request) bool {
	hop := strings.TrimSpace(r.Header.Get(peerHopHeader))
	if hop == "" {
		return false
	}
	if len(p.Secret) == 0 {
		return true
	}
	sig := strings.TrimSpace(r.Header.Get(peerSigHeader))
	if sig == "" {
		return false
	}
	expected := p.sign(hop, r.Method, r.URL.RequestURI())
	return hmac.Equal([]byte(sig), []byte(expected))
}

func (p *PeerProxy) ForwardRun(w http.ResponseWriter, r *http.Request, space, runID string) bool {
	if p == nil || p.Locator == nil {
		return false
	}
	return p.forwardIfRemote(w, r, func(ctx context.Context) (string, bool, error) {
		return p.Locator.RunPeer(ctx, space, runID)
	})
}

func (p *PeerProxy) ForwardExperiment(w http.ResponseWriter, r *http.Request, space, experimentID string) bool {
	if p == nil || p.Locator == nil {
		return false
	}
	return p.forwardIfRemote(w, r, func(ctx context.Context) (string, bool, error) {
		return p.Locator.ExperimentPeer(ctx, space, experimentID)
	})
}

func (p *PeerProxy) forwardIfRemote(w http.ResponseWriter, r *http.Request, lookup func(context.Context) (string, bool, error)) bool {
	peerAddr, remote, err := lookup(r.Context())
	if err != nil || !remote {
		return false
	}
	if p.isVerifiedHop(r) || strings.TrimSpace(peerAddr) == "" {
		apiError(w, run.ErrUnavailable, p.Logger)
		return true
	}
	target, err := url.Parse(peerAddr)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		apiError(w, run.ErrUnavailable, p.Logger)
		return true
	}
	requestURI := r.URL.RequestURI()
	nodeID := p.Locator.NodeID()
	sig := p.sign(nodeID, r.Method, requestURI)
	proxy := &httputil.ReverseProxy{
		Transport:     p.Transport,
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = target.Scheme
			pr.Out.URL.Host = target.Host
			pr.Out.Host = target.Host
			// Avoid gzip buffering so SSE frames stream immediately.
			pr.Out.Header.Set("Accept-Encoding", "identity")
			pr.Out.Header.Set(peerHopHeader, nodeID)
			if sig != "" {
				pr.Out.Header.Set(peerSigHeader, sig)
			} else {
				pr.Out.Header.Del(peerSigHeader)
			}
		},
		ErrorHandler: func(rw http.ResponseWriter, req *http.Request, proxyErr error) {
			if req.Context().Err() != nil {
				return
			}
			if p.Logger != nil {
				p.Logger.Warn("peer proxy failed", "target", target.Host, "path", req.URL.Path, "error", proxyErr)
			}
			apiError(rw, run.ErrUnavailable, p.Logger)
		},
	}
	proxy.ServeHTTP(w, r)
	return true
}
