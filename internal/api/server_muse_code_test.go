package api

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	proxyconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func registeredRouteEngine(t *testing.T, server *Server, method, path string) *gin.Engine {
	t.Helper()
	engine := gin.New()
	found := false
	for _, route := range server.engine.Routes() {
		if route.Method == method && route.Path == path {
			engine.Handle(method, path, route.HandlerFunc)
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("route %s %s not registered on server engine", method, path)
	}
	return engine
}

func newReadyHomeCatalogClient(t *testing.T, initialPayload string) (*home.Client, func(string), func() string) {
	t.Helper()
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen: %v", errListen)
	}

	var payloadVal atomic.Value
	payloadVal.Store(initialPayload)
	var lastModelsReq atomic.Value
	lastModelsReq.Store("")

	done := make(chan struct{})
	var connections sync.WaitGroup
	go func() {
		defer close(done)
		for {
			conn, errAccept := listener.Accept()
			if errAccept != nil {
				return
			}
			connections.Add(1)
			go func() {
				defer connections.Done()
				defer func() {
					_ = conn.Close()
				}()
				reader := bufio.NewReader(conn)
				for {
					args, errRead := readRESPArrayOfBulkStrings(reader)
					if errRead != nil || len(args) == 0 {
						return
					}
					cmd := strings.ToUpper(string(args[0]))
					var response string
					switch cmd {
					case "HELLO":
						response = "-ERR unknown command 'hello'\r\n"
					case "PING":
						if len(args) >= 2 {
							msg := string(args[1])
							response = fmt.Sprintf("$%d\r\n%s\r\n", len(msg), msg)
						} else {
							response = "+PONG\r\n"
						}
					case "SUBSCRIBE":
						ch := "config"
						if len(args) >= 2 {
							ch = string(args[1])
						}
						response = fmt.Sprintf("*3\r\n$9\r\nsubscribe\r\n$%d\r\n%s\r\n:1\r\n", len(ch), ch)
					case "GET":
						if len(args) == 2 && ojson.GetBytesNoCopy(args[1], "type").String() == "models" {
							lastModelsReq.Store(string(args[1]))
							p, _ := payloadVal.Load().(string)
							response = fmt.Sprintf("$%d\r\n%s\r\n", len(p), p)
						} else {
							response = "$2\r\n{}\r\n"
						}
					default:
						response = "+OK\r\n"
					}
					if _, errWrite := io.WriteString(conn, response); errWrite != nil {
						return
					}
				}
			}()
		}
	}()

	host, portText, errSplit := net.SplitHostPort(listener.Addr().String())
	if errSplit != nil {
		t.Fatalf("split Home test address: %v", errSplit)
	}
	port, errAtoi := strconv.Atoi(portText)
	if errAtoi != nil {
		t.Fatalf("parse Home test port: %v", errAtoi)
	}

	client := home.New(proxyconfig.HomeConfig{Enabled: true, Host: host, Port: port, DisableClusterDiscovery: true})
	subCtx, cancelSub := context.WithCancel(context.Background())
	ready := make(chan struct{})
	var readyOnce sync.Once
	subDone := make(chan struct{})
	go func() {
		defer close(subDone)
		_ = client.RunConfigSubscriberLifetime(subCtx, func([]byte) error {
			return nil
		}, func() {
			readyOnce.Do(func() { close(ready) })
		})
	}()

	t.Cleanup(func() {
		cancelSub()
		client.Close()
		_ = listener.Close()
		<-subDone
		<-done
		connections.Wait()
	})

	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("ready Home catalog client did not establish heartbeat")
	}

	return client, func(next string) { payloadVal.Store(next) }, func() string {
		v, _ := lastModelsReq.Load().(string)
		return v
	}
}

func parseAPIMuseCodeEntriesByID(t *testing.T, raw []byte) map[string]ojson.Result {
	t.Helper()
	root := ojson.ParseBytes(raw)
	data := root.Get("data")
	if !data.IsArray() {
		t.Fatalf("expected data array in response, got %s", string(raw))
	}
	entries := make(map[string]ojson.Result)
	data.ForEach(func(_, item ojson.Result) bool {
		entries[item.Get("id").String()] = item
		return true
	})
	return entries
}

func TestMuseCodeClientEndpointsRelayToMetaOrigin(t *testing.T) {
	var seenMethod, seenPath, seenQuery, seenAuth, seenBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenMethod = r.Method
		seenPath = r.URL.Path
		seenQuery = r.URL.RawQuery
		seenAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		seenBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	server := newTestServer(t)
	server.handlers.AuthManager.RegisterExecutor(executor.NewMetaExecutor(server.getConfig()))
	cred := &auth.Auth{
		ID:       "meta-muse-code-relay",
		Provider: "meta",
		Status:   auth.StatusActive,
		Attributes: map[string]string{
			"api_key":  "meta-upstream-secret",
			"base_url": upstream.URL + "/v1",
		},
	}
	registry.GetGlobalRegistry().RegisterClient(cred.ID, "meta", []*registry.ModelInfo{{ID: "muse-wonder-full-1.0"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(cred.ID)
	})
	if _, errRegister := server.handlers.AuthManager.Register(t.Context(), cred); errRegister != nil {
		t.Fatalf("register Meta credential: %v", errRegister)
	}

	for _, tc := range []struct {
		name      string
		method    string
		target    string
		body      string
		wantPath  string
		wantQuery string
	}{
		{
			name:      "config",
			method:    http.MethodGet,
			target:    "/muse-code/config?channel=public",
			wantPath:  "/muse-code/config",
			wantQuery: "channel=public",
		},
		{
			name:     "search",
			method:   http.MethodPost,
			target:   "/muse-code/search",
			body:     `{"query":"golang"}`,
			wantPath: "/muse-code/search",
		},
		{
			name:     "browser_open",
			method:   http.MethodPost,
			target:   "/muse-code/browser_open",
			body:     `{"url":"https://example.com"}`,
			wantPath: "/muse-code/browser_open",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seenMethod, seenPath, seenQuery, seenAuth, seenBody = "", "", "", "", ""
			req := httptest.NewRequest(tc.method, tc.target, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer test-key")
			if tc.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()
			server.engine.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%q", rec.Code, rec.Body.String())
			}
			if seenMethod != tc.method || seenPath != tc.wantPath || seenQuery != tc.wantQuery {
				t.Fatalf("upstream got %s %s?%s, want %s %s?%s", seenMethod, seenPath, seenQuery, tc.method, tc.wantPath, tc.wantQuery)
			}
			if seenAuth != "Bearer meta-upstream-secret" {
				t.Fatalf("upstream Authorization = %q, want Bearer meta-upstream-secret", seenAuth)
			}
			if tc.body != "" && seenBody != tc.body {
				t.Fatalf("upstream body = %q, want %q", seenBody, tc.body)
			}
		})
	}
}

func TestMuseCodeModelsUsesFullCPACatalogWithoutUpstreamRelay(t *testing.T) {
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()

	server := newTestServer(t)
	server.handlers.AuthManager.RegisterExecutor(executor.NewMetaExecutor(server.getConfig()))
	cred := &auth.Auth{
		ID:       "meta-muse-code-models-local",
		Provider: "meta",
		Status:   auth.StatusActive,
		Attributes: map[string]string{
			"api_key":  "meta-upstream-secret",
			"base_url": upstream.URL + "/v1",
		},
	}
	registry.GetGlobalRegistry().RegisterClient(cred.ID, "meta", []*registry.ModelInfo{
		{ID: "muse-wonder-1.0", DisplayName: "Muse Wonder 1.0", OwnedBy: "meta", Type: "meta", ContextLength: 1007997, MaxCompletionTokens: 128000, Thinking: &registry.ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high", "xhigh", "max"}}, SupportedInputModalities: []string{"text", "image", "video"}, SupportedOutputModalities: []string{"text"}},
		{ID: "muse-spark-1.3", DisplayName: "Muse Spark 1.3", OwnedBy: "meta", Type: "meta", ContextLength: 1007997, MaxCompletionTokens: 128000, Thinking: &registry.ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high", "xhigh", "max"}}, SupportedInputModalities: []string{"text", "image", "video"}, SupportedOutputModalities: []string{"text"}},
		{ID: "muse-image-1.0", DisplayName: "Muse Image 1.0", OwnedBy: "meta", Type: "meta", SupportedInputModalities: []string{"text", "image"}, SupportedOutputModalities: []string{"image"}},
	})
	registry.GetGlobalRegistry().RegisterClient("codex-muse-code-models-local", "codex", []*registry.ModelInfo{
		{ID: "gpt-6.1-sol", DisplayName: "GPT 6.1 Sol", OwnedBy: "openai", Type: "codex", ContextLength: 500000, MaxCompletionTokens: 128000, Thinking: &registry.ThinkingSupport{Levels: []string{"low", "medium", "high", "xhigh"}}, SupportedInputModalities: []string{"text", "image"}, SupportedOutputModalities: []string{"text"}},
	})
	registry.GetGlobalRegistry().RegisterClient("claude-muse-code-models-local", "claude", []*registry.ModelInfo{
		{ID: "claude-sonnet-4-5-20250929", DisplayName: "Claude 4.5 Sonnet", OwnedBy: "anthropic", Type: "claude", ContextLength: 200000, MaxCompletionTokens: 64000, Thinking: &registry.ThinkingSupport{Min: 1024, Max: 128000, ZeroAllowed: true}, SupportedInputModalities: []string{"text", "image"}, SupportedOutputModalities: []string{"text"}},
	})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(cred.ID)
		registry.GetGlobalRegistry().UnregisterClient("codex-muse-code-models-local")
		registry.GetGlobalRegistry().UnregisterClient("claude-muse-code-models-local")
	})
	if _, errRegister := server.handlers.AuthManager.Register(t.Context(), cred); errRegister != nil {
		t.Fatalf("register Meta credential: %v", errRegister)
	}

	req := httptest.NewRequest(http.MethodGet, "/muse-code/models?channel=public", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()
	server.engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if upstreamCalls.Load() != 0 {
		t.Fatalf("/muse-code/models must not call Meta upstream; calls=%d", upstreamCalls.Load())
	}
	if ojson.GetBytesNoCopy(rec.Body.Bytes(), "object").String() != "list" {
		t.Fatalf("object = %q, want list", ojson.GetBytesNoCopy(rec.Body.Bytes(), "object").String())
	}

	entries := parseAPIMuseCodeEntriesByID(t, rec.Body.Bytes())
	for _, wantID := range []string{"muse-wonder-1.0", "muse-spark-1.3", "muse-image-1.0", "gpt-6.1-sol", "claude-sonnet-4-5-20250929"} {
		if _, ok := entries[wantID]; !ok {
			t.Fatalf("missing expected model %q in /muse-code/models response: %s", wantID, rec.Body.String())
		}
	}
	if got := entries["muse-wonder-1.0"].Get("metadata.muse-code.background_agent_model").String(); got != "muse-spark-1.3" {
		t.Fatalf("muse-wonder-1.0 background_agent_model = %q, want muse-spark-1.3", got)
	}
	museImg := entries["muse-image-1.0"].Get("metadata.muse-code")
	if !museImg.Get("is_hidden").Bool() || museImg.Get("tool_call").Bool() {
		t.Fatalf("muse-image-1.0 expected hidden with tool_call=false: %s", museImg.Raw)
	}
	if len(entries["claude-sonnet-4-5-20250929"].Get("metadata.muse-code.variants").Map()) == 0 {
		t.Fatalf("claude-sonnet-4-5-20250929 should advertise derived reasoning variants")
	}
}

func TestHomeCodexBucketsAndPrecedenceForOAuthSettings(t *testing.T) {
	cfg := &proxyconfig.Config{
		OAuthSettings: map[string][]proxyconfig.OAuthModelSetting{
			"codex": {
				{Name: "shared-model", MaxContextLength: 524288},
				{Name: "upstream-sol", Alias: "public-sol", MaxContextLength: 750000},
			},
			"claude": {
				{Name: "shared-model", MaxContextLength: 200000},
				{Name: "upstream-sol", Alias: "public-sol", MaxContextLength: 300000},
			},
		},
	}

	for _, tc := range []struct {
		name      string
		entry     homeModelEntry
		wantLimit int
	}{
		{
			name: "codex-free bucket matches codex OAuthSettings",
			entry: homeModelEntry{
				id:        "shared-model",
				providers: []string{"codex-free"},
			},
			wantLimit: 524288,
		},
		{
			name: "codex-plus bucket matches codex OAuthSettings",
			entry: homeModelEntry{
				id:        "shared-model",
				providers: []string{"codex-plus"},
			},
			wantLimit: 524288,
		},
		{
			name: "codex-team bucket matches codex OAuthSettings",
			entry: homeModelEntry{
				id:        "shared-model",
				providers: []string{"codex-team"},
			},
			wantLimit: 524288,
		},
		{
			name: "codex-pro bucket matches codex OAuthSettings",
			entry: homeModelEntry{
				id:        "shared-model",
				providers: []string{"codex-pro"},
			},
			wantLimit: 524288,
		},
		{
			name: "multi-provider claude + codex-free prefers codex",
			entry: homeModelEntry{
				id:        "shared-model",
				providers: []string{"claude", "codex-free"},
				routes: []homeModelRoute{
					{provider: "claude"},
					{provider: "codex-free"},
				},
			},
			wantLimit: 524288,
		},
		{
			name: "multi-provider alias claude + codex-team prefers codex",
			entry: homeModelEntry{
				id:              "public-sol",
				metadataModelID: "upstream-sol",
				modelName:       "upstream-sol",
				providers:       []string{"claude", "codex-team"},
				routes: []homeModelRoute{
					{provider: "claude", metadataModelID: "upstream-sol", modelName: "upstream-sol"},
					{provider: "codex-team", metadataModelID: "upstream-sol", modelName: "upstream-sol"},
				},
			},
			wantLimit: 750000,
		},
		{
			name: "claude-only entry uses claude setting",
			entry: homeModelEntry{
				id:        "shared-model",
				providers: []string{"claude"},
				routes:    []homeModelRoute{{provider: "claude"}},
			},
			wantLimit: 200000,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveHomeModelMaxContextLength(tc.entry, cfg); got != tc.wantLimit {
				t.Fatalf("resolveHomeModelMaxContextLength = %d, want %d", got, tc.wantLimit)
			}
		})
	}
}

func TestMuseCodeModelsSupportsHomeModeOAuthSettingsHotReloadAndPluginInterceptor(t *testing.T) {
	previousHome := home.Current()
	home.ClearCurrent()
	t.Cleanup(func() { home.SetCurrent(previousHome) })

	localClientID := "local-only-muse-code-isolation"
	registry.GetGlobalRegistry().RegisterClient(localClientID, "openai", []*registry.ModelInfo{{ID: "local-only-secret-model"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(localClientID) })

	// 1. Real server.engine rejects Home request with 503 when Home heartbeat is absent, without leaking local models.
	cfgNoHome := &proxyconfig.Config{
		SDKConfig: sdkconfig.SDKConfig{APIKeys: []string{"test-key"}},
		Home:      proxyconfig.HomeConfig{Enabled: true},
	}
	serverNoHome := newTestServerWithConfig(t, cfgNoHome)
	engineReq503 := httptest.NewRequest(http.MethodGet, "/muse-code/models", nil)
	engineReq503.Header.Set("Authorization", "Bearer test-key")
	engineRec503 := httptest.NewRecorder()
	serverNoHome.engine.ServeHTTP(engineRec503, engineReq503)
	if engineRec503.Code != http.StatusServiceUnavailable {
		t.Fatalf("server.engine status without home heartbeat = %d, want 503", engineRec503.Code)
	}
	if strings.Contains(engineRec503.Body.String(), "local-only-secret-model") {
		t.Fatalf("server.engine 503 leaked local-only model: %s", engineRec503.Body.String())
	}

	routeNoHome := registeredRouteEngine(t, serverNoHome, http.MethodGet, "/muse-code/models")
	req503 := httptest.NewRequest(http.MethodGet, "/muse-code/models", nil)
	req503.Header.Set("Authorization", "Bearer test-key")
	rec503 := httptest.NewRecorder()
	routeNoHome.ServeHTTP(rec503, req503)
	if rec503.Code != http.StatusServiceUnavailable || !strings.Contains(rec503.Body.String(), "home control center unavailable") {
		t.Fatalf("route handler without home client = %d %s, want 503 home control center unavailable", rec503.Code, rec503.Body.String())
	}

	// 2. Full server.engine middleware chain in healthy Home mode:
	//    - codex-free / codex-plus / codex-pro buckets match OAuthSettings["codex"]
	//    - cross-route identity isolation (claude id=gpt-6.1-sol name=claude-opus-4-6 + codex-pro id=gpt-6.1-sol name=gpt-6.1-sol -> 272000)
	//    - canonical upstream name enrichment without leaking internal fields
	//    - explicit empty supportedInputModalities: []
	//    - conservative multi-section merge
	//    - query parameter forwarding & plugin interceptor
	homePayload := `{
		"codex-free": [{
			"id": "gpt-free-model",
			"name": "gpt-free-model",
			"owned_by": "openai",
			"type": "codex",
			"context_length": 128000,
			"max_completion_tokens": 16384
		}],
		"codex-pro": [{
			"id": "gpt-6.1-sol",
			"name": "gpt-6.1-sol"
		}],
		"codex-plus": [{
			"id": "gpt-plus-alias",
			"name": "gpt-5.6-sol",
			"display_name": "GPT Plus Alias"
		}],
		"claude": [{
			"id": "gpt-6.1-sol",
			"name": "claude-opus-4-6"
		}, {
			"id": "my-claude-alias",
			"name": "claude-opus-4-6",
			"display_name": "Aliased Claude"
		}, {
			"id": "muse-wonder-1.0",
			"owned_by": "meta",
			"type": "meta",
			"supportedInputModalities": [],
			"supportedOutputModalities": ["text"]
		}, {
			"id": "shared-home-model",
			"owned_by": "anthropic",
			"type": "claude",
			"context_length": 400000,
			"max_completion_tokens": 64000,
			"thinking": {"levels": ["low", "medium", "high"]},
			"supportedInputModalities": ["text", "image"],
			"supportedOutputModalities": ["text"]
		}],
		"openai-compat": [{
			"id": "shared-home-model",
			"owned_by": "openai",
			"type": "openai",
			"context_length": 128000,
			"max_completion_tokens": 16384,
			"thinking": {"levels": ["medium", "high"]},
			"supportedInputModalities": ["text"],
			"supportedOutputModalities": ["text"]
		}]
	}`
	homeClient, setHomePayload, getLastModelsReq := newReadyHomeCatalogClient(t, homePayload)
	home.SetCurrent(homeClient)

	cfgWithHome := &proxyconfig.Config{
		SDKConfig: sdkconfig.SDKConfig{APIKeys: []string{"test-key"}},
		Home:      proxyconfig.HomeConfig{Enabled: true},
		OAuthSettings: map[string][]proxyconfig.OAuthModelSetting{
			"codex": {
				{
					Name:             "gpt-free-model",
					MaxContextLength: 600000,
				},
				{
					Name:             "gpt-5.6-sol",
					MaxContextLength: 750000,
				},
			},
			"claude": {
				{
					Name:             "claude-opus-4-6",
					MaxContextLength: 350000,
				},
			},
		},
	}
	server := newTestServerWithConfig(t, cfgWithHome)

	var intercepted bool
	var capturedFormat string
	server.handlers.SetPluginHost(&mockModelListInterceptorHost{
		interceptResponse: func(_ context.Context, req pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
			intercepted = true
			capturedFormat = req.SourceFormat
			doc := ojson.NewDoc(req.Body)
			if errSet := doc.Set("muse_intercepted", true); errSet != nil {
				t.Fatalf("set muse_intercepted via ojson.Doc: %v", errSet)
			}
			return pluginapi.ResponseInterceptResponse{
				Headers: req.ResponseHeaders,
				Body:    doc.Bytes(),
			}
		},
	})

	// Unauthorized request still blocked by real auth middleware on server.engine even when Home is healthy.
	unauthRec := httptest.NewRecorder()
	unauthReq := httptest.NewRequest(http.MethodGet, "/muse-code/models", nil)
	unauthReq.Header.Set("Authorization", "Bearer wrong-key")
	server.engine.ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth Home request status = %d, want 401", unauthRec.Code)
	}

	// Authorized request flows through the full server.engine middleware chain!
	req := httptest.NewRequest(http.MethodGet, "/muse-code/models?channel=public", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()
	server.engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !intercepted || capturedFormat != "openai" {
		t.Fatalf("interceptor not invoked cleanly: intercepted=%v format=%q", intercepted, capturedFormat)
	}
	if strings.Contains(rec.Body.String(), "local-only-secret-model") {
		t.Fatalf("Home response leaked local-only model: %s", rec.Body.String())
	}
	if !ojson.GetBytesNoCopy(rec.Body.Bytes(), "muse_intercepted").Bool() {
		t.Fatalf("response missing plugin interceptor marker: %s", rec.Body.String())
	}
	if lastReq := getLastModelsReq(); !strings.Contains(lastReq, `"channel":"public"`) {
		t.Fatalf("Home GetModels did not receive forwarded query param: %s", lastReq)
	}

	entries := parseAPIMuseCodeEntriesByID(t, rec.Body.Bytes())
	if len(entries) != 6 {
		t.Fatalf("len(entries) = %d, want 6; body=%s", len(entries), rec.Body.String())
	}

	// codex-free bucket matches OAuthSettings["codex"].
	if got := entries["gpt-free-model"].Get("metadata.muse-code.limit.context").Int(); got != 600000 {
		t.Fatalf("codex-free gpt-free-model context = %d, want 600000", got)
	}
	// Cross-route identity isolation: claude (id=gpt-6.1-sol, name=claude-opus-4-6, 350k) +
	// codex-pro (id=gpt-6.1-sol, name=gpt-6.1-sol, 272k) MUST cap context conservatively at 272000!
	if got := entries["gpt-6.1-sol"].Get("metadata.muse-code.limit.context").Int(); got != 272000 {
		t.Fatalf("cross-route Home gpt-6.1-sol context = %d, want 272000; entry=%s", got, entries["gpt-6.1-sol"].Raw)
	}
	// codex-plus + canonical name matches OAuthSettings["codex"] ("gpt-5.6-sol") and enriches static metadata.
	plusAlias := entries["gpt-plus-alias"]
	if plusAlias.Get("metadata.muse-code.limit.context").Int() != 750000 || !plusAlias.Get("metadata.muse-code.reasoning").Bool() {
		t.Fatalf("codex-plus canonical name alias failed enrichment/settings: %s", plusAlias.Raw)
	}
	// claude canonical name ("claude-opus-4-6") enriches static metadata and matches OAuthSettings["claude"], without leaking internal keys.
	claudeAlias := entries["my-claude-alias"]
	if claudeAlias.Get("metadata.muse-code.limit.context").Int() != 350000 || !claudeAlias.Get("metadata.muse-code.variants.max").Exists() {
		t.Fatalf("claude canonical name alias failed enrichment/settings: %s", claudeAlias.Raw)
	}
	if claudeAlias.Get("name").Exists() || claudeAlias.Get("MetadataModelID").Exists() {
		t.Fatalf("public response must not expose internal canonical name or MetadataModelID: %s", claudeAlias.Raw)
	}
	// Explicit empty supportedInputModalities: [] in Home does not fall back to static attachment=true.
	if entries["muse-wonder-1.0"].Get("metadata.muse-code.attachment").Bool() {
		t.Fatalf("explicit empty supportedInputModalities in Home must keep attachment=false: %s", entries["muse-wonder-1.0"].Raw)
	}
	// Multi-section Home model merges conservatively across claude + openai-compat.
	shared := entries["shared-home-model"].Get("metadata.muse-code")
	if shared.Get("attachment").Bool() || shared.Get("limit.context").Int() != 128000 || shared.Get("limit.output").Int() != 16384 || shared.Get("variants.low").Exists() || !shared.Get("variants.medium").Exists() {
		t.Fatalf("multi-section Home model did not merge conservatively: %s", shared.Raw)
	}

	// 3. Home error envelopes and empty Home catalog via full server.engine.
	for _, tc := range []struct {
		name       string
		payload    string
		wantStatus int
	}{
		{name: "no_credentials", payload: `{"error":{"type":"no_credentials","message":"Missing API key"}}`, wantStatus: http.StatusUnauthorized},
		{name: "internal_error", payload: `{"error":{"type":"internal_error","message":"Home failed"}}`, wantStatus: http.StatusBadGateway},
		{name: "malformed", payload: `{bad}`, wantStatus: http.StatusBadGateway},
	} {
		t.Run("home_error_"+tc.name, func(t *testing.T) {
			setHomePayload(tc.payload)
			errRec := httptest.NewRecorder()
			errReq := httptest.NewRequest(http.MethodGet, "/muse-code/models", nil)
			errReq.Header.Set("Authorization", "Bearer test-key")
			server.engine.ServeHTTP(errRec, errReq)
			if errRec.Code != tc.wantStatus {
				t.Fatalf("%s status = %d, want %d; body=%s", tc.name, errRec.Code, tc.wantStatus, errRec.Body.String())
			}
		})
	}

	server.handlers.SetPluginHost(nil)
	setHomePayload(`{"openai":[]}`)
	recEmpty := httptest.NewRecorder()
	emptyReq := httptest.NewRequest(http.MethodGet, "/muse-code/models", nil)
	emptyReq.Header.Set("Authorization", "Bearer test-key")
	server.engine.ServeHTTP(recEmpty, emptyReq)
	if recEmpty.Code != http.StatusOK || strings.TrimSpace(recEmpty.Body.String()) != `{"object":"list","data":[]}` {
		t.Fatalf("empty Home catalog got %d %s, want 200 {\"object\":\"list\",\"data\":[]}", recEmpty.Code, recEmpty.Body.String())
	}

	// 4. Config hot-reload toggling Home.Enabled from false -> true verified via full server.engine.
	reloadServer := newTestServer(t)

	recLocal := httptest.NewRecorder()
	reqLocal := httptest.NewRequest(http.MethodGet, "/muse-code/models", nil)
	reqLocal.Header.Set("Authorization", "Bearer test-key")
	reloadServer.engine.ServeHTTP(recLocal, reqLocal)
	if recLocal.Code != http.StatusOK || !strings.Contains(recLocal.Body.String(), "local-only-secret-model") {
		t.Fatalf("expected local mode model before reload, got %d %s", recLocal.Code, recLocal.Body.String())
	}

	setHomePayload(`{"codex-team":[{"id":"reloaded-home-model"}]}`)
	reloadCfg := reloadServer.getConfig().CloneForRuntime()
	reloadCfg.Home.Enabled = true
	reloadServer.UpdateClients(reloadCfg)

	recReloaded := httptest.NewRecorder()
	reqReloaded := httptest.NewRequest(http.MethodGet, "/muse-code/models", nil)
	reqReloaded.Header.Set("Authorization", "Bearer test-key")
	reloadServer.engine.ServeHTTP(recReloaded, reqReloaded)
	if recReloaded.Code != http.StatusOK {
		t.Fatalf("reloaded Home status = %d, want 200; body=%s", recReloaded.Code, recReloaded.Body.String())
	}
	if strings.Contains(recReloaded.Body.String(), "local-only-secret-model") || !strings.Contains(recReloaded.Body.String(), "reloaded-home-model") {
		t.Fatalf("hot-reload to Home mode did not switch source cleanly: %s", recReloaded.Body.String())
	}
}

func TestMuseCodeRoutesEnforceAuthSafeModeAndDisallowedPaths(t *testing.T) {
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	// 1. Unknown client key returns 401; disallowed /muse-code/key returns 404.
	server := newTestServer(t)
	server.handlers.AuthManager.RegisterExecutor(executor.NewMetaExecutor(server.getConfig()))
	cred := &auth.Auth{
		ID:       "meta-server-auth-test",
		Provider: "meta",
		Status:   auth.StatusActive,
		Attributes: map[string]string{
			"api_key":  "meta-secret",
			"base_url": upstream.URL + "/v1",
		},
	}
	registry.GetGlobalRegistry().RegisterClient(cred.ID, "meta", []*registry.ModelInfo{{ID: "muse-wonder-1.0"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(cred.ID)
	})
	if _, errRegister := server.handlers.AuthManager.Register(t.Context(), cred); errRegister != nil {
		t.Fatalf("register Meta credential: %v", errRegister)
	}

	unauthReq := httptest.NewRequest(http.MethodGet, "/muse-code/models", nil)
	unauthReq.Header.Set("Authorization", "Bearer wrong-key")
	unauthRec := httptest.NewRecorder()
	server.engine.ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown client key status = %d, want 401", unauthRec.Code)
	}

	keyReq := httptest.NewRequest(http.MethodPost, "/muse-code/key", nil)
	keyReq.Header.Set("Authorization", "Bearer test-key")
	keyRec := httptest.NewRecorder()
	server.engine.ServeHTTP(keyRec, keyReq)
	if keyRec.Code != http.StatusNotFound {
		t.Fatalf("disallowed /muse-code/key status = %d, want 404", keyRec.Code)
	}

	// 2. Example API key safe mode blocks /muse-code/models with 403.
	safeCfg := &proxyconfig.Config{
		SDKConfig: sdkconfig.SDKConfig{
			APIKeys: []string{"your-api-key-1"},
		},
	}
	safeServer := newTestServerWithConfig(t, safeCfg, WithExampleAPIKeySafeMode())
	safeReq := httptest.NewRequest(http.MethodGet, "/muse-code/models", nil)
	safeReq.Header.Set("Authorization", "Bearer your-api-key-1")
	safeRec := httptest.NewRecorder()
	safeServer.engine.ServeHTTP(safeRec, safeReq)
	if safeRec.Code != http.StatusForbidden {
		t.Fatalf("safe mode status = %d, want 403; body=%s", safeRec.Code, safeRec.Body.String())
	}
	if upstreamCalls.Load() != 0 {
		t.Fatalf("upstream called %d times, want 0", upstreamCalls.Load())
	}
}
