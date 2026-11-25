package schemacache

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestPoolReuseEvictionAndErrors(t *testing.T) {
	pool := NewPool(2)
	s1 := []byte(`{"type":"object","additionalProperties":false,"properties":{"path":{"type":"string"}},"required":["path"]}`)
	s2 := []byte(`{"type":"object","additionalProperties":false,"properties":{"count":{"type":"integer"}},"required":["count"]}`)
	s3 := []byte(`{"type":"object","additionalProperties":false,"properties":{"ok":{"type":"boolean"}},"required":["ok"]}`)

	first, err := pool.Compile(s1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := pool.Compile(s1)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("expected identical cached *jsonschema.Schema pointer on repeat compile")
	}
	if err := pool.Validate(s1, map[string]any{"path": "notes.txt"}); err != nil {
		t.Fatalf("valid payload rejected: %v", err)
	}
	if err := pool.Validate(s1, map[string]any{"extra": true}); err == nil {
		t.Fatal("invalid payload accepted")
	}

	// Malformed JSON and invalid schema definitions fail with ErrInvalidSchema and are not cached.
	for _, bad := range [][]byte{[]byte(`{broken`), []byte(`{"type":"not_a_type"}`)} {
		if _, err := pool.Compile(bad); !errors.Is(err, ErrInvalidSchema) {
			t.Fatalf("expected ErrInvalidSchema for %s, got %v", bad, err)
		}
	}
	if pool.Len() != 1 {
		t.Fatalf("expected 1 cached schema after invalid compiles, got %d", pool.Len())
	}

	// Capacity bound evicts the oldest entry.
	if _, err := pool.Compile(s2); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Compile(s3); err != nil {
		t.Fatal(err)
	}
	if pool.Len() != 2 {
		t.Fatalf("expected pool capacity 2, got %d", pool.Len())
	}
}

func TestPoolConcurrentCompileAndValidate(t *testing.T) {
	pool := NewPool(16)
	raw := []byte(`{"type":"object","additionalProperties":false,"properties":{"id":{"type":"string"}},"required":["id"]}`)
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := pool.Validate(raw, map[string]any{"id": fmt.Sprintf("item-%d", i)}); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if pool.Len() != 1 {
		t.Fatalf("expected 1 cached schema, got %d", pool.Len())
	}
}
