package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newShareTestClient builds an apiClient pointed at a test server. The
// fields are unexported but in-package, so tests construct the client
// directly and skip the on-disk config/token that newAPIClientAuto loads.
func newShareTestClient(baseURL string) *apiClient {
	return &apiClient{
		baseURL: baseURL,
		token:   "test-token",
		http:    &http.Client{Timeout: 5 * time.Second},
	}
}

func TestParseShareRef(t *testing.T) {
	cases := []struct {
		name     string
		ref      string
		email    string
		wantUser string
		wantSlug string
		wantErr  string // substring; "" means expect success
	}{
		{name: "valid", ref: "alice/foo", email: "bob@example.com", wantUser: "alice", wantSlug: "foo"},
		{name: "missing email", ref: "alice/foo", email: "", wantErr: "--with is required"},
		{name: "no slash", ref: "foo", email: "bob@example.com", wantErr: "expected format"},
		{name: "empty slug", ref: "alice/", email: "bob@example.com", wantErr: "expected format"},
		{name: "empty owner", ref: "/foo", email: "bob@example.com", wantErr: "expected format"},
		{name: "bare slash", ref: "/", email: "bob@example.com", wantErr: "expected format"},
		// SplitN(_, 2) keeps any trailing slashes in the slug — the server
		// resolve is the authority on whether that slug exists, not the CLI.
		{name: "extra segment", ref: "alice/foo/bar", email: "bob@example.com", wantUser: "alice", wantSlug: "foo/bar"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			user, slug, err := parseShareRef(tc.ref, tc.email)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("parseShareRef(%q, %q) = nil error, want %q", tc.ref, tc.email, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %q, want substring %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseShareRef(%q, %q) unexpected error: %v", tc.ref, tc.email, err)
			}
			if user != tc.wantUser || slug != tc.wantSlug {
				t.Fatalf("parseShareRef = (%q, %q), want (%q, %q)", user, slug, tc.wantUser, tc.wantSlug)
			}
		})
	}
}

// TestShareSkillHappyPath drives the full resolve→share sequence for a
// plain skill and asserts the share POST lands on the skill endpoint
// carrying the target email.
func TestShareSkillHappyPath(t *testing.T) {
	const skillID = "11111111-1111-1111-1111-111111111111"
	var sharePath, shareMethod, shareEmail string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/resolve/alice/foo":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"type":"skill","id":"` + skillID + `"}`))
		case strings.HasSuffix(r.URL.Path, "/share"):
			sharePath = r.URL.Path
			shareMethod = r.Method
			body, _ := io.ReadAll(r.Body)
			var payload map[string]string
			json.Unmarshal(body, &payload)
			shareEmail = payload["email"]
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	err := shareSkill(newShareTestClient(srv.URL), "alice", "foo", "bob@example.com")
	if err != nil {
		t.Fatalf("shareSkill: %v", err)
	}
	if want := "/api/v1/skills/" + skillID + "/share"; sharePath != want {
		t.Errorf("share path = %q, want %q", sharePath, want)
	}
	if shareMethod != http.MethodPost {
		t.Errorf("share method = %q, want POST", shareMethod)
	}
	if shareEmail != "bob@example.com" {
		t.Errorf("share email = %q, want bob@example.com", shareEmail)
	}
}

// TestShareBundleRoutesToBundleEndpoint pins the type-based routing branch:
// a resolved type of "bundle" must hit /bundles/, not /skills/.
func TestShareBundleRoutesToBundleEndpoint(t *testing.T) {
	const bundleID = "22222222-2222-2222-2222-222222222222"
	var sharePath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/resolve/alice/mybundle":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"type":"bundle","id":"` + bundleID + `"}`))
		case strings.HasSuffix(r.URL.Path, "/share"):
			sharePath = r.URL.Path
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	err := shareSkill(newShareTestClient(srv.URL), "alice", "mybundle", "bob@example.com")
	if err != nil {
		t.Fatalf("shareSkill: %v", err)
	}
	if want := "/api/v1/bundles/" + bundleID + "/share"; sharePath != want {
		t.Errorf("share path = %q, want %q", sharePath, want)
	}
}

// TestShareResolveNotFound covers the resolve failure path: a 404 from the
// resolve endpoint must surface as "skill not found", and no share POST
// must be attempted.
func TestShareResolveNotFound(t *testing.T) {
	shareCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/share") {
			shareCalled = true
		}
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	err := shareSkill(newShareTestClient(srv.URL), "alice", "ghost", "bob@example.com")
	if err == nil {
		t.Fatal("shareSkill: expected error for unresolved skill, got nil")
	}
	if !strings.Contains(err.Error(), "skill not found") {
		t.Errorf("error = %q, want substring %q", err.Error(), "skill not found")
	}
	if shareCalled {
		t.Error("share endpoint was called despite resolve failure")
	}
}

// TestShareEndpointFailure covers the share POST failure path: resolve
// succeeds but the share endpoint rejects (e.g. 403), which must surface
// as "failed to share".
func TestShareEndpointFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/resolve/alice/foo":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"type":"skill","id":"33333333-3333-3333-3333-333333333333"}`))
		case strings.HasSuffix(r.URL.Path, "/share"):
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	err := shareSkill(newShareTestClient(srv.URL), "alice", "foo", "bob@example.com")
	if err == nil {
		t.Fatal("shareSkill: expected error when share endpoint rejects, got nil")
	}
	if !strings.Contains(err.Error(), "failed to share") {
		t.Errorf("error = %q, want substring %q", err.Error(), "failed to share")
	}
}

// TestShareMalformedResolveResponse covers the parseJSON branch: a 200 with
// a non-JSON body must error rather than silently sharing a zero-value id.
func TestShareMalformedResolveResponse(t *testing.T) {
	shareCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/resolve/"):
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`not json`))
		case strings.HasSuffix(r.URL.Path, "/share"):
			shareCalled = true
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	err := shareSkill(newShareTestClient(srv.URL), "alice", "foo", "bob@example.com")
	if err == nil {
		t.Fatal("shareSkill: expected error for malformed resolve response, got nil")
	}
	if shareCalled {
		t.Error("share endpoint was called despite an unparseable resolve response")
	}
}

// TestShareCmdRequiresWithFlag verifies the command wiring short-circuits on
// the missing flag before constructing a client or touching the network.
func TestShareCmdRequiresWithFlag(t *testing.T) {
	prev := shareWith
	shareWith = ""
	t.Cleanup(func() { shareWith = prev })

	err := shareCmd.RunE(shareCmd, []string{"alice/foo"})
	if err == nil {
		t.Fatal("shareCmd.RunE: expected error when --with is empty, got nil")
	}
	if !strings.Contains(err.Error(), "--with is required") {
		t.Errorf("error = %q, want substring %q", err.Error(), "--with is required")
	}
}
