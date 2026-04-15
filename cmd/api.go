package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/chrismdp/airskills/config"
	"github.com/chrismdp/airskills/telemetry"
)

// setAnonHeader attaches the machine-level anonymous telemetry ID so the
// server can attribute anonymous events (e.g. `airskills add` without login)
// to a stable identity across sessions.
func setAnonHeader(req *http.Request) {
	if id := telemetry.AnonymousID(); id != "" {
		req.Header.Set("X-Airskills-Anon-ID", id)
	}
}

// apiSkill represents a skill from the API.
//
// OwnerID is a pointer because skills can be owned by an org instead of a
// user — in that case owner_id is null and org_id is set.
//
// CurrentOwner is set on push responses so the CLI can detect server-side
// transfers and keep the local marker in sync. The server never returns
// "previous owner" — that would let attackers probe transfer history by
// spoofing markers.
type apiSkill struct {
	ID                  string          `json:"id"`
	OwnerID             *string         `json:"owner_id"`
	Slug                string          `json:"slug"`
	Name                string          `json:"name"`
	Description         string          `json:"description"`
	Version             string          `json:"version"`
	ContentHash         string          `json:"content_hash"`
	OrgID               *string         `json:"org_id"`
	ForkedFrom          *string         `json:"forked_from"`
	Visibility          string          `json:"visibility"`
	ToolFormats         []string        `json:"tool_formats"`
	Warning             string          `json:"warning,omitempty"`
	UpstreamContentHash *string         `json:"upstream_content_hash"`
	CurrentOwner        *ownerNamespace `json:"current_owner,omitempty"`
	DeletedAt           *string         `json:"deleted_at,omitempty"`
	DeletionReason      *string         `json:"deletion_reason,omitempty"`
}

// ownerNamespace identifies a skill's owner — either a user (kind="user",
// slug=username) or an org (kind="org", slug=org_slug).
type ownerNamespace struct {
	Kind string `json:"kind"`
	Slug string `json:"slug"`
}

// HasUpstreamUpdate returns true if this is a forked skill whose parent has changed.
func (s *apiSkill) HasUpstreamUpdate() bool {
	return s.ForkedFrom != nil && s.UpstreamContentHash != nil &&
		s.ContentHash != "" && *s.UpstreamContentHash != "" &&
		s.ContentHash != *s.UpstreamContentHash
}

// pullUpstream tells the server to advance this skill's pin to the parent's latest.
func (c *apiClient) pullUpstream(skillID string) (*apiSkill, error) {
	body, statusCode, err := c.put(fmt.Sprintf("/api/v1/skills/%s", skillID), map[string]interface{}{
		"pull_upstream": true,
	})
	if err != nil {
		return nil, err
	}
	if statusCode >= 400 {
		return nil, fmt.Errorf("API error (%d): %s", statusCode, string(body))
	}
	var skill apiSkill
	if err := json.Unmarshal(body, &skill); err != nil {
		return nil, err
	}
	return &skill, nil
}

// apiProfile represents the current user's profile from /api/v1/me.
type apiProfile struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
}

// getMe fetches the current user's profile.
func (c *apiClient) getMe() (*apiProfile, error) {
	body, err := c.get("/api/v1/me")
	if err != nil {
		return nil, err
	}
	var profile apiProfile
	if err := json.Unmarshal(body, &profile); err != nil {
		return nil, err
	}
	return &profile, nil
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
	ContentHash      string  `json:"content_hash"`
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

	// Auto-refresh expired tokens
	if time.Now().Unix() > token.ExpiresAt {
		if token.RefreshToken == "" {
			return nil, fmt.Errorf("session expired — run 'airskills login'")
		}
		refreshed, err := refreshAccessToken(cfg.APIURL, token.RefreshToken)
		if err != nil {
			return nil, fmt.Errorf("session expired and refresh failed — run 'airskills login'")
		}
		token = refreshed
		config.SaveToken(token)
	}

	return newAPIClient(cfg, token), nil
}

// refreshAccessToken exchanges a refresh token for a new access token via the platform API.
func refreshAccessToken(apiURL, refreshToken string) (*config.TokenData, error) {
	payload, _ := json.Marshal(map[string]string{"refresh_token": refreshToken})
	resp, err := http.Post(apiURL+"/api/v1/auth/refresh", "application/json", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("refresh returned %d", resp.StatusCode)
	}

	var token config.TokenData
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return nil, err
	}
	return &token, nil
}

func (c *apiClient) get(path string) ([]byte, error) {
	req, err := http.NewRequest("GET", c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	setAnonHeader(req)

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
	setAnonHeader(req)

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
	setAnonHeader(req)

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

func (c *apiClient) del(path string) error {
	req, err := http.NewRequest("DELETE", c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	setAnonHeader(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}
	return nil
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

// listDeletedSkills fetches soft-deleted skills owned by the caller.
func (c *apiClient) listDeletedSkills() ([]apiSkill, error) {
	body, err := c.get("/api/v1/skills?deleted=true")
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
// orgID is optional — non-empty creates the skill under the given org (caller must be admin/owner).
func (c *apiClient) createSkill(name, description string, tools []string, forkedFrom, orgID string) (*apiSkill, error) {
	payload := map[string]interface{}{
		"name":         name,
		"description":  description,
		"tool_formats": tools,
	}
	if forkedFrom != "" {
		payload["forked_from"] = forkedFrom
	}
	if orgID != "" {
		payload["org_id"] = orgID
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

// createSkillWithGitHub creates a skill on the server with GitHub provenance.
func (c *apiClient) createSkillWithGitHub(name, githubURL, githubSkill string) (*apiSkill, error) {
	payload := map[string]interface{}{
		"name":         name,
		"tool_formats": []string{"claude-code"},
		"github_url":   githubURL,
	}
	if githubSkill != "" {
		payload["github_skill"] = githubSkill
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
func (c *apiClient) putArchive(skillID string, archive []byte, expectedHash, contentHash string) (*apiSkill, int, error) {
	url := c.baseURL + fmt.Sprintf("/api/v1/skills/%s/archive", skillID)
	req, err := http.NewRequest("PUT", url, bytes.NewReader(archive))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/gzip")
	setAnonHeader(req)
	if expectedHash != "" {
		req.Header.Set("X-Expected-Hash", expectedHash)
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

type apiSuggestion struct {
	ID                 string  `json:"id"`
	SuggesterSkillID   string  `json:"suggester_skill_id"`
	OwnerSkillID       string  `json:"owner_skill_id"`
	SuggesterID        string  `json:"suggester_id"`
	OwnerID            string  `json:"owner_id"`
	BaseContentHash    string  `json:"base_content_hash"`
	Message            string  `json:"message"`
	Status             string  `json:"status"`
	ResponseMessage    string  `json:"response_message"`
	ReviewedAt         *string `json:"reviewed_at"`
	CreatedAt          string  `json:"created_at"`
	UpdatedAt          string  `json:"updated_at"`
	SuggesterUsername  string  `json:"suggester_username,omitempty"`
	SuggesterSkillName string  `json:"suggester_skill_name,omitempty"`
	OwnerSkillName     string  `json:"owner_skill_name,omitempty"`
	OwnerSkillSlug     string  `json:"owner_skill_slug,omitempty"`
}

func (c *apiClient) createSuggestion(suggesterSkillID, ownerSkillID, baseContentHash, message string) (*apiSuggestion, error) {
	payload := map[string]string{
		"suggester_skill_id": suggesterSkillID,
		"owner_skill_id":     ownerSkillID,
		"base_content_hash":  baseContentHash,
		"message":            message,
	}
	body, err := c.post("/api/v1/suggestions", payload)
	if err != nil {
		return nil, err
	}
	var s apiSuggestion
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func (c *apiClient) listSuggestions(role, status, skillID string) ([]apiSuggestion, error) {
	body, err := c.get(suggestionsPath(role, status, skillID, false))
	if err != nil {
		return nil, err
	}
	var resp struct {
		Suggestions []apiSuggestion `json:"suggestions"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	return resp.Suggestions, nil
}

// countSuggestions hits the count-only fast path — used by `airskills status`
// so the shell prompt doesn't pay for enrichment just to render a number.
func (c *apiClient) countSuggestions(role, status, skillID string) (int, error) {
	body, err := c.get(suggestionsPath(role, status, skillID, true))
	if err != nil {
		return 0, err
	}
	var resp struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0, err
	}
	return resp.Count, nil
}

func suggestionsPath(role, status, skillID string, countOnly bool) string {
	params := []string{}
	if role != "" {
		params = append(params, "role="+role)
	}
	if status != "" {
		params = append(params, "status="+status)
	}
	if skillID != "" {
		params = append(params, "skill_id="+skillID)
	}
	if countOnly {
		params = append(params, "count=1")
	}
	if len(params) == 0 {
		return "/api/v1/suggestions"
	}
	return "/api/v1/suggestions?" + strings.Join(params, "&")
}

func (c *apiClient) getSuggestion(id string) (*apiSuggestion, error) {
	body, err := c.get(fmt.Sprintf("/api/v1/suggestions/%s", id))
	if err != nil {
		return nil, err
	}
	var s apiSuggestion
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Server RLS enforces that only the owner of the referenced skill can update.
func (c *apiClient) updateSuggestion(id, status, responseMessage string) (*apiSuggestion, error) {
	payload := map[string]string{
		"status":           status,
		"response_message": responseMessage,
	}
	body, statusCode, err := c.put(fmt.Sprintf("/api/v1/suggestions/%s", id), payload)
	if err != nil {
		return nil, err
	}
	if statusCode >= 400 {
		return nil, fmt.Errorf("API error (%d): %s", statusCode, string(body))
	}
	var s apiSuggestion
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, err
	}
	return &s, nil
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
