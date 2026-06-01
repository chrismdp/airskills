package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chrismdp/airskills/config"
)

func TestListStateLabel(t *testing.T) {
	cases := []struct {
		name string
		info SkillStateInfo
		want string
	}{
		{"clean tracked", SkillStateInfo{State: StateTracked}, "synced"},
		{"local edits", SkillStateInfo{State: StateTracked, LocalDirty: true}, "modified"},
		{"fork upstream moved", SkillStateInfo{State: StateTracked, Sourced: true, UpstreamMoved: true}, "modified*"},
		{"fork upstream moved + local edits", SkillStateInfo{State: StateTracked, Sourced: true, LocalDirty: true, UpstreamMoved: true}, "modified*"},
		// remoteMoved alone ("behind") is not surfaced in list — stays synced,
		// matching the pre-refactor column which only compared local vs marker.
		{"remote moved only", SkillStateInfo{State: StateTracked, RemoteMoved: true}, "synced"},
		{"untracked", SkillStateInfo{State: StateUntracked}, "untracked"},
		{"adoptable", SkillStateInfo{State: StateAdoptable}, "untracked"},
		{"conflict", SkillStateInfo{State: StateConflict}, "untracked"},
		{"available", SkillStateInfo{State: StateAvailable}, "—"},
	}
	for _, c := range cases {
		got := listStateLabel(c.info)
		if got != c.want {
			t.Errorf("%s: listStateLabel = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestListStateLabelUnknownDefaultsToDash(t *testing.T) {
	if got := listStateLabel(SkillStateInfo{State: SkillState("garbage")}); got != "—" {
		t.Errorf("expected dash for unknown state, got %q", got)
	}
}

func TestListDefaultUsesEffectiveSkillsetListing(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/skills" {
			http.NotFound(w, r)
			return
		}
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"skillset":{"slug":"default","name":"Default"},"skills":[]}`))
	}))
	defer srv.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cfgDir := filepath.Join(home, ".config", "airskills")
	if err := os.MkdirAll(cfgDir, 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	cfgData, _ := json.Marshal(config.Config{APIURL: srv.URL})
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), cfgData, 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	tokenData, _ := json.Marshal(config.TokenData{
		AccessToken:  "x",
		RefreshToken: "y",
		ExpiresAt:    time.Now().Add(time.Hour).Unix(),
	})
	if err := os.WriteFile(filepath.Join(cfgDir, "token.json"), tokenData, 0600); err != nil {
		t.Fatalf("write token: %v", err)
	}

	cmd := *listCmd
	cmd.ResetFlags()
	cmd.Flags().String("scope", "", "")
	cmd.Flags().Bool("deleted", false, "")

	_ = captureStdout(t, func() {
		if err := cmd.RunE(&cmd, nil); err != nil {
			t.Fatalf("list: %v", err)
		}
	})
	if gotQuery == "scope=personal" {
		t.Fatalf("list default sent ownership scope; want effective skillset query")
	}
}

func TestListIgnoresStaleRememberedSkillsetFromConfig(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/skills" {
			http.NotFound(w, r)
			return
		}
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"skillset":{"slug":"poppins","name":"Poppins"},"skills":[]}`))
	}))
	defer srv.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cfgDir := filepath.Join(home, ".config", "airskills")
	if err := os.MkdirAll(cfgDir, 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	cfgData, _ := json.Marshal(config.Config{APIURL: srv.URL, Skillset: "poppins"})
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), cfgData, 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	tokenData, _ := json.Marshal(config.TokenData{
		AccessToken:  "x",
		RefreshToken: "y",
		ExpiresAt:    time.Now().Add(time.Hour).Unix(),
	})
	if err := os.WriteFile(filepath.Join(cfgDir, "token.json"), tokenData, 0600); err != nil {
		t.Fatalf("write token: %v", err)
	}

	cmd := *listCmd
	cmd.ResetFlags()
	cmd.Flags().String("scope", "", "")
	cmd.Flags().Bool("deleted", false, "")

	_ = captureStdout(t, func() {
		if err := cmd.RunE(&cmd, nil); err != nil {
			t.Fatalf("list: %v", err)
		}
	})
	if gotQuery != "" {
		t.Fatalf("list sent stale remembered skillset; gotQuery = %q, want default query", gotQuery)
	}
}
