package cmd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestResolveCommitID_FullUUIDPassesThrough — passing a full UUID skips
// the prefix-match path and returns the input unchanged. We still hit
// the versions endpoint to confirm the commit belongs to the skill;
// the value just doesn't get rewritten.
func TestResolveCommitID_FullUUIDPassesThrough(t *testing.T) {
	full := "0e3b77c3-1111-2222-3333-444455556666"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/versions") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"versions":[{"id":"` + full + `","created_at":"2026-04-03T00:00:00Z"}]}`))
	}))
	defer srv.Close()

	c := &apiClient{baseURL: srv.URL, token: "t", http: srv.Client()}
	got, err := resolveCommitID(c, "skill-id", full)
	if err != nil {
		t.Fatalf("resolveCommitID: %v", err)
	}
	if got != full {
		t.Errorf("want %q, got %q", full, got)
	}
}

// TestResolveCommitID_ShortPrefixResolvesToFull — the canonical bug repro:
// `airskills log` displays an 8-char prefix, the user copies it, the CLI
// must expand it to the full UUID before calling the archive endpoint
// (which requires an exact match server-side).
func TestResolveCommitID_ShortPrefixResolvesToFull(t *testing.T) {
	full := "0e3b77c3-1111-2222-3333-444455556666"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/versions") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"versions":[{"id":"` + full + `","created_at":"2026-04-03T00:00:00Z"},{"id":"aaaaaaaa-1111-2222-3333-444455556666","created_at":"2026-04-02T00:00:00Z"}]}`))
	}))
	defer srv.Close()

	c := &apiClient{baseURL: srv.URL, token: "t", http: srv.Client()}
	got, err := resolveCommitID(c, "skill-id", "0e3b77c3")
	if err != nil {
		t.Fatalf("resolveCommitID: %v", err)
	}
	if got != full {
		t.Errorf("want %q, got %q", full, got)
	}
}

// TestResolveCommitID_NoMatch — a prefix that matches nothing returns
// an actionable error, not a silent fallback to HEAD.
func TestResolveCommitID_NoMatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"versions":[{"id":"0e3b77c3-1111-2222-3333-444455556666","created_at":"2026-04-03T00:00:00Z"}]}`))
	}))
	defer srv.Close()

	c := &apiClient{baseURL: srv.URL, token: "t", http: srv.Client()}
	_, err := resolveCommitID(c, "skill-id", "deadbeef")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "deadbeef") {
		t.Errorf("error should mention the bad prefix; got %v", err)
	}
}

// TestResolveCommitID_Ambiguous — when a prefix matches multiple commits
// (rare but possible on very short prefixes) the CLI must refuse rather
// than silently picking one.
func TestResolveCommitID_Ambiguous(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"versions":[{"id":"0e3b77c3-1111-2222-3333-444455556666","created_at":"2026-04-03T00:00:00Z"},{"id":"0e3b77c3-aaaa-2222-3333-444455556666","created_at":"2026-04-02T00:00:00Z"}]}`))
	}))
	defer srv.Close()

	c := &apiClient{baseURL: srv.URL, token: "t", http: srv.Client()}
	_, err := resolveCommitID(c, "skill-id", "0e3b77c3")
	if err == nil {
		t.Fatal("expected ambiguity error, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "ambiguous") {
		t.Errorf("error should say ambiguous; got %v", err)
	}
}

// TestResolveCommitID_EmptyHistory — calling on a skill with no commits
// returns a clear error instead of an opaque "no match".
func TestResolveCommitID_EmptyHistory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"versions":[]}`))
	}))
	defer srv.Close()

	c := &apiClient{baseURL: srv.URL, token: "t", http: srv.Client()}
	_, err := resolveCommitID(c, "skill-id", "0e3b77c3")
	if err == nil {
		t.Fatal("expected error on empty history, got nil")
	}
}
