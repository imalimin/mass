// Package archive stores business content at explicit object keys in TOS.
package archive

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path"
	"sync"

	"github.com/imalimin/mass/internal/app/identity"
	"github.com/imalimin/mass/internal/domain/storage"
)

type Ref struct {
	storage.Object
	Key string `json:"key"`
}

func Reference(o storage.Object) Ref { return Ref{Object: o, Key: o.Key} }
func (r Ref) Metadata() storage.Object {
	o := r.Object
	o.Key = r.Key
	return o
}

type ObjectLookup func(context.Context, string, string) (Ref, error)

// Objects holds uploads until their owning record references them. Its index is
// process-local; after a restart the owning records resolve every retained object.
type Objects struct {
	mu        sync.Mutex
	pending   map[string]storage.Object
	Authorize func(context.Context, string, string, bool) error
	Lookups   []ObjectLookup
}

func (s *Objects) BeginObject(_ context.Context, _ string, o storage.Object) (storage.Object, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		s.pending = make(map[string]storage.Object)
	}
	key := o.SpaceID + "/" + o.ID
	if prior, ok := s.pending[key]; ok {
		if prior.SHA256 != o.SHA256 || prior.Key != o.Key || prior.Kind != o.Kind {
			return o, storage.ErrConflict
		}
		return prior, nil
	}
	s.pending[key] = o
	return o, nil
}

func (s *Objects) ReadyObject(_ context.Context, _ string, o storage.Object) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	o.State = "ready"
	s.pending[o.SpaceID+"/"+o.ID] = o
	return nil
}

func (s *Objects) Forget(space, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pending, space+"/"+id)
}
func (s *Objects) get(space, id string) (storage.Object, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.pending[space+"/"+id]
	return o, ok && o.State == "ready"
}

func (s *Objects) Object(ctx context.Context, actor, space, id string, committed bool) (storage.Object, error) {
	if err := s.Authorize(ctx, actor, space, false); err != nil {
		return storage.Object{}, err
	}
	if !committed {
		if o, ok := s.get(space, id); ok {
			return o, nil
		}
	}
	for _, lookup := range s.Lookups {
		r, err := lookup(ctx, space, id)
		if err == nil {
			return r.Metadata(), nil
		}
		if !errors.Is(err, identity.ErrNotFound) {
			return storage.Object{}, err
		}
	}
	return storage.Object{}, identity.ErrNotFound
}

func Key(prefix, space string, names ...string) string {
	return path.Join(append([]string{prefix, space}, names...)...)
}

func readJSON(ctx context.Context, blobs storage.BlobStore, key string, value any) error {
	r, err := blobs.Open(ctx, key, "")
	if err != nil {
		return err
	}
	defer r.Close()
	d := json.NewDecoder(r)
	if err := d.Decode(value); err != nil {
		return errors.Join(storage.ErrIntegrity, err)
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return storage.ErrIntegrity
	}
	return nil
}
