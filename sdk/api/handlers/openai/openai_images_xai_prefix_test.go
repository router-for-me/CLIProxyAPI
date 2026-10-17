package openai

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/tidwall/gjson"
)

type xaiImagesCaptureExecutor struct {
	calls        int
	requestModel string
	payloadModel string
}

func (e *xaiImagesCaptureExecutor) Identifier() string { return "xai" }

func (e *xaiImagesCaptureExecutor) Execute(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	e.calls++
	e.requestModel = req.Model
	e.payloadModel = gjson.GetBytes(req.Payload, "model").String()
	return coreexecutor.Response{Payload: []byte(`{"created":1,"data":[{"b64_json":"aW1n"}]}`)}, nil
}

func (e *xaiImagesCaptureExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (e *xaiImagesCaptureExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *xaiImagesCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *xaiImagesCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

// registerPrefixedXAIImagesAuth mirrors force-model-prefix: the xAI credential
// registers only prefixed image model IDs.
func registerPrefixedXAIImagesAuth(t *testing.T, authID string, prefix string) (*xaiImagesCaptureExecutor, *OpenAIAPIHandler) {
	t.Helper()
	executor := &xaiImagesCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{ID: authID, Provider: "xai", Prefix: prefix, Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{
		{ID: prefix + "/" + defaultXAIImagesModel, Type: "xai"},
		{ID: prefix + "/" + xaiImages20Model, Type: "xai"},
	})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})
	return executor, NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
}

func TestXAIImagesAcceptOnlyPrefixesRegisteredByXAI(t *testing.T) {
	registerPrefixedXAIImagesAuth(t, "xai-images-prefix-validation", "team")

	if !isSupportedImagesModel("team/grok-imagine-image") {
		t.Fatal("expected registered prefixed xAI image model to be supported")
	}
	if isSupportedImagesModel("other/grok-imagine-image") {
		t.Fatal("expected unregistered prefix to be rejected")
	}
	if got := xaiImagesRouteModel("team/grok-imagine-image-2.0"); got != "team/grok-imagine-image-2.0" {
		t.Fatalf("route model = %q, want prefixed ID", got)
	}
	if got := xaiImagesRouteModel("xai/grok-imagine-image-2.0"); got != xaiImages20Model {
		t.Fatalf("route model = %q, want %q", got, xaiImages20Model)
	}
}

func TestXAIImagesGenerationsRoutePrefixedModelAndSendCanonicalModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	executor, h := registerPrefixedXAIImagesAuth(t, "xai-images-prefix-generation", "team")

	resp := performImagesEndpointRequest(t, imagesGenerationsPath, "application/json",
		strings.NewReader(`{"model":"team/grok-imagine-image-2.0","prompt":"cat"}`), h.ImagesGenerations)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if executor.calls != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.calls)
	}
	if executor.requestModel != xaiImages20Model {
		t.Fatalf("executor request model = %q, want %q", executor.requestModel, xaiImages20Model)
	}
	if executor.payloadModel != xaiImages20Model {
		t.Fatalf("upstream payload model = %q, want %q", executor.payloadModel, xaiImages20Model)
	}
}
