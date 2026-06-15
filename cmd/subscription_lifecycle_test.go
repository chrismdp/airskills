package cmd

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIsPersonalSubscriptionMarker(t *testing.T) {
	mk := func(mut func(*SyncEntry)) *SyncEntry {
		e := &SyncEntry{
			SkillID:   "up-1",
			OwnerKind: "user",
			Source:    &skillSource{Owner: "alice", Slug: "retro", ID: "up-1", UpstreamSkillID: "up-1"},
		}
		if mut != nil {
			mut(e)
		}
		return e
	}
	cases := []struct {
		name string
		e    *SyncEntry
		want bool
	}{
		{"bare personal subscription", mk(nil), true},
		{"owned (no Source)", mk(func(e *SyncEntry) { e.Source = nil }), false},
		{"org skill", mk(func(e *SyncEntry) { e.OwnerKind = "org" }), false},
		{"org-distributed", mk(func(e *SyncEntry) { e.Source.SkillsetSlug = "team" }), false},
		{"backup-bearing (edited)", mk(func(e *SyncEntry) { e.Backup = &backupRef{SkillID: "b"} }), false},
		{"transfer tombstone", mk(func(e *SyncEntry) { e.Deleted = true }), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		if got := isPersonalSubscriptionMarker(tc.e); got != tc.want {
			t.Errorf("%s: want %v, got %v", tc.name, tc.want, got)
		}
	}
}

// TestClassifySubscriptionUpstream is the heart of the lifecycle: classification
// must be by HTTP STATUS, never by substring. The "blip" case (a 503 whose body
// contains "not found") must NOT be read as gone — that would false-promote on a
// deploy blip.
func TestClassifySubscriptionUpstream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/resolve/alice/readable":
			w.WriteHeader(200)
			fmt.Fprint(w, `{"id":"up-1"}`)
		case "/api/v1/resolve/alice/reused":
			w.WriteHeader(200)
			fmt.Fprint(w, `{"id":"a-different-skill"}`)
		case "/api/v1/resolve/alice/moved":
			w.WriteHeader(410)
			fmt.Fprint(w, `{"moved_to":{"skill_id":"succ-1"}}`)
		case "/api/v1/resolve/alice/gone410":
			w.WriteHeader(410)
			fmt.Fprint(w, `{}`)
		case "/api/v1/resolve/alice/deleted":
			w.WriteHeader(404)
			fmt.Fprint(w, `{"error":"not found"}`)
		case "/api/v1/resolve/alice/blip":
			w.WriteHeader(503)
			fmt.Fprint(w, `{"error":"upstream not found"}`) // 5xx body w/ "not found" — must stay transient
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := &apiClient{baseURL: srv.URL, token: "t", http: srv.Client()}
	src := func(slug string) *skillSource { return &skillSource{Owner: "alice", Slug: slug} }

	cases := []struct {
		slug     string
		wantKind int
		wantSucc string
	}{
		{"readable", subUpstreamReadable, ""},
		{"reused", subUpstreamGone, ""},
		{"moved", subUpstreamMoved, "succ-1"},
		{"gone410", subUpstreamGone, ""},
		{"deleted", subUpstreamGone, ""},
		{"blip", subUpstreamTransient, ""},
	}
	for _, tc := range cases {
		d := classifySubscriptionUpstream(c, src(tc.slug), "up-1")
		if d.kind != tc.wantKind || d.successorID != tc.wantSucc {
			t.Errorf("%s: want kind=%d succ=%q, got kind=%d succ=%q", tc.slug, tc.wantKind, tc.wantSucc, d.kind, d.successorID)
		}
	}
	// No owner/slug to resolve → can't classify → transient (don't act).
	if d := classifySubscriptionUpstream(c, &skillSource{Slug: "x"}, "up-1"); d.kind != subUpstreamTransient {
		t.Errorf("empty owner: want transient, got %d", d.kind)
	}
}

func subMarkerState() *SyncState {
	return &SyncState{Skills: map[string]*SyncEntry{
		"retro": {SkillID: "up-1", OwnerKind: "user", Source: &skillSource{Owner: "alice", Slug: "retro", ID: "up-1", UpstreamSkillID: "up-1"}},
	}}
}

// TestReconcileBackfill: a readable subscription absent from the listing (e.g.
// added anonymously) gets subscribed; one already in the listing is left alone.
func TestReconcileBackfill(t *testing.T) {
	var subscribed []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/resolve/alice/retro":
			w.WriteHeader(200)
			fmt.Fprint(w, `{"id":"up-1"}`)
		case strings.HasSuffix(r.URL.Path, "/subscribe") && r.Method == "POST":
			subscribed = append(subscribed, r.URL.Path)
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := &apiClient{baseURL: srv.URL, token: "t", http: srv.Client()}

	ss := subMarkerState()
	reconcileSubscriptions(c, ss, map[string]bool{}, map[string]string{}, newOwnerResolver(c))
	if len(subscribed) != 1 || subscribed[0] != "/api/v1/skills/up-1/subscribe" {
		t.Fatalf("expected backfill subscribe to up-1, got %v", subscribed)
	}

	subscribed = nil
	reconcileSubscriptions(c, ss, map[string]bool{"up-1": true}, map[string]string{}, newOwnerResolver(c))
	if len(subscribed) != 0 {
		t.Errorf("a live (in-listing) subscription should not be re-subscribed, got %v", subscribed)
	}
}

// TestReconcileTransientSkips: a deploy-blip 503 must produce no writes at all.
func TestReconcileTransientSkips(t *testing.T) {
	var writes int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			writes++
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/resolve/") {
			w.WriteHeader(503)
			fmt.Fprint(w, `{"error":"not found"}`)
			return
		}
		w.WriteHeader(204)
	}))
	defer srv.Close()
	c := &apiClient{baseURL: srv.URL, token: "t", http: srv.Client()}

	reconcileSubscriptions(c, subMarkerState(), map[string]bool{}, map[string]string{}, newOwnerResolver(c))
	if writes != 0 {
		t.Errorf("a transient resolve failure must not subscribe/unsubscribe/promote, got %d writes", writes)
	}
}

// TestReconcileGoneNoFilesDropsRow: a gone upstream with nothing on disk to
// promote drops the dead subscription row (unsubscribe) rather than retrying.
func TestReconcileGoneNoFilesDropsRow(t *testing.T) {
	var deleted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/resolve/"):
			w.WriteHeader(404)
		case strings.HasSuffix(r.URL.Path, "/subscribe") && r.Method == "DELETE":
			deleted = append(deleted, r.URL.Path)
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := &apiClient{baseURL: srv.URL, token: "t", http: srv.Client()}

	reconcileSubscriptions(c, subMarkerState(), map[string]bool{}, map[string]string{}, newOwnerResolver(c))
	if len(deleted) != 1 || deleted[0] != "/api/v1/skills/up-1/subscribe" {
		t.Errorf("expected the dead subscription row to be dropped, got %v", deleted)
	}
}
