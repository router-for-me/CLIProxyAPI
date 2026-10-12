package api

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/tidwall/gjson"
)

func TestHomeModelListsFollowNativeModelLists(t *testing.T) {
	server := newTestServer(t)
	server.cfg.Home.Enabled = true
	server.cfg.ClaudeCode.DisableCloakingModelList = true
	previousHome := home.Current()
	home.SetCurrent(newHomeCatalogClient(t, `{"claude":[{"id":"claude-home-native"},{"id":"home-native-shared"}],"codex":[{"id":"gpt-home-native"},{"id":"home-native-shared"}]}`))
	t.Cleanup(func() { home.SetCurrent(previousHome) })
	// The catalog fixture does not implement Home's readiness handshake.
	engine := gin.New()
	engine.GET("/v1/models", server.unifiedModelsHandler(nil, nil))

	listed := func(t *testing.T, path, anthropicVersion, idPath string) []string {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer test-key")
		if anthropicVersion != "" {
			request.Header.Set("Anthropic-Version", anthropicVersion)
		}
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, body=%s", path, recorder.Code, recorder.Body.String())
		}
		var ids []string
		for _, id := range gjson.Get(recorder.Body.String(), idPath).Array() {
			ids = append(ids, id.String())
		}
		sort.Strings(ids)
		return ids
	}

	every := []string{"claude-home-native", "gpt-home-native", "home-native-shared"}
	for _, tc := range []struct {
		name                  string
		enabled               bool
		wantClaude, wantCodex []string
	}{
		{name: "off", wantClaude: every, wantCodex: every},
		{name: "on", enabled: true, wantClaude: []string{"claude-home-native", "home-native-shared"}, wantCodex: []string{"gpt-home-native", "home-native-shared"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server.cfg.Client.NativeModelLists = tc.enabled
			if got := listed(t, "/v1/models", "2023-06-01", "data.#.id"); !slices.Equal(got, tc.wantClaude) {
				t.Errorf("Anthropic model list = %v, want %v", got, tc.wantClaude)
			}
			if got := listed(t, "/v1/models?client_version=0.153.4", "", "models.#.slug"); !slices.Equal(got, tc.wantCodex) {
				t.Errorf("Codex client catalog = %v, want %v", got, tc.wantCodex)
			}
		})
	}
}
