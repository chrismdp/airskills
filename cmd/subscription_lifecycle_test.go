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

// anonMarkerState is a marker added while ANONYMOUS: Source set, but no SkillID
// yet (add.go only sets it when logged in). The "registers on first login" path.
func anonMarkerState() *SyncState {
	return &SyncState{Skills: map[string]*SyncEntry{
		"retro": {OwnerKind: "user", Source: &skillSource{Owner: "alice", Slug: "retro", ID: "up-1", UpstreamSkillID: "up-1"}},
	}}
}

// #1 — TestReconcileBackfillsAnonMarker: a marker added while anonymous (no
// SkillID) must backfill-subscribe to its upstream and adopt the id. Before the
// fix, isPersonalSubscriptionMarker required SkillID!="" so this silently no-op'd.
func TestReconcileBackfillsAnonMarker(t *testing.T) {
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

	ss := anonMarkerState()
	reconcileSubscriptions(c, ss, map[string]bool{}, map[string]string{}, newOwnerResolver(c))
	if len(subscribed) != 1 || subscribed[0] != "/api/v1/skills/up-1/subscribe" {
		t.Fatalf("anon marker should backfill-subscribe to up-1, got %v", subscribed)
	}
	if ss.Skills["retro"].SkillID != "up-1" {
		t.Errorf("backfill should adopt the upstream id as SkillID, got %q", ss.Skills["retro"].SkillID)
	}
}

// #2 — TestReconcileRepointUsesMovedToOwner: a transfer to a readable successor
// re-points the marker using the resolve moved_to (which carries the new owner),
// NOT getSkill (which omits owner_username and would blank Source.Owner).
func TestReconcileRepointUsesMovedToOwner(t *testing.T) {
	var subscribed, unsubscribed []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/resolve/"):
			w.WriteHeader(410)
			fmt.Fprint(w, `{"error":"skill transferred","moved_to":{"skill_id":"succ-1","slug":"carol","skill_slug":"retro2","kind":"user"}}`)
		case strings.HasSuffix(r.URL.Path, "/subscribe") && r.Method == "POST":
			subscribed = append(subscribed, r.URL.Path)
			w.WriteHeader(204)
		case strings.HasSuffix(r.URL.Path, "/subscribe") && r.Method == "DELETE":
			unsubscribed = append(unsubscribed, r.URL.Path)
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := &apiClient{baseURL: srv.URL, token: "t", http: srv.Client()}

	ss := subMarkerState()
	reconcileSubscriptions(c, ss, map[string]bool{}, map[string]string{}, newOwnerResolver(c))

	m := ss.Skills["retro"]
	if m.SkillID != "succ-1" {
		t.Errorf("re-point should track successor succ-1, got %q", m.SkillID)
	}
	if m.Source.Owner != "carol" {
		t.Errorf("re-point should set Source.Owner from moved_to (carol), got %q — the empty-owner bug", m.Source.Owner)
	}
	if m.Source.Slug != "retro2" {
		t.Errorf("re-point Source.Slug = %q, want retro2", m.Source.Slug)
	}
	if len(subscribed) != 1 || subscribed[0] != "/api/v1/skills/succ-1/subscribe" {
		t.Errorf("should subscribe to successor, got %v", subscribed)
	}
	if len(unsubscribed) != 1 || unsubscribed[0] != "/api/v1/skills/up-1/subscribe" {
		t.Errorf("should unsubscribe the old id, got %v", unsubscribed)
	}
}

// #2 — TestReconcileBare410Orphans: a transfer the caller CANNOT read returns a
// bare 410 (no moved_to) → Gone → promote/orphan, never a silent re-point.
func TestReconcileBare410Orphans(t *testing.T) {
	var deleted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/resolve/"):
			w.WriteHeader(410)
			fmt.Fprint(w, `{"error":"skill transferred"}`) // no moved_to — successor unreadable
		case strings.HasSuffix(r.URL.Path, "/subscribe") && r.Method == "DELETE":
			deleted = append(deleted, r.URL.Path)
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := &apiClient{baseURL: srv.URL, token: "t", http: srv.Client()}

	// No local files → promote drops the row (orphan).
	reconcileSubscriptions(c, subMarkerState(), map[string]bool{}, map[string]string{}, newOwnerResolver(c))
	if len(deleted) != 1 {
		t.Errorf("a bare 410 (unreadable successor) should orphan, not re-point; got deletes %v", deleted)
	}
}

// #4 — TestClassify200UnparseableIsTransient: a 200 we can't parse is NOT proof
// the skill is gone; it must be transient (never promote on a CDN/HTML 200).
func TestClassify200UnparseableIsTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/resolve/alice/htmlbody":
			w.WriteHeader(200)
			fmt.Fprint(w, `<html>maintenance</html>`)
		case "/api/v1/resolve/alice/emptyid":
			w.WriteHeader(200)
			fmt.Fprint(w, `{}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := &apiClient{baseURL: srv.URL, token: "t", http: srv.Client()}

	for _, slug := range []string{"htmlbody", "emptyid"} {
		if d := classifySubscriptionUpstream(c, &skillSource{Owner: "alice", Slug: slug}, "up-1"); d.kind != subUpstreamTransient {
			t.Errorf("200 %q: want transient (not gone/promote), got kind=%d", slug, d.kind)
		}
	}
}

// #5 — TestReconcileSubscribe500NoPromote: a non-403 subscribe failure must NOT
// promote (it's not definitive); it's logged and retried next sync.
func TestReconcileSubscribe500NoPromote(t *testing.T) {
	var promoted bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/resolve/alice/retro":
			w.WriteHeader(200)
			fmt.Fprint(w, `{"id":"up-1"}`)
		case strings.HasSuffix(r.URL.Path, "/subscribe") && r.Method == "POST":
			w.WriteHeader(500) // transient, NOT 403
		case strings.HasSuffix(r.URL.Path, "/promote"):
			promoted = true
			w.WriteHeader(201)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := &apiClient{baseURL: srv.URL, token: "t", http: srv.Client()}

	reconcileSubscriptions(c, subMarkerState(), map[string]bool{}, map[string]string{}, newOwnerResolver(c))
	if promoted {
		t.Errorf("a 500 subscribe error must not promote a live subscription")
	}
}

// #1/#6 — TestSubscriptionPredicates: the reconcile predicate includes anon
// (no SkillID); the rm predicate includes edited (Backup) subs but excludes org.
func TestSubscriptionPredicates(t *testing.T) {
	sub := func(mut func(*SyncEntry)) *SyncEntry {
		e := &SyncEntry{SkillID: "up-1", OwnerKind: "user", Source: &skillSource{Slug: "retro", ID: "up-1", UpstreamSkillID: "up-1"}}
		if mut != nil {
			mut(e)
		}
		return e
	}
	recon := []struct {
		name string
		e    *SyncEntry
		want bool
	}{
		{"established", sub(nil), true},
		{"anon (no SkillID)", sub(func(e *SyncEntry) { e.SkillID = "" }), true},
		{"org", sub(func(e *SyncEntry) { e.OwnerKind = "org" }), false},
		{"backup-bearing (sweep handles)", sub(func(e *SyncEntry) { e.Backup = &backupRef{SkillID: "b"} }), false},
		{"mismatched id", sub(func(e *SyncEntry) { e.SkillID = "other" }), false},
	}
	for _, tc := range recon {
		if got := isReconcilableSubscription(tc.e); got != tc.want {
			t.Errorf("isReconcilableSubscription %s: want %v got %v", tc.name, tc.want, got)
		}
	}
	rm := []struct {
		name string
		e    *SyncEntry
		want bool
	}{
		{"personal", sub(nil), true},
		{"edited personal (Backup)", sub(func(e *SyncEntry) { e.Backup = &backupRef{SkillID: "b"} }), true},
		{"org overlay", sub(func(e *SyncEntry) { e.OwnerKind = "org" }), false},
		{"org-distributed", sub(func(e *SyncEntry) { e.Source.SkillsetSlug = "team" }), false},
		{"nil", nil, false},
	}
	for _, tc := range rm {
		if got := isPersonalSubscription(tc.e); got != tc.want {
			t.Errorf("isPersonalSubscription %s: want %v got %v", tc.name, tc.want, got)
		}
	}
}
