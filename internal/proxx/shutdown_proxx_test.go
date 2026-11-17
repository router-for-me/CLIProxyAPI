package proxx

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestDrainDefault(t *testing.T) {
	cfg := &config.Config{}
	applyDrainDefault(cfg)
	if cfg.ShutdownTimeoutSeconds != config.DefaultProxxShutdownTimeoutSeconds {
		t.Fatalf("unset shutdown-timeout = %d, want %d", cfg.ShutdownTimeoutSeconds, config.DefaultProxxShutdownTimeoutSeconds)
	}
	for _, v := range []int{-1, 5, 300} {
		cfg = &config.Config{}
		cfg.ShutdownTimeoutSeconds = v
		applyDrainDefault(cfg)
		if cfg.ShutdownTimeoutSeconds != v {
			t.Fatalf("explicit shutdown-timeout %d changed to %d", v, cfg.ShutdownTimeoutSeconds)
		}
	}
}

func TestShutdownBudget(t *testing.T) {
	cases := map[int]time.Duration{-1: 30 * time.Second, 5: 30 * time.Second, 120: 135 * time.Second}
	for v, want := range cases {
		cfg := &config.Config{}
		cfg.ShutdownTimeoutSeconds = v
		if got := ShutdownBudget(cfg); got != want {
			t.Errorf("ShutdownBudget(%d) = %s, want %s", v, got, want)
		}
	}
}

func TestShutdownContextWatchdog(t *testing.T) {
	fired := make(chan struct{}, 1)
	oldExit, oldGrace := forceExit, shutdownGrace
	forceExit = func() { fired <- struct{}{} }
	defer func() { forceExit, shutdownGrace = oldExit, oldGrace }()

	// Cancelled in time: the watchdog never fires.
	shutdownGrace = -29*time.Second - 900*time.Millisecond // budget 30s -> fires after 100ms
	_, cancel := ShutdownContext(&config.Config{})
	cancel()
	select {
	case <-fired:
		t.Fatal("watchdog fired after cancel")
	case <-time.After(300 * time.Millisecond):
	}

	// Overrun: it fires.
	_, cancel = ShutdownContext(&config.Config{})
	defer cancel()
	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog did not fire")
	}
}
