package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
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

// The old skillHasUpstreamUpdate lived here. It compared a fork's own
// content hash against its parent's live hash — a comparison that is always
// true for a fork (a fork's content always differs from its parent, that's
// what diverged means), so it could not distinguish "I customised" from "the
// parent moved" (spec bug #2). The correct signal is skillUpstreamMoved in
// cmd/skill_state.go: parent_head ≠ upstream_base.

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

// listOrgSkillsets fetches skillsets owned by an org namespace.
func (c *apiClient) listOrgSkillsets(orgSlug string) ([]apitypes.SkillsetListItem, error) {
	body, err := c.get("/api/v1/skillsets/" + url.PathEscape(orgSlug))
	if err != nil {
		return nil, err
	}
	var skillsets []apitypes.SkillsetListItem
	if err := json.Unmarshal(body, &skillsets); err != nil {
		return nil, err
	}
	return skillsets, nil
}

// createOrgSkillset creates a skillset owned by an org namespace.
func (c *apiClient) createOrgSkillset(orgSlug, slug, name, description string) (*apitypes.Skillset, error) {
	payload := map[string]string{
		"slug":        slug,
		"name":        name,
		"description": description,
	}
	body, err := c.post("/api/v1/skillsets/"+url.PathEscape(orgSlug), payload)
	if err != nil {
		return nil, err
	}
	var ss apitypes.Skillset
	if err := json.Unmarshal(body, &ss); err != nil {
		return nil, err
	}
	return &ss, nil
}

func (c *apiClient) deleteOrgSkillset(orgSlug, skillsetSlug string) error {
	return c.del("/api/v1/skillsets/" + url.PathEscape(orgSlug) + "/" + url.PathEscape(skillsetSlug))
}

func (c *apiClient) addSkillToOrgSkillset(orgSlug, skillsetSlug, skillOwner, skillSlug string) error {
	path := fmt.Sprintf(
		"/api/v1/skillsets/%s/%s/skills/%s/%s",
		url.PathEscape(orgSlug),
		url.PathEscape(skillsetSlug),
		url.PathEscape(skillOwner),
		url.PathEscape(skillSlug),
	)
	body, status, err := c.put(path, map[string]string{})
	if err != nil {
		return err
	}
	if status >= 400 {
		return fmt.Errorf("API error (%d): %s", status, string(body))
	}
	return nil
}

func (c *apiClient) removeSkillFromOrgSkillset(orgSlug, skillsetSlug, skillOwner, skillSlug string) error {
	return c.del(fmt.Sprintf(
		"/api/v1/skillsets/%s/%s/skills/%s/%s",
		url.PathEscape(orgSlug),
		url.PathEscape(skillsetSlug),
		url.PathEscape(skillOwner),
		url.PathEscape(skillSlug),
	))
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
	// httpArchive is used for archive uploads/downloads. Large skills
	// (hundreds of files) make the server's per-file Storage round-trips
	// add up well past the 30s general client cap, so this client gets
	// a much longer ceiling. Wire transfer is fast (gzipped tar); the
	// time is Cloudflare Worker CPU and Supabase Storage latency.
	httpArchive *http.Client
}

// newAPIClient creates an API client from config and token.
func newAPIClient(cfg *config.Config, token *config.TokenData) *apiClient {
	return &apiClient{
		baseURL:     cfg.APIURL,
		token:       token.AccessToken,
		http:        &http.Client{Timeout: 30 * time.Second},
		httpArchive: &http.Client{Timeout: 5 * time.Minute},
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
		// CLI processes serialise on it. The server no longer rotates the
		// refresh token on each call (rotation broke multi-device login by
		// mutually evicting machines that shared the same token), so concurrent
		// refreshes are now safe — both get back the same refresh token.
		if time.Now().Unix() > loaded.ExpiresAt {
			if loaded.RefreshToken == "" {
				telemetry.Capture("cli_refresh_failed", map[string]interface{}{
					"reason": "no_refresh_token",
				})
				return fmt.Errorf("session expired — run 'airskills login'")
			}
			telemetry.Capture("cli_refresh_attempted", nil)
			refreshed, err := refreshAccessToken(cfg.APIURL, loaded.RefreshToken)
			if err != nil {
				telemetry.Capture("cli_refresh_failed", map[string]interface{}{
					"error": err.Error(),
				})
				return fmt.Errorf("session expired and refresh failed (%s) — run 'airskills login'", err)
			}
			loaded = refreshed
			if err := config.SaveToken(loaded); err != nil {
				// SaveToken failure used to be swallowed. That was safe when
				// the server rotated tokens (next run could re-refresh from
				// disk and get a fresh token). Now the server returns the
				// SAME refresh token, so a save failure just means the file
				// wasn't updated — the existing on-disk token is still
				// valid. Log and continue.
				telemetry.Capture("cli_refresh_save_failed", map[string]interface{}{
					"error": err.Error(),
				})
			} else {
				telemetry.Capture("cli_refresh_succeeded", nil)
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

// ShadowInfo records a server-reported shadow on one of the caller's
// skills (migration 047). Returned by fetchShadowMap so pull can skip
// shadowed slugs and emit a warning instead of writing them to disk.
type ShadowInfo struct {
	SkillID string
	Slug    string
	OrgSlug string // winning org slug; empty for cross-org shadow on the loser side
}

// fetchShadowMap calls /api/v1/sync with since=epoch to get the
// caller's effective skill set with shadow tagging. Returns one entry
// per shadowed skill_id (the loser side), keyed by skill id. Pull
// uses this to filter remoteSkills before download, and to print the
// "shadowed by <org>/<slug>" warning the design promises fires on
// every pull until the user renames.
//
// Best-effort: a fetch error returns an empty map and nil — sync's
// extra surface shouldn't fail the whole pull, and a missing shadow
// map degrades to the pre-mig-047 behaviour (write all skills).
func (c *apiClient) fetchShadowMap() map[string]ShadowInfo {
	out := map[string]ShadowInfo{}
	req, err := http.NewRequest("GET", c.baseURL+"/api/v1/sync?since=1970-01-01T00:00:00Z", nil)
	if err != nil {
		return out
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	setStandardHeaders(req)
	resp, err := doRequest(c.http, req)
	if err != nil {
		return out
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return out
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return out
	}
	var parsed struct {
		Skills []struct {
			ID       string  `json:"id"`
			Slug     string  `json:"slug"`
			Shadowed bool    `json:"shadowed"`
			OrgSlug  *string `json:"org_slug"`
		} `json:"skills"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return out
	}
	// We also need the WINNING org slug per slug for the warning.
	// Build that map from the non-shadowed org rows first.
	winnerOrgBySlug := map[string]string{}
	for _, s := range parsed.Skills {
		if !s.Shadowed && s.OrgSlug != nil && *s.OrgSlug != "" {
			winnerOrgBySlug[s.Slug] = *s.OrgSlug
		}
	}
	for _, s := range parsed.Skills {
		if !s.Shadowed {
			continue
		}
		info := ShadowInfo{SkillID: s.ID, Slug: s.Slug}
		if winnerOrgBySlug[s.Slug] != "" {
			info.OrgSlug = winnerOrgBySlug[s.Slug]
		} else if s.OrgSlug != nil {
			info.OrgSlug = *s.OrgSlug
		}
		out[s.ID] = info
	}
	return out
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

// getSkillFilePaths returns the relative paths of every file the server
// holds for a skill — the remote manifest, paths only (no file bodies).
// GET /api/v1/skills/:id returns a SkillDetail whose `files` array carries
// path + size for each stored file. The deletion resolver uses this as the
// baseline to spot files the user removed locally, far cheaper than
// downloading the whole archive.
func (c *apiClient) getSkillFilePaths(id string) ([]string, error) {
	body, err := c.get(fmt.Sprintf("/api/v1/skills/%s", id))
	if err != nil {
		return nil, err
	}
	var detail struct {
		Files []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	if err := json.Unmarshal(body, &detail); err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(detail.Files))
	for _, f := range detail.Files {
		if f.Path != "" {
			paths = append(paths, f.Path)
		}
	}
	return paths, nil
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

// SkillConflictError is returned by createSkill when the server's
// effective-skills check (migration 047) rejects a new slug because it
// already exists in the caller's user-or-org-inherited skill set.
// Push surfaces this with a hint pointing at `airskills mv`.
type SkillConflictError struct {
	Slug           string // the slug that conflicted (the one the user tried to create)
	Source         string // "user" or "org" — where the conflicting skill lives
	OwnerOrOrgSlug string // org slug when Source=="org"; empty otherwise
	ServerMessage  string // verbatim server `message` if present
}

func (e *SkillConflictError) Error() string {
	if e.Source == "org" && e.OwnerOrOrgSlug != "" {
		return fmt.Sprintf("slug %q is already in your effective skill set via org %q — rename your local skill with `airskills mv %s <new-name>` and retry the push", e.Slug, e.OwnerOrOrgSlug, e.Slug)
	}
	if e.ServerMessage != "" {
		return e.ServerMessage
	}
	return fmt.Sprintf("slug %q already exists in your effective skill set", e.Slug)
}

// createSkill creates a skill metadata shell (files uploaded separately via archive).
// orgID is optional — non-empty creates the skill under the given org (caller must be admin/owner).
//
// On a 409 with the migration-047 `conflict_with` payload, returns a
// typed *SkillConflictError so push can format the user-facing message
// with the conflicting source named.
func (c *apiClient) createSkill(name, description string, tools []string, forkedFrom, orgID string) (*apiSkill, error) {
	return c.createSkillRow(name, description, tools, forkedFrom, orgID, false)
}

// createBackupFork creates the hidden overlay-backup fork of an upstream
// skill — backup: true is what lets the row share the upstream's slug
// (the server's collision guard requires it) and marks it as plumbing for
// fresh-device reconstruction and the convergence sweep.
func (c *apiClient) createBackupFork(name, forkedFrom string) (*apiSkill, error) {
	return c.createSkillRow(name, "", []string{"claude-code"}, forkedFrom, "", true)
}

func (c *apiClient) createSkillRow(name, description string, tools []string, forkedFrom, orgID string, backup bool) (*apiSkill, error) {
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
	if backup {
		payload["backup"] = true
	}
	body, err := c.postWithStatus("/api/v1/skills", payload)
	if err != nil {
		if conflict := parseSkillConflict(err); conflict != nil {
			conflict.Slug = slugify(name)
			return nil, conflict
		}
		return nil, err
	}
	var skill apiSkill
	if err := json.Unmarshal(body, &skill); err != nil {
		return nil, err
	}
	return &skill, nil
}

// withdrawSuggestion retracts the caller's own pending suggestion. Used by
// the fold-in (admin write supersedes the proposal), the supersede flow
// (re-edit while pending → withdraw stale, create fresh), and the
// convergence sweep. Callers tolerate failure — losing the race with an
// owner accepting is fine.
func (c *apiClient) withdrawSuggestion(id string) error {
	_, err := c.updateSuggestion(id, "withdrawn", "")
	return err
}

// promoteBackupSkill flips a hidden backup fork to a visible personal skill
// (backup: false — one direction only). Used when the overlay's upstream is
// lost and the backup is the user's only copy.
func (c *apiClient) promoteBackupSkill(id string) error {
	body, status, err := c.put(fmt.Sprintf("/api/v1/skills/%s", id), map[string]interface{}{"backup": false})
	if err != nil {
		return err
	}
	if status >= 400 {
		return fmt.Errorf("API error (%d): %s", status, strings.TrimSpace(string(body)))
	}
	return nil
}

// subscribe attaches an upstream skill to the caller's personal default
// skillset (a subscription), so a logged-in `add` follows them across machines.
// Idempotent server-side: 204 even if already subscribed. A 403 means the skill
// isn't readable by the caller — private / deleted / never-existed, deliberately
// not distinguished so subscribing can't probe a skill's existence; the caller
// treats that as "upstream gone" rather than retrying forever.
func (c *apiClient) subscribe(skillID string) error {
	req, err := http.NewRequest("POST", c.baseURL+fmt.Sprintf("/api/v1/skills/%s/subscribe", skillID), nil)
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
	if resp.StatusCode == http.StatusForbidden {
		// Private / deleted / never-existed — not distinguished. Carried as a
		// typed sentinel (not a substring of the message) so callers classify
		// on the actual status, never on body text.
		return errSubscribeForbidden
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// unsubscribe removes the caller's subscription row for an upstream skill, so
// it stops arriving on their other machines. Idempotent: 204 even if there was
// no row (e.g. a skill added anonymously and never synced while logged in).
func (c *apiClient) unsubscribe(skillID string) error {
	return c.del(fmt.Sprintf("/api/v1/skills/%s/subscribe", skillID))
}

// promote turns the caller's vanished subscription into an owned skill in ONE
// transactional server call: it uploads the local files (the caller's edits, if
// any) and the server creates a visible owned skill — recording sourceSkillID
// as provenance — AND deletes the subscription row, with no two-row window.
// Cross-machine idempotent on (owner, source): a second machine converges on the
// one owned skill (200); the first returns 201; a slug clash with an UNRELATED
// owned skill is 409. Mirrors the archive PUT's tar.gz-body + metadata-headers
// convention; the promoted copy is private. Returns the owned skill + status.
func (c *apiClient) promote(sourceSkillID string, archive []byte, slug, name, version, contentHash string) (*apiSkill, int, error) {
	url := c.baseURL + fmt.Sprintf("/api/v1/skills/%s/promote", sourceSkillID)
	req, err := http.NewRequest("POST", url, bytes.NewReader(archive))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/gzip")
	setStandardHeaders(req)
	req.Header.Set("X-Slug", slug)
	if name != "" {
		req.Header.Set("X-Name", name)
	}
	if version != "" {
		req.Header.Set("X-Version", version)
	}
	req.Header.Set("X-Visibility", "private")
	if contentHash != "" {
		req.Header.Set("X-Content-Hash", contentHash)
	}

	resp, err := doRequest(c.httpArchive, req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	var skill apiSkill
	_ = json.Unmarshal(body, &skill)
	if resp.StatusCode >= 400 {
		return &skill, resp.StatusCode, fmt.Errorf("API error (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return &skill, resp.StatusCode, nil
}

// errSubscribeForbidden is subscribe()'s typed answer for HTTP 403 — the
// readability guard's verdict for a skill the caller can no longer read
// (private / deleted / never-existed, not distinguished).
var errSubscribeForbidden = errors.New("subscribe: skill not readable (403)")

// isForbiddenError reports whether a subscribe error is the definitive 403.
// Matched on the typed sentinel, NEVER on a substring of the message — a 5xx
// whose body happens to contain "(403)" must not be read as forbidden (the
// false-promote the substring form risked).
func isForbiddenError(err error) bool {
	return errors.Is(err, errSubscribeForbidden)
}

// slugify mirrors the platform's lib/api-utils.ts slugify so the CLI
// can predict which slug the server will produce from a given name.
// Used to populate SkillConflictError.Slug when the server rejects.
func slugify(name string) string {
	out := make([]rune, 0, len(name))
	prevHyphen := false
	for _, r := range name {
		switch {
		case r >= 'A' && r <= 'Z':
			out = append(out, r+('a'-'A'))
			prevHyphen = false
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			out = append(out, r)
			prevHyphen = false
		default:
			if !prevHyphen && len(out) > 0 {
				out = append(out, '-')
				prevHyphen = true
			}
		}
	}
	for len(out) > 0 && out[len(out)-1] == '-' {
		out = out[:len(out)-1]
	}
	return string(out)
}

// parseSkillConflict tries to extract a migration-047 conflict_with
// payload from a generic API error string. Returns nil if the error
// isn't a 409 with the expected shape.
func parseSkillConflict(apiErr error) *SkillConflictError {
	if apiErr == nil {
		return nil
	}
	msg := apiErr.Error()
	// post() formats errors as "API error (%d): %s"; only the body of
	// a 409 carries the conflict_with payload.
	if !strings.Contains(msg, "(409)") {
		return nil
	}
	idx := strings.Index(msg, "{")
	if idx < 0 {
		return nil
	}
	var body struct {
		Error        string `json:"error"`
		Message      string `json:"message"`
		ConflictWith *struct {
			Source         string `json:"source"`
			OwnerOrOrgSlug string `json:"owner_or_org_slug"`
			Slug           string `json:"slug"`
		} `json:"conflict_with"`
	}
	if err := json.Unmarshal([]byte(msg[idx:]), &body); err != nil || body.ConflictWith == nil {
		return nil
	}
	return &SkillConflictError{
		Slug:           body.ConflictWith.Slug,
		Source:         body.ConflictWith.Source,
		OwnerOrOrgSlug: body.ConflictWith.OwnerOrOrgSlug,
		ServerMessage:  body.Message,
	}
}

// postWithStatus is an internal variant of post() that surfaces the
// HTTP status code so callers can distinguish 409 conflicts. The
// existing post() is unchanged so other callsites keep working.
func (c *apiClient) postWithStatus(path string, payload interface{}) ([]byte, error) {
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

	resp, err := doRequest(c.httpArchive, req)
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
		"status": status,
	}
	// Only owners may write response_message (the server 403s a suggester
	// who sends the key at all — including the empty string a withdraw
	// would otherwise carry).
	if responseMessage != "" {
		payload["response_message"] = responseMessage
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
