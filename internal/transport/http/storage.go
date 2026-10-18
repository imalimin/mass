package httptransport

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
	"github.com/imalimin/mass/internal/resource"
	"github.com/imalimin/mass/internal/transport/agui"
)

type StorageOptions struct {
	Content   *resource.ContentStore
	Journal   *journal.Journal
	Snapshots *resource.SnapshotStore
	Check     func(context.Context) error
}

// Cache only a bounded readiness result, never membership or object access.
func CachedReadiness(check func(context.Context) error) func(context.Context) error {
	var mu sync.Mutex
	var until time.Time
	var last error
	return func(ctx context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		if time.Now().Before(until) {
			return last
		}
		probe, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		last = check(probe)
		until = time.Now().Add(5 * time.Second)
		return last
	}
}
func journalQuery(r *http.Request) (int64, *int64, int, error) {
	after := int64(0)
	var through *int64
	limit := 20
	var err error
	if v := r.URL.Query().Get("after_sequence"); v != "" {
		after, err = agui.ParseCursor(v)
		if err != nil {
			return 0, nil, 0, storage.ErrInvalid
		}
	}
	if v := r.URL.Query().Get("through_sequence"); v != "" {
		n, e := agui.ParseCursor(v)
		if e != nil {
			return 0, nil, 0, storage.ErrInvalid
		}
		through = &n
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		limit, err = strconv.Atoi(v)
		if err != nil {
			return 0, nil, 0, storage.ErrInvalid
		}
	}
	return after, through, limit, nil
}

// After headers, a failed authorization stops bytes instead of appending JSON
// to the download. The client can recheck access using the ordinary API.
func copyAuthorized(ctx context.Context, w io.Writer, r io.Reader, check func(context.Context) error) error {
	buffer := make([]byte, 32<<10)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := check(ctx); err != nil {
			return err
		}
		n, err := r.Read(buffer)
		if n > 0 {
			if e := check(ctx); e != nil {
				return e
			}
			if _, e := w.Write(buffer[:n]); e != nil {
				return e
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
