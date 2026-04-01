package config

import "testing"

func TestLoadDefaultConfig(t *testing.T) {
	// With no config file, should return defaults
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.APIURL != DefaultAPIURL {
		t.Errorf("APIURL = %q, want %q", cfg.APIURL, DefaultAPIURL)
	}
}

func TestSaveAndLoadToken(t *testing.T) {
	token := &TokenData{
		AccessToken:  "test-token",
		RefreshToken: "test-refresh",
		ExpiresAt:    9999999999,
	}

	if err := SaveToken(token); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}

	loaded, err := LoadToken()
	if err != nil {
		t.Fatalf("LoadToken: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadToken returned nil")
	}
	if loaded.AccessToken != "test-token" {
		t.Errorf("AccessToken = %q, want %q", loaded.AccessToken, "test-token")
	}

	// Clean up
	ClearToken()
	cleared, _ := LoadToken()
	if cleared != nil {
		t.Error("ClearToken should result in nil token")
	}
}
