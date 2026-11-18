package api

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type restoredModelLocalTransport struct {
	base                       http.RoundTripper
	method, scheme, host, path string
}

func (g restoredModelLocalTransport) RoundTripperFor(*coreauth.Auth) http.RoundTripper { return g }
func (g restoredModelLocalTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil || req.Method != g.method || req.URL.Scheme != g.scheme || req.URL.Host != g.host || req.URL.Path != g.path || req.URL.RawQuery != "" || req.URL.User != nil || (req.Host != "" && req.Host != g.host) {
		return nil, fmt.Errorf("restored-model fixture rejected a request outside its local URL allowlist")
	}
	return g.base.RoundTrip(req)
}

func TestManagementV8RestoredExecutionModelIdentity(t *testing.T) {
	for _, tc := range []struct {
		name    string
		headers map[string]string
		recover bool
		rotate  bool
	}{
		{"foreign_bearer", map[string]string{"Authorization": "Bearer token-b"}, false, false},
		{"foreign_account", map[string]string{"Chatgpt-Account-Id": "account-b"}, false, false},
		{"matching", map[string]string{"Authorization": "Bearer token-a", "Chatgpt-Account-Id": "account-a"}, true, false},
		{"unrelated", map[string]string{"X-Model": "yes"}, true, false},
		{"rotate_foreign_bearer", map[string]string{"Authorization": "Bearer token-b"}, false, true},
		{"rotate_foreign_account", map[string]string{"Chatgpt-Account-Id": "account-b"}, false, true},
		{"rotate_matching", map[string]string{"Chatgpt-Account-Id": "account-a"}, true, true},
	} {
		for _, stream := range []bool{false, true} {
			name := tc.name
			if stream {
				name = "stream/" + name
			}
			t.Run(name, func(t *testing.T) {
				t.Setenv("DISABLE_QUOTA_COOLDOWN", "false")
				ctx := context.Background()
				id := t.Name()
				route := "gpt-selection-" + tc.name
				actual := "gpt-restored-" + tc.name
				cfg := &config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}}
				type wire struct{ model, token, account string }
				captured := make(chan wire, 1)
				generation := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					localAddr, _ := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
					if localAddr == nil || r.Host != localAddr.String() || r.TLS != nil || r.Method != http.MethodPost || r.URL.Path != "/responses" || r.URL.RawQuery != "" {
						t.Error("SDK fixture rejected unexpected local inference request")
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
						return
					}
					captured <- wire{ojson.GetBytes(body, "model").String(), r.Header.Get("Authorization"), r.Header.Get("Chatgpt-Account-Id")}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(429)
					_, _ = io.WriteString(w, `{"error":{"type":"usage_limit_reached","resets_in_seconds":3600}}`)
				}))
				defer generation.Close()
				reg := registry.GetGlobalRegistry()
				reg.RegisterClient(id, "codex", []*registry.ModelInfo{{ID: route, Type: "codex"}})
				defer reg.UnregisterClient(id)
				reg.RegisterClient(id+"-catalog", "codex", []*registry.ModelInfo{{ID: actual, Type: "codex", Config: &registry.ModelConfig{OverrideHeader: tc.headers}}})
				defer reg.UnregisterClient(id + "-catalog")
				inferenceTransport := &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					if network != "tcp" || addr != generation.Listener.Addr().String() {
						return nil, fmt.Errorf("SDK fixture refused nonlocal inference dial %s", addr)
					}
					return (&net.Dialer{}).DialContext(ctx, network, generation.Listener.Addr().String())
				}}
				inferenceGuard := restoredModelLocalTransport{base: inferenceTransport, method: http.MethodPost, scheme: "http", host: generation.Listener.Addr().String(), path: "/responses"}
				previousTransport := http.DefaultTransport
				http.DefaultTransport = inferenceTransport
				defer func() { http.DefaultTransport = previousTransport; inferenceTransport.CloseIdleConnections() }()
				manager := coreauth.NewManager(nil, nil, nil)
				manager.SetRoundTripperProvider(inferenceGuard)
				manager.SetConfig(cfg)
				manager.SetRetryConfig(0, 0, 1)
				manager.RegisterExecutor(runtimeexecutor.NewCodexExecutor(cfg))
				a, err := manager.Register(ctx, &coreauth.Auth{ID: id, Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{"base_url": generation.URL}, Metadata: map[string]any{"access_token": "token-a", "account_id": "account-a"}})
				if err != nil {
					t.Fatal(err)
				}
				doc := ojson.NewDocString(`{"input":[]}`)
				if err = doc.Set("model", actual); err != nil {
					t.Fatal(err)
				}
				if stream {
					_, err = manager.ExecuteStream(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: actual, Payload: doc.Bytes()}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), Metadata: map[string]any{cliproxyexecutor.AuthSelectionModelMetadataKey: route}})
				} else {
					_, err = manager.Execute(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: actual, Payload: doc.Bytes()}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), Metadata: map[string]any{cliproxyexecutor.AuthSelectionModelMetadataKey: route}})
				}
				if err == nil {
					t.Fatal("actual executor quota failure missing")
				}
				var got wire
				select {
				case got = <-captured:
				default:
					t.Fatalf("actual restored request missing: %v", err)
				}
				want := wire{actual, "Bearer token-a", "account-a"}
				if v, ok := tc.headers["Authorization"]; ok {
					want.token = v
				}
				if v, ok := tc.headers["Chatgpt-Account-Id"]; ok {
					want.account = v
				}
				if got != want {
					t.Fatalf("wire=%+v want=%+v", got, want)
				}
				before, _ := manager.GetByID(a.ID)
				if !coreauth.QuotaRecoveryCandidate(before) || before.ModelStates[route] == nil || !before.ModelStates[route].Quota.Exceeded {
					t.Fatalf("selection route quota missing: %+v", before)
				}
				probeToken := "token-a"
				if tc.rotate {
					publicBytes, marshalErr := json.Marshal(before)
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					public := &coreauth.Auth{}
					if err = json.Unmarshal(publicBytes, public); err != nil {
						t.Fatal(err)
					}
					public.Metadata["access_token"] = "token-rotated"
					if _, err = manager.Update(ctx, public); err != nil {
						t.Fatal(err)
					}
					before, _ = manager.GetByID(a.ID)
					if before.Metadata["access_token"] != "token-rotated" || before.Metadata["account_id"] != "account-a" || !coreauth.QuotaRecoveryCandidate(before) || before.ModelStates[route] == nil || !before.ModelStates[route].Quota.Exceeded {
						t.Fatal("same-account public rotation did not retain inspected quota")
					}
					probeToken = "token-rotated"
				}
				usageCalls := 0
				usage := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.TLS == nil || r.Method != http.MethodGet || r.Host != "chatgpt.com" || r.URL.Path != "/backend-api/wham/usage" || r.URL.RawQuery != "" {
						t.Error("SDK fixture rejected unexpected canonical usage request")
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					usageCalls++
					if r.URL.Path != "/backend-api/wham/usage" || r.Header.Get("Authorization") != "Bearer "+probeToken || r.Header.Get("Chatgpt-Account-Id") != "account-a" {
						t.Error("canonical probe changed inspected identity")
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":0},"secondary_window":{"used_percent":1}}}`)
				}))
				defer usage.Close()
				transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					if network != "tcp" || addr != "chatgpt.com:443" {
						return nil, fmt.Errorf("SDK fixture refused unexpected canonical authority %s", addr)
					}
					return (&net.Dialer{}).DialContext(ctx, network, usage.Listener.Addr().String())
				}}
				http.DefaultTransport = transport
				defer transport.CloseIdleConnections()
				router := gin.New()
				router.POST("/v8/management/requests/api-call", management.NewHandler(cfg, "", manager).APICallV8)
				body, err := json.Marshal(map[string]any{"authIndex": before.EnsureIndex(), "method": "GET", "url": "https://chatgpt.com/backend-api/wham/usage", "header": map[string]string{"Authorization": "Bearer $TOKEN$", "Chatgpt-Account-Id": "account-a"}})
				if err != nil {
					t.Fatal(err)
				}
				req := httptest.NewRequest(http.MethodPost, "/v8/management/requests/api-call", strings.NewReader(string(body)))
				req.Header.Set("Content-Type", "application/json")
				response := httptest.NewRecorder()
				router.ServeHTTP(response, req)
				if response.Code != 200 || usageCalls != 1 {
					t.Fatalf("canonical usage failed: status=%d calls=%d body=%s", response.Code, usageCalls, response.Body.String())
				}
				after, _ := manager.GetByID(a.ID)
				if tc.recover {
					if after.Unavailable || after.Quota.Exceeded {
						t.Fatal("matching restored execution identity did not recover")
					}
				} else if !reflect.DeepEqual(before, after) {
					t.Fatal("canonical outer usage cleared foreign restored execution quota")
				}
			})
		}
	}
}
