// Package telemetry sends fire-and-forget product analytics events to PostHog.
//
// Design notes:
//   - All HTTP I/O is non-blocking via goroutines tracked by a sync.WaitGroup;
//     callers must invoke Flush() at process exit (root.go does this) so events
//     have a chance to land before the binary returns.
//   - The anonymous distinct_id is generated on first run and persisted to
//     ~/.config/airskills/telemetry.json so events from a single machine link
//     together even before login.
//   - On login, Identify() emits a PostHog $identify event with $anon_distinct_id
//     so prior anonymous events get aliased to the real user.
//   - Failures are swallowed: telemetry must never break a user-facing command.
//   - Users can opt out with AIRSKILLS_NO_TELEMETRY=1.
package telemetry

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/chrismdp/airskills/config"
)

// PostHog project ingest key for the Airskills project (151269). This is the
// same kind of public key that PostHog embeds in client-side JS snippets — it
// only allows writing events, never reading data, so it's safe to ship in the
// binary.
const APIKey = "phc_tJ5pdZxz6wrM4JcTAXH86PnSKWbCS5YjwZ6MeMD2GX8R"

const endpoint = "https://eu.i.posthog.com/capture/"

type identity struct {
	AnonymousID string `json:"anonymous_id"`
	UserID      string `json:"user_id,omitempty"`
	Username    string `json:"username,omitempty"`
}

var (
	mu          sync.Mutex
	current     identity
	initialized bool
	disabled    bool
	wg          sync.WaitGroup

	// Cached anonymous ID — read without the mutex by setAnonHeader on the
	// HTTP hot path. Set once under `mu` during Init() and never mutated
	// afterwards (Logout clears user_id but keeps the anon ID), so a plain
	// string read is race-free for the lifetime of the process.
	cachedAnonID string

	// CLIVersion is set by root.go from the linker-injected version string so
	// every event carries the binary version as a super property. Assigned
	// once before Init() on the main goroutine, never mutated after.
	CLIVersion string

	httpClient = &http.Client{Timeout: 5 * time.Second}
)

// Init loads (or creates) the persisted identity. Safe to call multiple times.
// Errors are silently ignored — telemetry never blocks the user.
func Init() {
	mu.Lock()
	defer mu.Unlock()

	if initialized {
		return
	}
	initialized = true

	if os.Getenv("AIRSKILLS_NO_TELEMETRY") != "" {
		disabled = true
		return
	}

	loaded, err := loadIdentity()
	if err == nil && loaded.AnonymousID != "" {
		current = *loaded
		cachedAnonID = current.AnonymousID
		return
	}

	current = identity{AnonymousID: newAnonID()}
	cachedAnonID = current.AnonymousID
	_ = saveIdentity(&current)
}

// Capture queues an event for delivery. Non-blocking; the actual HTTP request
// runs in a goroutine and is awaited by Flush(). wg.Add happens under the
// mutex to guarantee it's ordered-before any subsequent Flush().
func Capture(event string, properties map[string]interface{}) {
	mu.Lock()
	if disabled || !initialized {
		mu.Unlock()
		return
	}
	distinctID := current.UserID
	if distinctID == "" {
		distinctID = current.AnonymousID
	}
	if distinctID == "" {
		mu.Unlock()
		return
	}
	wg.Add(1)
	mu.Unlock()

	go send(event, distinctID, mergeSuperProps(properties))
}

// Identify links the current anonymous ID to a real user. Should be called
// once after a successful login. Persists the user ID so subsequent runs
// continue using it as the distinct ID.
func Identify(userID, username string) {
	mu.Lock()
	if disabled || !initialized || userID == "" {
		mu.Unlock()
		return
	}
	anonID := current.AnonymousID
	current.UserID = userID
	current.Username = username
	_ = saveIdentity(&current)
	wg.Add(1)
	mu.Unlock()

	props := mergeSuperProps(map[string]interface{}{
		"$anon_distinct_id": anonID,
		"$set": map[string]interface{}{
			"username": username,
		},
	})
	go send("$identify", userID, props)
}

// AnonymousID returns the stable anonymous ID for this machine. Safe to call
// on the HTTP hot path — reads a cached copy without taking the mutex. Set
// once by Init() and never mutated, so a plain read is race-free.
func AnonymousID() string {
	return cachedAnonID
}

// Logout clears the persisted user ID so subsequent events fall back to the
// anonymous ID. The anonymous ID itself is preserved across logouts.
func Logout() {
	mu.Lock()
	defer mu.Unlock()
	if !initialized {
		return
	}
	current.UserID = ""
	current.Username = ""
	_ = saveIdentity(&current)
}

// Flush waits up to the given timeout for all in-flight captures to finish.
// Called from root.Execute() before the binary exits.
func Flush(timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
}

// send performs the HTTP POST to PostHog. The caller must have already
// called wg.Add(1) under the mutex — send owns the matching Done().
func send(event, distinctID string, props map[string]interface{}) {
	defer wg.Done()

	payload := map[string]interface{}{
		"api_key":     APIKey,
		"event":       event,
		"distinct_id": distinctID,
		"properties":  props,
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}

	resp, err := httpClient.Post(endpoint, "application/json", bytes.NewReader(data))
	if err != nil {
		return
	}
	resp.Body.Close()
}

func mergeSuperProps(props map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{
		"cli_version": CLIVersion,
		"os":          runtime.GOOS,
		"arch":        runtime.GOARCH,
	}
	for k, v := range props {
		out[k] = v
	}
	return out
}

func identityPath() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "telemetry.json"), nil
}

func loadIdentity() (*identity, error) {
	path, err := identityPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var id identity
	if err := json.Unmarshal(data, &id); err != nil {
		return nil, err
	}
	return &id, nil
}

func saveIdentity(id *identity) error {
	path, err := identityPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func newAnonID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fall back to timestamp-based ID — better than nothing.
		return "anon-" + time.Now().UTC().Format("20060102T150405.000000")
	}
	return "anon-" + hex.EncodeToString(b)
}
