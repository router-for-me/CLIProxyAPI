package management

import (
	"testing"

	minimaxauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/minimax"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// TestBuildMinimaxAuthRecordPersistsRegion locks in that a saved credential keeps
// the region it was issued for. The executor and the management UI both route on
// it, so losing it silently sends requests to the other region's host.
func TestBuildMinimaxAuthRecordPersistsRegion(t *testing.T) {
	storage := &minimaxauth.MinimaxTokenStorage{
		Type:         "minimax",
		Region:       minimaxauth.RegionCN,
		AccessToken:  "access-1",
		RefreshToken: "refresh-1",
		BaseURL:      minimaxauth.APIBaseURLCN,
		Expired:      "2026-10-08T00:00:00Z",
	}
	record := buildMinimaxAuthRecord(minimaxauth.RegionCN, &minimaxauth.AuthBundle{
		Region:    minimaxauth.RegionCN,
		TokenData: &minimaxauth.TokenData{AccessToken: "access-1"},
	}, storage)

	if record.Provider != minimaxauth.ProviderCN {
		t.Fatalf("provider = %q", record.Provider)
	}
	if got, _ := record.Metadata["region"].(string); got != minimaxauth.RegionCN {
		t.Fatalf("metadata region = %q, want %q", got, minimaxauth.RegionCN)
	}
	if got := record.Attributes["region"]; got != minimaxauth.RegionCN {
		t.Fatalf("attributes region = %q", got)
	}
	if got := record.Attributes["base_url"]; got != minimaxauth.APIBaseURLCN {
		t.Fatalf("attributes base_url = %q", got)
	}
	// The Claude-compatible base must be stamped so the executor does not have
	// to re-derive it for a file-backed credential.
	if got := record.Attributes["claude_base"]; got != minimaxauth.ResolveClaudeBaseURL(minimaxauth.RegionCN) {
		t.Fatalf("claude_base = %q", got)
	}
	if got, _ := record.Metadata["auth_kind"].(string); got != coreauth.AuthKindOAuth {
		t.Fatalf("auth_kind = %q", got)
	}
}

// TestMinimaxAuthRecordCarriesResourceURL ensures a resource URL returned by the
// authorization server is preserved so it can override the region default.
func TestMinimaxAuthRecordCarriesResourceURL(t *testing.T) {
	storage := &minimaxauth.MinimaxTokenStorage{
		Region:      minimaxauth.RegionGlobal,
		AccessToken: "access-1",
		BaseURL:     "https://api.minimax.io",
		ResourceURL: "https://api.minimaxi.com",
	}
	record := buildMinimaxAuthRecord(minimaxauth.RegionCN, &minimaxauth.AuthBundle{
		Region:    minimaxauth.RegionGlobal,
		TokenData: &minimaxauth.TokenData{AccessToken: "access-1"},
	}, storage)
	if got, _ := record.Metadata["resource_url"].(string); got != "https://api.minimaxi.com" {
		t.Fatalf("resource_url = %q", got)
	}
}

// TestRequestMinimaxTokenRejectsUnknownRegionIsNotNeeded documents that an
// unrecognized region falls back to the global default rather than erroring, so
// the management UI never receives an unusable authorization URL.
func TestRequestMinimaxTokenNormalizesRegion(t *testing.T) {
	if got := minimaxauth.NormalizeRegion("CN"); got != minimaxauth.RegionCN {
		t.Fatalf("NormalizeRegion(CN) = %q", got)
	}
	if got := minimaxauth.NormalizeRegion("nonsense"); got != minimaxauth.RegionGlobal {
		t.Fatalf("NormalizeRegion(nonsense) = %q", got)
	}
	if got := minimaxauth.NormalizeRegion(""); got != minimaxauth.RegionGlobal {
		t.Fatalf("NormalizeRegion(empty) = %q", got)
	}
}
