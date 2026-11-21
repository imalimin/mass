package einoadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/imalimin/mass/internal/domain/run"
)

// responseWire observes completion and usage fields that the SDK may omit.
// It retains only the last incomplete SSE line between reads.
type responseWire struct {
	mu              sync.Mutex
	pending         []byte
	done            bool
	usage           run.Usage
	usagePresent    bool
	reasoningTokens *int64
	status          int
	requestID       string
}

func (w *responseWire) complete() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.done || bytes.Equal(sseData(w.pending), []byte("[DONE]"))
}

func (w *responseWire) observation() (int, run.Usage, bool, *int64, string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.observeLine(w.pending)
	w.pending = nil
	return w.status, w.usage, w.usagePresent, w.reasoningTokens, w.requestID
}

func (w *responseWire) observe(p []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for len(p) > 0 {
		end := bytes.IndexByte(p, '\n')
		if end < 0 {
			w.pending = append(w.pending, p...)
			return
		}
		line := p[:end]
		if len(w.pending) > 0 {
			line = append(w.pending, line...)
			w.pending = nil
		}
		w.observeLine(line)
		p = p[end+1:]
	}
}

func sseData(line []byte) []byte {
	if data, ok := bytes.CutPrefix(line, []byte("data:")); ok {
		return bytes.TrimSpace(data)
	}
	return nil
}

func (w *responseWire) observeLine(line []byte) {
	data := sseData(line)
	if len(data) == 0 {
		return
	}
	if bytes.Equal(data, []byte("[DONE]")) {
		w.done = true
		return
	}
	var item struct {
		Usage map[string]json.RawMessage `json:"usage"`
	}
	if json.Unmarshal(data, &item) != nil || item.Usage == nil {
		return
	}
	usage := item.Usage
	w.usagePresent = true
	merge := func(target **int64, raw json.RawMessage) {
		var value *int64
		if json.Unmarshal(raw, &value) == nil && value != nil && *value >= 0 {
			*target = value
		}
	}
	merge(&w.usage.Input, usage["prompt_tokens"])
	merge(&w.usage.Output, usage["completion_tokens"])
	merge(&w.usage.CacheWrite, usage["cache_creation_input_tokens"])
	var prompt, completion map[string]json.RawMessage
	if json.Unmarshal(usage["prompt_tokens_details"], &prompt) == nil {
		merge(&w.usage.CacheRead, prompt["cached_tokens"])
	}
	if json.Unmarshal(usage["completion_tokens_details"], &completion) == nil {
		merge(&w.reasoningTokens, completion["reasoning_tokens"])
	}
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
	t.wire.requestID = response.Header.Get("X-Request-Id")
	if t.wire.requestID == "" {
		t.wire.requestID = response.Header.Get("X-Tt-Logid")
	}
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
		b.wire.observe(p[:n])
		b.remaining -= int64(n)
	}
	return n, err
}
