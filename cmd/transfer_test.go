package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestLookupCallerOrgIDMultiOrg verifies that lookupCallerOrgID works correctly
// for users who belong to multiple orgs, using the /api/v1/organizations endpoint.
func TestLookupCallerOrgIDMultiOrg(t *testing.T) {
	// Mock server only handles /api/v1/organizations (the multi-org endpoint).
	// Old code that calls /api/v1/organization will receive a 404 and fail.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/organizations" {
			http.Error(w, `{"error":"not found"}`, 404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"organizations": []map[string]interface{}{
				{"id": "org-abc", "slug": "cherrypick"},
				{"id": "org-xyz", "slug": "loomery"},
			},
		})
	}))
	defer srv.Close()

	client := &apiClient{baseURL: srv.URL, token: "test-token", http: srv.Client()}

	// Success: first org found.
	id, err := lookupCallerOrgID(client, "cherrypick")
	if err != nil {
		t.Fatalf("unexpected error for cherrypick: %v", err)
	}
	if id != "org-abc" {
		t.Errorf("cherrypick: expected org-abc, got %s", id)
	}

	// Success: second org found — validates multi-org support.
	id, err = lookupCallerOrgID(client, "loomery")
	if err != nil {
		t.Fatalf("unexpected error for loomery: %v", err)
	}
	if id != "org-xyz" {
		t.Errorf("loomery: expected org-xyz, got %s", id)
	}

	// Error: slug not in the list.
	_, err = lookupCallerOrgID(client, "unknown-org")
	if err == nil {
		t.Fatal("expected error for unknown org, got nil")
	}
}

// TestLookupCallerOrgIDNotMember verifies the error message when the user
// belongs to no orgs at all.
func TestLookupCallerOrgIDNotMember(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/organizations" {
			http.Error(w, `{"error":"not found"}`, 404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"organizations": []map[string]interface{}{},
		})
	}))
	defer srv.Close()

	client := &apiClient{baseURL: srv.URL, token: "test-token", http: srv.Client()}

	_, err := lookupCallerOrgID(client, "any-org")
	if err == nil {
		t.Fatal("expected error when not a member of any org")
	}
}
