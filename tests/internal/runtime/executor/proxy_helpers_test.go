package executor_test

import (
	"context"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	execpkg "github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor"
	sdkexec "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// Table-driven tests to verify local-only bridge URL policy via public executor surface.
func TestBridgeURL_Validation_LocalOnlyAndEnv(t *testing.T) {
	cases := []struct {
		name            string
		envURL          string
		cfgURL          string
		expectErrSubstr string
	}{
		{name: "env remote rejects", envURL: "http://10.0.0.5:35331", cfgURL: "", expectErrSubstr: "local-only"},
		{name: "cfg remote rejects", envURL: "", cfgURL: "http://10.0.0.5:35331", expectErrSubstr: "local-only"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.envURL != "" {
				t.Setenv("CLAUDE_AGENT_SDK_URL", tc.envURL)
			}
			// Disallow remote by default
			t.Setenv("CLAUDE_AGENT_SDK_ALLOW_REMOTE", "")

			cfg := &config.Config{}
			cfg.PythonAgent.Enabled = true
			cfg.PythonAgent.BaseURL = tc.cfgURL

			exec := execpkg.NewZhipuExecutor(cfg)
			// Minimal request; should fail fast before HTTP due to URL validation
			_, err := exec.Execute(context.Background(), nil, sdkexec.Request{Model: "glm-4.6", Payload: []byte(`{"messages":[]}`)}, sdkexec.Options{})
			if err == nil {
				t.Fatalf("expected error due to remote URL policy")
			}
			if !strings.Contains(strings.ToLower(err.Error()), tc.expectErrSubstr) {
				t.Fatalf("expected error to contain %q, got: %v", tc.expectErrSubstr, err)
			}
		})
	}
}
