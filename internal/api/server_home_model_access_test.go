package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
)

func TestHomeGeminiModelDetailRespectsModelPolicy(t *testing.T) {
	client := newHomeCatalogClient(t, `{"gemini":[{"id":"gemini-private","name":"gemini-private"}]}`)
	previousHome := home.Current()
	home.SetCurrent(client)
	t.Cleanup(func() { home.SetCurrent(previousHome) })
	server := &Server{cfg: &config.Config{Home: config.HomeConfig{Enabled: true}}}
	for _, tc := range []struct {
		name   string
		policy *sdkaccess.ModelPolicy
		status int
	}{
		{"unrestricted", nil, http.StatusOK},
		{"allowed", sdkaccess.NewModelPolicy([]string{"gemini-*"}, nil), http.StatusOK},
		{"denied", sdkaccess.NewModelPolicy(nil, []string{"gemini-private"}), http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/v1beta/models/gemini-private", nil)
			ctx.Params = gin.Params{{Key: "action", Value: "/gemini-private"}}
			ctx.Set(sdkaccess.ModelPolicyContextKey, tc.policy)
			server.geminiGetHandler(nil)(ctx)
			if recorder.Code != tc.status {
				t.Fatalf("status = %d, want %d; body = %s", recorder.Code, tc.status, recorder.Body.String())
			}
		})
	}
}

func TestHomeGeminiModelDetailIgnoresThinkingSuffixForPolicy(t *testing.T) {
	client := newHomeCatalogClient(t, `{"gemini":[{"id":"gemini-private(high)","name":"gemini-private(high)"}]}`)
	previousHome := home.Current()
	home.SetCurrent(client)
	t.Cleanup(func() { home.SetCurrent(previousHome) })
	server := &Server{cfg: &config.Config{Home: config.HomeConfig{Enabled: true}}}
	for _, tc := range []struct {
		name   string
		policy *sdkaccess.ModelPolicy
		status int
	}{
		{"allowed by base name", sdkaccess.NewModelPolicy([]string{"gemini-private"}, nil), http.StatusOK},
		{"denied by base name", sdkaccess.NewModelPolicy(nil, []string{"gemini-private"}), http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/v1beta/models/gemini-private(high)", nil)
			ctx.Params = gin.Params{{Key: "action", Value: "/gemini-private(high)"}}
			ctx.Set(sdkaccess.ModelPolicyContextKey, tc.policy)
			server.geminiGetHandler(nil)(ctx)
			if recorder.Code != tc.status {
				t.Fatalf("status = %d, want %d; body = %s", recorder.Code, tc.status, recorder.Body.String())
			}
		})
	}
}
