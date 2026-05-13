package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
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

// setStandardHeaders attaches headers airskills sends on every API
// request: the machine-level anonymous telemetry ID (so the server can
// attribute anonymous events to a stable identity) and the CLI version
// (so the server can refuse calls from CLIs below the hardcoded minimum
// — see doRequest for the 426 Upgrade Required recovery flow).
func setStandardHeaders(req *http.Request) {
	if id := telemetry.AnonymousID(); id != "" {
		req.Header.Set("X-Airskills-Anon-ID", id)
	}
	req.Header.Set("X-Airskills-CLI-Version", version)
}

// reExecGuardEnv is set on the child process after a 426-triggered
// auto-update + re-exec. If the new process *also* hits a 426, that
// means the floor is still ahead of the latest release we could fetch
// — abort instead of looping into another upgrade attempt.
const reExecGuardEnv = "AIRSKILLS_POST_FLOOR_UPGRADE"

var (
	// upgradeAttempted is per-process: at most one auto-update attempt
	// per CLI invocation. Without this a stuck floor (CLI version still
	// below floor after update) would loop forever.
	upgradeAttempted atomic.Bool

	// performUpdateFn and reExecFn are indirection points so tests can
	// stub the upgrade and re-exec paths without hitting GitHub or
	// actually replacing the running process.
	performUpdateFn = performUpdate
	reExecFn        = reExec
)

// doRequest is the single entry point for issuing API HTTP requests.
// It wraps client.Do() with hardcoded-floor recovery: on HTTP 426
// Upgrade Required, attempt to auto-self-update and re-exec the
// current process so the user's command completes on the new binary.
// When the upgrade or re-exec is unsafe (system-managed install,
// network failure, no newer release published), returns a user-facing
// error telling the user to upgrade manually.
func doRequest(client *http.Client, req *http.Request) (*http.Response, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUpgradeRequired {
		return resp, nil
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return nil, handleUpgradeRequired()
}

// handleUpgradeRequired runs the 426 recovery: at most one upgrade
// attempt per process, only on writable installs, with a re-exec so
// the running command completes on the new binary. Returns the error
// the caller should propagate; never returns nil because either we
// re-exec (and don't return) or we exit non-zero.
func handleUpgradeRequired() error {
	if os.Getenv(reExecGuardEnv) == "1" {
		return fmt.Errorf("airskills %s is still below the server's minimum after auto-update — run `airskills self-update` manually", version)
	}
	if upgradeAttempted.Swap(true) {
		return fmt.Errorf("airskills %s is no longer supported — run `airskills self-update` manually", version)
	}

	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("airskills %s is no longer supported — run `airskills self-update` manually (cannot resolve current binary: %v)", version, err)
	}
	if !isAutoUpdateSafe(execPath) {
		return fmt.Errorf("airskills %s is no longer supported — run `airskills self-update` manually", version)
	}

	fmt.Fprintf(os.Stderr, "airskills: server requires a newer CLI version; auto-updating...\n")
	newVersion, err := performUpdateFn(version, false, "auto-426")
	if err != nil {
		return fmt.Errorf("airskills %s is no longer supported and auto-update failed (%s) — run `airskills self-update` manually", version, classifyUpdateError(err))
	}
	if newVersion == "" {
		return fmt.Errorf("airskills %s is no longer supported and no newer release is published — please wait for an update", version)
	}

	env := append(os.Environ(), reExecGuardEnv+"=1")
	if err := reExecFn(execPath, os.Args, env); err != nil {
		return fmt.Errorf("airskills upgraded to v%s but cannot re-exec (%v) — please re-run your command", newVersion, err)
	}
	// reExecFn does not return on success.
	return nil
}

// apiSkill is now an alias for the codegen'd apitypes.Skill. The hand-
// rolled struct is gone: the spec is the single source of truth. The
// alias keeps existing call-sites readable while making it explicit
// that the type is owned by the platform, not the CLI.
//
// Note: `current_owner` lives on apitypes.ArchivePutResponse only — the
// spec scopes it to that response shape. apitypes.Skill does not carry it.
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

// Profile shape comes from apitypes.Profile (full /me row). Phase A.5
// aligned both GET /me and PATCH /me on the same shape — the slimmer
// User schema was dropped.

// listSkillsets fetches the caller's personal skillsets. Used by
// `airskills skillset list`, by `skillset use` for validation, and by
// `skillset delete` to resolve the id. Returns
// apitypes.SkillsetListItem because the server enriches each row with
// skill_count from the skillset_skills join — the bare Skillset type
// lacks that field.
func (c *apiClient) listSkillsets() ([]apitypes.SkillsetListItem, error) {
	body, err := c.get("/api/v1/skillsets")
	if err != nil {
		return nil, err
	}
	var skillsets []apitypes.SkillsetListItem
	if err := json.Unmarshal(body, &skillsets); err != nil {
		return nil, err
	}
	return skillsets, nil
}

// createSkillset creates a personal skillset. Server-side: the POST
// handler in app/api/v1/skillsets/route.ts owns slug defaulting and
// the (owner_user_id, slug) uniqueness enforcement. Returns the bare
// apitypes.Skillset (the create response has no skill_count yet).
func (c *apiClient) createSkillset(slug, name, description string) (*apitypes.Skillset, error) {
	payload := map[string]string{
		"slug":        slug,
		"name":        name,
		"description": description,
	}
	body, err := c.post("/api/v1/skillsets", payload)
	if err != nil {
		return nil, err
	}
	var ss apitypes.Skillset
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
func (c *apiClient) getMe() (*apitypes.Profile, error) {
	body, err := c.get("/api/v1/me")
	if err != nil {
		return nil, err
	}
	var profile apitypes.Profile
	if err := json.Unmarshal(body, &profile); err != nil {
		return nil, err
	}
	return &profile, nil
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

	var token *config.TokenData
	if err := config.WithTokenLock(func() error {
		loaded, err := config.LoadToken()
		if err != nil {
			return err
		}
		if loaded == nil {
			return fmt.Errorf("not logged in — run 'airskills login' first")
		}

		// Auto-refresh expired tokens while holding the token lock so concurrent
		// CLI processes re-read the rotated token instead of burning the same
		// refresh token twice.
		if time.Now().Unix() > loaded.ExpiresAt {
			if loaded.RefreshToken == "" {
				return fmt.Errorf("session expired — run 'airskills login'")
			}
			refreshed, err := refreshAccessToken(cfg.APIURL, loaded.RefreshToken)
			if err != nil {
				return fmt.Errorf("session expired and refresh failed (%s) — run 'airskills login'", err)
			}
			loaded = refreshed
			if err := config.SaveToken(loaded); err != nil {
				// Non-fatal — continue with in-memory token for this request.
				// Next run will re-refresh (slightly wasteful but correct).
				_ = err
			}
		}
		token = loaded
		return nil
	}); err != nil {
		return nil, err
	}

	return newAPIClient(cfg, token), nil
}

// refreshAccessToken exchanges a refresh token for a new access token via the platform API.
func refreshAccessToken(apiURL, refreshToken string) (*config.TokenData, error) {
	payload, _ := json.Marshal(map[string]string{"refresh_token": refreshToken})
	req, err := http.NewRequest("POST", apiURL+"/api/v1/auth/refresh", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	setStandardHeaders(req)

	resp, err := doRequest(http.DefaultClient, req)
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
	setStandardHeaders(req)

	resp, err := doRequest(c.http, req)
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
	setStandardHeaders(req)

	resp, err := doRequest(c.http, req)
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
	setStandardHeaders(req)

	resp, err := doRequest(c.http, req)
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
	setStandardHeaders(req)

	resp, err := doRequest(c.http, req)
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
	setStandardHeaders(req)

	resp, err := doRequest(c.http, req)
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

// listPersonalSkillsInSkillset fetches the caller's effective sync skills for
// a specific personal skillset (empty slug = server resolves to the caller's
// is_default=true skillset). Returns the resolved skillset slug alongside the
// skills so callers can log which skillset they synced against.
//
// A SkillsetNotFoundError is returned when the server reports an unknown
// slug; callers should render its message and exit non-zero.
func (c *apiClient) listPersonalSkillsInSkillset(skillset string) ([]apiSkill, string, error) {
	path := "/api/v1/skills"
	if skillset != "" {
		path += "?skillset=" + url.QueryEscape(skillset)
	}

	req, err := http.NewRequest("GET", c.baseURL+path, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	setStandardHeaders(req)

	resp, err := doRequest(c.http, req)
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

// getVersionHistory fetches the commit history for a skill.
func (c *apiClient) getVersionHistory(skillID string) ([]apitypes.SkillCommit, error) {
	body, err := c.get(fmt.Sprintf("/api/v1/skills/%s/versions", skillID))
	if err != nil {
		return nil, err
	}
	var result struct {
		Versions []apitypes.SkillCommit `json:"versions"`
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
// Returns *apitypes.ArchivePutResponse so callers see the spec-defined
// Warning + UnresolvedDependencies + CurrentOwner extras directly.
func (c *apiClient) putArchive(skillID string, archive []byte, expectedHash, contentHash string) (*apitypes.ArchivePutResponse, int, error) {
	url := c.baseURL + fmt.Sprintf("/api/v1/skills/%s/archive", skillID)
	req, err := http.NewRequest("PUT", url, bytes.NewReader(archive))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/gzip")
	setStandardHeaders(req)
	if expectedHash != "" {
		req.Header.Set("X-Expected-Hash", expectedHash)
	}
	if contentHash != "" {
		req.Header.Set("X-Content-Hash", contentHash)
	}

	resp, err := doRequest(c.http, req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}

	var skill apitypes.ArchivePutResponse
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

// Suggestion types come from apitypes:
//   - apitypes.Suggestion        — bare row (POST /suggestions, GET /suggestions/{id}, PUT /suggestions/{id})
//   - apitypes.EnrichedSuggestion — list response with joined username + skill name/slug

func (c *apiClient) createSuggestion(suggesterSkillID, ownerSkillID, baseContentHash, message string) (*apitypes.Suggestion, error) {
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
	var s apitypes.Suggestion
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func (c *apiClient) listSuggestions(role, status, skillID string) ([]apitypes.EnrichedSuggestion, error) {
	body, err := c.get(suggestionsPath(role, status, skillID, false))
	if err != nil {
		return nil, err
	}
	var resp struct {
		Suggestions []apitypes.EnrichedSuggestion `json:"suggestions"`
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

func (c *apiClient) getSuggestion(id string) (*apitypes.Suggestion, error) {
	body, err := c.get(fmt.Sprintf("/api/v1/suggestions/%s", id))
	if err != nil {
		return nil, err
	}
	var s apitypes.Suggestion
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Server RLS enforces that only the owner of the referenced skill can update.
func (c *apiClient) updateSuggestion(id, status, responseMessage string) (*apitypes.Suggestion, error) {
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
	var s apitypes.Suggestion
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
