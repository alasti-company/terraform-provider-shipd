package client

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestAuditDestinationWriteDoesNotReplayLostResponse(t *testing.T) {
	for _, operation := range []string{"createDestination", "updateDestination"} {
		t.Run(operation, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/api/v1/rpc/audit/"+operation || r.Header.Get("Idempotency-Key") != "" {
					t.Error("unexpected write operation or replay header")
				}
				// A truncated response after the mutation committed.
				_, _ = w.Write([]byte(`{"data":{`))
			}))
			defer server.Close()
			c, err := New(server.URL, "fixture-token")
			if err != nil {
				t.Fatal(err)
			}
			config := AuditDestinationConfig{URL: "https://receiver.example/audit"}
			if operation == "createDestination" {
				_, err = c.CreateAuditDestination(t.Context(), "webhook", "Audit webhook", false, config, "fixture-signing-secret")
			} else {
				_, err = c.UpdateAuditDestination(t.Context(), "destination", "webhook", nil, nil, &config, "fixture-signing-secret")
			}
			if err == nil || calls.Load() != 1 {
				t.Fatalf("expected a single attempt and a failed response, got %d calls, error %v", calls.Load(), err)
			}
		})
	}
}

func TestAuditDestinationReadDistinguishesMissingFromUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"present", http.StatusOK, `{"data":[{"id":"other"},{"id":"destination","name":"Matches"}]}`},
		{"absent", http.StatusOK, `{"data":[{"id":"other"}]}`},
		{"forbidden", http.StatusForbidden, ""},
		{"route missing", http.StatusNotFound, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			c, err := New(server.URL, "fixture-token")
			if err != nil {
				t.Fatal(err)
			}
			result, err := c.AuditDestination(t.Context(), "destination")
			if tc.name == "present" {
				if err != nil || result.ID != "destination" || result.Name != "Matches" {
					t.Fatalf("expected matching destination, got %+v, error %v", result, err)
				}
			} else if err == nil || errors.Is(err, ErrNotFound) != (tc.name == "absent") {
				t.Fatalf("unexpected result for list status %d: %v", tc.status, err)
			}
		})
	}
}
