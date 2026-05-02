package amp

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// These tests pin the AMP routing concurrency invariants the Phase C
// atomic.Pointer[routingTable] refactor must preserve:
//
//   1. Concurrent toggle of ForceModelMappings, restrictToLocalhost,
//      upstream URL, upstream API key, model mappings — all under
//      go test -race must show no data race.
//   2. Captured-pointer staleness — handlers that capture m.modelMapper
//      at registration time must observe model-mapping updates without
//      restart. Today this holds because UpdateMappings mutates in place.
//      After Phase C, the snapshot accessor must preserve this invariant.
//
// Run with: go test -race ./internal/api/modules/amp -run AmpRace

// ampStubConfig returns a Config with the given AmpCode payload that is
// safe to feed into OnConfigUpdated without triggering the upstream-proxy
// initialization code paths (which require a live HTTP target).
func ampStubConfig(s config.AmpCode) *config.Config {
	return &config.Config{AmpCode: s}
}

// newAmpForRace builds an AmpModule with the minimum mutable state primed
// so OnConfigUpdated's branches that require lastConfig != nil and
// modelMapper != nil execute. Avoids the full Register flow which needs a
// live upstream and Gin engine.
func newAmpForRace(t *testing.T) *AmpModule {
	t.Helper()
	m := New()
	m.lastConfig = &config.AmpCode{}
	m.modelMapper = NewModelMapper(nil)
	return m
}

func TestAmpRace_ForceModelMappings_ToggleUnderConcurrentReads(t *testing.T) {
	m := newAmpForRace(t)

	stop := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = m.forceModelMappings()
				}
			}
		}()
	}

	deadline := time.Now().Add(50 * time.Millisecond)
	for time.Now().Before(deadline) {
		_ = m.OnConfigUpdated(ampStubConfig(config.AmpCode{ForceModelMappings: true}))
		_ = m.OnConfigUpdated(ampStubConfig(config.AmpCode{ForceModelMappings: false}))
	}
	close(stop)
	wg.Wait()
}

func TestAmpRace_RestrictToLocalhost_ToggleUnderConcurrentReads(t *testing.T) {
	m := newAmpForRace(t)
	// First update primes lastConfig with the previous restrict value so
	// subsequent toggles fire setRestrictToLocalhost.
	_ = m.OnConfigUpdated(ampStubConfig(config.AmpCode{RestrictManagementToLocalhost: false}))

	stop := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = m.IsRestrictedToLocalhost()
				}
			}
		}()
	}

	deadline := time.Now().Add(50 * time.Millisecond)
	flip := false
	for time.Now().Before(deadline) {
		flip = !flip
		_ = m.OnConfigUpdated(ampStubConfig(config.AmpCode{RestrictManagementToLocalhost: flip}))
	}
	close(stop)
	wg.Wait()
}

func TestAmpRace_ModelMappings_UpdateUnderConcurrentReads(t *testing.T) {
	m := newAmpForRace(t)
	// Seed: foo -> claude-sonnet-4-5 (target with available providers).
	_ = m.OnConfigUpdated(ampStubConfig(config.AmpCode{
		ModelMappings: []config.AmpModelMapping{{From: "foo", To: "claude-sonnet-4-5"}},
	}))

	stop := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = m.modelMapper.MapModel("foo")
					_ = m.modelMapper.GetMappings()
				}
			}
		}()
	}

	deadline := time.Now().Add(50 * time.Millisecond)
	flip := false
	for time.Now().Before(deadline) {
		flip = !flip
		to := "claude-sonnet-4-5"
		if flip {
			to = "gemini-3-pro-preview"
		}
		_ = m.OnConfigUpdated(ampStubConfig(config.AmpCode{
			ModelMappings: []config.AmpModelMapping{{From: "foo", To: to}},
		}))
	}
	close(stop)
	wg.Wait()
}

func TestAmpRace_LastConfig_OnConfigUpdated_Concurrent(t *testing.T) {
	// Hammer OnConfigUpdated from multiple goroutines while reads happen
	// against m.forceModelMappings (configMu reader) and the mapper.
	m := newAmpForRace(t)

	stop := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = m.forceModelMappings()
					_ = m.IsRestrictedToLocalhost()
				}
			}
		}()
	}

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(seed int64) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					force := (seed % 2) == 0
					_ = m.OnConfigUpdated(ampStubConfig(config.AmpCode{
						ForceModelMappings:            force,
						RestrictManagementToLocalhost: !force,
					}))
					seed++
				}
			}
		}(int64(i))
	}

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// TestAmpStaleness_CapturedMapperSeesUpdates verifies that a ModelMapper
// pointer captured at handler-registration time observes mapping updates
// applied through OnConfigUpdated.
//
// This invariant holds today because OnConfigUpdated calls
// m.modelMapper.UpdateMappings, which mutates the mapper's internal state
// without changing the pointer. routes.go and fallback_handlers.go capture
// m.modelMapper at registration; they expect mutate-in-place semantics.
//
// Phase C swaps mutable AMP state behind atomic.Pointer[routingTable]. If
// that refactor swaps the modelMapper pointer along with the routing table,
// captured pointers in already-registered handlers go stale. This test
// catches that regression: it must keep passing after Phase C.
//
// We inspect mapper internals directly rather than going through MapModel,
// because MapModel filters via util.GetProviderName which depends on
// per-process provider registration that isn't set up here. The staleness
// invariant is purely about the mapper's internal table reflecting updates.
func TestAmpStaleness_CapturedMapperSeesUpdates(t *testing.T) {
	m := newAmpForRace(t)

	_ = m.OnConfigUpdated(ampStubConfig(config.AmpCode{
		ModelMappings: []config.AmpModelMapping{{From: "foo", To: "claude-sonnet-4-5"}},
	}))
	capturedMapper := m.modelMapper

	if got := capturedMapper.GetMappings()["foo"]; got != "claude-sonnet-4-5" {
		t.Fatalf("baseline mapping: want claude-sonnet-4-5, got %q", got)
	}

	// Hot-reload to a different target.
	_ = m.OnConfigUpdated(ampStubConfig(config.AmpCode{
		ModelMappings: []config.AmpModelMapping{{From: "foo", To: "gemini-3-pro-preview"}},
	}))

	if got := capturedMapper.GetMappings()["foo"]; got != "gemini-3-pro-preview" {
		t.Fatalf("captured mapper failed to see hot-reload update: want gemini-3-pro-preview, got %q", got)
	}

	// Mapping removed entirely.
	_ = m.OnConfigUpdated(ampStubConfig(config.AmpCode{ModelMappings: nil}))
	if got, ok := capturedMapper.GetMappings()["foo"]; ok {
		t.Fatalf("captured mapper failed to see mapping removal: still has foo -> %q", got)
	}
}

// TestAmpStaleness_CapturedMapperSeesRegexUpdates extends the staleness
// invariant to regex mappings. Reads mapper.regexps directly under the
// mapper's RWMutex (same-package access) since GetMappings() returns only
// the exact-match table.
func TestAmpStaleness_CapturedMapperSeesRegexUpdates(t *testing.T) {
	m := newAmpForRace(t)

	_ = m.OnConfigUpdated(ampStubConfig(config.AmpCode{
		ModelMappings: []config.AmpModelMapping{
			{From: `^claude-.*$`, To: "claude-sonnet-4-5", Regex: true},
		},
	}))
	capturedMapper := m.modelMapper

	firstRegexTarget := func() string {
		capturedMapper.mu.RLock()
		defer capturedMapper.mu.RUnlock()
		if len(capturedMapper.regexps) == 0 {
			return ""
		}
		return capturedMapper.regexps[0].to
	}

	if got := firstRegexTarget(); got != "claude-sonnet-4-5" {
		t.Fatalf("baseline regex target: want claude-sonnet-4-5, got %q", got)
	}

	_ = m.OnConfigUpdated(ampStubConfig(config.AmpCode{
		ModelMappings: []config.AmpModelMapping{
			{From: `^claude-.*$`, To: "gemini-3-pro-preview", Regex: true},
		},
	}))

	if got := firstRegexTarget(); got != "gemini-3-pro-preview" {
		t.Fatalf("captured mapper failed to see regex hot-reload: got %q", got)
	}
}

// Cheap belt-and-suspenders: counter-based check that concurrent reads
// observed at least one of each toggle state, so we know the parallel
// section actually exercised the contention path.
func TestAmpRace_ParallelObservedBothStates(t *testing.T) {
	m := newAmpForRace(t)

	var sawTrue, sawFalse atomic.Int32
	stop := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					if m.forceModelMappings() {
						sawTrue.Add(1)
					} else {
						sawFalse.Add(1)
					}
				}
			}
		}()
	}

	deadline := time.Now().Add(40 * time.Millisecond)
	flip := false
	for time.Now().Before(deadline) {
		flip = !flip
		_ = m.OnConfigUpdated(ampStubConfig(config.AmpCode{ForceModelMappings: flip}))
	}
	close(stop)
	wg.Wait()

	if sawTrue.Load() == 0 || sawFalse.Load() == 0 {
		t.Fatalf("expected both toggle states to be observed; sawTrue=%d sawFalse=%d", sawTrue.Load(), sawFalse.Load())
	}
}
