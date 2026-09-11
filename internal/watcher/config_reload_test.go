package watcher

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestRequiresAuthRefreshForRoutingStrategyChanges(t *testing.T) {
	tests := []struct {
		name string
		old  string
		new  string
		want bool
	}{
		{name: "unset to round-robin", old: "", new: "round-robin", want: false},
		{name: "round-robin aliases", old: "round-robin", new: "ROUND-ROBIN", want: false},
		{name: "round-robin to weighted", old: "round-robin", new: "wrr", want: true},
		{name: "weighted to fill-first", old: "weighted-round-robin", new: "fillfirst", want: true},
		{name: "fill-first to round-robin", old: "fill-first", new: "", want: true},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			oldConfig := &config.Config{Routing: config.RoutingConfig{Strategy: testCase.old}}
			newConfig := &config.Config{Routing: config.RoutingConfig{Strategy: testCase.new}}
			if got := requiresAuthRefresh(oldConfig, newConfig); got != testCase.want {
				t.Fatalf("requiresAuthRefresh() = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestRequiresAuthRefreshForExistingMaterialChanges(t *testing.T) {
	base := &config.Config{}
	tests := []struct {
		name   string
		mutate func(*config.Config)
	}{
		{name: "force model prefix", mutate: func(cfg *config.Config) { cfg.ForceModelPrefix = true }},
		{name: "request retry", mutate: func(cfg *config.Config) { cfg.RequestRetry = 2 }},
		{name: "max retry interval", mutate: func(cfg *config.Config) { cfg.MaxRetryInterval = 2 }},
		{name: "max retry credentials", mutate: func(cfg *config.Config) { cfg.MaxRetryCredentials = 2 }},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			changed := *base
			testCase.mutate(&changed)
			if !requiresAuthRefresh(base, &changed) {
				t.Fatal("expected material change to require auth refresh")
			}
		})
	}
}
