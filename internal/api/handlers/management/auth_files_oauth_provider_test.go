package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestAuthFilesList_ReportsOAuthProvider(t *testing.T) {
	cases := []struct {
		id         string
		attributes map[string]string
		metadata   map[string]any
		want       string
	}{
		{id: "builtin"},
		{id: "attributes-normalized", attributes: map[string]string{"oauth_provider": " Vendor-CN "}, want: "vendor-cn"},
		{id: "metadata-normalized", metadata: map[string]any{"oauth_provider": " Vendor-CN "}, want: "vendor-cn"},
		{id: "attributes-portal-normalized", attributes: map[string]string{"portal": " Vendor-CN "}, want: "vendor-cn"},
		{id: "metadata-portal-normalized", metadata: map[string]any{"portal": " Vendor-CN "}, want: "vendor-cn"},
		{
			id:         "attributes-oauth-over-metadata-oauth",
			attributes: map[string]string{"oauth_provider": " Vendor ", "portal": "ignored-attributes"},
			metadata:   map[string]any{"oauth_provider": "vendor-cn", "portal": "ignored-metadata"},
			want:       "vendor",
		},
		{
			id:         "metadata-oauth-over-attributes-portal",
			attributes: map[string]string{"portal": "ignored-attributes"},
			metadata:   map[string]any{"oauth_provider": " Vendor-CN ", "portal": "ignored-metadata"},
			want:       "vendor-cn",
		},
		{
			id:         "attributes-portal-over-metadata-portal",
			attributes: map[string]string{"portal": " Vendor-CN "},
			metadata:   map[string]any{"portal": "ignored-metadata"},
			want:       "vendor-cn",
		},
		{
			id:         "blank-attributes-oauth",
			attributes: map[string]string{"oauth_provider": "   ", "portal": "ignored"},
			metadata:   map[string]any{"oauth_provider": " Vendor-CN "},
			want:       "vendor-cn",
		},
		{
			id:         "blank-metadata-oauth",
			attributes: map[string]string{"oauth_provider": "   ", "portal": " Vendor-CN "},
			metadata:   map[string]any{"oauth_provider": "   ", "portal": "ignored"},
			want:       "vendor-cn",
		},
		{
			id:         "blank-attributes-portal",
			attributes: map[string]string{"oauth_provider": "   ", "portal": "   "},
			metadata:   map[string]any{"oauth_provider": "   ", "portal": " Vendor-CN "},
			want:       "vendor-cn",
		},
		{id: "numeric-oauth-falls-back", attributes: map[string]string{"portal": "vendor-cn"}, metadata: map[string]any{"oauth_provider": 42}, want: "vendor-cn"},
		{id: "boolean-oauth-falls-back", metadata: map[string]any{"oauth_provider": true, "portal": "vendor-cn"}, want: "vendor-cn"},
		{id: "numeric-portal", metadata: map[string]any{"portal": 42}},
		{id: "boolean-portal", metadata: map[string]any{"portal": true}},
		{id: "non-string-hints", metadata: map[string]any{"oauth_provider": 42, "portal": true}},
		{id: "blank-hints", attributes: map[string]string{"oauth_provider": "   ", "portal": "   "}, metadata: map[string]any{"oauth_provider": "   ", "portal": "   "}},
	}
	manager := coreauth.NewManager(nil, nil, nil)
	for _, tc := range cases {
		attributes := tc.attributes
		if attributes == nil {
			attributes = make(map[string]string)
		}
		// Runtime-only entries do not require files on disk.
		attributes["runtime_only"] = "true"
		if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
			ID: tc.id, FileName: tc.id + ".json", Provider: "vendor", Attributes: attributes, Metadata: tc.metadata,
		}); errRegister != nil {
			t.Fatalf("register auth %s: %v", tc.id, errRegister)
		}
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	router := oauthProviderTestRouter(h)
	files := listOAuthProviderTestFiles(t, router, "/v8/management/credentials")
	if len(files) != len(cases) {
		t.Fatalf("v8 files count = %d, want %d", len(files), len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			assertOAuthProviderTestFile(t, files, tc.id, "vendor", tc.want)
		})
	}
	legacyFiles := listOAuthProviderTestFiles(t, router, "/v0/management/auth-files")
	if len(legacyFiles) != len(cases) {
		t.Fatalf("v0 files count = %d, want %d", len(legacyFiles), len(cases))
	}
	for id, file := range legacyFiles {
		if value := file.Get("oauth_provider"); value.Exists() {
			t.Errorf("%s: v0 must not expose oauth_provider, got %s", id, value.Raw)
		}
	}
}

func TestAuthFilesOAuthProvider_LoginLifecycle(t *testing.T) {
	for _, tc := range []struct {
		portal      string
		provider    string
		identifiers []string
		want        string
	}{
		{portal: "vendor", provider: "vendor", identifiers: []string{"vendor", "vendor-cn"}, want: "vendor"},
		{portal: "vendor-cn", provider: "vendor", identifiers: []string{"vendor", "vendor-cn"}, want: "vendor-cn"},
		{portal: "single-vendor", provider: "single-vendor", identifiers: []string{"single-vendor"}},
	} {
		for _, multipleAuths := range []bool{false, true} {
			responseKind := "auth"
			if multipleAuths {
				responseKind = "auths"
			}
			t.Run(tc.portal+"/"+responseKind, func(t *testing.T) {
				authDir := t.TempDir()
				state := "state-oauth-provider-" + tc.portal + "-" + responseKind
				t.Cleanup(func() { CancelOAuthSession(state) })
				provider := &testAuthProvider{
					identifier: tc.identifiers[0], identifiers: tc.identifiers,
					startLogin: func(_ context.Context, req pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
						if req.Provider != tc.portal {
							t.Fatalf("start provider = %q, want %q", req.Provider, tc.portal)
						}
						return pluginapi.AuthLoginStartResponse{
							Provider: req.Provider, State: state, URL: "https://login.example/" + req.Provider, ExpiresAt: time.Now().Add(time.Hour),
						}, nil
					},
					pollLogin: func(_ context.Context, req pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error) {
						if req.Provider != tc.portal {
							t.Fatalf("poll provider = %q, want %q", req.Provider, tc.portal)
						}
						data := pluginapi.AuthData{Provider: tc.provider, ID: "account.json", FileName: "account.json"}
						resp := pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusSuccess, Auth: data}
						if multipleAuths {
							resp.Auths = []pluginapi.AuthData{data}
						}
						return resp, nil
					},
				}
				host := pluginhost.New()
				host.RegisterPluginForTest("oauth-provider", pluginapi.Plugin{Capabilities: pluginapi.Capabilities{AuthProvider: provider}})
				if got := host.HasMultipleAuthProviders(tc.portal); got != (len(tc.identifiers) > 1) {
					t.Fatalf("HasMultipleAuthProviders(%q) = %t", tc.portal, got)
				}

				// Exercise management conversion independently of the host's stamping.
				rawResp, errPoll := provider.PollLogin(context.Background(), pluginapi.AuthLoginPollRequest{Provider: tc.portal})
				if errPoll != nil {
					t.Fatalf("plugin poll: %v", errPoll)
				}
				records := pluginLoginPollAuthsWithProvider(host, tc.portal, rawResp)
				if len(records) != 1 {
					t.Fatalf("management poll records = %d, want 1", len(records))
				}
				assertOAuthProviderTestAuth(t, records[0], tc.want)

				resp, handled, errHostPoll := host.PollLogin(context.Background(), tc.portal, state)
				if errHostPoll != nil || !handled {
					t.Fatalf("host poll handled=%t error=%v", handled, errHostPoll)
				}
				assertOAuthProviderTestAuth(t, host.AuthDataToCoreAuth(resp.Auth, "", ""), tc.want)
				for _, data := range resp.Auths {
					assertOAuthProviderTestAuth(t, host.AuthDataToCoreAuth(data, "", ""), tc.want)
				}

				manager := coreauth.NewManager(nil, nil, nil)
				h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir, Port: 8317}, manager)
				h.SetPluginHost(host)
				router := oauthProviderTestRouter(h)
				start := oauthProviderTestGET(t, router, "/v8/management/oauth/auth-url?provider="+tc.portal)
				if got := start.Get("state").String(); got != state {
					t.Fatalf("login state = %q, want %q: %s", got, state, start.Raw)
				}
				poll := oauthProviderTestGET(t, router, "/v8/management/get-auth-status?state="+state)
				if got := poll.Get("status").String(); got != "ok" {
					t.Fatalf("poll status = %q: %s", got, poll.Raw)
				}
				auth, ok := manager.GetByID("account.json")
				if !ok {
					t.Fatal("signed-in credential was not registered")
				}
				if got := authOAuthProvider(auth); got != tc.want {
					t.Fatalf("registered oauth_provider = %q, want %q", got, tc.want)
				}
				if tc.want == "" {
					assertOAuthProviderTestAuth(t, auth, "")
				}
				files := listOAuthProviderTestFiles(t, router, "/v8/management/credentials")
				assertOAuthProviderTestFile(t, files, "account.json", tc.provider, tc.want)
				saved, errRead := os.ReadFile(filepath.Join(authDir, "account.json"))
				if errRead != nil {
					t.Fatalf("read saved auth: %v", errRead)
				}
				value := ojson.GetBytes(saved, "oauth_provider")
				if value.String() != tc.want || (tc.want == "" && value.Exists()) {
					t.Fatalf("saved oauth_provider = %s, want %q", value.Raw, tc.want)
				}
			})
		}
	}
}

func TestPluginLoginPollAuths_PreservesStorageOAuthProviderThroughHost(t *testing.T) {
	for _, responseKind := range []string{"auth", "auths"} {
		for _, key := range []string{"oauth_provider", "portal"} {
			t.Run(responseKind+"/"+key, func(t *testing.T) {
				host := pluginhost.New()
				host.RegisterPluginForTest("vendor-plugin", pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
					AuthProvider: &testAuthProvider{
						identifier: "vendor", identifiers: []string{"vendor", "vendor-cn"},
						pollLogin: func(_ context.Context, req pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error) {
							if req.Provider != "vendor" {
								t.Fatalf("poll provider = %q, want vendor", req.Provider)
							}
							doc := ojson.NewDocString(`{"type":"vendor"}`)
							doc.Set(key, " Vendor-CN ")
							data := pluginapi.AuthData{Provider: "vendor", StorageJSON: doc.Bytes()}
							resp := pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusSuccess}
							if responseKind == "auths" {
								resp.Auths = []pluginapi.AuthData{data}
							} else {
								resp.Auth = data
							}
							return resp, nil
						},
					},
				}})
				resp, handled, errPoll := host.PollLogin(context.Background(), "vendor", "state")
				if errPoll != nil || !handled {
					t.Fatalf("PollLogin() handled=%t error=%v", handled, errPoll)
				}
				records := pluginLoginPollAuthsWithProvider(host, "vendor", resp)
				if len(records) != 1 {
					t.Fatalf("poll records = %d, want 1", len(records))
				}
				assertOAuthProviderTestAuth(t, records[0], "vendor-cn")
				storage := records[0].Storage.(interface{ RawJSON() []byte })
				if got := ojson.GetBytes(storage.RawJSON(), "oauth_provider").String(); got != "vendor-cn" {
					t.Fatalf("stored oauth_provider = %q, want vendor-cn", got)
				}
			})
		}
	}
}

func TestPluginLoginPollAuths_ExplicitOAuthProviderHints(t *testing.T) {
	for _, tc := range []struct {
		name string
		data pluginapi.AuthData
	}{
		{name: "attribute-oauth", data: pluginapi.AuthData{Attributes: map[string]string{"oauth_provider": " Vendor "}}},
		{name: "metadata-oauth", data: pluginapi.AuthData{Metadata: map[string]any{"oauth_provider": " Vendor "}}},
		{name: "attribute-portal", data: pluginapi.AuthData{Attributes: map[string]string{"portal": " Vendor "}}},
		{name: "metadata-portal", data: pluginapi.AuthData{Metadata: map[string]any{"portal": " Vendor "}}},
		{name: "storage-oauth", data: pluginapi.AuthData{StorageJSON: []byte(`{"type":"vendor","oauth_provider":" Vendor "}`)}},
		{name: "storage-portal-fallback", data: pluginapi.AuthData{StorageJSON: []byte(`{"oauth_provider":42,"portal":" Vendor "}`)}},
		{
			name: "attribute-oauth-over-all",
			data: pluginapi.AuthData{
				Attributes:  map[string]string{"oauth_provider": " Vendor ", "portal": "vendor-cn"},
				Metadata:    map[string]any{"oauth_provider": "vendor-cn", "portal": "vendor-cn"},
				StorageJSON: []byte(`{"oauth_provider":"vendor-cn","portal":"vendor-cn"}`),
			},
		},
		{
			name: "metadata-oauth-over-attribute-portal",
			data: pluginapi.AuthData{
				Attributes:  map[string]string{"portal": "vendor-cn"},
				Metadata:    map[string]any{"oauth_provider": " Vendor ", "portal": "vendor-cn"},
				StorageJSON: []byte(`{"oauth_provider":"vendor-cn","portal":"vendor-cn"}`),
			},
		},
		{
			name: "attribute-portal-over-metadata-portal",
			data: pluginapi.AuthData{
				Attributes:  map[string]string{"portal": " Vendor "},
				Metadata:    map[string]any{"portal": "vendor-cn"},
				StorageJSON: []byte(`{"oauth_provider":"vendor-cn","portal":"vendor-cn"}`),
			},
		},
		{
			name: "metadata-portal-over-storage-oauth",
			data: pluginapi.AuthData{
				Metadata:    map[string]any{"portal": " Vendor "},
				StorageJSON: []byte(`{"oauth_provider":"vendor-cn","portal":"vendor-cn"}`),
			},
		},
		{name: "storage-oauth-over-storage-portal", data: pluginapi.AuthData{StorageJSON: []byte(`{"oauth_provider":" Vendor ","portal":"vendor-cn"}`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.data.Provider = "vendor"
			records := pluginLoginPollAuthsWithProvider(pluginhost.New(), "vendor-cn", pluginapi.AuthLoginPollResponse{
				Status: pluginapi.AuthLoginStatusSuccess, Auth: tc.data,
			})
			if len(records) != 1 {
				t.Fatalf("poll records = %d, want 1", len(records))
			}
			assertOAuthProviderTestAuth(t, records[0], "vendor")
		})
	}
}

func TestAuthFilesOAuthProvider_ColdStartLifecycle(t *testing.T) {
	for _, parserKind := range []string{"plugin", "file-fallback", "unhandled-plugin", "core-plugin-parser"} {
		t.Run(parserKind, func(t *testing.T) {
			authDir := t.TempDir()
			path := filepath.Join(authDir, "cold.json")
			raw := []byte(`{"type":"vendor","oauth_provider":"vendor"}`)
			if errWrite := os.WriteFile(path, raw, 0o600); errWrite != nil {
				t.Fatalf("write cold-start auth: %v", errWrite)
			}
			host := pluginhost.New()
			ctx := &synthesizer.SynthesisContext{AuthDir: authDir, Now: time.Now(), StrictFileParsing: true}
			switch parserKind {
			case "plugin":
				host.RegisterPluginForTest("vendor-plugin", pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
					AuthProvider: &oauthProviderParseTestProvider{testAuthProvider: testAuthProvider{
						identifier: "vendor", identifiers: []string{"vendor", "vendor-cn"},
					}},
				}})
				ctx.PluginAuthParser = host
			case "unhandled-plugin":
				ctx.PluginAuthParser = host
			case "core-plugin-parser":
				// The synthesizer must also support parsers that omit file attribution.
				ctx.PluginAuthParser = oauthProviderFileParser(func(_ context.Context, req pluginapi.AuthParseRequest) (*coreauth.Auth, bool, error) {
					auth := host.AuthDataToCoreAuth(pluginapi.AuthData{Provider: "vendor", StorageJSON: []byte(`{"type":"vendor"}`)}, req.Path, req.FileName)
					auth.Metadata = nil
					return auth, true, nil
				})
			}
			auths, errSynthesize := synthesizer.SynthesizeAuthFile(ctx, path, raw)
			if errSynthesize != nil || len(auths) != 1 {
				t.Fatalf("SynthesizeAuthFile() count=%d error=%v", len(auths), errSynthesize)
			}
			auth := auths[0]
			if got := authOAuthProvider(auth); got != "vendor" {
				t.Fatalf("cold-start oauth_provider = %q, want vendor", got)
			}
			if parserKind == "plugin" || parserKind == "core-plugin-parser" {
				assertOAuthProviderTestAuth(t, auth, "vendor")
				storage, ok := auth.Storage.(interface{ RawJSON() []byte })
				if !ok || ojson.GetBytes(storage.RawJSON(), "oauth_provider").String() != "vendor" {
					t.Fatal("plugin storage did not retain oauth_provider=vendor")
				}
			}
			manager := coreauth.NewManager(nil, nil, nil)
			if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
				t.Fatalf("register synthesized auth: %v", errRegister)
			}
			h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
			files := listOAuthProviderTestFiles(t, oauthProviderTestRouter(h), "/v8/management/credentials")
			assertOAuthProviderTestFile(t, files, auth.ID, "vendor", "vendor")
		})
	}
}

type oauthProviderParseTestProvider struct {
	testAuthProvider
}

func (p *oauthProviderParseTestProvider) ParseAuth(_ context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
	return pluginapi.AuthParseResponse{Handled: true, Auth: pluginapi.AuthData{Provider: req.Provider, StorageJSON: req.RawJSON}}, nil
}

type oauthProviderFileParser func(context.Context, pluginapi.AuthParseRequest) (*coreauth.Auth, bool, error)

func (f oauthProviderFileParser) ParseAuth(ctx context.Context, req pluginapi.AuthParseRequest) (*coreauth.Auth, bool, error) {
	return f(ctx, req)
}

func oauthProviderTestRouter(h *Handler) *gin.Engine {
	router := gin.New()
	v8 := router.Group("/v8/management", func(c *gin.Context) {
		c.Set(ConfigV8ContextKey, true)
		c.Next()
	})
	v8.GET("/credentials", h.ListAuthFiles)
	v8.GET("/oauth/auth-url", h.StartOAuthV8)
	v8.GET("/get-auth-status", h.GetAuthStatus)
	router.GET("/v0/management/auth-files", h.ListAuthFiles)
	return router
}

func oauthProviderTestGET(t *testing.T, router http.Handler, path string) ojson.Result {
	t.Helper()
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK || !ojson.ValidBytes(rec.Body.Bytes()) {
		t.Fatalf("GET %s status=%d body=%s", path, rec.Code, rec.Body.String())
	}
	return ojson.Parse(rec.Body.String())
}

func listOAuthProviderTestFiles(t *testing.T, router http.Handler, path string) map[string]ojson.Result {
	t.Helper()
	resp := oauthProviderTestGET(t, router, path)
	files := resp.Get("files")
	if !files.IsArray() || len(files.Array()) == 0 {
		t.Fatalf("GET %s returned no files: %s", path, resp.Raw)
	}
	out := make(map[string]ojson.Result)
	for _, file := range files.Array() {
		out[file.Get("id").String()] = file
	}
	return out
}

func assertOAuthProviderTestFile(t *testing.T, files map[string]ojson.Result, id, provider, want string) {
	t.Helper()
	file, ok := files[id]
	if !ok {
		t.Fatalf("missing credential %s", id)
	}
	value := file.Get("oauth_provider")
	if value.String() != want || (want == "" && value.Exists()) {
		t.Errorf("%s: oauth_provider = %s, want %q", id, value.Raw, want)
	}
	if file.Get("type").String() != provider || file.Get("provider").String() != provider {
		t.Errorf("%s: credential provider changed: %s", id, file.Raw)
	}
}

func assertOAuthProviderTestAuth(t *testing.T, auth *coreauth.Auth, want string) {
	t.Helper()
	if auth == nil {
		t.Fatal("missing auth")
	}
	if want == "" {
		for _, key := range []string{"oauth_provider", "portal"} {
			if value, exists := auth.Metadata[key]; exists {
				t.Errorf("single-entry metadata must omit %s, got %#v", key, value)
			}
			if value, exists := auth.Attributes[key]; exists {
				t.Errorf("single-entry attributes must omit %s, got %q", key, value)
			}
		}
		return
	}
	if auth.Metadata["oauth_provider"] != want || auth.Attributes["oauth_provider"] != want {
		t.Errorf("oauth_provider metadata=%#v attributes=%q, want %q", auth.Metadata["oauth_provider"], auth.Attributes["oauth_provider"], want)
	}
}
