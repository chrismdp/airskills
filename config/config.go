package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const (
	DefaultAPIURL = "https://airskills.ai"
	configDir     = "airskills"
	configFile         = "config.json"
	tokenFile          = "token.json"
)

type Config struct {
	APIURL   string `json:"api_url"`
	Skillset string `json:"skillset,omitempty"`
}

type TokenData struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"`
}

func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".config", configDir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}

func Load() (*Config, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}

	path := filepath.Join(dir, configFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{APIURL: DefaultAPIURL}, nil
		}
		return nil, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.APIURL == "" {
		cfg.APIURL = DefaultAPIURL
	}
	return &cfg, nil
}

func (c *Config) Save() error {
	dir, err := Dir()
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, configFile), data, 0600)
}

func LoadToken() (*TokenData, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}

	path := filepath.Join(dir, tokenFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var token TokenData
	if err := json.Unmarshal(data, &token); err != nil {
		return nil, err
	}
	return &token, nil
}

func SaveToken(token *TokenData) error {
	dir, err := Dir()
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(token, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, tokenFile), data, 0600)
}

func ClearToken() error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, tokenFile)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
