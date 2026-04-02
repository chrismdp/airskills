package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/chrismdp/airskills/config"
)

// apiSkill represents a skill from the API.
type apiSkill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Version     string   `json:"version"`
	ContentHash string   `json:"content_hash"`
	OrgID       *string  `json:"org_id"`
	ToolFormats []string `json:"tool_formats"`
}

// syncResult represents the response from the sync check endpoint.
type syncResult struct {
	NeedsUpdate int         `json:"needs_update"`
	Skills      []syncSkill `json:"skills"`
}

// syncSkill is a skill entry in the sync check response.
type syncSkill struct {
	ID               string  `json:"id"`
	Name             string  `json:"name"`
	Version          string  `json:"version"`
	InstalledVersion *string `json:"installed_version"`
}

// apiClient wraps authenticated HTTP calls to the airskills API.
type apiClient struct {
	baseURL string
	token   string
	http    *http.Client
}

// newAPIClient creates an API client from config and token.
func newAPIClient(cfg *config.Config, token *config.TokenData) *apiClient {
	return &apiClient{
		baseURL: cfg.APIURL,
		token:   token.AccessToken,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// newAPIClientAuto loads config and token, returning a ready-to-use client.
func newAPIClientAuto() (*apiClient, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}

	token, err := config.LoadToken()
	if err != nil {
		return nil, err
	}
	if token == nil {
		return nil, fmt.Errorf("not logged in — run 'airskills login' first")
	}
	if time.Now().Unix() > token.ExpiresAt {
		return nil, fmt.Errorf("session expired — run 'airskills login'")
	}

	return newAPIClient(cfg, token), nil
}

func (c *apiClient) get(path string) ([]byte, error) {
	req, err := http.NewRequest("GET", c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}
	return body, nil
}

func (c *apiClient) post(path string, payload interface{}) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}
	return body, nil
}

func (c *apiClient) put(path string, payload interface{}) ([]byte, int, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, err
	}

	req, err := http.NewRequest("PUT", c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, err
	}

	return body, resp.StatusCode, nil
}

// listSkills fetches all skills, optionally filtered by scope.
func (c *apiClient) listSkills(scope string) ([]apiSkill, error) {
	path := "/api/v1/skills"
	if scope != "" {
		path += "?scope=" + scope
	}
	body, err := c.get(path)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Skills []apiSkill `json:"skills"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	return resp.Skills, nil
}

// getSkill fetches skill metadata (no content — files are in Storage).
func (c *apiClient) getSkill(id string) (*apiSkill, error) {
	body, err := c.get(fmt.Sprintf("/api/v1/skills/%s", id))
	if err != nil {
		return nil, err
	}
	var skill apiSkill
	if err := json.Unmarshal(body, &skill); err != nil {
		return nil, err
	}
	return &skill, nil
}

// skillCommit represents a commit from the version history endpoint.
type skillCommit struct {
	ID        string   `json:"id"`
	ParentIDs []string `json:"parent_ids"`
	Message   string   `json:"message"`
	CreatedAt string   `json:"created_at"`
	PushedBy  *string  `json:"pushed_by"`
}

// getVersionHistory fetches the commit history for a skill.
func (c *apiClient) getVersionHistory(skillID string) ([]skillCommit, error) {
	body, err := c.get(fmt.Sprintf("/api/v1/skills/%s/versions", skillID))
	if err != nil {
		return nil, err
	}
	var result struct {
		Versions []skillCommit `json:"versions"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	return result.Versions, nil
}

// createSkill creates a skill metadata shell (files uploaded separately via archive).
func (c *apiClient) createSkill(name, description string, tools []string, forkedFrom string) (*apiSkill, error) {
	payload := map[string]interface{}{
		"name":         name,
		"description":  description,
		"tool_formats": tools,
	}
	if forkedFrom != "" {
		payload["forked_from"] = forkedFrom
	}
	body, err := c.post("/api/v1/skills", payload)
	if err != nil {
		return nil, err
	}
	var skill apiSkill
	if err := json.Unmarshal(body, &skill); err != nil {
		return nil, err
	}
	return &skill, nil
}

// putArchive uploads a tar.gz to the archive endpoint (single write path).
func (c *apiClient) putArchive(skillID string, archive []byte, expectedVersion, contentHash string) (*apiSkill, int, error) {
	url := c.baseURL + fmt.Sprintf("/api/v1/skills/%s/archive", skillID)
	req, err := http.NewRequest("PUT", url, bytes.NewReader(archive))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/gzip")
	if expectedVersion != "" {
		req.Header.Set("X-Expected-Version", expectedVersion)
	}
	if contentHash != "" {
		req.Header.Set("X-Content-Hash", contentHash)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}

	var skill apiSkill
	json.Unmarshal(body, &skill)

	if resp.StatusCode >= 400 {
		return &skill, resp.StatusCode, fmt.Errorf("%s", string(body))
	}
	return &skill, resp.StatusCode, nil
}

// recordInstallation records that a skill was installed for a tool.
func (c *apiClient) recordInstallation(skillID, tool, version string) error {
	payload := map[string]string{
		"skill_id":          skillID,
		"tool":              tool,
		"installed_version": version,
	}
	_, err := c.post("/api/v1/installations", payload)
	return err
}

func parseJSON(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}

// syncCheck checks for updates since the given timestamp.
func (c *apiClient) syncCheck(since string) (*syncResult, error) {
	body, err := c.get(fmt.Sprintf("/api/v1/sync?since=%s", since))
	if err != nil {
		return nil, err
	}
	var result syncResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	return &result, nil
}
