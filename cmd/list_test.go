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
		state SkillState
		want  string
	}{
		{StateSynced, "synced"},
		{StateModified, "modified"},
		{StateModifiedPending, "modified*"},
		{StateUntracked, "untracked"},
		{StateLinked, "untracked"},
		{StateUntrackedConflict, "untracked"},
		{StateNotLocal, "—"},
	}
	for _, c := range cases {
		got := listStateLabel(c.state)
		if got != c.want {
			t.Errorf("listStateLabel(%s) = %q, want %q", c.state, got, c.want)
		}
	}
}

func TestListStateLabelUnknownDefaultsToDash(t *testing.T) {
	if got := listStateLabel(SkillState("garbage")); got != "—" {
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

// Regression: `airskills skillset use poppins` writes cfg.Skillset
// locally, but `airskills list` was sending an empty slug and so the
// server resolved to the caller's is_default skillset — reporting the
// wrong skillset's skills. The remembered slug must flow through.
func TestListUsesRememberedSkillsetFromConfig(t *testing.T) {
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
	if gotQuery != "skillset=poppins" {
		t.Fatalf("list ignored remembered skillset; gotQuery = %q, want skillset=poppins", gotQuery)
	}
}
