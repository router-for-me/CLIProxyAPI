package cliproxy

import (
	"os"
	"path/filepath"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestSequentialRoutingSelector(t *testing.T) {
	authDir := t.TempDir()
	reserve := 7.5
	state := normalizedRoutingRuntimeState(&internalconfig.Config{
		AuthDir: authDir,
		Routing: internalconfig.RoutingConfig{
			Strategy:                 "sequential",
			SequentialReservePercent: &reserve,
			SessionAffinity:          true,
		},
	})
	if state.strategy != "sequential" || state.sequentialReserve != reserve {
		t.Fatalf("state = %+v", state)
	}
	if state.sessionAffinity {
		t.Fatal("sequential routing must not keep session affinity")
	}
	if want := filepath.Join(authDir, sequentialStateFile); state.sequentialStatePath != want {
		t.Fatalf("state path = %q, want %q", state.sequentialStatePath, want)
	}
	if _, ok := newRoutingSelector(state).(*coreauth.SequentialSelector); !ok {
		t.Fatalf("selector type = %T, want *auth.SequentialSelector", newRoutingSelector(state))
	}
}

func TestSequentialRoutingReserveDefaults(t *testing.T) {
	invalid := 100.0
	for name, value := range map[string]*float64{"omitted": nil, "out of range": &invalid} {
		state := normalizedRoutingRuntimeState(&internalconfig.Config{
			Routing: internalconfig.RoutingConfig{Strategy: "sequential-fill", SequentialReservePercent: value},
		})
		if state.strategy != "sequential" || state.sequentialReserve != coreauth.DefaultSequentialReservePercent {
			t.Fatalf("%s: state = %+v", name, state)
		}
	}
	zero := 0.0
	state := normalizedRoutingRuntimeState(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{Strategy: "sequential", SequentialReservePercent: &zero},
	})
	if state.sequentialReserve != 0 {
		t.Fatalf("explicit zero reserve = %v", state.sequentialReserve)
	}
}

func TestSequentialRoutingWithHomeKeepsNoStateFile(t *testing.T) {
	cfg := &internalconfig.Config{AuthDir: t.TempDir(), Routing: internalconfig.RoutingConfig{Strategy: "sequential"}}
	cfg.Home.Enabled = true
	if state := normalizedRoutingRuntimeState(cfg); state.sequentialStatePath != "" {
		t.Fatalf("state path = %q, want none with Home", state.sequentialStatePath)
	}
}

func TestSequentialRoutingConfigLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte("config-version: 8\nrouting:\n  strategy: \"sequential\"\n  sequential-reserve-percent: 3\n")
	if errWrite := os.WriteFile(path, data, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg, errLoad := internalconfig.LoadConfig(path)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	if cfg.Routing.Strategy != "sequential" || cfg.Routing.SequentialReservePercent == nil || *cfg.Routing.SequentialReservePercent != 3 {
		t.Fatalf("routing = %+v", cfg.Routing)
	}
}
