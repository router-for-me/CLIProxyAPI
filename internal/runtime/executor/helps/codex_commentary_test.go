package helps

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
)

func TestCodexCommentaryPolicyDefaultsAndSnapshots(t *testing.T) {
	t.Setenv("CPA_VISIBLE_CODEX_COMMENTARY", "")
	if translatorcommon.VisibleCodexCommentary(WithCodexCommentaryPolicy(nil, nil)) {
		t.Fatal("commentary must remain hidden by default")
	}
	cfg := &config.Config{}
	cfg.Client.Claude.ShowCodexCommentary = true
	inFlight := WithCodexCommentaryPolicy(context.Background(), cfg)
	cfg.Client.Claude.ShowCodexCommentary = false
	if !translatorcommon.VisibleCodexCommentary(inFlight) {
		t.Fatal("config reload changed an in-flight stream")
	}
	if translatorcommon.VisibleCodexCommentary(WithCodexCommentaryPolicy(context.Background(), cfg)) {
		t.Fatal("new request did not use the changed policy")
	}
}

func TestCodexCommentaryPolicyEnvironmentCompatibility(t *testing.T) {
	t.Setenv("CPA_VISIBLE_CODEX_COMMENTARY", "1")
	if !translatorcommon.VisibleCodexCommentary(WithCodexCommentaryPolicy(context.Background(), &config.Config{})) {
		t.Fatal("legacy opt-in environment variable was ignored")
	}
}
