package cliproxy

import (
	"context"
	"runtime"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// Every routing config change installs a fresh routing selector; when session
// affinity is enabled that selector owns a background cache cleanup goroutine.
// The Service must stop the selector it previously installed, otherwise each
// reload leaks one goroutine for the process lifetime.
func TestApplyManagerConfigStopsReplacedServiceAffinitySelector(t *testing.T) {
	service := &Service{coreManager: coreauth.NewManager(nil, nil, nil)}

	apply := func(ttl string) {
		t.Helper()
		cfg := &internalconfig.Config{Routing: internalconfig.RoutingConfig{
			SessionAffinity:    true,
			SessionAffinityTTL: ttl,
		}}
		if !service.applyManagerConfig(context.Background(), configCommit{cfg: cfg}) {
			t.Fatalf("applyManagerConfig(%q) failed", ttl)
		}
	}

	apply("1h") // installs the first service-owned affinity selector
	baseline := runtime.NumGoroutine()
	apply("2h")
	apply("3h")

	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > baseline {
		if time.Now().After(deadline) {
			t.Fatalf("goroutines = %d, want baseline %d: replaced affinity selectors were not stopped",
				runtime.NumGoroutine(), baseline)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The Service tracks the selector it installed so that only service-owned
// affinity selectors are stopped on replacement; the freshly installed
// selector becomes the new tracked instance.
func TestApplyManagerConfigTracksServiceInstalledSelector(t *testing.T) {
	service := &Service{coreManager: coreauth.NewManager(nil, nil, nil)}

	cfg := &internalconfig.Config{Routing: internalconfig.RoutingConfig{SessionAffinity: true}}
	if !service.applyManagerConfig(context.Background(), configCommit{cfg: cfg}) {
		t.Fatal("first applyManagerConfig failed")
	}
	if _, ok := service.appliedRoutingSelector.(*coreauth.SessionAffinitySelector); !ok {
		t.Fatalf("appliedRoutingSelector = %T, want *SessionAffinitySelector", service.appliedRoutingSelector)
	}

	// Disabling affinity swaps in a plain selector and stops the old one; the
	// newly installed selector must stay service-owned and unstopped.
	cfg = &internalconfig.Config{}
	if !service.applyManagerConfig(context.Background(), configCommit{cfg: cfg}) {
		t.Fatal("second applyManagerConfig failed")
	}
	if _, ok := service.appliedRoutingSelector.(*coreauth.RoundRobinSelector); !ok {
		t.Fatalf("appliedRoutingSelector = %T, want *RoundRobinSelector", service.appliedRoutingSelector)
	}
	if current := service.coreManager.Selector(); current != service.appliedRoutingSelector {
		t.Fatal("active selector diverged from the service-installed selector")
	}
}
