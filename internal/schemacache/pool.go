// Package schemacache provides a concurrency-safe, bounded cache pool for
// compiled JSON Schemas so callers avoid recompiling identical schemas on every
// validation.
package schemacache

import (
	"container/list"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	defaultMaxEntries = 512
	schemaResourceURL = "https://mass.local/schema"
)

// ErrInvalidSchema indicates that the supplied JSON Schema document could not
// be decoded or compiled.
var ErrInvalidSchema = errors.New("invalid json schema")

// Default is the process-wide shared JSON Schema cache pool.
var Default = NewPool(defaultMaxEntries)

type cacheEntry struct {
	key    string
	schema *jsonschema.Schema
}

// Pool caches compiled JSON Schemas keyed by their raw JSON bytes with LRU
// eviction once maxEntries is exceeded.
type Pool struct {
	mu         sync.RWMutex
	maxEntries int
	entries    map[string]*list.Element
	order      *list.List
}

// NewPool creates a bounded JSON Schema cache pool. Non-positive maxEntries
// falls back to the default capacity.
func NewPool(maxEntries int) *Pool {
	if maxEntries <= 0 {
		maxEntries = defaultMaxEntries
	}
	return &Pool{
		maxEntries: maxEntries,
		entries:    make(map[string]*list.Element, min(maxEntries, 32)),
		order:      list.New(),
	}
}

// Compile returns a cached compiled *jsonschema.Schema from the Default pool.
func Compile(raw []byte) (*jsonschema.Schema, error) {
	return Default.Compile(raw)
}

// Validate validates value against raw JSON Schema using the Default pool.
func Validate(raw []byte, value any) error {
	return Default.Validate(raw, value)
}

// Compile returns a compiled *jsonschema.Schema for raw, compiling and caching
// it on first sight. Malformed schemas are never cached.
func (p *Pool) Compile(raw []byte) (*jsonschema.Schema, error) {
	key := string(raw)

	p.mu.RLock()
	if elem, ok := p.entries[key]; ok {
		compiled := elem.Value.(*cacheEntry).schema
		p.mu.RUnlock()
		return compiled, nil
	}
	p.mu.RUnlock()

	compiled, err := compileSchema(raw)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if elem, ok := p.entries[key]; ok {
		p.order.MoveToFront(elem)
		return elem.Value.(*cacheEntry).schema, nil
	}
	elem := p.order.PushFront(&cacheEntry{key: key, schema: compiled})
	p.entries[key] = elem
	for p.order.Len() > p.maxEntries {
		oldest := p.order.Back()
		if oldest == nil {
			break
		}
		p.order.Remove(oldest)
		delete(p.entries, oldest.Value.(*cacheEntry).key)
	}
	return compiled, nil
}

// Validate compiles (or reuses from cache) raw and validates value against it.
func (p *Pool) Validate(raw []byte, value any) error {
	compiled, err := p.Compile(raw)
	if err != nil {
		return err
	}
	return compiled.Validate(value)
}

// Len reports the number of compiled schemas currently retained in the pool.
func (p *Pool) Len() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.entries)
}

func compileSchema(raw []byte) (*jsonschema.Schema, error) {
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidSchema, err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err := compiler.AddResource(schemaResourceURL, doc); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidSchema, err)
	}
	compiled, err := compiler.Compile(schemaResourceURL)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidSchema, err)
	}
	return compiled, nil
}
