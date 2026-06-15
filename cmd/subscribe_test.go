package cmd

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	// #3: classified on a typed sentinel, not a substring of the message.
	if !errors.Is(err, errSubscribeForbidden) {
		t.Errorf("403 should return the errSubscribeForbidden sentinel, got %v", err)
	}
	if !isForbiddenError(err) {
		t.Errorf("isForbiddenError should be true for a 403")
	}
}

// #3 — TestIsForbiddenErrorNotSubstring: a non-403 error whose message merely
// CONTAINS "(403)" must NOT be read as forbidden (the false-promote the old
// substring form risked on a 5xx body).
func TestIsForbiddenErrorNotSubstring(t *testing.T) {
	err := fmt.Errorf("API error (500): upstream proxy mentioned (403) in its log")
	if isForbiddenError(err) {
		t.Errorf("a 500 error containing the text '(403)' must not match the forbidden sentinel")
	}
	if isForbiddenError(nil) {
		t.Errorf("nil is not forbidden")
	}
}
