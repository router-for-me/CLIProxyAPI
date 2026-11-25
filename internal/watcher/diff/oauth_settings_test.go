package diff

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestDiffOAuthSettingsChanges(t *testing.T) {
	oldMap := map[string][]config.OAuthModelSetting{
		"codex": {
			{Name: "gpt-6-sol", MaxContextLength: 272000},
		},
		"vertex": {
			{Name: "gemini-2.5-pro", MaxContextLength: 1048576},
		},
	}

	newMap := map[string][]config.OAuthModelSetting{
		"codex": {
			{Name: "gpt-6-sol", MaxContextLength: 524288},
		},
		"claude": {
			{Name: "claude-sonnet-4-5-20250929", MaxContextLength: 200000},
		},
	}

	changes, affected := DiffOAuthSettingsChanges(oldMap, newMap)

	// vertex removed, claude added, codex updated
	expectContains := func(slice []string, expected string) {
		t.Helper()
		found := false
		for _, s := range slice {
			if strings.Contains(s, expected) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected slice to contain %q, but got %#v", expected, slice)
		}
	}

	expectContains(changes, "oauth-settings[vertex]: removed")
	expectContains(changes, "oauth-settings[claude]: added (1 entries)")
	expectContains(changes, "oauth-settings[codex]: updated (1 -> 1 entries)")

	expectContains(affected, "vertex")
	expectContains(affected, "claude")
	expectContains(affected, "codex")
}

func TestDiffOAuthSettingsChanges_Reordering(t *testing.T) {
	oldMap := map[string][]config.OAuthModelSetting{
		"codex": {
			{Name: "gpt-6-sol", MaxContextLength: 524288},
			{Name: "deepseek-v4-flash", MaxContextLength: 1048576},
		},
	}

	newMap := map[string][]config.OAuthModelSetting{
		"codex": {
			{Name: "deepseek-v4-flash", MaxContextLength: 1048576},
			{Name: "gpt-6-sol", MaxContextLength: 524288},
		},
	}

	changes, affected := DiffOAuthSettingsChanges(oldMap, newMap)
	if len(changes) == 0 {
		t.Fatalf("expected changes when rules are reordered, got none")
	}
	if len(affected) == 0 || affected[0] != "codex" {
		t.Fatalf("expected codex to be affected by reordering: %#v", affected)
	}
}

func TestDiffOAuthSettingsCompletionLimits(t *testing.T) {
	settings := func(limit int) map[string][]config.OAuthModelSetting {
		return map[string][]config.OAuthModelSetting{"codex": {{Name: "model", MaxCompletionTokens: limit}}}
	}
	if _, affected := DiffOAuthSettingsChanges(settings(4096), settings(8192)); len(affected) != 1 || affected[0] != "codex" {
		t.Fatalf("affected channels = %v, want [codex]", affected)
	}
	if _, affected := DiffOAuthSettingsChanges(settings(0), settings(-1)); len(affected) != 0 {
		t.Fatalf("affected channels = %v, want none", affected)
	}
}
