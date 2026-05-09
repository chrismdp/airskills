package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chrismdp/airskills/config"
)

// TestCountSuggestionsPrefersNewEndpoint verifies that countSuggestions
// hits the new /api/v1/suggestions/count route by default, not the legacy
// polymorphic /api/v1/suggestions?count=1 form.
func TestCountSuggestionsPrefersNewEndpoint(t *testing.T) {
	countUseLegacy.Store(false)
	t.Cleanup(func() { countUseLegacy.Store(false) })

	var newHits, legacyHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/suggestions/count":
			newHits++
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"count":7}`))
		case "/api/v1/suggestions":
			legacyHits++
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"count":99}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := &apiClient{baseURL: srv.URL, token: "t", http: srv.Client()}
	got, err := c.countSuggestions("owner", "pending", "")
	if err != nil {
		t.Fatalf("countSuggestions: %v", err)
	}
	if got != 7 {
		t.Errorf("count: want 7 (from new endpoint), got %d", got)
	}
	if newHits != 1 {
		t.Errorf("expected 1 hit on /suggestions/count, got %d", newHits)
	}
	if legacyHits != 0 {
		t.Errorf("expected 0 hits on legacy ?count=1 path, got %d", legacyHits)
	}
}

// TestCountSuggestionsFallsBackOnNotFound verifies the back-compat path:
// older self-hosted platforms without /suggestions/count return 404, and
// the CLI must transparently fall back to the legacy ?count=1 form. The
// per-process cache then sticks so subsequent calls skip the probe.
func TestCountSuggestionsFallsBackOnNotFound(t *testing.T) {
	countUseLegacy.Store(false)
	t.Cleanup(func() { countUseLegacy.Store(false) })

	var newHits, legacyHits int
	var lastLegacyQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/suggestions/count":
			newHits++
			http.NotFound(w, r)
		case "/api/v1/suggestions":
			legacyHits++
			lastLegacyQuery = r.URL.RawQuery
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"count":3}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := &apiClient{baseURL: srv.URL, token: "t", http: srv.Client()}

	// First call probes the new endpoint, gets 404, falls back.
	got, err := c.countSuggestions("owner", "pending", "")
	if err != nil {
		t.Fatalf("countSuggestions (first): %v", err)
	}
	if got != 3 {
		t.Errorf("count (first): want 3, got %d", got)
	}
	if newHits != 1 || legacyHits != 1 {
		t.Errorf("after first call: newHits=%d legacyHits=%d (want 1, 1)", newHits, legacyHits)
	}
	// Legacy path must include count=1 to trigger the count branch.
	if !strings.Contains(lastLegacyQuery, "count=1") {
		t.Errorf("legacy fallback query missing count=1: %q", lastLegacyQuery)
	}

	// Second call must NOT re-probe — go straight to legacy.
	if _, err := c.countSuggestions("suggester", "", ""); err != nil {
		t.Fatalf("countSuggestions (second): %v", err)
	}
	if newHits != 1 {
		t.Errorf("after second call: newHits=%d (want still 1 — probe should be cached)", newHits)
	}
	if legacyHits != 2 {
		t.Errorf("after second call: legacyHits=%d (want 2)", legacyHits)
	}
}

// TestSuggestionsCountPathPropagatesFilters guards against silently dropping
// query params on the new endpoint.
func TestSuggestionsCountPathPropagatesFilters(t *testing.T) {
	cases := []struct {
		role, status, skill string
		want                string
	}{
		{"", "", "", "/api/v1/suggestions/count"},
		{"owner", "", "", "/api/v1/suggestions/count?role=owner"},
		{"owner", "pending", "", "/api/v1/suggestions/count?role=owner&status=pending"},
		{"", "", "abc-123", "/api/v1/suggestions/count?skill_id=abc-123"},
		{"suggester", "accepted", "abc-123", "/api/v1/suggestions/count?role=suggester&status=accepted&skill_id=abc-123"},
	}
	for _, tc := range cases {
		got := suggestionsCountPath(tc.role, tc.status, tc.skill)
		if got != tc.want {
			t.Errorf("suggestionsCountPath(%q,%q,%q): want %q, got %q",
				tc.role, tc.status, tc.skill, tc.want, got)
		}
	}
}

// TestRefreshAccessTokenExposeError verifies that a non-200 response includes
// both the HTTP status code and the response body in the error — not just a
// bare status integer — so callers can diagnose why refresh failed.
func TestRefreshAccessTokenExposeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/refresh" {
			http.Error(w, `{"error":"not found"}`, 404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"refresh token expired"}`))
	}))
	defer srv.Close()

	_, err := refreshAccessToken(srv.URL, "stale-refresh-token")
	if err == nil {
		t.Fatal("expected error for 401 response, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "401") {
		t.Errorf("error should include HTTP status 401, got: %s", msg)
	}
	if !strings.Contains(msg, "refresh token expired") {
		t.Errorf("error should include response body, got: %s", msg)
	}
}

// TestRefreshAccessTokenSuccess verifies that a 200 response returns the new
// token with correct fields.
func TestRefreshAccessTokenSuccess(t *testing.T) {
	newToken := config.TokenData{
		AccessToken:  "new-access-token",
		RefreshToken: "new-refresh-token",
		ExpiresAt:    time.Now().Add(30 * 24 * time.Hour).Unix(),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(newToken)
	}))
	defer srv.Close()

	got, err := refreshAccessToken(srv.URL, "valid-refresh-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.AccessToken != newToken.AccessToken {
		t.Errorf("AccessToken: want %q, got %q", newToken.AccessToken, got.AccessToken)
	}
	if got.RefreshToken != newToken.RefreshToken {
		t.Errorf("RefreshToken: want %q, got %q", newToken.RefreshToken, got.RefreshToken)
	}
}

// TestNewAPIClientAutoRefreshSavesToken verifies that when the stored token is
// expired, newAPIClientAuto refreshes it and writes the new token to disk.
func TestNewAPIClientAutoRefreshSavesToken(t *testing.T) {
	newToken := config.TokenData{
		AccessToken:  "refreshed-access-token",
		RefreshToken: "refreshed-refresh-token",
		ExpiresAt:    time.Now().Add(30 * 24 * time.Hour).Unix(),
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/refresh" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(newToken)
			return
		}
		http.Error(w, `{"error":"not found"}`, 404)
	}))
	defer srv.Close()

	// Redirect config reads/writes to a temp dir.
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome) // Windows

	cfgDir := filepath.Join(tmpHome, ".config", "airskills")
	if err := os.MkdirAll(cfgDir, 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Write config pointing at mock server.
	cfgData, _ := json.Marshal(config.Config{APIURL: srv.URL})
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), cfgData, 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Write an expired token with a valid refresh token.
	expiredToken := config.TokenData{
		AccessToken:  "old-access-token",
		RefreshToken: "old-refresh-token",
		ExpiresAt:    time.Now().Add(-1 * time.Hour).Unix(),
	}
	tokenData, _ := json.Marshal(expiredToken)
	if err := os.WriteFile(filepath.Join(cfgDir, "token.json"), tokenData, 0600); err != nil {
		t.Fatalf("write token: %v", err)
	}

	client, err := newAPIClientAuto()
	if err != nil {
		t.Fatalf("newAPIClientAuto: %v", err)
	}
	if client.token != newToken.AccessToken {
		t.Errorf("in-memory token: want %q, got %q", newToken.AccessToken, client.token)
	}

	// Verify the refreshed token was persisted to disk.
	raw, err := os.ReadFile(filepath.Join(cfgDir, "token.json"))
	if err != nil {
		t.Fatalf("read token.json after refresh: %v", err)
	}
	var saved config.TokenData
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatalf("unmarshal saved token: %v", err)
	}
	if saved.AccessToken != newToken.AccessToken {
		t.Errorf("saved AccessToken: want %q, got %q", newToken.AccessToken, saved.AccessToken)
	}
	if saved.RefreshToken != newToken.RefreshToken {
		t.Errorf("saved RefreshToken: want %q, got %q", newToken.RefreshToken, saved.RefreshToken)
	}
}
