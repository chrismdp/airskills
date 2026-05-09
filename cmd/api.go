package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/chrismdp/airskills/config"
	"github.com/chrismdp/airskills/internal/apitypes"
	"github.com/chrismdp/airskills/telemetry"
	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

// SkillsetNotFoundError is returned when the server reports an unknown
// skillset slug on /api/v1/skills. Callers render it as a human-readable
// hint listing the user's available skillsets.
type SkillsetNotFoundError struct {
	RequestedSlug string
	Available     []string
}

func (e *SkillsetNotFoundError) Error() string {
	if len(e.Available) == 0 {
		return fmt.Sprintf("skillset %q not found — you have no personal skillsets yet", e.RequestedSlug)
	}
	return fmt.Sprintf("skillset %q not found. Your skillsets: %s",
		e.RequestedSlug, strings.Join(e.Available, ", "))
}

// setAnonHeader attaches the machine-level anonymous telemetry ID so the
// server can attribute anonymous events (e.g. `airskills add` without login)
// to a stable identity across sessions.
func setAnonHeader(req *http.Request) {
	if id := telemetry.AnonymousID(); id != "" {
		req.Header.Set("X-Airskills-Anon-ID", id)
	}
}

// apiSkill is now an alias for the codegen'd apitypes.Skill. The hand-
// rolled struct is gone: the spec is the single source of truth. The
// alias keeps existing call-sites readable while making it explicit
// that the type is owned by the platform, not the CLI.
//
// Note: `current_owner` lives on apiArchivePutResponse only — the spec
// scopes it to that response shape. apitypes.Skill does not carry it.
type apiSkill = apitypes.Skill

// strDeref returns the pointed-to string, or "" if nil. Use at the
// boundary where nullable spec fields meet code that wants a value-type
// string (formatting, marker writes, etc). Distinct from "" sentinel
// semantics: if a caller cares about "missing vs empty" it must check
// the pointer directly.
func strDeref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// strPtr returns a pointer to s. Convenience for constructing
// apiSkill / apitypes.Skill literals where nullable fields are
// declared as *string. Mostly used in tests.
func strPtr(s string) *string {
	return &s
}

// testUUID maps an arbitrary test-fixture string ("skill-1" etc) to
// a deterministic UUID so tests can keep their human-readable IDs
// while satisfying apitypes.Skill.Id's openapi_types.UUID type. SHA-1
// based so collisions across distinct strings are vanishingly unlikely.
func testUUID(s string) openapi_types.UUID {
	if u, err := uuid.Parse(s); err == nil {
		return u
	}
	return uuid.NewSHA1(uuid.Nil, []byte(s))
}

// apiArchivePutResponse mirrors apitypes.ArchivePutResponse — embeds
// apiSkill (so all skill fields are accessible on the same value) and
// adds the archive-PUT-only extras the spec scopes here: warning
// (storage soft-limit notice), unresolved_dependencies, and
// current_owner (used by the CLI to detect out-of-band transfers).
type apiArchivePutResponse struct {
	apiSkill
	Warning                string                  `json:"warning,omitempty"`
	UnresolvedDependencies []string                `json:"unresolved_dependencies,omitempty"`
	CurrentOwner           *apitypes.OwnerNamespace `json:"current_owner,omitempty"`
}

// skillHasUpstreamUpdate reports whether a forked skill's pinned upstream
// hash has drifted from the parent's live hash. Free function rather
// than a method because apiSkill is an alias for the codegen'd
// apitypes.Skill — methods can't hang off a type alias to an
// imported type.
func skillHasUpstreamUpdate(s apiSkill) bool {
	if s.ForkedFrom == nil || s.UpstreamContentHash == nil || s.ContentHash == nil {
		return false
	}
	return *s.ContentHash != "" && *s.UpstreamContentHash != "" &&
		*s.ContentHash != *s.UpstreamContentHash
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

// apiSkillset represents a personal skillset from GET /api/v1/skillsets.
// skill_count is added by the server from a count-map join on
// skillset_skills — see app/api/v1/skillsets/route.ts.
type apiSkillset struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Slug                string `json:"slug"`
	Description         string `json:"description"`
	IsDefault           bool   `json:"is_default"`
	AutoAbsorbNewSkills bool   `json:"auto_absorb_new_skills"`
	SkillCount          int    `json:"skill_count"`
}

// listSkillsets fetches the caller's personal skillsets. Used by
// `airskills skillset list`, by `skillset use` for validation, and by
// `skillset delete` to resolve the id.
func (c *apiClient) listSkillsets() ([]apiSkillset, error) {
	body, err := c.get("/api/v1/skillsets")
	if err != nil {
		return nil, err
	}
	var skillsets []apiSkillset
	if err := json.Unmarshal(body, &skillsets); err != nil {
		return nil, err
	}
	return skillsets, nil
}

// createSkillset creates a personal skillset. Server-side: the POST
// handler in app/api/v1/skillsets/route.ts owns slug defaulting and
// the (owner_user_id, slug) uniqueness enforcement.
func (c *apiClient) createSkillset(slug, name, description string) (*apiSkillset, error) {
	payload := map[string]string{
		"slug":        slug,
		"name":        name,
		"description": description,
	}
	body, err := c.post("/api/v1/skillsets", payload)
	if err != nil {
		return nil, err
	}
	var ss apiSkillset
	if err := json.Unmarshal(body, &ss); err != nil {
		return nil, err
	}
	return &ss, nil
}

// deletePersonalSkillset hits the owner-scoped DELETE route for the
// caller's own skillsets. Owner slug = the caller's username (fetched
// here to keep callers simple).
func (c *apiClient) deletePersonalSkillset(slug string) error {
	profile, err := c.getMe()
	if err != nil {
		return err
	}
	return c.del("/api/v1/skillsets/" + url.PathEscape(profile.Username) + "/" + url.PathEscape(slug))
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
			return nil, fmt.Errorf("session expired and refresh failed (%s) — run 'airskills login'", err)
		}
		token = refreshed
		if err := config.SaveToken(token); err != nil {
			// Non-fatal — continue with in-memory token for this request.
			// Next run will re-refresh (slightly wasteful but correct).
			_ = err
		}
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
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("refresh returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
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

// getWithStatus is like get() but returns the HTTP status code instead of
// folding it into an error string. Used by callers that need to react to
// specific codes (e.g. fall back on 404) before treating other >=400 codes
// as errors.
func (c *apiClient) getWithStatus(path string) ([]byte, int, error) {
	req, err := http.NewRequest("GET", c.baseURL+path, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	setAnonHeader(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, resp.StatusCode, readErr
	}
	return body, resp.StatusCode, nil
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

// listPersonalSkillsInSkillset fetches the caller's personal skills scoped
// to a specific skillset (empty slug = server resolves to the caller's
// is_default=true skillset). Returns the resolved skillset slug alongside
// the skills so callers can log which skillset they synced against.
//
// A SkillsetNotFoundError is returned when the server reports an unknown
// slug; callers should render its message and exit non-zero.
func (c *apiClient) listPersonalSkillsInSkillset(skillset string) ([]apiSkill, string, error) {
	path := "/api/v1/skills?scope=personal"
	if skillset != "" {
		path += "&skillset=" + url.QueryEscape(skillset)
	}

	req, err := http.NewRequest("GET", c.baseURL+path, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	setAnonHeader(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, "", readErr
	}

	if resp.StatusCode == http.StatusNotFound {
		var errResp struct {
			Error     string   `json:"error"`
			Available []string `json:"available"`
		}
		if jErr := json.Unmarshal(body, &errResp); jErr == nil && errResp.Available != nil {
			return nil, "", &SkillsetNotFoundError{
				RequestedSlug: skillset,
				Available:     errResp.Available,
			}
		}
		return nil, "", fmt.Errorf("API error (404): %s", string(body))
	}
	if resp.StatusCode >= 400 {
		return nil, "", fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	var parsed struct {
		Skillset *struct {
			Slug string `json:"slug"`
			Name string `json:"name"`
		} `json:"skillset,omitempty"`
		Skills []apiSkill `json:"skills"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, "", err
	}
	resolved := ""
	if parsed.Skillset != nil {
		resolved = parsed.Skillset.Slug
	}
	return parsed.Skills, resolved, nil
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
	ID          string   `json:"id"`
	ParentIDs   []string `json:"parent_ids"`
	ContentHash string   `json:"content_hash,omitempty"`
	Message     string   `json:"message"`
	CreatedAt   string   `json:"created_at"`
	PushedBy    *string  `json:"pushed_by"`
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

// getVersionContent downloads a skill's files as of a specific commit.
// Uses GET /api/v1/skills/:id/archive?commit=<commitID>.
func (c *apiClient) getVersionContent(skillID, commitID string) (map[string][]byte, error) {
	archiveBody, err := c.get(fmt.Sprintf("/api/v1/skills/%s/archive?commit=%s", skillID, commitID))
	if err != nil {
		return nil, err
	}
	return extractTarGzToMap(bytes.NewReader(archiveBody))
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
// Returns *apiArchivePutResponse so callers can read the spec-correct
// Warning + UnresolvedDependencies extras alongside the embedded Skill.
func (c *apiClient) putArchive(skillID string, archive []byte, expectedHash, contentHash string) (*apiArchivePutResponse, int, error) {
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

	var skill apiArchivePutResponse
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

// Per-process cache of the count-endpoint shape decision. The new
// /api/v1/suggestions/count route is the preferred path; on a 404 we
// fall back to the legacy ?count=1 form (older self-hosted platforms)
// and remember the decision so we don't re-probe on every status call.
var countUseLegacy atomic.Bool

// countSuggestions hits the count-only fast path — used by `airskills status`
// so the shell prompt doesn't pay for enrichment just to render a number.
//
// Tries GET /api/v1/suggestions/count first. On HTTP 404 (older platform
// without the split), falls back to the legacy GET /api/v1/suggestions
// ?count=1 polymorphic-response form. Per-process cache: once a 404 has
// been observed, subsequent calls go straight to the legacy path.
func (c *apiClient) countSuggestions(role, status, skillID string) (int, error) {
	if countUseLegacy.Load() {
		return c.countSuggestionsLegacy(role, status, skillID)
	}
	body, statusCode, err := c.getWithStatus(suggestionsCountPath(role, status, skillID))
	if err != nil {
		return 0, err
	}
	if statusCode == http.StatusNotFound {
		countUseLegacy.Store(true)
		return c.countSuggestionsLegacy(role, status, skillID)
	}
	if statusCode >= 400 {
		return 0, fmt.Errorf("API error (%d): %s", statusCode, string(body))
	}
	var resp struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0, err
	}
	return resp.Count, nil
}

func (c *apiClient) countSuggestionsLegacy(role, status, skillID string) (int, error) {
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

func suggestionsCountPath(role, status, skillID string) string {
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
	if len(params) == 0 {
		return "/api/v1/suggestions/count"
	}
	return "/api/v1/suggestions/count?" + strings.Join(params, "&")
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
