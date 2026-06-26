package helps

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/config"
	homekv "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/home"
)

func TestSetCodexCacheRequiredHomeUnavailableReturnsError(t *testing.T) {
	homekv.SetCurrent(homekv.New(config.HomeConfig{Enabled: false}))
	t.Cleanup(homekv.ClearCurrent)

	errSet := SetCodexCacheRequired(context.Background(), "cpa:codex:prompt-cache:test", CodexCache{
		ID:     "cache-id",
		Expire: time.Now().Add(time.Hour),
	})
	if errSet == nil {
		t.Fatal("SetCodexCacheRequired() error = nil, want home kv unavailable error")
	}
	if !strings.Contains(errSet.Error(), "home kv store unavailable") {
		t.Fatalf("SetCodexCacheRequired() error = %v, want home kv store unavailable", errSet)
	}
}
