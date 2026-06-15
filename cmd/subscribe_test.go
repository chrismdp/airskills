package cmd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSubscribeUnsubscribe verifies the client methods hit the right verb +
// path and treat the server's 204 as success.
func TestSubscribeUnsubscribe(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent) // 204, the idempotent success
	}))
	defer srv.Close()
	c := &apiClient{baseURL: srv.URL, token: "t", http: srv.Client()}

	if err := c.subscribe("abc-123"); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if gotMethod != "POST" || gotPath != "/api/v1/skills/abc-123/subscribe" {
		t.Errorf("subscribe hit %s %s", gotMethod, gotPath)
	}

	if err := c.unsubscribe("abc-123"); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	if gotMethod != "DELETE" || gotPath != "/api/v1/skills/abc-123/subscribe" {
		t.Errorf("unsubscribe hit %s %s", gotMethod, gotPath)
	}
}

// TestSubscribeForbidden verifies a 403 (skill unreadable — private / deleted /
// never-existed, not distinguished) surfaces as an error carrying the status,
// so the lifecycle path can tell it apart from a transport failure.
func TestSubscribeForbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"skill not found or not accessible"}`))
	}))
	defer srv.Close()
	c := &apiClient{baseURL: srv.URL, token: "t", http: srv.Client()}

	err := c.subscribe("abc-123")
	if err == nil {
		t.Fatalf("expected an error on 403")
	}
	if !strings.Contains(err.Error(), "(403)") {
		t.Errorf("expected 403 in error, got %v", err)
	}
}
