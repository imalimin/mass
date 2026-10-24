package projector

import (
	"context"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/resource"
	"sync"
)

// Cache only caches immutable bytes, never authorization or semantic results.
// Semantic inputs (space/history/versions/config/summary) are rebuilt each time.
// Every hit rechecks current access and the object's committed digest.
type Cache struct {
	mu    sync.Mutex
	items map[string][]byte
	order []string
	size  int
}

func (c *Cache) Read(ctx context.Context, store *resource.ContentStore, actor, space, id string, limit int64) ([]byte, error) {
	o, err := store.Repo.Object(ctx, actor, space, id, true)
	if err != nil {
		return nil, err
	}
	if o.Size > limit {
		return nil, storage.ErrIntegrity
	}
	key := space + "/" + id + "/" + o.SHA256
	c.mu.Lock()
	raw, ok := c.items[key]
	if ok {
		raw = append([]byte(nil), raw...)
	}
	c.mu.Unlock()
	if ok {
		return raw, nil
	}
	raw, err = resource.ReadBytes(ctx, store, actor, space, id, limit)
	if err != nil {
		return nil, err
	}
	if len(raw) > 1<<20 {
		return raw, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.items == nil {
		c.items = map[string][]byte{}
	}
	if _, ok := c.items[key]; !ok {
		for len(c.order) >= 128 || c.size+len(raw) > 8<<20 {
			old := c.order[0]
			c.order = c.order[1:]
			c.size -= len(c.items[old])
			delete(c.items, old)
		}
		c.items[key] = append([]byte(nil), raw...)
		c.order = append(c.order, key)
		c.size += len(raw)
	}
	return raw, nil
}
