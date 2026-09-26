package management

import (
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestNormalizeAPIKeyCredentialGroupsKeepsConfiguredKeysOnly(t *testing.T) {
	got := normalizeAPIKeyCredentialGroups(map[string][]string{
		" client-a ": {" team-a ", "team-a", "team-b", ""},
		"removed":    {"team-c"},
	}, []string{"client-a", "client-b"})
	want := map[string][]string{"client-a": {"team-a", "team-b"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeAPIKeyCredentialGroups() = %#v, want %#v", got, want)
	}
}

func TestNormalizeAPIKeyCredentialGroupsPreservesExplicitEmptyRestriction(t *testing.T) {
	got := normalizeAPIKeyCredentialGroups(map[string][]string{
		"client-a": {"", "   "},
	}, []string{"client-a", "client-b"})
	want := map[string][]string{"client-a": {}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeAPIKeyCredentialGroups() = %#v, want %#v", got, want)
	}
}

func TestPruneAPIKeyCredentialGroupsAfterAPIKeyRemoval(t *testing.T) {
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeys: []string{"client-b"},
			APIKeyCredentialGroups: map[string][]string{
				"client-a": {"team-a"},
				"client-b": {"team-b"},
			},
		},
	}
	handler := NewHandlerWithoutConfigFilePath(cfg, nil)

	handler.pruneAPIKeyCredentialGroups()

	want := map[string][]string{"client-b": {"team-b"}}
	if !reflect.DeepEqual(cfg.APIKeyCredentialGroups, want) {
		t.Fatalf("APIKeyCredentialGroups = %#v, want %#v", cfg.APIKeyCredentialGroups, want)
	}
}
