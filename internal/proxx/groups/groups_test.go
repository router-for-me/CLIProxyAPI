package groups

import (
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func TestNormalizeDropsInvalidEntries(t *testing.T) {
	got := Normalize([]config.ProxxModelGroup{
		{Name: " auto ", Strategy: "Round-Robin", Models: []string{" a ", "a", "", "free", "b"}},
		{Name: "auto", Models: []string{"c"}},
		{Name: "", Models: []string{"x"}},
		{Name: "empty"},
		{Name: "free", Strategy: "weird", Models: []string{"c"}},
	})
	want := []Group{
		{Name: "auto", Strategy: StrategyRoundRobin, Models: []string{"a", "b"}},
		{Name: "free", Strategy: StrategyOrdered, Models: []string{"c"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Normalize() = %+v, want %+v", got, want)
	}
}

func TestLookupSuffixAndMembers(t *testing.T) {
	t.Cleanup(func() { Apply(nil) })
	Apply(&config.Config{Proxx: config.ProxxConfig{ModelGroups: []config.ProxxModelGroup{
		{Name: "proxx-test-auto", Models: []string{"m1", "m2(low)"}},
		{Name: "proxx-test-rr", Strategy: "round-robin", Models: []string{"r1", "r2", "r3"}},
	}}})
	group, suffix, ok := Lookup("proxx-test-auto(high)")
	if !ok || suffix != "high" {
		t.Fatalf("Lookup() = %+v %q %v", group, suffix, ok)
	}
	if got := Members(group, suffix); !reflect.DeepEqual(got, []string{"m1(high)", "m2(low)"}) {
		t.Fatalf("Members() = %v", got)
	}
	if providers, isGroup := RequestProviders("proxx-test-auto"); !isGroup || !reflect.DeepEqual(providers, []string{Provider}) {
		t.Fatalf("RequestProviders() = %v %v", providers, isGroup)
	}
	if IsGroup("m1") {
		t.Fatal("member reported as group")
	}
	rr, _, _ := Lookup("proxx-test-rr")
	firsts := []string{Members(rr, "")[0], Members(rr, "")[0], Members(rr, "")[0], Members(rr, "")[0]}
	if !reflect.DeepEqual(firsts, []string{"r1", "r2", "r3", "r1"}) {
		t.Fatalf("round-robin starts = %v", firsts)
	}
	if providers := registry.GetGlobalRegistry().GetModelProviders("proxx-test-rr"); !reflect.DeepEqual(providers, []string{Provider}) {
		t.Fatalf("registry providers = %v", providers)
	}
}

func TestApplyHomeModeDisablesGroups(t *testing.T) {
	t.Cleanup(func() { Apply(nil) })
	cfg := &config.Config{Proxx: config.ProxxConfig{ModelGroups: []config.ProxxModelGroup{{Name: "proxx-test-home", Models: []string{"m"}}}}}
	cfg.Home.Enabled = true
	Apply(cfg)
	if IsGroup("proxx-test-home") {
		t.Fatal("groups must be disabled in Home mode")
	}
}

func TestHiddenGroupNotRegistered(t *testing.T) {
	t.Cleanup(func() { Apply(nil) })
	Apply(&config.Config{Proxx: config.ProxxConfig{ModelGroups: []config.ProxxModelGroup{{Name: "proxx-test-hidden", Hidden: true, Models: []string{"m"}}}}})
	if !IsGroup("proxx-test-hidden") {
		t.Fatal("hidden group must still route")
	}
	if providers := registry.GetGlobalRegistry().GetModelProviders("proxx-test-hidden"); len(providers) != 0 {
		t.Fatalf("hidden group registered: %v", providers)
	}
}
