package api

import (
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestEffectiveSDKConfigMirrorsModelFallback(t *testing.T) {
	cfg := &config.Config{}
	cfg.Routing.ModelFallback = config.ModelFallbackConfig{Enabled: true, Chains: map[string][]string{"a": {"b"}}}
	sdkCfg := effectiveSDKConfig(cfg)
	if !reflect.DeepEqual(sdkCfg.ModelFallback, cfg.Routing.ModelFallback) {
		t.Fatalf("handler config ModelFallback = %+v, want %+v", sdkCfg.ModelFallback, cfg.Routing.ModelFallback)
	}
}
