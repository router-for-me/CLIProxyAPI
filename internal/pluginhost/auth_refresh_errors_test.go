package pluginhost

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	openaihandlers "github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestAuthRefreshErrorPresentation(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "buffered"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			const token = "synthetic-ONLY-refresh-secret"
			const bearer = "synthetic-ONLY-bearer-secret"
			const path = "/synthetic-private/auths/credential.json"
			failure := &coreauth.Error{HTTPStatus: 503, Message: "synthetic refresh unavailable refresh_token=" + token + " Bearer " + bearer + " from \"" + path + "\""}
			var refreshes, inferences atomic.Int32
			record := normalizeTestCapabilityRecord(capabilityRecord{id: "error-presentation", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{AuthProvider: fakeAuthProvider{identifier: "plugin-provider", refreshAuth: func(context.Context, pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
				refreshes.Add(1)
				return pluginapi.AuthRefreshResponse{}, failure
			}}}}})
			host := newHostWithRecords(record)
			dir := t.TempDir()
			store := sdkauth.NewFileTokenStore()
			store.SetBaseDir(dir)
			manager := coreauth.NewManager(store, nil, nil)
			executor := newExecutorAdapterForRecordForTest(host, record, &fakeExecutor{identifier: "plugin-provider", execute: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
				inferences.Add(1)
				return pluginapi.ExecutorResponse{}, nil
			}}, []sdktranslator.Format{sdktranslator.FormatOpenAI}, []sdktranslator.Format{sdktranslator.FormatOpenAI})
			manager.RegisterExecutor(executor)
			auth := host.AuthDataToCoreAuth(pluginapi.AuthData{Provider: "plugin-provider", ID: "error.json", FileName: "error.json", RefreshPolicy: new(pluginapi.RefreshOnDemand), Metadata: map[string]any{"access_token": "synthetic-access", "refresh_token": "synthetic-refresh", "expired": "2000-01-01T00:00:00Z"}}, filepath.Join(dir, "error.json"), "error.json")
			selected, err := manager.Register(context.Background(), auth)
			if err != nil {
				t.Fatal(err)
			}
			registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "auth-refresh-error-model"}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			_, err = manager.EnsureRequestAuth(context.Background(), selected, "")
			var cause *coreauth.Error
			if !errors.Is(err, failure) || !errors.As(err, &cause) || cause != failure {
				t.Fatal("managed error lost original typed cause")
			}
			errManaged := err
			status, ok := errManaged.(interface{ StatusCode() int })
			if !ok || status.StatusCode() != 503 {
				t.Fatalf("managed error lost status: %v", err)
			}
			base := handlers.NewBaseAPIHandlers(&config.SDKConfig{}, manager)
			openai := openaihandlers.NewOpenAIAPIHandler(base)
			router := gin.New()
			router.POST("/v1/chat/completions", openai.ChatCompletions)
			server := httptest.NewServer(router)
			defer server.Close()
			payload := `{"model":"auth-refresh-error-model","messages":[{"role":"user","content":"synthetic"}]}`
			if stream {
				payload = `{"model":"auth-refresh-error-model","messages":[{"role":"user","content":"synthetic"}],"stream":true}`
			}
			response, err := server.Client().Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(payload))
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{token, bearer, path} {
				if strings.Contains(string(body), secret) || strings.Contains(errManaged.Error(), secret) {
					t.Errorf("managed refresh response exposed synthetic secret: %s", body)
				}
			}
			if response.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body), "synthetic refresh unavailable") || refreshes.Load() != 1 || inferences.Load() != 0 {
				t.Fatalf("response=%d %s refreshes=%d inference=%d", response.StatusCode, body, refreshes.Load(), inferences.Load())
			}
		})
	}
}
