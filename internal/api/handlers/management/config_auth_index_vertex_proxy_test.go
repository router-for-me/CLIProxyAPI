package management

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"gopkg.in/yaml.v3"
)

func TestInjectV8APIKeyAuthIndexesVertexProxyURLDedup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	rawYAML := `config-version: 8
port: 8317
api-keys:
  vertex:
    - name: vertex-group
      base-url: https://vertex.example.invalid
      keys:
        - api-key: sk-vertex-shared
          proxy-url: http://proxy-a.invalid:8080
        - api-key: sk-vertex-shared
          proxy-url: http://proxy-b.invalid:8080
`
	if err := os.WriteFile(path, []byte(rawYAML), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(cfg.VertexCompatAPIKey) != 2 {
		t.Fatalf("expected 2 vertex entries, got %d", len(cfg.VertexCompatAPIKey))
	}

	synth := synthesizer.NewConfigSynthesizer()
	synthCtx := &synthesizer.SynthesisContext{
		Config:      cfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	}
	auths, err := synth.Synthesize(synthCtx)
	if err != nil {
		t.Fatalf("synthesize auths: %v", err)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	expectedByProxy := make(map[string]string)
	for _, a := range auths {
		if a == nil || a.Provider != "vertex" {
			continue
		}
		if _, errReg := manager.Register(context.Background(), a); errReg != nil {
			t.Fatalf("register auth: %v", errReg)
		}
		idx := a.EnsureIndex()
		if idx == "" {
			t.Fatalf("auth %s has empty EnsureIndex", a.ID)
		}
		expectedByProxy[a.ProxyURL] = idx
	}
	if len(expectedByProxy) != 2 {
		t.Fatalf("expected 2 distinct vertex auths by proxy-url, got %d", len(expectedByProxy))
	}

	h := &Handler{cfg: cfg, configFilePath: path, authManager: manager}

	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(rawYAML), &doc); err != nil {
		t.Fatalf("unmarshal yaml: %v", err)
	}
	root := doc.Content[0]

	h.mu.Lock()
	h.injectV8APIKeyAuthIndexesLocked(root, []byte(rawYAML))
	h.mu.Unlock()

	keysNode := configV8Node(root, []string{"api-keys", "vertex"})
	if keysNode == nil || keysNode.Kind != yaml.SequenceNode || len(keysNode.Content) != 1 {
		t.Fatalf("expected 1 vertex group in yaml")
	}
	groupKeys := configV8Node(keysNode.Content[0], []string{"keys"})
	if groupKeys == nil || groupKeys.Kind != yaml.SequenceNode || len(groupKeys.Content) != 2 {
		t.Fatalf("expected 2 vertex keys in yaml")
	}

	idxA := yamlMapScalar(groupKeys.Content[0], "auth_index")
	idxB := yamlMapScalar(groupKeys.Content[1], "auth_index")
	if idxA == "" || idxB == "" {
		t.Fatalf("expected non-empty auth_index values, got %q and %q", idxA, idxB)
	}
	if idxA == idxB {
		t.Fatalf("expected distinct auth_index for vertex keys with different proxy-url, both got %q", idxA)
	}
	if wantA := expectedByProxy["http://proxy-a.invalid:8080"]; idxA != wantA {
		t.Fatalf("proxy-a auth_index mismatch: got %q, want %q", idxA, wantA)
	}
	if wantB := expectedByProxy["http://proxy-b.invalid:8080"]; idxB != wantB {
		t.Fatalf("proxy-b auth_index mismatch: got %q, want %q", idxB, wantB)
	}
}
