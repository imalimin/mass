package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/imalimin/mass/internal/adapter/tos"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/tests/testdb"
	sdk "github.com/volcengine/ve-tos-golang-sdk/v2/tos"
)

type recoveryEntry struct {
	Key      string          `json:"key"`
	Versions map[string]bool `json:"versions_deleted"`
	Absent   bool            `json:"absence_confirmed"`
}

// The ledger is persisted before any possible upload. Only exact keys written
// or planned by this isolated rehearsal can be inspected or removed.
type recoveryLedger struct {
	*tos.Store `json:"-"`
	mu         sync.Mutex
	Prefix     string                    `json:"prefix"`
	Entries    map[string]*recoveryEntry `json:"objects"`
	Path       string                    `json:"-"`
	Deleted    int                       `json:"deleted_versions"`
}

func (l *recoveryLedger) persist() error {
	raw, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(l.Path+".new", append(raw, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(l.Path+".new", l.Path)
}
func (l *recoveryLedger) register(key string, version *string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	relative, ok := strings.CutPrefix(key, l.Prefix)
	if !ok || strings.Contains(key, "..") || (!strings.HasPrefix(relative, "source/") && !strings.HasPrefix(relative, "restored/")) {
		return errors.New("recovery key outside owned test prefixes")
	}
	e := l.Entries[key]
	if e == nil {
		e = &recoveryEntry{Key: key, Versions: map[string]bool{}}
		l.Entries[key] = e
	}
	if version != nil {
		if _, known := e.Versions[*version]; !known {
			e.Versions[*version] = false
		}
	}
	return l.persist()
}
func (l *recoveryLedger) live(key, version string) error {
	if err := l.register(key, &version); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.Entries[key]
	e.Versions[version] = false
	e.Absent = false
	return l.persist()
}
func (l *recoveryLedger) PutNew(ctx context.Context, key string, r io.Reader, n int64, media string) (storage.BlobInfo, error) {
	if err := l.register(key, nil); err != nil {
		return storage.BlobInfo{}, err
	}
	info, err := l.Store.PutNew(ctx, key, r, n, media)
	if err == nil {
		err = l.live(key, info.VersionID)
	}
	return info, err
}
func (l *recoveryLedger) Overwrite(ctx context.Context, key string, r io.Reader, n int64, media string) (storage.BlobInfo, error) {
	if err := l.register(key, nil); err != nil {
		return storage.BlobInfo{}, err
	}
	info, err := l.Store.Overwrite(ctx, key, r, n, media)
	if err == nil {
		err = l.live(key, info.VersionID)
	}
	return info, err
}
func (l *recoveryLedger) inventory(ctx context.Context, db *testdb.Database) error {
	if db == nil {
		return nil
	}
	rows, err := db.Admin.DB.QueryContext(ctx, "SELECT object_key,version_id,state FROM content_objects")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key, version, state string
		if err = rows.Scan(&key, &version, &state); err != nil {
			return err
		}
		var known *string
		if state == "ready" {
			known = &version
		}
		if err = l.register(key, known); err != nil {
			return err
		}
	}
	return rows.Err()
}
func (l *recoveryLedger) remove(ctx context.Context, keys []string) error {
	var result error
	for _, key := range keys {
		if err := l.register(key, nil); err != nil {
			return err
		}
		// Resolve an upload with a lost acknowledgment without listing the bucket.
		out, err := l.Client.GetObjectV2(ctx, &sdk.GetObjectV2Input{Bucket: l.Bucket, Key: key})
		if err == nil {
			out.Content.Close()
			version := out.VersionID
			if err = l.live(key, version); err != nil {
				return err
			}
		} else if sdk.StatusCode(err) != 404 {
			result = errors.Join(result, fmt.Errorf("cleanup read failed (HTTP %d)", sdk.StatusCode(err)))
			continue
		}
		l.mu.Lock()
		entry := l.Entries[key]
		versions := map[string]bool{}
		for v, removed := range entry.Versions {
			versions[v] = removed
		}
		l.mu.Unlock()
		for version, removed := range versions {
			if removed {
				continue
			}
			_, err = l.Client.DeleteObjectV2(ctx, &sdk.DeleteObjectV2Input{Bucket: l.Bucket, Key: key, VersionID: version})
			if err != nil {
				result = errors.Join(result, fmt.Errorf("cleanup delete failed (HTTP %d)", sdk.StatusCode(err)))
				continue
			}
			check, readErr := l.Client.GetObjectV2(ctx, &sdk.GetObjectV2Input{Bucket: l.Bucket, Key: key, VersionID: version})
			if check != nil {
				check.Content.Close()
			}
			if sdk.StatusCode(readErr) != 404 {
				result = errors.Join(result, errors.New("deleted test version remains readable"))
				continue
			}
			l.mu.Lock()
			entry.Versions[version] = true
			l.Deleted++
			err = l.persist()
			l.mu.Unlock()
			if err != nil {
				return err
			}
		}
		check, readErr := l.Client.GetObjectV2(ctx, &sdk.GetObjectV2Input{Bucket: l.Bucket, Key: key})
		if check != nil {
			check.Content.Close()
		}
		if sdk.StatusCode(readErr) != 404 {
			result = errors.Join(result, errors.New("deleted test key remains readable"))
			continue
		}
		l.mu.Lock()
		entry.Absent = true
		err = l.persist()
		l.mu.Unlock()
		if err != nil {
			return err
		}
	}
	return result
}
func (l *recoveryLedger) keys(prefix string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var keys []string
	for key := range l.Entries {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

// Local exercise uses the same SDK and REST operations; real mode replaces only
// this endpoint/credential configuration with the explicitly supplied test TOS.
func recoveryTOSFixture(t *testing.T) string {
	t.Helper()
	var mu sync.Mutex
	objects := map[string][]byte{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-tos-request-id", "recovery-fixture")
		if r.Method == http.MethodHead {
			w.WriteHeader(200)
			return
		}
		key := r.URL.Path
		if r.Method == http.MethodPut {
			data, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
			if err != nil {
				w.WriteHeader(400)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			_, exists := objects[key]
			immutable := r.Header.Get("x-tos-forbid-overwrite") == "true"
			if (immutable && exists) || (!immutable && !strings.HasSuffix(key, "/trace.jsonl")) {
				w.WriteHeader(409)
				_, _ = io.WriteString(w, `{"Code":"FileAlreadyExists"}`)
				return
			}
			objects[key] = data
			w.Header().Set("ETag", "local-immutable")
			w.WriteHeader(200)
			_, _ = io.WriteString(w, "{}")
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodDelete {
			delete(objects, key)
			w.WriteHeader(204)
			return
		}
		if data, ok := objects[key]; ok && r.Method == http.MethodGet {
			w.Header().Set("Content-Length", fmt.Sprint(len(data)))
			w.WriteHeader(200)
			_, _ = w.Write(data)
			return
		}
		w.WriteHeader(404)
		_, _ = io.WriteString(w, `{"Code":"NoSuchKey"}`)
	}))
	t.Cleanup(server.Close)
	return server.URL
}
func recoveryModelFixture(t *testing.T) (string, *atomic.Int64) {
	t.Helper()
	calls := &atomic.Int64{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || !strings.HasSuffix(r.URL.Path, "/chat/completions") || r.Header.Get("Authorization") != "Bearer recovery-fixture-key" {
			w.WriteHeader(400)
			return
		}
		calls.Add(1)
		var request struct {
			Messages []struct{ Role, Content string }
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			w.WriteHeader(400)
			return
		}
		hasResult := false
		for _, m := range request.Messages {
			if m.Role == "tool" && strings.Contains(m.Content, "retained D09") {
				hasResult = true
			}
		}
		delta := map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "recovery-read", "type": "function", "function": map[string]any{"name": "read_file", "arguments": `{"path":"retained.txt"}`}}}}
		reason := "tool_calls"
		if hasResult {
			delta = map[string]any{"role": "assistant", "content": "Recovered retained D09 from TOS snapshot"}
			reason = "stop"
		}
		frame, _ := json.Marshal(map[string]any{"id": "recovery-fixture", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": reason}}, "usage": map[string]int{"prompt_tokens": 100, "completion_tokens": 10, "total_tokens": 110}})
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", frame)
	}))
	t.Cleanup(server.Close)
	return server.URL, calls
}
func recoveryPath(t *testing.T, name string) string {
	t.Helper()
	path, err := filepath.Abs("../../.cache/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRecoveryLedgerScope(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	client, err := tos.New(server.URL, "local", "local", "access-not-in-ledger", "secret-not-in-ledger")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Client.Close()
	prefix := "tmp/mass-d10-scope/"
	ledger := &recoveryLedger{Store: client, Prefix: prefix, Entries: map[string]*recoveryEntry{}, Path: filepath.Join(t.TempDir(), "objects.json")}
	if _, err = ledger.PutNew(context.Background(), "other/objects/test", strings.NewReader("x"), 1, "text/plain"); err == nil {
		t.Fatal("out-of-scope upload allowed")
	}
	if err = ledger.remove(context.Background(), []string{prefix + "source/../outside"}); err == nil {
		t.Fatal("out-of-scope cleanup allowed")
	}
	if requests.Load() != 0 {
		t.Fatal("rejected keys reached the storage endpoint")
	}
	if err = ledger.register(prefix+"source/space/objects/test", nil); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(ledger.Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"access-not-in-ledger", "secret-not-in-ledger", "Client", "Store"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("storage client or secret serialized into cleanup ledger")
		}
	}
}
