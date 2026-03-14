package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestConfig(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(strings.TrimSpace(body)+"\n"), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	return path
}

func TestLoadConfigOptionalClusterDefaults(t *testing.T) {
	t.Parallel()

	path := writeTestConfig(t, "port: 8317")
	cfg, err := LoadConfigOptional(path, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}

	if cfg.Cluster.Enabled {
		t.Fatal("cluster.enabled default should be false")
	}
	if cfg.Cluster.PollIntervalSeconds != DefaultClusterPollSeconds {
		t.Fatalf("cluster.poll-interval-seconds = %d, want %d", cfg.Cluster.PollIntervalSeconds, DefaultClusterPollSeconds)
	}
	if cfg.Cluster.ForwardTimeoutSeconds != DefaultClusterTimeoutSeconds {
		t.Fatalf("cluster.forward-timeout-seconds = %d, want %d", cfg.Cluster.ForwardTimeoutSeconds, DefaultClusterTimeoutSeconds)
	}
	if cfg.Cluster.PreferLocal {
		t.Fatal("cluster.prefer-local default should be false")
	}
	if cfg.Cluster.RegisterNodePrefixAlias {
		t.Fatal("cluster.register-node-prefix-alias default should be false")
	}
}

func TestLoadConfigOptionalClusterNormalizesValues(t *testing.T) {
	t.Parallel()

	path := writeTestConfig(t, `
cluster:
  enabled: true
  node-id: " node-a "
  advertise-url: " http://node-a.example.com/ "
  poll-interval-seconds: 0
  forward-timeout-seconds: -1
  nodes:
    - id: " node-b "
      enabled: true
      management-url: " https://node-b.example.com/admin/ "
      management-key: " peer-key "
      api-keys:
        - " key-1 "
        - ""
        - "key-1"
        - " key-2 "
      model-list-mode: ""
      model-list:
        - " model-a "
        - ""
        - "model-a"
        - " model-b "
`)
	cfg, err := LoadConfigOptional(path, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}

	if cfg.Cluster.NodeID != "node-a" {
		t.Fatalf("cluster.node-id = %q, want %q", cfg.Cluster.NodeID, "node-a")
	}
	if cfg.Cluster.AdvertiseURL != "http://node-a.example.com" {
		t.Fatalf("cluster.advertise-url = %q", cfg.Cluster.AdvertiseURL)
	}
	if cfg.Cluster.PollIntervalSeconds != DefaultClusterPollSeconds {
		t.Fatalf("cluster.poll-interval-seconds = %d, want default %d", cfg.Cluster.PollIntervalSeconds, DefaultClusterPollSeconds)
	}
	if cfg.Cluster.ForwardTimeoutSeconds != DefaultClusterTimeoutSeconds {
		t.Fatalf("cluster.forward-timeout-seconds = %d, want default %d", cfg.Cluster.ForwardTimeoutSeconds, DefaultClusterTimeoutSeconds)
	}
	if len(cfg.Cluster.Nodes) != 1 {
		t.Fatalf("cluster.nodes len = %d, want 1", len(cfg.Cluster.Nodes))
	}
	node := cfg.Cluster.Nodes[0]
	if node.ID != "node-b" {
		t.Fatalf("cluster.nodes[0].id = %q, want %q", node.ID, "node-b")
	}
	if node.ManagementURL != "https://node-b.example.com/admin" {
		t.Fatalf("cluster.nodes[0].management-url = %q", node.ManagementURL)
	}
	if node.ManagementKey != "peer-key" {
		t.Fatalf("cluster.nodes[0].management-key = %q, want trimmed value", node.ManagementKey)
	}
	if got := strings.Join(node.APIKeys, ","); got != "key-1,key-2" {
		t.Fatalf("cluster.nodes[0].api-keys = %q", got)
	}
	if node.ModelListMode != ClusterModelListModeBlack {
		t.Fatalf("cluster.nodes[0].model-list-mode = %q, want %q", node.ModelListMode, ClusterModelListModeBlack)
	}
	if got := strings.Join(node.ModelList, ","); got != "model-a,model-b" {
		t.Fatalf("cluster.nodes[0].model-list = %q", got)
	}
}

func TestLoadConfigOptionalClusterValidationErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name: "missing local node id",
			body: `
cluster:
  enabled: true
`,
			wantErr: "cluster.node-id is required",
		},
		{
			name: "missing advertise url",
			body: `
cluster:
  enabled: true
  node-id: "node-a"
`,
			wantErr: "cluster.advertise-url is required",
		},
		{
			name: "duplicate peer ids",
			body: `
cluster:
  enabled: true
  advertise-url: "http://node-a.example.com"
  node-id: "node-a"
  nodes:
    - id: "node-b"
      enabled: false
    - id: "node-b"
      enabled: false
`,
			wantErr: "duplicates peer",
		},
		{
			name: "local peer collision",
			body: `
cluster:
  enabled: true
  advertise-url: "http://node-a.example.com"
  node-id: "node-a"
  nodes:
    - id: "node-a"
      enabled: false
`,
			wantErr: "collides with peer id",
		},
		{
			name: "enabled peer missing management key",
			body: `
cluster:
  enabled: true
  advertise-url: "http://node-a.example.com"
  node-id: "node-a"
  nodes:
    - id: "node-b"
      enabled: true
      management-url: "http://node-b.example.com"
      api-keys: ["peer-public-key"]
`,
			wantErr: "management-key is required",
		},
		{
			name: "invalid model list mode",
			body: `
cluster:
  enabled: true
  advertise-url: "http://node-a.example.com"
  node-id: "node-a"
  nodes:
    - id: "node-b"
      enabled: false
      model-list-mode: "maybe"
`,
			wantErr: "model-list-mode must be",
		},
		{
			name: "invalid advertise url scheme",
			body: `
cluster:
  enabled: true
  node-id: "node-a"
  advertise-url: "ftp://node-a.example.com"
`,
			wantErr: "cluster.advertise-url",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			path := writeTestConfig(t, tc.body)
			_, err := LoadConfigOptional(path, false)
			if err == nil {
				t.Fatalf("LoadConfigOptional() error = nil, want substring %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("LoadConfigOptional() error = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}
