package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRetryRetainsMutationIdentity(t *testing.T) {
	calls := 0
	key := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("x-api-key") != "secret" {
			t.Error("missing authentication")
		}
		if key == "" {
			key = r.Header.Get("Idempotency-Key")
		}
		if key == "" || r.Header.Get("Idempotency-Key") != key {
			t.Error("mutation identity changed")
		}
		if calls == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"error":{"code":"tenant_unavailable","message":"retry"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"id":"project"}}`))
	}))
	defer server.Close()
	c, _ := New(server.URL, "secret")
	var p Project
	if err := c.RPC(context.Background(), "project/create", struct{}{}, &p, true); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || p.ID != "project" {
		t.Fatalf("calls %d, project %#v", calls, p)
	}
}
func TestConflictsAreNotRetriedAndSecretsAreRedacted(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(409)
		_, _ = w.Write([]byte(`{"error":{"code":"conflict","message":"secret changed","requestId":"req-1"}}`))
	}))
	defer server.Close()
	c, _ := New(server.URL, "secret")
	err := c.RPC(context.Background(), "project/update", struct{}{}, nil, true)
	if err == nil || calls != 1 || strings.Contains(err.Error(), "secret") || !strings.Contains(err.Error(), "req-1") {
		t.Fatalf("calls %d, error %v", calls, err)
	}
}
func TestRetryWaitIsCancellable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Retry-After", "30"); w.WriteHeader(429) }))
	defer server.Close()
	c, _ := New(server.URL, "secret")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := c.RPC(ctx, "project/list", struct{}{}, nil, false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
}
func TestRedirectDoesNotForwardCredentials(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("redirect followed") }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer source.Close()
	c, _ := New(source.URL, "secret")
	if err := c.RPC(context.Background(), "project/list", struct{}{}, nil, false); err == nil {
		t.Fatal("expected redirect diagnostic")
	}
}
func TestURLValidation(t *testing.T) {
	for _, u := range []string{"", ":bad", "ftp://example.com", "https://user:pass@example.com", "https://example.com?token=x", "https://example.com#fragment"} {
		if _, err := New(u, "secret"); err == nil {
			t.Errorf("accepted %s", u)
		}
	}
}

func TestHTTPRequiresLoopbackHost(t *testing.T) {
	for _, baseURL := range []string{"http://example.com", "http://192.168.1.1", "http://localhost.example.com", "http://127.0.0.1.example.com", "http://[::]"} {
		if _, err := New(baseURL, "secret"); err == nil {
			t.Errorf("accepted plaintext remote URL %s", baseURL)
		}
	}
	for _, baseURL := range []string{"https://example.com", "http://localhost:8000", "http://127.0.0.1:8000", "http://[::1]:8000"} {
		if _, err := New(baseURL, "secret"); err != nil {
			t.Errorf("rejected %s: %v", baseURL, err)
		}
	}
}

func TestCompleteLargeListResponse(t *testing.T) {
	name := strings.Repeat("x", 17<<20)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"large","name":"` + name + `"}]}`))
	}))
	defer server.Close()
	c, _ := New(server.URL, "secret")
	projects, err := c.Projects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].Name != name {
		t.Fatal("project list was truncated")
	}
}
