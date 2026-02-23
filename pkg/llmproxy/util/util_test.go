package util

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/config"
)

func TestSetLogLevel(t *testing.T) {
	cfg := &config.Config{Debug: true}
	SetLogLevel(cfg)
	// No easy way to assert without global state check, but ensures no panic

	cfg.Debug = false
	SetLogLevel(cfg)
}

func TestResolveAuthDir(t *testing.T) {
	home, _ := os.UserHomeDir()
	cases := []struct {
		dir  string
		want string
	}{
		{"", ""},
		{"/abs/path", "/abs/path"},
		{"~", home},
		{"~/test", filepath.Join(home, "test")},
	}
	for _, tc := range cases {
		got, err := ResolveAuthDir(tc.dir)
		if err != nil {
			t.Errorf("ResolveAuthDir(%q) error: %v", tc.dir, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ResolveAuthDir(%q) = %q, want %q", tc.dir, got, tc.want)
		}
	}
}

type mockStore struct {
	items []int
}

func (m *mockStore) List(ctx context.Context) ([]int, error) {
	return m.items, nil
}

func TestCountAuthFiles(t *testing.T) {
	store := &mockStore{items: []int{1, 2, 3}}
	if got := CountAuthFiles(context.Background(), store); got != 3 {
		t.Errorf("CountAuthFiles() = %d, want 3", got)
	}
}
