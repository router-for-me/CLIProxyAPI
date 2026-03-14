package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gin "github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/cluster"
	proxyconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v6/sdk/access"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func newClusterTestServer(t *testing.T, cfg *proxyconfig.Config) *Server {
	t.Helper()

	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}

	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: 0\n"), 0o644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	if cfg == nil {
		cfg = &proxyconfig.Config{}
	}
	cfg.AuthDir = authDir
	cfg.Port = 0
	cfg.Debug = true
	cfg.RemoteManagement.AllowRemote = true
	if len(cfg.SDKConfig.APIKeys) == 0 {
		cfg.SDKConfig = sdkconfig.SDKConfig{APIKeys: []string{"test-api-key"}}
	}

	authManager := coreauth.NewManager(nil, nil, nil)
	accessManager := sdkaccess.NewManager()
	clusterService := cluster.NewService(cfg)
	return NewServer(cfg, authManager, accessManager, configPath, WithClusterService(clusterService))
}

func TestManagementClusterState(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "local-secret")

	server := newClusterTestServer(t, &proxyconfig.Config{
		Cluster: proxyconfig.ClusterConfig{
			Enabled:      true,
			NodeID:       "node-a",
			AdvertiseURL: "http://node-a.example.com",
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/v0/management/cluster/state", nil)
	req.Header.Set("Authorization", "Bearer local-secret")
	req.RemoteAddr = "127.0.0.1:12345"

	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if got := payload["node_id"]; got != "node-a" {
		t.Fatalf("node_id = %v", got)
	}
	if got := payload["advertise_url"]; got != "http://node-a.example.com" {
		t.Fatalf("advertise_url = %v", got)
	}
	if _, ok := payload["version"]; !ok {
		t.Fatalf("version missing from payload: %+v", payload)
	}
	if len(payload) != 3 {
		t.Fatalf("payload keys = %+v, want only node_id/advertise_url/version", payload)
	}
}

func TestManagementClusterStateErrors(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "local-secret")

	tests := []struct {
		name       string
		cfg        *proxyconfig.Config
		wantStatus int
		wantBody   string
	}{
		{
			name: "cluster disabled",
			cfg: &proxyconfig.Config{
				Cluster: proxyconfig.ClusterConfig{
					Enabled: false,
				},
			},
			wantStatus: http.StatusConflict,
			wantBody:   "cluster_disabled",
		},
		{
			name: "cluster missing advertise url",
			cfg: &proxyconfig.Config{
				Cluster: proxyconfig.ClusterConfig{
					Enabled: true,
					NodeID:  "node-a",
				},
			},
			wantStatus: http.StatusConflict,
			wantBody:   "cluster_invalid",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			server := newClusterTestServer(t, tc.cfg)
			req := httptest.NewRequest(http.MethodGet, "/v0/management/cluster/state", nil)
			req.Header.Set("Authorization", "Bearer local-secret")
			req.RemoteAddr = "127.0.0.1:12345"

			rr := httptest.NewRecorder()
			server.engine.ServeHTTP(rr, req)

			if rr.Code != tc.wantStatus {
				t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), tc.wantBody) {
				t.Fatalf("body = %s, want substring %q", rr.Body.String(), tc.wantBody)
			}
		})
	}
}

func TestManagementTargetProxyConfigYAML(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "local-secret")

	const peerManagementKey = "peer-management-secret"
	var receivedBody string
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/config.yaml" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.URL.RawQuery; got != "mode=raw" {
			t.Fatalf("raw query = %q", got)
		}
		if got := r.Header.Get("X-Management-Key"); got != peerManagementKey {
			t.Fatalf("X-Management-Key = %q", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("io.ReadAll() error = %v", err)
		}
		receivedBody = string(body)
		w.Header().Set("Content-Type", "application/yaml")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("proxied-config"))
	}))
	defer peer.Close()

	server := newClusterTestServer(t, &proxyconfig.Config{
		Cluster: proxyconfig.ClusterConfig{
			Enabled:      true,
			NodeID:       "node-a",
			AdvertiseURL: "http://node-a.example.com",
			Nodes: []proxyconfig.ClusterNode{
				{
					ID:            "node-b",
					Enabled:       true,
					ManagementURL: peer.URL,
					ManagementKey: peerManagementKey,
					APIKeys:       []string{"peer-public-key"},
					ModelListMode: proxyconfig.ClusterModelListModeBlack,
				},
			},
		},
	})

	req := httptest.NewRequest(http.MethodPut, "/v0/management/config.yaml?target=node-b&mode=raw", strings.NewReader("port: 9000\n"))
	req.Header.Set("Authorization", "Bearer local-secret")
	req.Header.Set("Content-Type", "application/yaml")
	req.RemoteAddr = "127.0.0.1:12345"

	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	if receivedBody != "port: 9000\n" {
		t.Fatalf("proxied body = %q", receivedBody)
	}
	if rr.Body.String() != "proxied-config" {
		t.Fatalf("response body = %q", rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); !strings.Contains(got, "application/yaml") {
		t.Fatalf("content-type = %q", got)
	}
}

func TestManagementTargetProxyAuthFiles(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "local-secret")

	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/auth-files" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.URL.RawQuery; got != "scope=all" {
			t.Fatalf("raw query = %q", got)
		}
		_, _ = w.Write([]byte(`{"files":[{"name":"peer-auth.json"}]}`))
	}))
	defer peer.Close()

	server := newClusterTestServer(t, &proxyconfig.Config{
		Cluster: proxyconfig.ClusterConfig{
			Enabled:      true,
			NodeID:       "node-a",
			AdvertiseURL: "http://node-a.example.com",
			Nodes: []proxyconfig.ClusterNode{
				{
					ID:            "node-b",
					Enabled:       true,
					ManagementURL: peer.URL,
					ManagementKey: "peer-management-secret",
					APIKeys:       []string{"peer-public-key"},
					ModelListMode: proxyconfig.ClusterModelListModeBlack,
				},
			},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/v0/management/auth-files?target=node-b&scope=all", nil)
	req.Header.Set("Authorization", "Bearer local-secret")
	req.RemoteAddr = "127.0.0.1:12345"

	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	if strings.TrimSpace(rr.Body.String()) != `{"files":[{"name":"peer-auth.json"}]}` {
		t.Fatalf("response body = %s", rr.Body.String())
	}
}

func TestManagementTargetProxyTargetErrors(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "local-secret")

	server := newClusterTestServer(t, &proxyconfig.Config{
		Cluster: proxyconfig.ClusterConfig{
			Enabled:      true,
			NodeID:       "node-a",
			AdvertiseURL: "http://node-a.example.com",
			Nodes: []proxyconfig.ClusterNode{
				{
					ID:            "node-disabled",
					Enabled:       false,
					ManagementURL: "http://peer.example.com",
					ManagementKey: "peer-management-secret",
					APIKeys:       []string{"peer-public-key"},
					ModelListMode: proxyconfig.ClusterModelListModeBlack,
				},
			},
		},
	})

	tests := []struct {
		name       string
		target     string
		wantStatus int
		wantBody   string
	}{
		{name: "unknown target", target: "node-missing", wantStatus: http.StatusNotFound, wantBody: "unknown_target"},
		{name: "disabled target", target: "node-disabled", wantStatus: http.StatusConflict, wantBody: "target_disabled"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v0/management/config?target="+tc.target, nil)
			req.Header.Set("Authorization", "Bearer local-secret")
			req.RemoteAddr = "127.0.0.1:12345"

			rr := httptest.NewRecorder()
			server.engine.ServeHTTP(rr, req)

			if rr.Code != tc.wantStatus {
				t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), tc.wantBody) {
				t.Fatalf("body = %s, want substring %q", rr.Body.String(), tc.wantBody)
			}
		})
	}
}
