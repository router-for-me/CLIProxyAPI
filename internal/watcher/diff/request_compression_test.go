package diff

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestBuildConfigChangeDetailsRequestCompression(t *testing.T) {
	oldCfg := &config.Config{SDKConfig: config.SDKConfig{RequestCompression: "off", RequestCompressionMinSize: "16k"}}
	newCfg := &config.Config{SDKConfig: config.SDKConfig{RequestCompression: "auto", RequestCompressionMinSize: "32k"}}

	details := BuildConfigChangeDetails(oldCfg, newCfg)
	expectContains(t, details, "request-compression: off -> auto")
	expectContains(t, details, "request-compression-min-size: 16k -> 32k")
}

func TestBuildConfigChangeDetailsRequestCompressionIgnoresExplicitDefault(t *testing.T) {
	oldCfg := &config.Config{}
	newCfg := &config.Config{SDKConfig: config.SDKConfig{RequestCompressionMinSize: "16k"}}

	for _, detail := range BuildConfigChangeDetails(oldCfg, newCfg) {
		if strings.HasPrefix(detail, "request-compression") {
			t.Fatalf("unexpected effective compression change: %s", detail)
		}
	}
}
