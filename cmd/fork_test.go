package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// `airskills fork --as` creates a visible, independent personal skill: a
// real fork row (forked_from lineage, NO backup flag), installed locally
// under the new name with the SKILL.md name rewritten, and a marker that
// tracks the NEW skill as identity (Source kept for upstream awareness,
// no Backup, no suggest loop).
func TestForkCreatesVisibleIndependentSkill(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	oldIsTTY := isTTY
	isTTY = false
	t.Cleanup(func() { isTTY = oldIsTTY })

	upstreamID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaa01"
	forkID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaa02"

	var createBody map[string]interface{}
	var archivePuts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/resolve/upstream-org/home":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"type":"skill","id":%q,"slug":"home","version":"1.4.0","content":"---\nname: home\ndescription: d\n---\n\nBody.\n","content_hash":"upstream-hash"}`, upstreamID)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/skills/"+upstreamID+"/archive":
			http.Error(w, "no archive", 404) // falls back to resolve content
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/skills":
			_ = json.NewDecoder(r.Body).Decode(&createBody)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":%q,"name":"my-home","slug":"my-home","version":"1.4.0","content_hash":""}`, forkID)
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/skills/"+forkID+"/archive":
			archivePuts++
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":%q,"slug":"my-home","name":"my-home","version":"1.4.1","content_hash":"fork-hash","head_commit_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaa09"}`, forkID)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/me":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"00000000-0000-0000-0000-000000000099","username":"callerslug"}`)
		default:
			t.Logf("unexpected request: %s %s", r.Method, r.URL.String())
			http.Error(w, "not handled", 404)
		}
	}))
	defer srv.Close()

	writeTestConfigAndToken(t, home, srv.URL)

	forkAsName = "my-home"
	t.Cleanup(func() { forkAsName = "" })
	_ = captureStdout(t, func() {
		if err := forkCmd.RunE(forkCmd, []string{"upstream-org/home"}); err != nil {
			t.Fatalf("fork: %v", err)
		}
	})

	if createBody["forked_from"] != upstreamID {
		t.Errorf("forked_from = %v, want %q", createBody["forked_from"], upstreamID)
	}
	if _, hasBackup := createBody["backup"]; hasBackup {
		t.Error("an explicit fork must NOT carry the backup flag — it is a visible skill")
	}
	if archivePuts != 1 {
		t.Errorf("archive PUTs = %d, want 1 (name-rewritten bytes pushed to the fork)", archivePuts)
	}

	// Local install under the new name with the name field rewritten.
	skillMd, err := os.ReadFile(filepath.Join(home, ".claude", "skills", "my-home", "SKILL.md"))
	if err != nil {
		t.Fatalf("fork not installed locally: %v", err)
	}
	if want := "name: my-home"; !containsLine(string(skillMd), want) {
		t.Errorf("SKILL.md name not rewritten to the fork slug:\n%s", skillMd)
	}

	entry := loadSyncState().Skills["my-home"]
	if entry == nil {
		t.Fatal("marker missing after fork")
	}
	if entry.SkillID != forkID {
		t.Errorf("marker must track the FORK as identity, got %q", entry.SkillID)
	}
	if entry.Backup != nil {
		t.Errorf("explicit fork must have no Backup ref, got %+v", entry.Backup)
	}
	if entry.Source == nil || entry.Source.ID != upstreamID {
		t.Errorf("Source must record the upstream for awareness, got %+v", entry.Source)
	}
	if entry.OwnerKind != "user" || entry.OwnerSlug != "callerslug" {
		t.Errorf("owner = %q/%q, want user/callerslug", entry.OwnerKind, entry.OwnerSlug)
	}
}

func containsLine(text, line string) bool {
	for _, l := range splitLines(text) {
		if l == line {
			return true
		}
	}
	return false
}

func splitLines(text string) []string {
	var out []string
	start := 0
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			out = append(out, text[start:i])
			start = i + 1
		}
	}
	return append(out, text[start:])
}
