package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestReadWebhookDistinguishesMissingFromFailedList(t *testing.T) {
	const secret = "sensitive-value"
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		missing bool
	}{
		{"deleted webhook", 200, `{"data":[]}`, true},
		{"deleted environment", 404, `{"error":{"code":"not_found","message":"Environment not found"}}`, true},
		{"unsupported route", 404, "404 Not Found", false},
		{"forbidden", 403, fmt.Sprintf(`{"error":{"code":%q,"message":%q,"requestId":%q}}`, secret, secret, secret), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			c, err := New(server.URL, "test-token")
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.ReadWebhook(context.Background(), "eventWebhook", "env-id", "endpoint-id")
			if err == nil || errors.Is(err, ErrNotFound) != tc.missing {
				t.Fatalf("expected missing=%t, got error %v", tc.missing, err)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatal("webhook diagnostic exposed an API-supplied secret")
			}
		})
	}
}

func TestEventWebhookUpdateDoesNotRetryAnAmbiguousResponse(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	c, err := New(server.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.UpdateWebhook(context.Background(), "eventWebhook", Webhook{
		ID: "endpoint-id", EnvironmentID: "env-id", EventTypes: []string{"invoice.paid"},
	})
	if calls.Load() != 1 || err == nil {
		t.Fatalf("expected one attempt and a diagnostic, got %d attempts and %v", calls.Load(), err)
	}
}
