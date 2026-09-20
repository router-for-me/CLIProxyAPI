package helps

import (
	"errors"
	"sync"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// TestApplyClaudeCredentialMetadataConcurrentSharedAuth pins the invariant that a
// single *Auth shared by concurrent requests is safe to use. Before the device
// pool accessors were introduced these paths initialized and wrote auth.Metadata
// outside claudeDevicePoolMu, which aborts the process with "concurrent map
// writes" rather than failing a request. Run with -race.
func TestApplyClaudeCredentialMetadataConcurrentSharedAuth(t *testing.T) {
	auth := &cliproxyauth.Auth{
		ID:       "shared-credential",
		Metadata: map[string]any{"account_uuid": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
	}
	payload := []byte(`{"model":"claude-opus-4-6","messages":[{"role":"user","content":"hi"}]}`)

	const goroutines = 32
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	start := make(chan struct{})

	for i := range goroutines {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			sessionID := "session-" + string(rune('a'+i%26))
			if _, _, err := ApplyClaudeCredentialMetadata(payload, auth, sessionID); err != nil {
				errs <- err
				return
			}
			// Concurrent readers of the same map must be safe too.
			_ = ClaudeCredentialAccountUUID(auth)
		}(i)
	}

	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("ApplyClaudeCredentialMetadata on shared auth: %v", err)
	}

	if auth.Metadata == nil {
		t.Fatal("expected metadata to be initialized")
	}
}

// TestEnsureClaudeCredentialDevicePoolConcurrentSharedAuth covers the local
// (non Home KV) branch of the pool bootstrap on a shared credential.
func TestEnsureClaudeCredentialDevicePoolConcurrentSharedAuth(t *testing.T) {
	auth := &cliproxyauth.Auth{ID: "shared-credential"}

	const goroutines = 32
	var wg sync.WaitGroup
	results := make(chan string, goroutines)
	errs := make(chan error, goroutines)
	start := make(chan struct{})

	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			deviceIDs, err := EnsureClaudeCredentialDevicePoolRequired(t.Context(), auth)
			if err != nil {
				errs <- err
				return
			}
			if len(deviceIDs) == 0 {
				errs <- errEmptyPool
				return
			}
			results <- deviceIDs[0]
		}()
	}

	close(start)
	wg.Wait()
	close(errs)
	close(results)
	for err := range errs {
		t.Fatalf("EnsureClaudeCredentialDevicePoolRequired on shared auth: %v", err)
	}

	// Every caller must agree on the pool; a racing bootstrap would hand out
	// different device IDs to different requests on the same credential.
	seen := make(map[string]struct{})
	for deviceID := range results {
		seen[deviceID] = struct{}{}
	}
	if len(seen) != 1 {
		t.Fatalf("device pool bootstrap was not stable: got %d distinct device IDs, want 1", len(seen))
	}
}

var errEmptyPool = errors.New("device pool is empty")
