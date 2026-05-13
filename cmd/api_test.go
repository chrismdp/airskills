package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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

func TestNewAPIClientAutoSerializesConcurrentRefresh(t *testing.T) {
	newToken := config.TokenData{
		AccessToken:  "refreshed-access-token",
		RefreshToken: "refreshed-refresh-token",
		ExpiresAt:    time.Now().Add(30 * 24 * time.Hour).Unix(),
	}

	var refreshCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/refresh" {
			call := atomic.AddInt32(&refreshCalls, 1)
			time.Sleep(50 * time.Millisecond)
			if call > 1 {
				http.Error(w, `{"error":"refresh token already used"}`, http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(newToken)
			return
		}
		http.Error(w, `{"error":"not found"}`, 404)
	}))
	defer srv.Close()

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)

	cfgDir := filepath.Join(tmpHome, ".config", "airskills")
	if err := os.MkdirAll(cfgDir, 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	cfgData, _ := json.Marshal(config.Config{APIURL: srv.URL})
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), cfgData, 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	expiredToken := config.TokenData{
		AccessToken:  "old-access-token",
		RefreshToken: "old-refresh-token",
		ExpiresAt:    time.Now().Add(-1 * time.Hour).Unix(),
	}
	tokenData, _ := json.Marshal(expiredToken)
	if err := os.WriteFile(filepath.Join(cfgDir, "token.json"), tokenData, 0600); err != nil {
		t.Fatalf("write token: %v", err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client, err := newAPIClientAuto()
			if err != nil {
				errs <- err
				return
			}
			if client.token != newToken.AccessToken {
				errs <- fmt.Errorf("client token: want %q, got %q", newToken.AccessToken, client.token)
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := atomic.LoadInt32(&refreshCalls); got != 1 {
		t.Fatalf("refresh calls: want 1, got %d", got)
	}
}

// TestSetStandardHeadersSendsCLIVersion verifies every authenticated
// request carries the X-Airskills-CLI-Version header so the server can
// compare it against the hardcoded floor and return 426 if the CLI is
// below it.
func TestSetStandardHeadersSendsCLIVersion(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Airskills-CLI-Version")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"count":0}`))
	}))
	defer srv.Close()

	c := &apiClient{baseURL: srv.URL, token: "t", http: srv.Client()}
	if _, err := c.countSuggestions("", "", ""); err != nil {
		t.Fatalf("countSuggestions: %v", err)
	}
	if got != version {
		t.Errorf("X-Airskills-CLI-Version: want %q, got %q", version, got)
	}
}

// TestRefreshAccessTokenSendsCLIVersion verifies the unauthenticated
// refresh endpoint also carries the version header. A user with an
// expired token AND an obsolete CLI must hit 426 on refresh, not on
// the first authenticated call after — otherwise the auto-update path
// can't trigger.
func TestRefreshAccessTokenSendsCLIVersion(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Airskills-CLI-Version")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(config.TokenData{AccessToken: "x", RefreshToken: "y", ExpiresAt: time.Now().Add(time.Hour).Unix()})
	}))
	defer srv.Close()

	if _, err := refreshAccessToken(srv.URL, "rt"); err != nil {
		t.Fatalf("refreshAccessToken: %v", err)
	}
	if got != version {
		t.Errorf("X-Airskills-CLI-Version on refresh: want %q, got %q", version, got)
	}
}

func TestListPersonalSkillsInSkillsetDoesNotSendPersonalScope(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"skillset":{"slug":"client-work","name":"Client work"},"skills":[]}`))
	}))
	defer srv.Close()

	c := &apiClient{baseURL: srv.URL, token: "t", http: srv.Client()}
	_, resolved, err := c.listPersonalSkillsInSkillset("client-work")
	if err != nil {
		t.Fatalf("listPersonalSkillsInSkillset: %v", err)
	}
	if resolved != "client-work" {
		t.Fatalf("resolved skillset: want %q, got %q", "client-work", resolved)
	}
	if gotPath != "/api/v1/skills" {
		t.Fatalf("path: want /api/v1/skills, got %q", gotPath)
	}
	if gotQuery != "skillset=client-work" {
		t.Fatalf("query: want skillset=client-work, got %q", gotQuery)
	}
}

// TestDoRequestPassThroughOnNon426 verifies the wrapper does not
// interfere with non-426 responses — same status code, same body.
func TestDoRequestPassThroughOnNon426(t *testing.T) {
	for _, status := range []int{200, 201, 400, 404, 500} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				w.Write([]byte("body"))
			}))
			defer srv.Close()

			req, _ := http.NewRequest("GET", srv.URL, nil)
			resp, err := doRequest(srv.Client(), req)
			if err != nil {
				t.Fatalf("doRequest: %v", err)
			}
			if resp.StatusCode != status {
				t.Errorf("status: want %d, got %d", status, resp.StatusCode)
			}
			resp.Body.Close()
		})
	}
}

// TestDoRequest426TriggersUpgradeAndReExec verifies the happy-path
// recovery: the first 426 attempts an auto-update, and on success the
// new binary is re-execed (we stub both to assert the call shape).
func TestDoRequest426TriggersUpgradeAndReExec(t *testing.T) {
	resetUpgradeState(t)
	t.Setenv(reExecGuardEnv, "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUpgradeRequired)
		w.Write([]byte(`{"error":"upgrade required"}`))
	}))
	defer srv.Close()

	var updateCalled, reExecCalled bool
	performUpdateFn = func(currentVersion string, verbose bool, trigger string) (string, error) {
		updateCalled = true
		if trigger != "auto-426" {
			t.Errorf("performUpdate trigger: want %q, got %q", "auto-426", trigger)
		}
		return "9.9.9", nil
	}
	t.Cleanup(func() { performUpdateFn = performUpdate })

	reExecFn = func(execPath string, args []string, env []string) error {
		reExecCalled = true
		// Verify the guard env var is set so the new process won't loop.
		found := false
		for _, kv := range env {
			if kv == reExecGuardEnv+"=1" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("re-exec env missing %s=1", reExecGuardEnv)
		}
		return nil
	}
	t.Cleanup(func() { reExecFn = reExec })

	req, _ := http.NewRequest("GET", srv.URL, nil)
	_, err := doRequest(srv.Client(), req)
	if err != nil {
		t.Fatalf("doRequest after successful update should return nil error (re-exec stubbed), got %v", err)
	}
	if !updateCalled {
		t.Error("performUpdateFn was not called")
	}
	if !reExecCalled {
		t.Error("reExecFn was not called")
	}
}

// TestDoRequest426SecondTimeBailsOut verifies the no-infinite-loop
// guarantee: once an upgrade has been attempted in this process, a
// subsequent 426 returns an error rather than triggering a second
// upgrade.
func TestDoRequest426SecondTimeBailsOut(t *testing.T) {
	resetUpgradeState(t)
	t.Setenv(reExecGuardEnv, "")
	upgradeAttempted.Store(true) // simulate prior attempt

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUpgradeRequired)
	}))
	defer srv.Close()

	performUpdateFn = func(string, bool, string) (string, error) {
		t.Fatal("performUpdateFn must NOT be called on a repeat 426")
		return "", nil
	}
	t.Cleanup(func() { performUpdateFn = performUpdate })

	req, _ := http.NewRequest("GET", srv.URL, nil)
	_, err := doRequest(srv.Client(), req)
	if err == nil {
		t.Fatal("expected error on repeat 426")
	}
	if !strings.Contains(err.Error(), "no longer supported") {
		t.Errorf("error should mention 'no longer supported': %v", err)
	}
	if !strings.Contains(err.Error(), "self-update") {
		t.Errorf("error should mention 'self-update': %v", err)
	}
}

// TestDoRequest426InsideReExecedChildBailsOut verifies that a 426
// reaching a process that itself was spawned by a re-exec (i.e. has
// the guard env set) does not trigger another upgrade — the floor is
// genuinely above the latest published release and the user must wait.
func TestDoRequest426InsideReExecedChildBailsOut(t *testing.T) {
	resetUpgradeState(t)
	t.Setenv(reExecGuardEnv, "1")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUpgradeRequired)
	}))
	defer srv.Close()

	performUpdateFn = func(string, bool, string) (string, error) {
		t.Fatal("performUpdateFn must NOT be called when re-exec guard is set")
		return "", nil
	}
	t.Cleanup(func() { performUpdateFn = performUpdate })

	req, _ := http.NewRequest("GET", srv.URL, nil)
	_, err := doRequest(srv.Client(), req)
	if err == nil {
		t.Fatal("expected error when 426 hits inside re-execed child")
	}
	if !strings.Contains(err.Error(), "still below") {
		t.Errorf("error should mention 'still below the server's minimum': %v", err)
	}
}

// TestDoRequest426UpdateFailureSurfacesError verifies that when
// performUpdate returns an error, the user sees a clear "auto-update
// failed" message pointing at manual self-update, and reExec is never
// called.
func TestDoRequest426UpdateFailureSurfacesError(t *testing.T) {
	resetUpgradeState(t)
	t.Setenv(reExecGuardEnv, "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUpgradeRequired)
	}))
	defer srv.Close()

	performUpdateFn = func(string, bool, string) (string, error) {
		return "", fmt.Errorf("download failed: connection reset")
	}
	t.Cleanup(func() { performUpdateFn = performUpdate })

	reExecFn = func(string, []string, []string) error {
		t.Fatal("reExecFn must NOT be called after a failed update")
		return nil
	}
	t.Cleanup(func() { reExecFn = reExec })

	req, _ := http.NewRequest("GET", srv.URL, nil)
	_, err := doRequest(srv.Client(), req)
	if err == nil {
		t.Fatal("expected error after failed update")
	}
	if !strings.Contains(err.Error(), "auto-update failed") {
		t.Errorf("error should mention 'auto-update failed': %v", err)
	}
}

// TestDoRequest426NoNewerReleaseBailsOut verifies that when the
// upgrade attempt finds we're already on the latest published version
// (performUpdate returns "" with no error), the user gets a "no newer
// release" message rather than a re-exec attempt that would just loop.
func TestDoRequest426NoNewerReleaseBailsOut(t *testing.T) {
	resetUpgradeState(t)
	t.Setenv(reExecGuardEnv, "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUpgradeRequired)
	}))
	defer srv.Close()

	performUpdateFn = func(string, bool, string) (string, error) {
		return "", nil // already on latest
	}
	t.Cleanup(func() { performUpdateFn = performUpdate })

	reExecFn = func(string, []string, []string) error {
		t.Fatal("reExecFn must NOT be called when no newer release exists")
		return nil
	}
	t.Cleanup(func() { reExecFn = reExec })

	req, _ := http.NewRequest("GET", srv.URL, nil)
	_, err := doRequest(srv.Client(), req)
	if err == nil {
		t.Fatal("expected error when no newer release is available")
	}
	if !strings.Contains(err.Error(), "no newer release") {
		t.Errorf("error should mention 'no newer release': %v", err)
	}
}

// resetUpgradeState clears the per-process upgrade state between tests
// so each test starts from a fresh slate. Without this, the first test
// that calls handleUpgradeRequired will set upgradeAttempted=true and
// poison every subsequent test in the package.
func resetUpgradeState(t *testing.T) {
	t.Helper()
	upgradeAttempted.Store(false)
	t.Cleanup(func() { upgradeAttempted.Store(false) })
}
