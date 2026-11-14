// Package fakes contains test-only adapters, never production storage fallbacks.
package fakes

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"

	"github.com/imalimin/mass/internal/domain/storage"
)

type Blobs struct {
	mu   sync.Mutex
	Data map[string][]byte
	Fail bool
}

func NewBlobs() *Blobs                           { return &Blobs{Data: map[string][]byte{}} }
func (b *Blobs) URI(key string) string           { return "tos://test-bucket/" + key }
func (b *Blobs) Check(ctx context.Context) error { return ctx.Err() }
func (b *Blobs) PutNew(ctx context.Context, key string, r io.Reader, size int64, media string) (storage.BlobInfo, error) {
	return b.put(ctx, key, r, true)
}
func (b *Blobs) Overwrite(ctx context.Context, key string, r io.Reader, size int64, media string) (storage.BlobInfo, error) {
	return b.put(ctx, key, r, false)
}
func (b *Blobs) put(ctx context.Context, key string, r io.Reader, immutable bool) (storage.BlobInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.Fail {
		return storage.BlobInfo{}, errors.New("injected TOS outage")
	}
	if _, ok := b.Data[key]; ok && immutable {
		return storage.BlobInfo{}, errors.New("overwrite forbidden")
	}
	data, err := io.ReadAll(storage.ContextReader{Context: ctx, Reader: r})
	if err != nil {
		return storage.BlobInfo{}, err
	}
	b.Data[key] = data
	return storage.BlobInfo{ETag: "opaque-not-sha256"}, nil
}
func (b *Blobs) Open(ctx context.Context, key, version string) (io.ReadCloser, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.Fail {
		return nil, errors.New("injected TOS outage")
	}
	data, ok := b.Data[key]
	if !ok {
		return nil, storage.ErrBlobNotFound
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), data...))), nil
}
func (b *Blobs) SetFailure(fail bool) { b.mu.Lock(); defer b.mu.Unlock(); b.Fail = fail }
func (b *Blobs) Corrupt(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Data[key] = []byte("corrupt")
}
