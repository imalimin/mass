package einoadapter

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
)

// Recorder must durably accept bytes before returning nil. D00 verifies the
// transport with an in-memory test recorder; D04 supplies Journal/TOS storage.
// Headers and URLs are intentionally absent: they may contain credentials.
type Recorder interface {
	Request(context.Context, []byte) error
	Response(context.Context, int, []byte) error
}

type CaptureTransport struct {
	Base             http.RoundTripper
	Recorder         Recorder
	MaxRequestBytes  int64
	MaxResponseBytes int64
}

func (t *CaptureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.Base == nil || t.Recorder == nil || t.MaxRequestBytes <= 0 || t.MaxResponseBytes <= 0 {
		return nil, errors.New("incomplete capture transport configuration")
	}
	if req.Body == nil {
		return nil, errors.New("model request body is required")
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, t.MaxRequestBytes+1))
	_ = req.Body.Close()
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > t.MaxRequestBytes {
		return nil, errors.New("model request exceeds capture limit")
	}
	if err = t.Recorder.Request(req.Context(), bytes.Clone(body)); err != nil {
		return nil, err
	}
	copyReq := req.Clone(req.Context())
	copyReq.Body = io.NopCloser(bytes.NewReader(body))
	copyReq.ContentLength = int64(len(body))
	copyReq.GetBody = nil // A retry must be a distinct gateway attempt, recorded again.
	response, err := t.Base.RoundTrip(copyReq)
	if err != nil {
		return nil, err
	}
	response.Body = &capturedBody{ReadCloser: response.Body, ctx: req.Context(), recorder: t.Recorder, status: response.StatusCode, remaining: t.MaxResponseBytes}
	return response, nil
}

type capturedBody struct {
	io.ReadCloser
	ctx       context.Context
	recorder  Recorder
	status    int
	remaining int64
	failed    error
}

func (b *capturedBody) Read(p []byte) (int, error) {
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
		b.failed = errors.New("model response exceeds capture limit")
		_ = b.Close()
		return 0, b.failed
	}
	if n > 0 {
		if saveErr := b.recorder.Response(b.ctx, b.status, bytes.Clone(p[:n])); saveErr != nil {
			b.failed = saveErr
			_ = b.Close()
			return 0, saveErr
		}
		b.remaining -= int64(n)
	}
	return n, err
}
