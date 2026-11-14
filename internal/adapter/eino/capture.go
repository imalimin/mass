package einoadapter

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/imalimin/mass/internal/domain/run"
)

// responseWire keeps a bounded copy of the response for checking stream
// completion and extracting usage fields that the SDK may omit.
type responseWire struct {
	mu     sync.Mutex
	raw    bytes.Buffer
	status int
}

func (w *responseWire) complete() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return wireComplete(w.raw.Bytes())
}

func (w *responseWire) observation() (int, run.Usage, bool, *int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	usage, present, reasoning := wireTraceUsage(w.raw.Bytes())
	return w.status, usage, present, reasoning
}

type observedTransport struct {
	base http.RoundTripper
	wire *responseWire
}

func (t *observedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body == nil {
		return nil, errors.New("model request body is required")
	}
	const maxRequestBytes = 4 << 20
	body, err := io.ReadAll(io.LimitReader(req.Body, maxRequestBytes+1))
	_ = req.Body.Close()
	if err != nil {
		return nil, err
	}
	if len(body) > maxRequestBytes {
		return nil, errors.New("model request exceeds size limit")
	}
	copyReq := req.Clone(req.Context())
	copyReq.Body = io.NopCloser(bytes.NewReader(body))
	copyReq.ContentLength = int64(len(body))
	copyReq.GetBody = nil // A retry must be a distinct gateway attempt, recorded again.
	response, err := t.base.RoundTrip(copyReq)
	if err != nil {
		return nil, err
	}
	t.wire.mu.Lock()
	t.wire.status = response.StatusCode
	t.wire.mu.Unlock()
	response.Body = &observedBody{ReadCloser: response.Body, ctx: req.Context(), wire: t.wire, remaining: 8 << 20}
	return response, nil
}

type observedBody struct {
	io.ReadCloser
	ctx       context.Context
	wire      *responseWire
	remaining int64
	failed    error
}

func (b *observedBody) Read(p []byte) (int, error) {
	if b.failed != nil {
		return 0, b.failed
	}
	if len(p) == 0 {
		return 0, nil
	}
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	// Read at most one byte beyond the remaining budget to detect overflow.
	if int64(len(p)) > b.remaining+1 {
		p = p[:b.remaining+1]
	}
	n, err := b.ReadCloser.Read(p)
	if int64(n) > b.remaining {
		b.failed = errors.New("model response exceeds size limit")
		_ = b.Close()
		return 0, b.failed
	}
	if n > 0 {
		b.wire.mu.Lock()
		b.wire.raw.Write(p[:n])
		b.wire.mu.Unlock()
		b.remaining -= int64(n)
	}
	return n, err
}
