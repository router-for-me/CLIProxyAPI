package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
)

// staticCatalog builds a minimal valid catalog so tryRefreshModels treats a
// mocked fetch as a successful remote response.
func staticCatalog(t *testing.T) *staticModelsJSON {
	t.Helper()
	var parsed staticModelsJSON
	if err := json.Unmarshal([]byte(`{"gemini": []}`), &parsed); err != nil {
		t.Fatalf("unmarshal static catalog: %v", err)
	}
	return &parsed
}

// restoreUpdaterHooks saves package-level test hooks and restores them via
// t.Cleanup so parallel tests in the package are not affected.
func restoreUpdaterHooks(t *testing.T) (sleepCalls *[]time.Duration) {
	t.Helper()
	origSleep, origFetch := startupSleep, fetchModelsFromRemoteFn
	t.Cleanup(func() {
		startupSleep, fetchModelsFromRemoteFn = origSleep, origFetch
	})
	calls := &[]time.Duration{}
	return calls
}

func TestRunStartupRefreshWithBackoff_SucceedsFirstTry(t *testing.T) {
	calls := restoreUpdaterHooks(t)

	fetchOK := false
	fetchModelsFromRemoteFn = func(ctx context.Context) (*staticModelsJSON, string) {
		fetchOK = true
		return staticCatalog(t), "https://example.test/models.json"
	}
	startupSleep = func(ctx context.Context, d time.Duration) error {
		t.Fatalf("startupSleep must not be called when the first fetch succeeds (got delay %s)", d)
		return nil
	}

	runStartupRefreshWithBackoff(context.Background())
	if !fetchOK {
		t.Fatal("fetch was never attempted")
	}
	if len(*calls) != 0 {
		t.Fatalf("unexpected sleep calls: %v", *calls)
	}
}

func TestRunStartupRefreshWithBackoff_ExponentialCapped(t *testing.T) {
	calls := restoreUpdaterHooks(t)

	var mu sync.Mutex
	fetches := 0
	fetchModelsFromRemoteFn = func(ctx context.Context) (*staticModelsJSON, string) {
		mu.Lock()
		defer mu.Unlock()
		fetches++
		if fetches <= 4 {
			return nil, "" // fail the first 4 attempts
		}
		return staticCatalog(t), "https://example.test/models.json"
	}

	var sleepOrder []time.Duration
	startupSleep = func(ctx context.Context, d time.Duration) error {
		sleepOrder = append(sleepOrder, d)
		*calls = append(*calls, d)
		return nil
	}

	runStartupRefreshWithBackoff(context.Background())

	if len(sleepOrder) != 4 {
		t.Fatalf("expected 4 retry sleeps, got %d: %v", len(sleepOrder), sleepOrder)
	}
	// Nominal sequence: 10s, 20s, 40s, 80s (jitter ±20% must stay in band).
	wantNominal := []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second, 80 * time.Second}
	for i, got := range sleepOrder {
		nominal := wantNominal[i]
		lo := nominal - nominal/5
		hi := nominal + nominal/5
		if got < lo || got > hi {
			t.Errorf("sleep[%d]=%s outside jitter band [%s, %s] (nominal %s)", i, got, lo, hi, nominal)
		}
		if i > 0 && got <= sleepOrder[i-1]/2 {
			t.Errorf("sleep[%d]=%s did not grow from previous %s", i, got, sleepOrder[i-1])
		}
	}
}

func TestRunStartupRefreshWithBackoff_CapsAtFiveMinutes(t *testing.T) {
	calls := restoreUpdaterHooks(t)

	var mu sync.Mutex
	fetches := 0
	fetchModelsFromRemoteFn = func(ctx context.Context) (*staticModelsJSON, string) {
		mu.Lock()
		defer mu.Unlock()
		fetches++
		if fetches <= 12 {
			return nil, "" // long failure streak: 10,20,40,80,160,300(cap hits here)
		}
		return staticCatalog(t), "https://example.test/models.json"
	}

	startupSleep = func(ctx context.Context, d time.Duration) error {
		*calls = append(*calls, d)
		return nil
	}

	runStartupRefreshWithBackoff(context.Background())

	slept := *calls
	if len(slept) != 12 {
		t.Fatalf("expected 12 retry sleeps, got %d", len(slept))
	}
	for i, got := range slept {
		if got > startupRetryMaxDelay+startupRetryMaxDelay/5 {
			t.Errorf("sleep[%d]=%s exceeds 5min cap + jitter", i, got)
		}
	}
	if last := slept[len(slept)-1]; last < startupRetryMaxDelay-startupRetryMaxDelay/5 {
		t.Errorf("final sleep %s should be near the 5min cap (jitter floor 4m)", last)
	}
}

func TestRunStartupRefreshWithBackoff_StopsOnContextCancel(t *testing.T) {
	calls := restoreUpdaterHooks(t)

	fetchModelsFromRemoteFn = func(ctx context.Context) (*staticModelsJSON, string) {
		return nil, "" // always fail
	}

	ctx, cancel := context.WithCancel(context.Background())
	startupSleep = func(ctx context.Context, d time.Duration) error {
		cancel() // simulate shutdown during the first backoff wait
		return ctx.Err()
	}

	runStartupRefreshWithBackoff(ctx)
	if len(*calls) != 0 {
		t.Fatalf("unexpected sleeps after cancel: %v", *calls)
	}
}

func TestRunStartupRefreshWithBackoff_LogsFailedAttempts(t *testing.T) {
	restoreUpdaterHooks(t)

	var mu sync.Mutex
	fetches := 0
	fetchModelsFromRemoteFn = func(ctx context.Context) (*staticModelsJSON, string) {
		mu.Lock()
		defer mu.Unlock()
		fetches++
		if fetches <= 1 {
			return nil, ""
		}
		return staticCatalog(t), "https://example.test/models.json"
	}

	var buf bytes.Buffer
	log.SetOutput(&buf)
	log.SetLevel(log.ErrorLevel)
	t.Cleanup(func() {
		log.SetOutput(log.StandardLogger().Out)
		log.SetLevel(log.InfoLevel)
	})

	startupSleep = func(ctx context.Context, d time.Duration) error { return nil }

	runStartupRefreshWithBackoff(context.Background())

	out := buf.String()
	if !strings.Contains(out, "startup model refresh failed (attempt 1)") {
		t.Errorf("error-level log for failed attempt missing, got: %q", out)
	}
	if !strings.Contains(out, "retrying in") {
		t.Errorf("retry delay missing from error log, got: %q", out)
	}
}

func TestTryPeriodicRefresh_LogsFailure(t *testing.T) {
	restoreUpdaterHooks(t)

	fetchModelsFromRemoteFn = func(ctx context.Context) (*staticModelsJSON, string) {
		return nil, ""
	}

	var buf bytes.Buffer
	log.SetOutput(&buf)
	log.SetLevel(log.ErrorLevel)
	t.Cleanup(func() {
		log.SetOutput(log.StandardLogger().Out)
		log.SetLevel(log.InfoLevel)
	})

	tryPeriodicRefresh(context.Background())

	if out := buf.String(); !strings.Contains(out, "periodic model refresh failed") {
		t.Errorf("error-level log for periodic failure missing, got: %q", out)
	}
}

func TestJitteredDelay_ZeroAndSmall(t *testing.T) {
	if got := jitteredDelay(0); got != 0 {
		t.Errorf("jitteredDelay(0)=%s, want 0", got)
	}
	if got := jitteredDelay(time.Nanosecond); got != time.Nanosecond {
		t.Errorf("jitteredDelay(1ns)=%s, want 1ns (no sub-spread floor)", got)
	}
	for range 200 {
		got := jitteredDelay(10 * time.Second)
		if got < 8*time.Second || got > 12*time.Second {
			t.Fatalf("jitteredDelay(10s)=%s outside ±20%% band", got)
		}
	}
}
