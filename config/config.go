package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	DefaultAPIURL = "https://airskills.ai"
	configDir     = "airskills"
	configFile    = "config.json"
	tokenFile     = "token.json"
	tokenLockFile = "token.lock"
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
	path := filepath.Join(dir, tokenFile)
	tmp, err := os.CreateTemp(dir, ".token-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

func WithTokenLock(fn func() error) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	lockPath := filepath.Join(dir, tokenLockFile)
	release, err := acquireTokenLock(lockPath)
	if err != nil {
		return err
	}
	defer release()
	return fn()
}

func acquireTokenLock(path string) (func(), error) {
	deadline := time.Now().Add(10 * time.Second)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
			_ = f.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		if info, statErr := os.Stat(path); statErr == nil && time.Since(info.ModTime()) > 30*time.Second {
			_ = os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for token lock")
		}
		time.Sleep(25 * time.Millisecond)
	}
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
