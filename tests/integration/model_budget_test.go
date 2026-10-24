package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

func TestRealRequestBudgetGuard(t *testing.T) {
	var received atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1); w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	budget := &paidRequestBudget{limit: 8}
	client := &http.Client{Transport: budget}
	var group sync.WaitGroup
	var completed atomic.Int64
	for range 20 {
		group.Go(func() {
			request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, nil)
			if err != nil {
				t.Error(err)
				return
			}
			response, err := client.Do(request)
			if err == nil {
				response.Body.Close()
				completed.Add(1)
			}
		})
	}
	group.Wait()
	if received.Load() != 8 || completed.Load() != 8 || budget.sent.Load() != 8 {
		t.Fatalf("budget exceeded or did not send exactly 8: received=%d completed=%d sent=%d", received.Load(), completed.Load(), budget.sent.Load())
	}
}
