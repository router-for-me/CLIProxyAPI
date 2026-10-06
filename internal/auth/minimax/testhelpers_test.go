package minimax

import (
	"encoding/json"
	"net/http/httptest"
	"os"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// cliproxyauthAuthStub is a minimal Auth carrying only metadata, matching how
// file-backed credentials are reloaded at runtime.
type cliproxyauthAuthStub struct {
	metadata map[string]any
	provider string
}

func (s *cliproxyauthAuthStub) asAuth() *cliproxyauth.Auth {
	provider := s.provider
	if provider == "" {
		provider = "minimax"
	}
	return &cliproxyauth.Auth{Provider: provider, Metadata: s.metadata}
}

// resolveClaudeURL is a thin wrapper so tests can exercise URL resolution
// without importing the executor package.
func resolveClaudeURL(s *cliproxyauthAuthStub) string {
	return ResolveClaudeUpstreamURL(s.asAuth())
}

// readJSONFile decodes a JSON file into a generic map.
func readJSONFile(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := make(map[string]any)
	if err = json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// newTestClient returns a DeviceFlowClient whose OAuth endpoints point at a
// local test server, so tests never reach the live MiniMax account host.
func newTestClient(srv *httptest.Server) *DeviceFlowClient {
	client := &DeviceFlowClient{httpClient: srv.Client(), region: RegionGlobal}
	client.SetEndpoints(srv.URL+"/oauth2/device/code", srv.URL+"/oauth2/token")
	return client
}
