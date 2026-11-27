package engine

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	proxconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/prox/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/router"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

type fakeNext struct {
	has    bool
	called int
}

func (f *fakeNext) PickAuth(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
	f.called++
	return pluginapi.SchedulerPickResponse{AuthID: "next", Handled: true}, true, nil
}

func (f *fakeNext) HasScheduler() bool { return f.has }

type fakeHost struct{}

func (fakeHost) Roster() ([]router.RosterEntry, error) { return nil, nil }
func (fakeHost) Credential(string) ([]byte, error)     { return nil, nil }

func TestSchedulerFallsBackWhenDisabled(t *testing.T) {
	next := &fakeNext{has: true}
	s := &Scheduler{Engine: New(), Next: next}
	if !s.HasScheduler() {
		t.Fatal("expected HasScheduler from next")
	}
	resp, handled, err := s.PickAuth(context.Background(), pluginapi.SchedulerPickRequest{})
	if err != nil || !handled || resp.AuthID != "next" || next.called != 1 {
		t.Fatalf("expected delegation to next, got %+v handled=%v called=%d", resp, handled, next.called)
	}
}

func TestSchedulerInactiveWhenNothingEnabled(t *testing.T) {
	s := &Scheduler{Engine: New(), Next: &fakeNext{has: false}}
	if s.HasScheduler() {
		t.Fatal("expected no active scheduler")
	}
	if _, handled, _ := s.PickAuth(context.Background(), pluginapi.SchedulerPickRequest{}); handled {
		t.Fatal("expected unhandled pick")
	}
}

func TestSchedulerDelegatesWhenEngineDeclines(t *testing.T) {
	e := New()
	e.SetHost(fakeHost{})
	cfg := proxconfig.Default()
	cfg.StateDir = t.TempDir()
	e.Configure(cfg)
	defer e.Shutdown()
	next := &fakeNext{has: true}
	s := &Scheduler{Engine: e, Next: next}
	// No quota data yet: prox delegates, so the next scheduler decides.
	req := pluginapi.SchedulerPickRequest{Provider: "claude", Model: "claude-opus", Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "a", Provider: "claude"}}}
	resp, handled, _ := s.PickAuth(context.Background(), req)
	if !handled || resp.AuthID != "next" {
		t.Fatalf("expected fallback, got %+v", resp)
	}
	if !s.HasScheduler() {
		t.Fatal("enabled engine must report an active scheduler")
	}
}

func TestSettingsFromConfig(t *testing.T) {
	var node yaml.Node
	if err := yaml.Unmarshal([]byte("enabled: true\nmode: active\nstrategy: balanced\n"), &node); err != nil {
		t.Fatal(err)
	}
	on := true
	cfg := &config.Config{}
	cfg.Plugins.Configs = map[string]config.PluginInstanceConfig{proxconfig.PluginID: {Enabled: &on, Raw: *node.Content[0]}}
	enabled, raw := settingsFromConfig(cfg)
	if !enabled {
		t.Fatal("expected enabled")
	}
	decoded, err := proxconfig.Decode(raw)
	if err != nil || decoded.Mode != proxconfig.ModeActive || decoded.Strategy != proxconfig.StrategyBalanced {
		t.Fatalf("decode: %+v %v", decoded, err)
	}
	off := false
	cfg.Plugins.Configs[proxconfig.PluginID] = config.PluginInstanceConfig{Enabled: &off}
	if enabled, _ := settingsFromConfig(cfg); enabled {
		t.Fatal("expected disabled")
	}
}

func TestApplyDisableStopsRouting(t *testing.T) {
	e := New()
	e.SetHost(fakeHost{})
	e.Now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	if err := e.Apply(true, []byte("state_dir: "+t.TempDir()+"\n")); err != nil {
		t.Fatal(err)
	}
	if !e.Enabled() {
		t.Fatal("expected enabled")
	}
	if err := e.Apply(false, nil); err != nil {
		t.Fatal(err)
	}
	if e.Enabled() {
		t.Fatal("expected disabled")
	}
	e.Shutdown()
}

func TestWrapSchedulerPassThroughWhenDisabled(t *testing.T) {
	next := &fakeNext{has: true}
	if got := WrapScheduler(next); got != next {
		t.Fatalf("disabled prox must return next unchanged, got %T", got)
	}
}
