package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestAuditDestinationCreateDoesNotReplayLostResponse(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Idempotency-Key") != "" {
			t.Error("destination create requested durable replay")
		}
		w.WriteHeader(http.StatusOK)
		// A truncated response after the mutation committed.
		_, _ = w.Write([]byte(`{"data":{`))
	}))
	defer server.Close()
	c, err := New(server.URL, "fixture-token")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.CreateAuditDestination(t.Context(), "webhook", "Audit webhook", false,
		AuditDestinationConfig{URL: "https://receiver.example/audit"}, "fixture-signing-secret")
	if err == nil || calls.Load() != 1 {
		t.Fatalf("expected a single attempt and a failed response, got %d calls, error %v", calls.Load(), err)
	}
}

func TestAuditDestinationReadDistinguishesMissingFromUnavailable(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusForbidden, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				if status == http.StatusOK {
					_, _ = w.Write([]byte(`{"data":[]}`))
				}
			}))
			defer server.Close()
			c, err := New(server.URL, "fixture-token")
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.AuditDestination(context.Background(), "destination")
			if err == nil || (err == ErrNotFound) != (status == http.StatusOK) {
				t.Fatalf("unexpected result for list status %d: %v", status, err)
			}
		})
	}
}
