package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestQoderVPCConfigDecoding(t *testing.T) {
	var cfg Config
	if err := yaml.Unmarshal([]byte("qoder:\n  vpc-endpoint: acme.vpc.qoder.com.cn\n"), &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", err)
	}
	if cfg.Qoder.VPCEndpoint != "acme.vpc.qoder.com.cn" {
		t.Fatalf("Qoder.VPCEndpoint = %q", cfg.Qoder.VPCEndpoint)
	}
}
