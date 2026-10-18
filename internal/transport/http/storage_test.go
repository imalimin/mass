package httptransport

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestDownloadStopsOnRevocation(t *testing.T) {
	var output bytes.Buffer
	checks := 0
	denied := errors.New("forbidden")
	err := copyAuthorized(context.Background(), &output, bytes.NewReader(make([]byte, 100000)), func(context.Context) error {
		checks++
		if checks >= 3 {
			return denied
		}
		return nil
	})
	if !errors.Is(err, denied) || output.Len() != 32<<10 {
		t.Fatalf("stream continued after revocation: %d %v", output.Len(), err)
	}
}
func TestReadinessCache(t *testing.T) {
	checks := 0
	check := CachedReadiness(func(context.Context) error { checks++; return errors.New("unavailable") })
	for range 3 {
		if check(context.Background()) == nil {
			t.Fatal("failed dependency reported healthy")
		}
	}
	if checks != 1 {
		t.Fatal("readiness not cached")
	}
}
