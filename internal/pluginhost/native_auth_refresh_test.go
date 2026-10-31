//go:build cgo && (linux || darwin || freebsd)

package pluginhost

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	openaihandlers "github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const nativeAuthRefreshProvider = "native-auth-refresh"
const nativeAuthRefreshMarker = "native-auth-refresh-c-shared-schema9-v1"
const nativeAuthRefreshFuture = "2099-01-01T00:00:00Z"
const nativeAuthRefreshPast = "2000-01-01T00:00:00Z"

type nativeAuthRefreshCounters struct {
	Marker            string `json:"marker"`
	Parse             int    `json:"parse"`
	Refresh           int    `json:"refresh"`
	ConsistencyErrors int    `json:"consistency_errors"`
	Shutdown          int    `json:"shutdown"`
}

type nativeAuthRefreshUpstreamRequest struct {
	Path          string `json:"path"`
	Stream        bool   `json:"stream"`
	Authorization string `json:"authorization"`
	RefreshToken  string `json:"refresh_token,omitempty"`
	Body          string `json:"body"`
}

type nativeAuthRefreshLoopback struct {
	mu             sync.Mutex
	requests       []nativeAuthRefreshUpstreamRequest
	refreshStarted chan struct{}
	refreshRelease chan struct{}
}

func (u *nativeAuthRefreshLoopback) snapshot() []nativeAuthRefreshUpstreamRequest {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]nativeAuthRefreshUpstreamRequest(nil), u.requests...)
}

func (u *nativeAuthRefreshLoopback) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, errRead := io.ReadAll(r.Body)
	if errRead != nil {
		http.Error(w, errRead.Error(), http.StatusBadRequest)
		return
	}
	var payload struct {
		Model        string `json:"model"`
		Stream       bool   `json:"stream"`
		RefreshToken string `json:"refresh_token"`
		RefreshMode  string `json:"refresh_mode"`
	}
	if errDecode := json.Unmarshal(body, &payload); errDecode != nil {
		http.Error(w, errDecode.Error(), http.StatusBadRequest)
		return
	}
	u.mu.Lock()
	u.requests = append(u.requests, nativeAuthRefreshUpstreamRequest{Path: r.URL.Path, Stream: payload.Stream, Authorization: r.Header.Get("Authorization"), RefreshToken: payload.RefreshToken, Body: string(body)})
	u.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/oauth/token" {
		if u.refreshStarted != nil {
			close(u.refreshStarted)
			<-u.refreshRelease
		}
		if payload.RefreshMode == "transient" {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, "synthetic refresh unavailable")
			return
		}
		if payload.RefreshToken != "synthetic-refresh-expired" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, "synthetic invalid_grant")
			return
		}
		_, _ = fmt.Fprintf(w, `{"access_token":"synthetic-access-rotated","refresh_token":"synthetic-refresh-rotated","expired":%q}`, nativeAuthRefreshFuture)
		return
	}
	bearer := r.Header.Get("Authorization")
	if bearer != "Bearer synthetic-access-valid" && bearer != "Bearer synthetic-access-rotated" {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"synthetic expired bearer reached inference"}}`)
		return
	}
	if r.URL.Path == "/v1/images/generations" {
		image := base64.StdEncoding.EncodeToString([]byte("synthetic-native-image"))
		if payload.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "event: image_generation.completed\ndata: {\"type\":\"image_generation.completed\",\"b64_json\":%q}\n\n", image)
			w.(http.Flusher).Flush()
			return
		}
		_, _ = fmt.Fprintf(w, `{"created":1,"data":[{"b64_json":%q}]}`, image)
		return
	}
	if r.URL.Path == "/v1/quota" || r.URL.Path == "/v1/quota/reset" {
		_, _ = io.WriteString(w, `{}`)
		return
	}
	if r.URL.Path != "/v1/chat/completions" {
		http.Error(w, "unexpected synthetic endpoint", http.StatusNotFound)
		return
	}
	if payload.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"id\":\"synthetic-native-chat\",\"object\":\"chat.completion.chunk\",\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"synthetic-native-ok\"},\"finish_reason\":null}]}\n\n", payload.Model)
		_, _ = fmt.Fprintf(w, "data: {\"id\":\"synthetic-native-chat\",\"object\":\"chat.completion.chunk\",\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", payload.Model)
		w.(http.Flusher).Flush()
		return
	}
	_, _ = fmt.Fprintf(w, `{"id":"synthetic-native-chat","object":"chat.completion","model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":"synthetic-native-ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`, payload.Model)
}

func nativeAuthRefreshNativeCounters(t *testing.T, client pluginClient) nativeAuthRefreshCounters {
	t.Helper()
	raw, errCall := client.Call(context.Background(), "test.counters", nil)
	if errCall != nil {
		t.Fatal(errCall)
	}
	var counters nativeAuthRefreshCounters
	if errDecode := json.Unmarshal(raw, &counters); errDecode != nil {
		t.Fatal(errDecode)
	}
	if counters.Marker != nativeAuthRefreshMarker {
		t.Fatalf("native marker = %q", counters.Marker)
	}
	return counters
}

func nativeAuthRefreshCredential(id, state, upstreamURL string) map[string]any {
	expiry := nativeAuthRefreshPast
	if state == "valid" {
		expiry = nativeAuthRefreshFuture
	}
	return map[string]any{"type": nativeAuthRefreshProvider, "id": id, "access_token": "synthetic-access-" + state, "refresh_token": "synthetic-refresh-" + state, "expired": expiry, "base_url": upstreamURL + "/v1", "refresh_url": upstreamURL + "/oauth/token"}
}

func nativeAuthRefreshWriteJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, errMarshal := json.MarshalIndent(value, "", "  ")
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	if errWrite := os.WriteFile(path, append(raw, '\n'), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
}

func nativeAuthRefreshParse(t *testing.T, host *Host, authDir string, credential map[string]any) *coreauth.Auth {
	t.Helper()
	id := credential["id"].(string)
	path := filepath.Join(authDir, id)
	nativeAuthRefreshWriteJSON(t, path, credential)
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	auth, handled, errParse := host.ParseAuth(context.Background(), pluginapi.AuthParseRequest{Provider: nativeAuthRefreshProvider, Path: path, FileName: id, RawJSON: raw})
	if errParse != nil || !handled || auth == nil {
		t.Fatalf("native ParseAuth handled=%v error=%v", handled, errParse)
	}
	wantExpiry, errTime := time.Parse(time.RFC3339, credential["expired"].(string))
	if errTime != nil {
		t.Fatal(errTime)
	}
	gotExpiry, known := auth.AccessTokenExpirationTime()
	if !known || !gotExpiry.Equal(wantExpiry) || auth.Metadata["access_token"] != credential["access_token"] || auth.Metadata["refresh_token"] != credential["refresh_token"] || auth.Attributes["api_key"] != credential["access_token"] || auth.AuthKind() != coreauth.AuthKindOAuth {
		t.Fatal("native parse did not preserve truthful token views and OAuth kind")
	}
	return auth
}

func nativeAuthRefreshManager(t *testing.T, cfg *config.Config, host *Host, auth *coreauth.Auth) *coreauth.Manager {
	t.Helper()
	store := sdkauth.NewFileTokenStore()
	store.SetBaseDir(cfg.AuthDir)
	manager := coreauth.NewManager(store, &coreauth.RoundRobinSelector{}, nil)
	manager.SetConfig(cfg)
	manager.SetRetryConfig(0, 0, 1)
	wrapped := NewPluginRefreshCompatExecutor(runtimeexecutor.NewOpenAICompatExecutor(nativeAuthRefreshProvider, cfg), host, cfg)
	if !IsPluginRefreshCompatExecutor(wrapped) {
		t.Fatal("native inference executor is not plugin refresh compatible")
	}
	manager.RegisterExecutor(wrapped)
	registry.GetGlobalRegistry().RegisterClient(auth.ID, nativeAuthRefreshProvider, []*registry.ModelInfo{
		{ID: "native-auth-refresh-chat", Object: "model", OwnedBy: nativeAuthRefreshProvider, Type: "openai-compatibility"},
		{ID: "native-auth-refresh-image", Object: "model", OwnedBy: nativeAuthRefreshProvider, Type: registry.OpenAIImageModelType},
	})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatal(errRegister)
	}
	host.SetAuthManager(manager)
	return manager
}

func nativeAuthRefreshHandlerServer(t *testing.T, cfg *config.Config, manager *coreauth.Manager, host *Host) *httptest.Server {
	t.Helper()
	base := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
	base.SetPluginHost(host)
	openai := openaihandlers.NewOpenAIAPIHandler(base)
	router := gin.New()
	router.POST("/v1/chat/completions", openai.ChatCompletions)
	router.POST("/v1/images/generations", openai.ImagesGenerations)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return server
}

type nativeAuthRefreshNativeFixture struct {
	root, library, librarySHA string
	cfg                       *config.Config
	host                      *Host
	client                    pluginClient
}

func nativeAuthRefreshBuildNativeHost(t *testing.T) nativeAuthRefreshNativeFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		t.Setenv(key, "")
	}
	root := t.TempDir()
	if artifactDir := os.Getenv("NATIVE_AUTH_REFRESH_ARTIFACT_DIR"); artifactDir != "" {
		if errMkdir := os.MkdirAll(artifactDir, 0o700); errMkdir != nil {
			t.Fatal(errMkdir)
		}
		var errTemp error
		root, errTemp = os.MkdirTemp(artifactDir, "run-")
		if errTemp != nil {
			t.Fatal(errTemp)
		}
	}
	pluginDir := filepath.Join(root, "plugins", runtime.GOOS, runtime.GOARCH)
	authDir := filepath.Join(root, "auths")
	for _, path := range []string{pluginDir, authDir} {
		if errMkdir := os.MkdirAll(path, 0o700); errMkdir != nil {
			t.Fatal(errMkdir)
		}
	}
	library := filepath.Join(pluginDir, nativeAuthRefreshProvider+PluginExtension(runtime.GOOS))
	build := exec.Command("go", "build", "-buildmode=c-shared", "-o", library, ".")
	build.Dir = filepath.Join("testdata", "native_auth_refresh")
	build.Env = append(os.Environ(), "CGO_ENABLED=1", "GOWORK=off", "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local")
	output, errBuild := build.CombinedOutput()
	t.Logf("native build cwd=%s command=%v output=%s error=%v", build.Dir, build.Args, output, errBuild)
	if errBuild != nil {
		t.Fatal(errBuild)
	}
	binary, errRead := os.ReadFile(library)
	if errRead != nil {
		t.Fatal(errRead)
	}
	librarySHA := fmt.Sprintf("%x", sha256.Sum256(binary))
	enabled := true
	cfg := &config.Config{AuthDir: authDir, Plugins: config.PluginsConfig{Enabled: true, Dir: filepath.Join(root, "plugins"), Configs: map[string]config.PluginInstanceConfig{nativeAuthRefreshProvider: {Enabled: &enabled}}}}
	host := New()
	t.Cleanup(host.ShutdownAll)
	host.ApplyConfig(context.Background(), cfg)
	if !host.PluginRegistered(nativeAuthRefreshProvider) || !host.HasAuthProvider(nativeAuthRefreshProvider) {
		t.Fatal("real native auth plugin did not register")
	}
	host.mu.Lock()
	client := host.loaded[nativeAuthRefreshProvider].client
	host.mu.Unlock()
	marker, errMarker := client.Call(context.Background(), "test.marker", nil)
	if errMarker != nil || string(marker) != nativeAuthRefreshMarker {
		t.Fatalf("native marker=%q error=%v", marker, errMarker)
	}
	t.Logf("native artifact=%s sha256=%s marker=%s", library, librarySHA, marker)
	return nativeAuthRefreshNativeFixture{root: root, library: library, librarySHA: librarySHA, cfg: cfg, host: host, client: client}
}

func TestNativeAuthRefreshDefaultPolicyRequiresExplicitRefresh(t *testing.T) {
	native := nativeAuthRefreshBuildNativeHost(t)
	root, cfg, host, client := native.root, native.cfg, native.host, native.client
	authDir := cfg.AuthDir
	upstream := &nativeAuthRefreshLoopback{}
	providerServer := httptest.NewServer(upstream)
	defer providerServer.Close()
	receipt := map[string]any{"purpose": "native default refresh policy requires explicit refresh", "marker": nativeAuthRefreshMarker, "native_library": native.library, "native_library_sha256": native.librarySHA, "auto_refresh_started": false, "requests": []map[string]any{}}
	t.Cleanup(func() {
		receipt["native_counters"] = nativeAuthRefreshNativeCounters(t, client)
		receipt["upstream_requests"] = upstream.snapshot()
		nativeAuthRefreshWriteJSON(t, filepath.Join(root, "receipt.json"), receipt)
		t.Logf("native receipt=%s", filepath.Join(root, "receipt.json"))
	})
	cases := []struct {
		name, path, body, marker string
	}{
		{"buffered_chat", "/v1/chat/completions", `{"model":"native-auth-refresh-chat","messages":[{"role":"user","content":"synthetic"}],"stream":false}`, "synthetic-native-ok"},
		{"streaming_chat", "/v1/chat/completions", `{"model":"native-auth-refresh-chat","messages":[{"role":"user","content":"synthetic"}],"stream":true}`, "synthetic-native-ok"},
		{"buffered_image", "/v1/images/generations", `{"model":"native-auth-refresh-image","prompt":"synthetic","stream":false}`, base64.StdEncoding.EncodeToString([]byte("synthetic-native-image"))},
		{"streaming_image", "/v1/images/generations", `{"model":"native-auth-refresh-image","prompt":"synthetic","stream":true}`, "image_generation.completed"},
	}
	for _, state := range []string{"valid", "expired"} {
		t.Run(state, func(t *testing.T) {
			credential := nativeAuthRefreshCredential("native-auth-refresh-"+state+".json", state, providerServer.URL)
			auth := nativeAuthRefreshParse(t, host, authDir, credential)
			manager := nativeAuthRefreshManager(t, cfg, host, auth)
			server := nativeAuthRefreshHandlerServer(t, cfg, manager, host)
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					beforePlugin := nativeAuthRefreshNativeCounters(t, client)
					beforeUpstream := len(upstream.snapshot())
					resp, errRequest := server.Client().Post(server.URL+tc.path, "application/json", strings.NewReader(tc.body))
					if errRequest != nil {
						t.Fatal(errRequest)
					}
					body, errBody := io.ReadAll(resp.Body)
					_ = resp.Body.Close()
					if errBody != nil {
						t.Fatal(errBody)
					}
					afterPlugin := nativeAuthRefreshNativeCounters(t, client)
					afterUpstream := len(upstream.snapshot())
					row := map[string]any{"credential": state, "case": tc.name, "path": tc.path, "status": resp.StatusCode, "body": string(body), "upstream_delta": afterUpstream - beforeUpstream, "native_refresh_delta": afterPlugin.Refresh - beforePlugin.Refresh}
					receipt["requests"] = append(receipt["requests"].([]map[string]any), row)
					encoded, _ := json.Marshal(row)
					t.Logf("DEFAULT_POLICY %s", encoded)
					if afterPlugin.Refresh != beforePlugin.Refresh {
						t.Fatal("default-policy request unexpectedly refreshed")
					}
					if state == "valid" {
						if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), tc.marker) || afterUpstream-beforeUpstream != 1 {
							t.Fatal("valid native fixture did not reach exactly one loopback inference request")
						}
					} else if resp.StatusCode == http.StatusOK || (!strings.Contains(string(body), "auth_not_found") && !strings.Contains(string(body), "auth_unavailable")) || afterUpstream != beforeUpstream {
						t.Fatal("truthful expired auth did not reject selection before refresh and inference")
					}
				})
			}
			current, found := manager.GetByID(auth.ID)
			if !found || current.Metadata["access_token"] != credential["access_token"] || current.Metadata["expired"] != credential["expired"] {
				t.Fatal("inference mutated the default-policy credential")
			}
			if state != "expired" {
				return
			}
			beforePlugin := nativeAuthRefreshNativeCounters(t, client)
			beforeUpstream := len(upstream.snapshot())
			refreshed, errRefresh := manager.ForceRefreshAuth(context.Background(), auth.ID)
			if errRefresh != nil || refreshed == nil {
				t.Fatalf("explicit operator-refresh control error=%v", errRefresh)
			}
			afterPlugin := nativeAuthRefreshNativeCounters(t, client)
			if afterPlugin.Refresh-beforePlugin.Refresh != 1 || len(upstream.snapshot())-beforeUpstream != 1 || afterPlugin.ConsistencyErrors != 0 {
				t.Fatal("explicit operator-refresh control did not call native auth.refresh and loopback token endpoint exactly once")
			}
			storedRaw, errStored := os.ReadFile(filepath.Join(authDir, auth.FileName))
			if errStored != nil {
				t.Fatal(errStored)
			}
			var stored map[string]any
			if errDecode := json.Unmarshal(storedRaw, &stored); errDecode != nil {
				t.Fatal(errDecode)
			}
			current, found = manager.GetByID(auth.ID)
			for key, expected := range map[string]any{"access_token": "synthetic-access-rotated", "refresh_token": "synthetic-refresh-rotated", "expired": nativeAuthRefreshFuture} {
				if !found || current.Metadata[key] != expected || stored[key] != expected {
					t.Fatalf("rotated %s did not agree in manager and saved file", key)
				}
			}
			if current.Attributes["api_key"] != "synthetic-access-rotated" || !current.HasValidAccessToken(time.Now()) {
				t.Fatal("explicit refresh did not produce a fresh compat bearer")
			}
			receipt["explicit_operator_refresh"] = map[string]any{"native_refresh_delta": afterPlugin.Refresh - beforePlugin.Refresh, "upstream_refresh_delta": len(upstream.snapshot()) - beforeUpstream, "persisted_access_token": stored["access_token"], "persisted_refresh_token": stored["refresh_token"], "persisted_expired": stored["expired"], "not_lazy_refresh": true}
			resp, errRequest := server.Client().Post(server.URL+cases[0].path, "application/json", strings.NewReader(cases[0].body))
			if errRequest != nil {
				t.Fatal(errRequest)
			}
			body, errBody := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if errBody != nil || resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "synthetic-native-ok") {
				t.Fatalf("rotated inference status=%d body=%s error=%v", resp.StatusCode, body, errBody)
			}
			seen := upstream.snapshot()
			if seen[len(seen)-1].Authorization != "Bearer synthetic-access-rotated" {
				t.Fatal("inference did not send the committed rotated token")
			}
			receipt["after_explicit_refresh"] = map[string]any{"status": resp.StatusCode, "body": string(body), "authorization": seen[len(seen)-1].Authorization}
		})
	}
}

func TestNativeAuthRefreshOnDemand(t *testing.T) {
	native := nativeAuthRefreshBuildNativeHost(t)
	upstream := &nativeAuthRefreshLoopback{}
	providerServer := httptest.NewServer(upstream)
	t.Cleanup(providerServer.Close)
	credential := nativeAuthRefreshCredential("native-auth-refresh-on-demand.json", "expired", providerServer.URL)
	credential["test_refresh_policy"] = "on-demand"
	auth := nativeAuthRefreshParse(t, native.host, native.cfg.AuthDir, credential)

	rawCredential, errMarshal := json.Marshal(credential)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	request, errMarshal := json.Marshal(pluginapi.AuthParseRequest{Provider: nativeAuthRefreshProvider, FileName: auth.FileName, RawJSON: rawCredential})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	wire, errCall := native.client.Call(context.Background(), "auth.parse", request)
	if errCall != nil {
		t.Fatal(errCall)
	}
	var parsed struct {
		OK     bool `json:"ok"`
		Result struct {
			Handled bool
			Auth    struct{ RefreshPolicy string }
		} `json:"result"`
	}
	if errDecode := json.Unmarshal(wire, &parsed); errDecode != nil || !parsed.OK || !parsed.Result.Handled || parsed.Result.Auth.RefreshPolicy != "on-demand" {
		t.Fatalf("native Auth.RefreshPolicy wire opt-in=%s error=%v", wire, errDecode)
	}
	manager := nativeAuthRefreshManager(t, native.cfg, native.host, auth)
	server := nativeAuthRefreshHandlerServer(t, native.cfg, manager, native.host)
	if counters := nativeAuthRefreshNativeCounters(t, native.client); counters.Refresh != 0 || len(upstream.snapshot()) != 0 {
		t.Fatal("idle opted-in credential refreshed before demand")
	}
	receipt := map[string]any{"purpose": "intended native on-demand refresh regression", "refresh_policy": parsed.Result.Auth.RefreshPolicy, "native_library": native.library, "native_library_sha256": native.librarySHA, "auto_refresh_started": false, "requests": []map[string]any{}}
	t.Cleanup(func() {
		receipt["native_counters"] = nativeAuthRefreshNativeCounters(t, native.client)
		receipt["upstream_requests"] = upstream.snapshot()
		nativeAuthRefreshWriteJSON(t, filepath.Join(native.root, "receipt.json"), receipt)
		t.Logf("native lazy receipt=%s", filepath.Join(native.root, "receipt.json"))
	})
	cases := []struct{ name, path, body, marker string }{
		{"buffered_chat", "/v1/chat/completions", `{"model":"native-auth-refresh-chat","messages":[{"role":"user","content":"synthetic"}],"stream":false}`, "synthetic-native-ok"},
		{"streaming_chat", "/v1/chat/completions", `{"model":"native-auth-refresh-chat","messages":[{"role":"user","content":"synthetic"}],"stream":true}`, "synthetic-native-ok"},
		{"buffered_image", "/v1/images/generations", `{"model":"native-auth-refresh-image","prompt":"synthetic","stream":false}`, base64.StdEncoding.EncodeToString([]byte("synthetic-native-image"))},
		{"streaming_image", "/v1/images/generations", `{"model":"native-auth-refresh-image","prompt":"synthetic","stream":true}`, "image_generation.completed"},
	}
	for i, tc := range cases {
		demand := i + 1
		resp, errRequest := server.Client().Post(server.URL+tc.path, "application/json", strings.NewReader(tc.body))
		if errRequest != nil {
			t.Fatal(errRequest)
		}
		response, errRead := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if errRead != nil {
			t.Fatal(errRead)
		}
		counters := nativeAuthRefreshNativeCounters(t, native.client)
		seen := upstream.snapshot()
		row := map[string]any{"demand": demand, "case": tc.name, "status": resp.StatusCode, "body": string(response), "native_refresh_calls": counters.Refresh, "upstream_requests": len(seen)}
		receipt["requests"] = append(receipt["requests"].([]map[string]any), row)
		encoded, _ := json.Marshal(row)
		t.Logf("LAZY_REFRESH %s", encoded)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expired on-demand auth must refresh before inference: HTTP status=%d, want 200; body=%s", resp.StatusCode, response)
		}
		if !strings.Contains(string(response), tc.marker) || counters.Refresh != 1 || counters.ConsistencyErrors != 0 || len(seen) != demand+1 {
			t.Fatalf("demand %d must use one native refresh and exactly one inference per request: counters=%+v upstream=%+v", demand, counters, seen)
		}
		if seen[0].Path != "/oauth/token" || seen[0].RefreshToken != "synthetic-refresh-expired" {
			t.Fatal("token endpoint was not called once before inference")
		}
		for _, inference := range seen[1:] {
			if inference.Path != "/v1/chat/completions" && inference.Path != "/v1/images/generations" || inference.Authorization != "Bearer synthetic-access-rotated" {
				t.Fatalf("inference must use only the committed rotated bearer: %+v", inference)
			}
		}
		storedRaw, errStored := os.ReadFile(filepath.Join(native.cfg.AuthDir, auth.FileName))
		if errStored != nil {
			t.Fatal(errStored)
		}
		var stored map[string]any
		if errDecode := json.Unmarshal(storedRaw, &stored); errDecode != nil {
			t.Fatal(errDecode)
		}
		current, found := manager.GetByID(auth.ID)
		for key, expected := range map[string]any{"access_token": "synthetic-access-rotated", "refresh_token": "synthetic-refresh-rotated", "expired": nativeAuthRefreshFuture} {
			if !found || current.Metadata[key] != expected || stored[key] != expected {
				t.Fatalf("rotated %s must agree in metadata and saved storage", key)
			}
		}
		if current.Attributes["api_key"] != "synthetic-access-rotated" || !current.HasValidAccessToken(time.Now()) || stored["test_refresh_policy"] != "on-demand" {
			t.Fatal("refresh must preserve policy and a fresh coherent compat bearer")
		}
		receipt["persisted_auth"] = stored
	}
}
