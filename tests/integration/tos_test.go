package integration

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/adapter/tos"
	"github.com/imalimin/mass/internal/domain/storage"
	sdk "github.com/volcengine/ve-tos-golang-sdk/v2/tos"
)

type trackedTOS struct {
	*tos.Store
	prefix string
	mu     sync.Mutex
	keys   map[string]string
}

func (s *trackedTOS) PutNew(ctx context.Context, key string, r io.Reader, n int64, media string) (storage.BlobInfo, error) {
	if !strings.HasPrefix(key, s.prefix) {
		return storage.BlobInfo{}, storage.ErrInvalid
	}
	out, err := s.Store.PutNew(ctx, key, r, n, media)
	// Track every attempted unique key, including an uncertain upload response.
	s.mu.Lock()
	if _, exists := s.keys[key]; err == nil || !exists {
		s.keys[key] = out.VersionID
	}
	s.mu.Unlock()
	return out, err
}
func TestRealTOSStorage(t *testing.T) {
	if os.Getenv("MASS_TEST_TOS") != "1" {
		t.Skip("set MASS_TEST_TOS=1 and dedicated test endpoint/bucket/prefix")
	}
	for _, name := range []string{"MASS_TEST_ADMIN_DSN", "MASS_TEST_TOS_ENDPOINT", "MASS_TEST_TOS_REGION", "MASS_TEST_TOS_BUCKET", "MASS_TEST_TOS_PREFIX", "TOS_ACCESS_KEY", "TOS_SECRET_KEY"} {
		if os.Getenv(name) == "" {
			t.Fatalf("missing %s", name)
		}
	}
	base := os.Getenv("MASS_TEST_TOS_PREFIX")
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9/_-]*/$`).MatchString(base) || strings.Contains(base, "//") {
		t.Fatal("explicit relative test prefix ending / required")
	}
	prefix := base + "mass-d01-" + uuid.NewString() + "/"
	client, err := tos.New(os.Getenv("MASS_TEST_TOS_ENDPOINT"), os.Getenv("MASS_TEST_TOS_REGION"), os.Getenv("MASS_TEST_TOS_BUCKET"), os.Getenv("TOS_ACCESS_KEY"), os.Getenv("TOS_SECRET_KEY"))
	if err != nil {
		t.Fatal("TOS client configuration rejected")
	}
	defer client.Client.Close()
	tracked := &trackedTOS{Store: client, prefix: prefix, keys: map[string]string{}}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		for key, version := range tracked.keys {
			if !strings.HasPrefix(key, prefix) {
				t.Error("refused cleanup outside test prefix")
				continue
			}
			_, err := client.Client.DeleteObjectV2(ctx, &sdk.DeleteObjectV2Input{Bucket: client.Bucket, Key: key, VersionID: version})
			if err != nil {
				t.Errorf("test object cleanup failed (HTTP %d)", sdk.StatusCode(err))
				continue
			}
			remaining, readErr := client.Client.GetObjectV2(ctx, &sdk.GetObjectV2Input{Bucket: client.Bucket, Key: key, VersionID: version})
			if remaining != nil {
				remaining.Content.Close()
			}
			if sdk.StatusCode(readErr) != 404 {
				t.Error("test object deletion not confirmed")
			}
		}
		t.Logf("cleaned %d exact test object keys", len(tracked.keys))
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err = tracked.Check(ctx); err != nil {
		t.Fatal(err)
	}
	key := prefix + "immutable-check"
	if _, err = tracked.PutNew(ctx, key, bytes.NewBufferString("first"), 5, "text/plain"); err != nil {
		t.Fatal(err)
	}
	// Invoke the actual SDK twice against the same key, bypassing SQL retry logic.
	if _, err = client.PutNew(ctx, key, bytes.NewBufferString("changed"), 7, "text/plain"); err == nil {
		t.Fatal("TOS accepted an overwrite")
	} else {
		var e *tos.Error
		if !errors.As(err, &e) || (e.Status != 409 && e.Status != 412) {
			t.Fatal("overwrite did not fail with an immutable-object conflict")
		}
	}
	reader, err := tracked.Open(ctx, key, "")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	reader.Close()
	if err != nil || string(data) != "first" {
		t.Fatal("TOS immutable object changed")
	}
	exerciseStorage(t, tracked, prefix)
	exerciseCatalog(t, tracked, prefix+"catalog/")
	exerciseRuntime(t, tracked, prefix+"runtime/", true)
	exerciseTools(t, tracked, prefix+"tools/")
	if os.Getenv("MASS_TEST_REAL_MODEL") == "1" {
		for _, key := range []string{"MASS_LLM_MODEL_BASE_URL", "MASS_LLM_MODEL_KEY", "MASS_LLM_MODEL_ID"} {
			if os.Getenv(key) == "" {
				t.Fatalf("missing %s", key)
			}
		}
		limit := int64(8)
		if value := os.Getenv("MASS_TEST_REAL_REQUEST_LIMIT"); value != "" {
			parsed, err := strconv.ParseInt(value, 10, 64)
			if err != nil || parsed < 1 || parsed > 8 {
				t.Fatal("MASS_TEST_REAL_REQUEST_LIMIT must be between 1 and 8")
			}
			limit = parsed
		}
		realModelHTTPBudget = &paidRequestBudget{limit: limit}
		t.Cleanup(func() {
			t.Logf("real model HTTP requests sent: %d (hard limit %d)", realModelHTTPBudget.sent.Load(), limit)
		})
		exerciseRuntime(t, tracked, prefix+"real-model/", false, true)
		exerciseD07(t, tracked, prefix+"d07/")
	}
}
