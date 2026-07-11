package openai

import (
	"bytes"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

func performImagesEndpointRequest(t *testing.T, endpointPath string, contentType string, body io.Reader, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST(endpointPath, handler)

	req := httptest.NewRequest(http.MethodPost, endpointPath, body)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	return resp
}

type capturedImageExecution struct {
	authID  string
	model   string
	payload []byte
}

type imageAuthCaptureExecutor struct {
	identifier string
	mu         sync.Mutex
	executions []capturedImageExecution
}

func (e *imageAuthCaptureExecutor) Identifier() string {
	if e != nil && e.identifier != "" {
		return e.identifier
	}
	return "xai"
}

func (e *imageAuthCaptureExecutor) Execute(_ context.Context, auth *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	e.capture(auth, req)
	return coreexecutor.Response{Payload: []byte(`{"created":123,"data":[{"b64_json":"AA=="}]}`)}, nil
}

func (e *imageAuthCaptureExecutor) capture(auth *coreauth.Auth, req coreexecutor.Request) {
	authID := ""
	if auth != nil {
		authID = auth.ID
	}
	e.mu.Lock()
	e.executions = append(e.executions, capturedImageExecution{
		authID:  authID,
		model:   req.Model,
		payload: append([]byte(nil), req.Payload...),
	})
	e.mu.Unlock()
}

func (e *imageAuthCaptureExecutor) ExecuteStream(_ context.Context, auth *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.capture(auth, req)
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: []byte("data: {\"type\":\"image_generation.completed\"}\n\n")}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (*imageAuthCaptureExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (*imageAuthCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, &coreauth.Error{Code: "not_implemented", Message: "CountTokens not implemented"}
}

func (*imageAuthCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, &coreauth.Error{Code: "not_implemented", Message: "HttpRequest not implemented"}
}

func (e *imageAuthCaptureExecutor) Executions() []capturedImageExecution {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]capturedImageExecution, len(e.executions))
	for i := range e.executions {
		out[i] = capturedImageExecution{
			authID:  e.executions[i].authID,
			model:   e.executions[i].model,
			payload: append([]byte(nil), e.executions[i].payload...),
		}
	}
	return out
}

func assertUnsupportedImagesModelResponse(t *testing.T, resp *httptest.ResponseRecorder, model string) {
	t.Helper()

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusBadRequest, resp.Body.String())
	}

	message := gjson.GetBytes(resp.Body.Bytes(), "error.message").String()
	expectedMessage := "Model " + model + " is not supported on " + imagesGenerationsPath + " or " + imagesEditsPath + ". Use " + gptImage15Model + ", " + defaultImagesToolModel + ", " + defaultXAIImagesModel + ", " + xaiImagesQualityModel + ", or a configured openai-compatibility image model."
	if message != expectedMessage {
		t.Fatalf("error message = %q, want %q", message, expectedMessage)
	}
	if errorType := gjson.GetBytes(resp.Body.Bytes(), "error.type").String(); errorType != "invalid_request_error" {
		t.Fatalf("error type = %q, want invalid_request_error", errorType)
	}
}

func assertImagesModelClassification(t *testing.T, model string, wantKind imagesModelKind, wantDynamic bool) {
	t.Helper()
	got := classifyImagesModel(model)
	if got.kind != wantKind || got.dynamic != wantDynamic {
		t.Fatalf("classifyImagesModel(%q) = %+v, want kind %d dynamic %v", model, got, wantKind, wantDynamic)
	}
}

func TestImagesModelValidationAllowsGPTImageAndXAIModels(t *testing.T) {
	for _, model := range []string{"gpt-image-1.5", "codex/gpt-image-1.5", "gpt-image-2", "codex/gpt-image-2", "grok-imagine-image", "xai/grok-imagine-image", "grok-imagine-image-quality", "xai/grok-imagine-image-quality"} {
		if !isSupportedImagesModel(model) {
			t.Fatalf("expected %s to be supported", model)
		}
	}
	if isSupportedImagesModel("gpt-5.4-mini") {
		t.Fatal("expected gpt-5.4-mini to be rejected")
	}
	if isSupportedImagesModel("codex/grok-imagine-image") {
		t.Fatal("expected codex/grok-imagine-image to be rejected")
	}
}

func TestImagesModelClassifierDynamicPrecedenceMatrix(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	legacyModels := []struct {
		id           string
		fallbackKind imagesModelKind
	}{
		{id: defaultXAIImagesModel, fallbackKind: imagesModelXAI},
		{id: xaiImagesQualityModel, fallbackKind: imagesModelXAI},
		{id: gptImage15Model, fallbackKind: imagesModelCodex},
		{id: defaultImagesToolModel, fallbackKind: imagesModelCodex},
	}

	t.Run("compatibility registrations override legacy basenames", func(t *testing.T) {
		const clientID = "compatibility-legacy-image-names"
		models := make([]*registry.ModelInfo, 0, len(legacyModels))
		for _, model := range legacyModels {
			models = append(models, &registry.ModelInfo{
				ID:               model.id,
				Type:             registry.OpenAIImageModelType,
				SupportsImageAPI: true,
				ChatDisabled:     true,
			})
		}
		modelRegistry.RegisterClient(clientID, "openai-compatibility", models)
		defer modelRegistry.UnregisterClient(clientID)
		for _, model := range legacyModels {
			assertImagesModelClassification(t, model.id, imagesModelOpenAICompat, true)
		}

		modelRegistry.UnregisterClient(clientID)
		for _, model := range legacyModels {
			assertImagesModelClassification(t, model.id, model.fallbackKind, false)
		}
	})

	t.Run("chat-only registrations suppress legacy basenames", func(t *testing.T) {
		const clientID = "chat-only-legacy-image-names"
		models := make([]*registry.ModelInfo, 0, len(legacyModels))
		for _, model := range legacyModels {
			models = append(models, &registry.ModelInfo{
				ID:   model.id,
				Type: "openai-compatibility",
			})
		}
		modelRegistry.RegisterClient(clientID, "openai-compatibility", models)
		defer modelRegistry.UnregisterClient(clientID)

		for _, model := range legacyModels {
			assertImagesModelClassification(t, model.id, imagesModelUnsupported, true)
		}
	})

	t.Run("native xai wins a shared Codex-name registration", func(t *testing.T) {
		const (
			modelID        = defaultImagesToolModel
			xaiClientID    = "shared-codex-name-xai"
			compatClientID = "shared-codex-name-compat"
		)
		imageInfo := func(modelType string) *registry.ModelInfo {
			return &registry.ModelInfo{
				ID:               modelID,
				Type:             modelType,
				SupportsImageAPI: true,
				ChatDisabled:     true,
			}
		}
		modelRegistry.RegisterClient(compatClientID, "openai-compatibility", []*registry.ModelInfo{imageInfo(registry.OpenAIImageModelType)})
		modelRegistry.RegisterClient(xaiClientID, "xai", []*registry.ModelInfo{imageInfo("xai")})
		defer modelRegistry.UnregisterClient(compatClientID)
		defer modelRegistry.UnregisterClient(xaiClientID)

		assertImagesModelClassification(t, modelID, imagesModelXAI, true)
	})

	t.Run("native Codex wins an xai-name registration", func(t *testing.T) {
		const (
			modelID        = defaultXAIImagesModel
			codexClientID  = "shared-xai-name-codex"
			compatClientID = "shared-xai-name-compat"
		)
		modelRegistry.RegisterClient(compatClientID, "openai-compatibility", []*registry.ModelInfo{{
			ID:               modelID,
			Type:             registry.OpenAIImageModelType,
			SupportsImageAPI: true,
			ChatDisabled:     true,
		}})
		modelRegistry.RegisterClient(codexClientID, "codex", []*registry.ModelInfo{{
			ID:               modelID,
			Type:             "codex",
			SupportsImageAPI: true,
			ChatDisabled:     true,
		}})
		defer modelRegistry.UnregisterClient(compatClientID)
		defer modelRegistry.UnregisterClient(codexClientID)

		assertImagesModelClassification(t, modelID, imagesModelCodex, true)
	})
}

func TestCodexImagesToolClassifierDefersToRegisteredXAI(t *testing.T) {
	const clientID = "xai-codex-name-collision"
	models := []string{
		gptImage15Model,
		"tenant/" + defaultImagesToolModel,
	}
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.UnregisterClient(clientID)
	for _, model := range models {
		if !isCodexImagesToolModel(model) {
			t.Fatalf("isCodexImagesToolModel(%q) = false before xAI registration, want true", model)
		}
	}
	modelRegistry.RegisterClient(clientID, "xai", []*registry.ModelInfo{
		{
			ID:               models[0],
			SupportsImageAPI: true,
			ChatDisabled:     true,
		},
		{
			ID:               models[1],
			SupportsImageAPI: true,
			ChatDisabled:     true,
		},
	})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(clientID)
	})

	for _, model := range models {
		if isCodexImagesToolModel(model) {
			t.Fatalf("isCodexImagesToolModel(%q) = true after xAI registration, want false", model)
		}
	}
}

func TestImagesModelValidationAllowsOpenAICompatImageModels(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	clientID := "test-openai-compat-image-model-validation"
	shadowClientID := "test-openai-compat-image-model-validation-shadow"
	modelRegistry.RegisterClient(clientID, "openai-compatibility", []*registry.ModelInfo{
		{ID: "compat-image-model", Object: "model", OwnedBy: "compat", Type: registry.OpenAIImageModelType},
		{ID: "tenant/compat-image-model", Object: "model", OwnedBy: "compat", Type: registry.OpenAIImageModelType},
		{ID: "compat-shared-model", Object: "model", OwnedBy: "compat", Type: "openai-compatibility", SupportsImageAPI: true},
		{ID: "compat-chat-model", Object: "model", OwnedBy: "compat", Type: "openai-compatibility"},
	})
	modelRegistry.RegisterClient(shadowClientID, "openai-compatibility", []*registry.ModelInfo{
		{ID: "compat-shared-model", Object: "model", OwnedBy: "chat", Type: "openai"},
	})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(shadowClientID)
		modelRegistry.UnregisterClient(clientID)
	})

	if !isSupportedImagesModel("compat-image-model") {
		t.Fatal("expected configured openai-compatibility image model to be supported")
	}
	if !isSupportedImagesModel("tenant/compat-image-model") {
		t.Fatal("expected prefixed openai-compatibility image model to be supported")
	}
	if !isSupportedImagesModel("compat-shared-model") {
		t.Fatal("expected image-capable provider registration to remain visible after a chat-only registration")
	}
	if isXAIImagesModel("compat-image-model") || !isOpenAICompatImagesModel("compat-image-model") {
		t.Fatal("expected configured openai-compatibility image model to retain compatibility request handling")
	}
	if isSupportedImagesModel("compat-chat-model") {
		t.Fatal("expected non-image openai-compatibility model to be rejected")
	}
}

func TestRegisteredXAIImagesModelRequiresProviderSpecificImageCapability(t *testing.T) {
	const (
		modelID        = "shared-xai-chat-compat-image"
		xaiClientID    = "shared-xai-chat-client"
		compatClientID = "shared-compat-image-client"
	)
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(xaiClientID, "xai", []*registry.ModelInfo{{
		ID:   modelID,
		Type: "xai",
	}})
	modelRegistry.RegisterClient(compatClientID, "openai-compatibility", []*registry.ModelInfo{{
		ID:               modelID,
		Type:             registry.OpenAIImageModelType,
		SupportsImageAPI: true,
		ChatDisabled:     true,
	}})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(compatClientID)
		modelRegistry.UnregisterClient(xaiClientID)
	})

	if isXAIImagesModel(modelID) {
		t.Fatalf("isXAIImagesModel(%q) = true, want xAI chat registration ignored", modelID)
	}
	if !isOpenAICompatImagesModel(modelID) {
		t.Fatalf("isOpenAICompatImagesModel(%q) = false, want compatibility image route", modelID)
	}
}

func TestRegisteredXAIImagesModelAggregatesProviderClients(t *testing.T) {
	tests := []struct {
		name       string
		imageFirst bool
	}{
		{name: "image then chat", imageFirst: true},
		{name: "chat then image", imageFirst: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			modelID := "shared-xai-image-" + strings.ReplaceAll(tc.name, " ", "-")
			imageClientID := modelID + "-image"
			chatClientID := modelID + "-chat"
			modelRegistry := registry.GetGlobalRegistry()
			registerImage := func() {
				modelRegistry.RegisterClient(imageClientID, "xai", []*registry.ModelInfo{{
					ID:               modelID,
					Type:             "xai",
					SupportsImageAPI: true,
					ChatDisabled:     true,
				}})
			}
			registerChat := func() {
				modelRegistry.RegisterClient(chatClientID, "xai", []*registry.ModelInfo{{
					ID:   modelID,
					Type: "xai",
				}})
			}
			if tc.imageFirst {
				registerImage()
				registerChat()
			} else {
				registerChat()
				registerImage()
			}
			t.Cleanup(func() {
				modelRegistry.UnregisterClient(chatClientID)
				modelRegistry.UnregisterClient(imageClientID)
			})

			if !isXAIImagesModel(modelID) {
				t.Fatalf("isXAIImagesModel(%q) = false after %s", modelID, tc.name)
			}
			if got := canonicalXAIImagesModel(modelID); got != modelID {
				t.Fatalf("canonicalXAIImagesModel(%q) = %q, want exact route", modelID, got)
			}

			modelRegistry.UnregisterClient(imageClientID)
			if isXAIImagesModel(modelID) {
				t.Fatalf("isXAIImagesModel(%q) = true after image client removal", modelID)
			}
		})
	}
}

func TestBuildXAIImagesGenerationsRequest(t *testing.T) {
	rawJSON := []byte(`{"model":"xai/grok-imagine-image-quality","prompt":"abstract art","aspect_ratio":"landscape","resolution":"2k","n":2,"response_format":"url"}`)
	model := "xai/grok-imagine-image-quality"

	req := buildXAIImagesGenerationsRequest(rawJSON, model, classifyImagesModel(model), "url")

	if got := gjson.GetBytes(req, "model").String(); got != "grok-imagine-image-quality" {
		t.Fatalf("model = %q, want grok-imagine-image-quality", got)
	}
	if got := gjson.GetBytes(req, "prompt").String(); got != "abstract art" {
		t.Fatalf("prompt = %q, want abstract art", got)
	}
	if got := gjson.GetBytes(req, "aspect_ratio").String(); got != "16:9" {
		t.Fatalf("aspect_ratio = %q, want 16:9", got)
	}
	if got := gjson.GetBytes(req, "resolution").String(); got != "2k" {
		t.Fatalf("resolution = %q, want 2k", got)
	}
	if got := gjson.GetBytes(req, "response_format").String(); got != "url" {
		t.Fatalf("response_format = %q, want url", got)
	}
	if got := gjson.GetBytes(req, "n").Int(); got != 2 {
		t.Fatalf("n = %d, want 2", got)
	}
}

func TestBuildXAIImagesEditRequest(t *testing.T) {
	model := "grok-imagine-image"
	req := buildXAIImagesEditRequest(model, classifyImagesModel(model), "edit it", []string{"data:image/png;base64,AA==", "https://example.com/image.png"}, "b64_json", "3:2", "1k", 0)

	if got := gjson.GetBytes(req, "model").String(); got != "grok-imagine-image" {
		t.Fatalf("model = %q, want grok-imagine-image", got)
	}
	if got := gjson.GetBytes(req, "images.0.type").String(); got != "image_url" {
		t.Fatalf("images.0.type = %q, want image_url", got)
	}
	if got := gjson.GetBytes(req, "images.0.url").String(); got != "data:image/png;base64,AA==" {
		t.Fatalf("images.0.url = %q", got)
	}
	if got := gjson.GetBytes(req, "images.1.url").String(); got != "https://example.com/image.png" {
		t.Fatalf("images.1.url = %q", got)
	}
	if gjson.GetBytes(req, "image").Exists() {
		t.Fatalf("multiple image edits must use images array: %s", string(req))
	}
}

func TestBuildXAIImagesEditRequestSingleImage(t *testing.T) {
	model := "grok-imagine-image"
	req := buildXAIImagesEditRequest(model, classifyImagesModel(model), "edit it", []string{"https://example.com/image.png"}, "url", "", "", 0)

	if got := gjson.GetBytes(req, "image.type").String(); got != "image_url" {
		t.Fatalf("image.type = %q, want image_url", got)
	}
	if got := gjson.GetBytes(req, "image.url").String(); got != "https://example.com/image.png" {
		t.Fatalf("image.url = %q", got)
	}
	if gjson.GetBytes(req, "images").Exists() {
		t.Fatalf("single image edit must use image object: %s", string(req))
	}
}

func TestNativeXAIImageAliasUsesXAIRequestNormalization(t *testing.T) {
	const (
		prefix        = "tenant"
		publicAlias   = "public-image"
		routeAlias    = prefix + "/" + publicAlias
		upstreamModel = xaiImagesQualityModel
		apiKey        = "xai-image-handler-key"
		authID        = "xai-image-handler-alias-auth"
		compatAuthID  = "compat-image-handler-alias-auth"
	)

	executor := &imageAuthCaptureExecutor{}
	compatExecutor := &imageAuthCaptureExecutor{identifier: "openai-compatibility"}
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	manager.RegisterExecutor(executor)
	manager.RegisterExecutor(compatExecutor)
	manager.SetConfig(&sdkconfig.Config{XAIKey: []sdkconfig.XAIKey{{
		APIKey: apiKey,
		Prefix: prefix,
		Models: []sdkconfig.XAIModel{{
			Name:  upstreamModel,
			Alias: publicAlias,
		}},
	}}})
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "xai",
		Prefix:   prefix,
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			coreauth.AttributeAPIKey: apiKey,
			coreauth.AttributeSource: "config:xai[test]",
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("manager.Register(): %v", errRegister)
	}
	compatAuth := &coreauth.Auth{
		ID:       compatAuthID,
		Provider: "openai-compatibility",
		Status:   coreauth.StatusActive,
	}
	if _, errRegister := manager.Register(context.Background(), compatAuth); errRegister != nil {
		t.Fatalf("manager.Register(compat): %v", errRegister)
	}

	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(authID, auth.Provider, []*registry.ModelInfo{{
		ID:               routeAlias,
		Type:             "xai",
		SupportsImageAPI: true,
		ChatDisabled:     true,
	}})
	modelRegistry.RegisterClient(compatAuthID, compatAuth.Provider, []*registry.ModelInfo{{
		ID:               routeAlias,
		Type:             registry.OpenAIImageModelType,
		SupportsImageAPI: true,
		ChatDisabled:     true,
	}})
	manager.RefreshSchedulerEntry(authID)
	manager.RefreshSchedulerEntry(compatAuthID)
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(compatAuthID)
		modelRegistry.UnregisterClient(authID)
	})

	if !isXAIImagesModel(routeAlias) {
		t.Fatalf("isXAIImagesModel(%q) = false, want native xAI registration", routeAlias)
	}
	if isOpenAICompatImagesModel(routeAlias) {
		t.Fatalf("isOpenAICompatImagesModel(%q) = true, want native xAI request handling", routeAlias)
	}
	if got := canonicalXAIImagesModel(routeAlias); got != routeAlias {
		t.Fatalf("canonicalXAIImagesModel(%q) = %q, want public route for auth mapping", routeAlias, got)
	}

	handler := NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
	generationResp := performImagesEndpointRequest(
		t,
		imagesGenerationsPath,
		"application/json",
		strings.NewReader(`{"model":"`+routeAlias+`","prompt":"draw","size":"1792x1024"}`),
		handler.ImagesGenerations,
	)
	if generationResp.Code != http.StatusOK {
		t.Fatalf("generation status = %d, want %d: %s", generationResp.Code, http.StatusOK, generationResp.Body.String())
	}

	var editBody bytes.Buffer
	writer := multipart.NewWriter(&editBody)
	if errWrite := writer.WriteField("model", routeAlias); errWrite != nil {
		t.Fatalf("write model field: %v", errWrite)
	}
	if errWrite := writer.WriteField("prompt", "edit"); errWrite != nil {
		t.Fatalf("write prompt field: %v", errWrite)
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", multipart.FileContentDisposition("image", "image.png"))
	header.Set("Content-Type", "image/png")
	part, errCreate := writer.CreatePart(header)
	if errCreate != nil {
		t.Fatalf("create image field: %v", errCreate)
	}
	if _, errWrite := part.Write([]byte("png-data")); errWrite != nil {
		t.Fatalf("write image field: %v", errWrite)
	}
	if errClose := writer.Close(); errClose != nil {
		t.Fatalf("close multipart writer: %v", errClose)
	}
	editResp := performImagesEndpointRequest(t, imagesEditsPath, writer.FormDataContentType(), &editBody, handler.ImagesEdits)
	if editResp.Code != http.StatusOK {
		t.Fatalf("edit status = %d, want %d: %s", editResp.Code, http.StatusOK, editResp.Body.String())
	}

	jsonEditResp := performImagesEndpointRequest(
		t,
		imagesEditsPath,
		"application/json",
		strings.NewReader(`{"model":"`+routeAlias+`","prompt":"edit JSON","images":[{"image_url":"https://example.com/input-1.png"},{"image_url":"https://example.com/input-2.png"}],"size":"1024x1536"}`),
		handler.ImagesEdits,
	)
	if jsonEditResp.Code != http.StatusOK {
		t.Fatalf("JSON edit status = %d, want %d: %s", jsonEditResp.Code, http.StatusOK, jsonEditResp.Body.String())
	}

	streamResp := performImagesEndpointRequest(
		t,
		imagesGenerationsPath,
		"application/json",
		strings.NewReader(`{"model":"`+routeAlias+`","prompt":"draw stream","stream":true}`),
		handler.ImagesGenerations,
	)
	if streamResp.Code != http.StatusOK {
		t.Fatalf("stream generation status = %d, want %d: %s", streamResp.Code, http.StatusOK, streamResp.Body.String())
	}

	executions := executor.Executions()
	if len(executions) != 4 {
		t.Fatalf("xAI execution count = %d, want 4", len(executions))
	}
	if executions := compatExecutor.Executions(); len(executions) != 0 {
		t.Fatalf("compatibility executions = %#v, want none while native xAI registration exists", executions)
	}
	for i, execution := range executions {
		if execution.authID != authID || execution.model != upstreamModel {
			t.Fatalf("execution %d = auth %q model %q, want %q %q", i, execution.authID, execution.model, authID, upstreamModel)
		}
		if got := gjson.GetBytes(execution.payload, "model").String(); got != routeAlias {
			t.Fatalf("execution %d payload model = %q, want route alias %q", i, got, routeAlias)
		}
	}
	if got := gjson.GetBytes(executions[0].payload, "aspect_ratio").String(); got != "16:9" {
		t.Fatalf("generation aspect_ratio = %q, want 16:9; body=%s", got, executions[0].payload)
	}
	if got := gjson.GetBytes(executions[0].payload, "resolution").String(); got != "1k" {
		t.Fatalf("generation resolution = %q, want 1k; body=%s", got, executions[0].payload)
	}
	if got := gjson.GetBytes(executions[1].payload, "image.type").String(); got != "image_url" {
		t.Fatalf("edit image.type = %q, want image_url; body=%s", got, executions[1].payload)
	}
	if got := gjson.GetBytes(executions[1].payload, "image.url").String(); !strings.HasPrefix(got, "data:image/png;base64,") {
		t.Fatalf("edit image.url = %q, want image/png data URL", got)
	}
	if got := gjson.GetBytes(executions[2].payload, "images.0.url").String(); got != "https://example.com/input-1.png" {
		t.Fatalf("JSON edit images.0.url = %q, want first source URL; body=%s", got, executions[2].payload)
	}
	if got := gjson.GetBytes(executions[2].payload, "images.1.url").String(); got != "https://example.com/input-2.png" {
		t.Fatalf("JSON edit images.1.url = %q, want second source URL; body=%s", got, executions[2].payload)
	}
	if got := gjson.GetBytes(executions[2].payload, "aspect_ratio").String(); got != "2:3" {
		t.Fatalf("JSON edit aspect_ratio = %q, want 2:3; body=%s", got, executions[2].payload)
	}

	modelRegistry.UnregisterClient(authID)
	compatResp := performImagesEndpointRequest(
		t,
		imagesGenerationsPath,
		"application/json",
		strings.NewReader(`{"model":"`+routeAlias+`","prompt":"draw compat","size":"1024x1024"}`),
		handler.ImagesGenerations,
	)
	if compatResp.Code != http.StatusOK {
		t.Fatalf("compatibility generation status = %d, want %d: %s", compatResp.Code, http.StatusOK, compatResp.Body.String())
	}
	compatExecutions := compatExecutor.Executions()
	if len(compatExecutions) != 1 {
		t.Fatalf("compatibility execution count = %d, want 1 after xAI removal", len(compatExecutions))
	}
	if got := gjson.GetBytes(compatExecutions[0].payload, "size").String(); got != "1024x1024" {
		t.Fatalf("compatibility size = %q, want 1024x1024; body=%s", got, compatExecutions[0].payload)
	}
	if gjson.GetBytes(compatExecutions[0].payload, "aspect_ratio").Exists() {
		t.Fatalf("compatibility payload received xAI normalization: %s", compatExecutions[0].payload)
	}
}

func TestOpenAICompatImageAliasesOverrideBuiltinXAINames(t *testing.T) {
	for _, modelID := range []string{defaultXAIImagesModel, xaiImagesQualityModel} {
		t.Run(modelID, func(t *testing.T) {
			authID := "compat-builtin-xai-name-" + modelID
			executor := &imageAuthCaptureExecutor{identifier: "openai-compatibility"}
			manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
			manager.RegisterExecutor(executor)
			auth := &coreauth.Auth{
				ID:       authID,
				Provider: "openai-compatibility",
				Status:   coreauth.StatusActive,
			}
			if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
				t.Fatalf("manager.Register(): %v", errRegister)
			}

			modelRegistry := registry.GetGlobalRegistry()
			modelRegistry.RegisterClient(authID, auth.Provider, []*registry.ModelInfo{{
				ID:               modelID,
				Type:             registry.OpenAIImageModelType,
				SupportsImageAPI: true,
				ChatDisabled:     true,
			}})
			manager.RefreshSchedulerEntry(authID)
			t.Cleanup(func() {
				modelRegistry.UnregisterClient(authID)
			})

			assertImagesModelClassification(t, modelID, imagesModelOpenAICompat, true)
			handler := NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))

			generationResp := performImagesEndpointRequest(
				t,
				imagesGenerationsPath,
				"application/json",
				strings.NewReader(`{"model":"`+modelID+`","prompt":"draw","size":"1024x1024"}`),
				handler.ImagesGenerations,
			)
			if generationResp.Code != http.StatusOK {
				t.Fatalf("generation status = %d, want %d: %s", generationResp.Code, http.StatusOK, generationResp.Body.String())
			}

			var editBody bytes.Buffer
			writer := multipart.NewWriter(&editBody)
			if errWrite := writer.WriteField("model", modelID); errWrite != nil {
				t.Fatalf("write model field: %v", errWrite)
			}
			if errWrite := writer.WriteField("prompt", "edit"); errWrite != nil {
				t.Fatalf("write prompt field: %v", errWrite)
			}
			header := make(textproto.MIMEHeader)
			header.Set("Content-Disposition", multipart.FileContentDisposition("image", "image.png"))
			header.Set("Content-Type", "image/png")
			part, errCreate := writer.CreatePart(header)
			if errCreate != nil {
				t.Fatalf("create image field: %v", errCreate)
			}
			if _, errWrite := part.Write([]byte("png-data")); errWrite != nil {
				t.Fatalf("write image field: %v", errWrite)
			}
			if errClose := writer.Close(); errClose != nil {
				t.Fatalf("close multipart writer: %v", errClose)
			}
			editResp := performImagesEndpointRequest(t, imagesEditsPath, writer.FormDataContentType(), &editBody, handler.ImagesEdits)
			if editResp.Code != http.StatusOK {
				t.Fatalf("multipart edit status = %d, want %d: %s", editResp.Code, http.StatusOK, editResp.Body.String())
			}

			jsonEditResp := performImagesEndpointRequest(
				t,
				imagesEditsPath,
				"application/json",
				strings.NewReader(`{"model":"`+modelID+`","prompt":"edit JSON","images":[{"image_url":"https://example.com/input.png"}],"size":"1024x1536"}`),
				handler.ImagesEdits,
			)
			if jsonEditResp.Code != http.StatusOK {
				t.Fatalf("JSON edit status = %d, want %d: %s", jsonEditResp.Code, http.StatusOK, jsonEditResp.Body.String())
			}

			streamResp := performImagesEndpointRequest(
				t,
				imagesGenerationsPath,
				"application/json",
				strings.NewReader(`{"model":"`+modelID+`","prompt":"draw stream","stream":true}`),
				handler.ImagesGenerations,
			)
			if streamResp.Code != http.StatusOK {
				t.Fatalf("stream generation status = %d, want %d: %s", streamResp.Code, http.StatusOK, streamResp.Body.String())
			}

			executions := executor.Executions()
			if len(executions) != 4 {
				t.Fatalf("compatibility execution count = %d, want 4", len(executions))
			}
			for i, execution := range executions {
				if execution.authID != authID || execution.model != modelID {
					t.Fatalf("execution %d = auth %q model %q, want %q %q", i, execution.authID, execution.model, authID, modelID)
				}
			}
			if got := gjson.GetBytes(executions[0].payload, "size").String(); got != "1024x1024" {
				t.Fatalf("compatibility size = %q, want 1024x1024; body=%s", got, executions[0].payload)
			}
			if gjson.GetBytes(executions[0].payload, "aspect_ratio").Exists() {
				t.Fatalf("compatibility generation received xAI normalization: %s", executions[0].payload)
			}
			if !bytes.Contains(executions[1].payload, []byte(`name="image"; filename="image.png"`)) {
				t.Fatalf("compatibility edit did not preserve multipart image: %s", executions[1].payload)
			}
			if gjson.ValidBytes(executions[1].payload) {
				t.Fatalf("compatibility edit was normalized to xAI JSON: %s", executions[1].payload)
			}
			if got := gjson.GetBytes(executions[2].payload, "images.0.image_url").String(); got != "https://example.com/input.png" {
				t.Fatalf("compatibility JSON edit image = %q, want source URL; body=%s", got, executions[2].payload)
			}
			if gjson.GetBytes(executions[2].payload, "aspect_ratio").Exists() {
				t.Fatalf("compatibility JSON edit received xAI normalization: %s", executions[2].payload)
			}
			if !gjson.GetBytes(executions[3].payload, "stream").Bool() {
				t.Fatalf("compatibility stream flag missing: %s", executions[3].payload)
			}

			modelRegistry.UnregisterClient(authID)
			assertImagesModelClassification(t, modelID, imagesModelXAI, false)
		})
	}
}

func TestNativeXAIImageAliasTakesPrecedenceOverCodexName(t *testing.T) {
	const (
		prefix        = "tenant"
		publicAlias   = defaultImagesToolModel
		routeAlias    = prefix + "/" + publicAlias
		upstreamModel = xaiImagesQualityModel
		apiKey        = "xai-codex-name-collision-key"
		authID        = "xai-codex-name-collision-auth"
	)

	executor := &imageAuthCaptureExecutor{}
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	manager.RegisterExecutor(executor)
	manager.SetConfig(&sdkconfig.Config{XAIKey: []sdkconfig.XAIKey{{
		APIKey: apiKey,
		Prefix: prefix,
		Models: []sdkconfig.XAIModel{{
			Name:  upstreamModel,
			Alias: publicAlias,
		}},
	}}})
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "xai",
		Prefix:   prefix,
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			coreauth.AttributeAPIKey: apiKey,
			coreauth.AttributeSource: "config:xai[codex-name-collision]",
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("manager.Register(): %v", errRegister)
	}
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(authID, auth.Provider, []*registry.ModelInfo{{
		ID:               routeAlias,
		Type:             "xai",
		SupportsImageAPI: true,
		ChatDisabled:     true,
	}})
	manager.RefreshSchedulerEntry(authID)
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(authID)
	})

	if isCodexImagesToolModel(routeAlias) {
		t.Fatalf("isCodexImagesToolModel(%q) = true, want exact xAI registration precedence", routeAlias)
	}

	handler := NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
	generationResp := performImagesEndpointRequest(
		t,
		imagesGenerationsPath,
		"application/json",
		strings.NewReader(`{"model":"`+routeAlias+`","prompt":"draw","size":"1792x1024"}`),
		handler.ImagesGenerations,
	)
	if generationResp.Code != http.StatusOK {
		t.Fatalf("generation status = %d, want %d: %s", generationResp.Code, http.StatusOK, generationResp.Body.String())
	}

	var editBody bytes.Buffer
	writer := multipart.NewWriter(&editBody)
	if errWrite := writer.WriteField("model", routeAlias); errWrite != nil {
		t.Fatalf("write model field: %v", errWrite)
	}
	if errWrite := writer.WriteField("prompt", "edit"); errWrite != nil {
		t.Fatalf("write prompt field: %v", errWrite)
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", multipart.FileContentDisposition("image", "image.png"))
	header.Set("Content-Type", "image/png")
	part, errCreate := writer.CreatePart(header)
	if errCreate != nil {
		t.Fatalf("create image field: %v", errCreate)
	}
	if _, errWrite := part.Write([]byte("png-data")); errWrite != nil {
		t.Fatalf("write image field: %v", errWrite)
	}
	if errClose := writer.Close(); errClose != nil {
		t.Fatalf("close multipart writer: %v", errClose)
	}
	editResp := performImagesEndpointRequest(t, imagesEditsPath, writer.FormDataContentType(), &editBody, handler.ImagesEdits)
	if editResp.Code != http.StatusOK {
		t.Fatalf("edit status = %d, want %d: %s", editResp.Code, http.StatusOK, editResp.Body.String())
	}

	jsonEditResp := performImagesEndpointRequest(
		t,
		imagesEditsPath,
		"application/json",
		strings.NewReader(`{"model":"`+routeAlias+`","prompt":"edit JSON","images":[{"image_url":"https://example.com/input.png"}]}`),
		handler.ImagesEdits,
	)
	if jsonEditResp.Code != http.StatusOK {
		t.Fatalf("JSON edit status = %d, want %d: %s", jsonEditResp.Code, http.StatusOK, jsonEditResp.Body.String())
	}

	streamResp := performImagesEndpointRequest(
		t,
		imagesGenerationsPath,
		"application/json",
		strings.NewReader(`{"model":"`+routeAlias+`","prompt":"draw stream","stream":true}`),
		handler.ImagesGenerations,
	)
	if streamResp.Code != http.StatusOK {
		t.Fatalf("stream generation status = %d, want %d: %s", streamResp.Code, http.StatusOK, streamResp.Body.String())
	}

	executions := executor.Executions()
	if len(executions) != 4 {
		t.Fatalf("xAI execution count = %d, want 4", len(executions))
	}
	for i, execution := range executions {
		if execution.authID != authID || execution.model != upstreamModel {
			t.Fatalf("execution %d = auth %q model %q, want %q %q", i, execution.authID, execution.model, authID, upstreamModel)
		}
		if got := gjson.GetBytes(execution.payload, "model").String(); got != routeAlias {
			t.Fatalf("execution %d payload model = %q, want route alias %q", i, got, routeAlias)
		}
	}
	if got := gjson.GetBytes(executions[0].payload, "aspect_ratio").String(); got != "16:9" {
		t.Fatalf("generation aspect_ratio = %q, want 16:9; body=%s", got, executions[0].payload)
	}
	if got := gjson.GetBytes(executions[1].payload, "image.url").String(); !strings.HasPrefix(got, "data:image/png;base64,") {
		t.Fatalf("multipart edit payload was not normalized to xAI JSON: %s", executions[1].payload)
	}
	if got := gjson.GetBytes(executions[2].payload, "image.url").String(); got != "https://example.com/input.png" {
		t.Fatalf("JSON edit image.url = %q, want source URL; body=%s", got, executions[2].payload)
	}
}

func TestRegisteredLegacyShapedXAIImageRoutePreservesPrefixForAuthSelection(t *testing.T) {
	const (
		prefix        = "xai"
		upstreamModel = xaiImagesQualityModel
		routeModel    = prefix + "/" + upstreamModel
		apiKey        = "xai-prefixed-image-key"
		authID        = "xai-prefixed-image-auth"
	)

	if got := canonicalXAIImagesModel(routeModel); got != upstreamModel {
		t.Fatalf("unregistered canonicalXAIImagesModel(%q) = %q, want legacy shorthand %q", routeModel, got, upstreamModel)
	}

	executor := &imageAuthCaptureExecutor{}
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	manager.RegisterExecutor(executor)
	manager.SetConfig(&sdkconfig.Config{
		SDKConfig: sdkconfig.SDKConfig{ForceModelPrefix: true},
		XAIKey: []sdkconfig.XAIKey{{
			APIKey: apiKey,
			Prefix: prefix,
			Models: []sdkconfig.XAIModel{{
				Name:  upstreamModel,
				Alias: upstreamModel,
			}},
		}},
	})
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "xai",
		Prefix:   prefix,
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			coreauth.AttributeAPIKey: apiKey,
			coreauth.AttributeSource: "config:xai[prefixed]",
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("manager.Register(): %v", errRegister)
	}
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(authID, auth.Provider, []*registry.ModelInfo{{
		ID:               routeModel,
		Type:             "xai",
		SupportsImageAPI: true,
		ChatDisabled:     true,
	}})
	manager.RefreshSchedulerEntry(authID)
	t.Cleanup(func() { modelRegistry.UnregisterClient(authID) })

	if got := canonicalXAIImagesModel(routeModel); got != routeModel {
		t.Fatalf("registered canonicalXAIImagesModel(%q) = %q, want exact public route", routeModel, got)
	}

	handler := NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
	resp := performImagesEndpointRequest(
		t,
		imagesGenerationsPath,
		"application/json",
		strings.NewReader(`{"model":"`+routeModel+`","prompt":"draw"}`),
		handler.ImagesGenerations,
	)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusOK, resp.Body.String())
	}
	executions := executor.Executions()
	if len(executions) != 1 {
		t.Fatalf("execution count = %d, want 1", len(executions))
	}
	if got := executions[0]; got.authID != authID || got.model != upstreamModel || gjson.GetBytes(got.payload, "model").String() != routeModel {
		t.Fatalf("execution = auth %q model %q payload %s, want %q %q with exact route", got.authID, got.model, got.payload, authID, upstreamModel)
	}
}

func TestBuildOpenAICompatImagesJSONRequestPreservesStreamForStreaming(t *testing.T) {
	req := buildOpenAICompatImagesJSONRequest([]byte(`{"model":"compat-image","prompt":"draw","stream":false}`), "upstream-image", true)

	if got := gjson.GetBytes(req, "model").String(); got != "upstream-image" {
		t.Fatalf("model = %q, want upstream-image; body=%s", got, string(req))
	}
	if !gjson.GetBytes(req, "stream").Bool() {
		t.Fatalf("stream flag missing: %s", string(req))
	}
}

func TestBuildOpenAICompatImagesJSONRequestDropsStreamForNonStreaming(t *testing.T) {
	req := buildOpenAICompatImagesJSONRequest([]byte(`{"model":"compat-image","prompt":"draw","stream":true}`), "upstream-image", false)

	if got := gjson.GetBytes(req, "model").String(); got != "upstream-image" {
		t.Fatalf("model = %q, want upstream-image; body=%s", got, string(req))
	}
	if gjson.GetBytes(req, "stream").Exists() {
		t.Fatalf("stream flag should be removed from non-streaming request: %s", string(req))
	}
}

func TestBuildOpenAICompatImagesMultipartRequestPreservesStreamAndFileContentType(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if errWrite := writer.WriteField("model", "compat-image"); errWrite != nil {
		t.Fatalf("write model field: %v", errWrite)
	}
	if errWrite := writer.WriteField("stream", "false"); errWrite != nil {
		t.Fatalf("write stream field: %v", errWrite)
	}
	if errWrite := writer.WriteField("prompt", "edit"); errWrite != nil {
		t.Fatalf("write prompt field: %v", errWrite)
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", multipart.FileContentDisposition("image", "image.png"))
	header.Set("Content-Type", "image/png")
	part, errCreate := writer.CreatePart(header)
	if errCreate != nil {
		t.Fatalf("create image field: %v", errCreate)
	}
	if _, errWrite := part.Write([]byte("png-data")); errWrite != nil {
		t.Fatalf("write image field: %v", errWrite)
	}
	if errClose := writer.Close(); errClose != nil {
		t.Fatalf("close multipart writer: %v", errClose)
	}

	reader := multipart.NewReader(bytes.NewReader(body.Bytes()), writer.Boundary())
	form, errRead := reader.ReadForm(32 << 20)
	if errRead != nil {
		t.Fatalf("read source form: %v", errRead)
	}
	defer func() {
		if errRemove := form.RemoveAll(); errRemove != nil {
			t.Fatalf("remove source form files: %v", errRemove)
		}
	}()

	out, contentType, errBuild := buildOpenAICompatImagesMultipartRequest(form, "upstream-image", true)
	if errBuild != nil {
		t.Fatalf("buildOpenAICompatImagesMultipartRequest error: %v", errBuild)
	}
	mediaType, params, errParse := mime.ParseMediaType(contentType)
	if errParse != nil {
		t.Fatalf("parse content type: %v", errParse)
	}
	if mediaType != "multipart/form-data" {
		t.Fatalf("media type = %q, want multipart/form-data", mediaType)
	}
	rewrittenReader := multipart.NewReader(bytes.NewReader(out), params["boundary"])
	rewrittenForm, errRead := rewrittenReader.ReadForm(32 << 20)
	if errRead != nil {
		t.Fatalf("read rewritten form: %v", errRead)
	}
	defer func() {
		if errRemove := rewrittenForm.RemoveAll(); errRemove != nil {
			t.Fatalf("remove rewritten form files: %v", errRemove)
		}
	}()
	if got := rewrittenForm.Value["model"]; len(got) != 1 || got[0] != "upstream-image" {
		t.Fatalf("model values = %#v, want upstream-image", got)
	}
	if got := rewrittenForm.Value["stream"]; len(got) != 1 || got[0] != "true" {
		t.Fatalf("stream values = %#v, want true", got)
	}
	if got := rewrittenForm.Value["prompt"]; len(got) != 1 || got[0] != "edit" {
		t.Fatalf("prompt values = %#v, want edit", got)
	}
	if got := rewrittenForm.File["image"]; len(got) != 1 || got[0].Header.Get("Content-Type") != "image/png" {
		t.Fatalf("image headers = %#v, want image/png", got)
	}
}

func TestBuildImagesAPIResponseFromXAI(t *testing.T) {
	payload := []byte(`{"created":123,"data":[{"b64_json":"AA==","revised_prompt":"refined","mime_type":"image/png"}],"usage":{"total_tokens":0}}`)

	out, err := buildImagesAPIResponseFromXAI(payload, "b64_json")
	if err != nil {
		t.Fatalf("buildImagesAPIResponseFromXAI() error = %v", err)
	}

	if got := gjson.GetBytes(out, "created").Int(); got != 123 {
		t.Fatalf("created = %d, want 123", got)
	}
	if got := gjson.GetBytes(out, "data.0.b64_json").String(); got != "AA==" {
		t.Fatalf("data.0.b64_json = %q, want AA==", got)
	}
	if got := gjson.GetBytes(out, "data.0.revised_prompt").String(); got != "refined" {
		t.Fatalf("data.0.revised_prompt = %q, want refined", got)
	}
	if !gjson.GetBytes(out, "usage").Exists() {
		t.Fatalf("usage missing: %s", string(out))
	}
}

func TestImagesGenerationsRejectsUnsupportedModel(t *testing.T) {
	handler := &OpenAIAPIHandler{}
	body := strings.NewReader(`{"model":"gpt-5.4-mini","prompt":"draw a square"}`)

	resp := performImagesEndpointRequest(t, imagesGenerationsPath, "application/json", body, handler.ImagesGenerations)

	assertUnsupportedImagesModelResponse(t, resp, "gpt-5.4-mini")
}

func TestImagesEditsJSONRejectsUnsupportedModel(t *testing.T) {
	handler := &OpenAIAPIHandler{}
	body := strings.NewReader(`{"model":"gpt-5.4-mini","prompt":"edit this","images":[{"image_url":"data:image/png;base64,AA=="}]}`)

	resp := performImagesEndpointRequest(t, imagesEditsPath, "application/json", body, handler.ImagesEdits)

	assertUnsupportedImagesModelResponse(t, resp, "gpt-5.4-mini")
}

func TestImagesEditsMultipartRejectsUnsupportedModel(t *testing.T) {
	handler := &OpenAIAPIHandler{}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("model", "gpt-5.4-mini"); err != nil {
		t.Fatalf("write model field: %v", err)
	}
	if err := writer.WriteField("prompt", "edit this"); err != nil {
		t.Fatalf("write prompt field: %v", err)
	}
	if errClose := writer.Close(); errClose != nil {
		t.Fatalf("close multipart writer: %v", errClose)
	}

	resp := performImagesEndpointRequest(t, imagesEditsPath, writer.FormDataContentType(), &body, handler.ImagesEdits)

	assertUnsupportedImagesModelResponse(t, resp, "gpt-5.4-mini")
}

func TestImagesGenerations_DisableImageGeneration_Returns404(t *testing.T) {
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{DisableImageGeneration: internalconfig.DisableImageGenerationAll}, nil)
	handler := NewOpenAIAPIHandler(base)
	body := strings.NewReader(`{"prompt":"draw a square"}`)

	resp := performImagesEndpointRequest(t, imagesGenerationsPath, "application/json", body, handler.ImagesGenerations)

	if resp.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusNotFound, resp.Body.String())
	}
}

func TestImagesEdits_DisableImageGeneration_Returns404(t *testing.T) {
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{DisableImageGeneration: internalconfig.DisableImageGenerationAll}, nil)
	handler := NewOpenAIAPIHandler(base)
	body := strings.NewReader(`{"prompt":"edit this","images":[{"image_url":"data:image/png;base64,AA=="}]}`)

	resp := performImagesEndpointRequest(t, imagesEditsPath, "application/json", body, handler.ImagesEdits)

	if resp.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusNotFound, resp.Body.String())
	}
}

func TestImagesGenerations_DisableImageGenerationChat_DoesNotReturn404(t *testing.T) {
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{DisableImageGeneration: internalconfig.DisableImageGenerationChat}, nil)
	handler := NewOpenAIAPIHandler(base)
	body := strings.NewReader(`{"model":"gpt-5.4-mini","prompt":"draw a square"}`)

	resp := performImagesEndpointRequest(t, imagesGenerationsPath, "application/json", body, handler.ImagesGenerations)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusBadRequest, resp.Body.String())
	}
}

func TestImagesEdits_DisableImageGenerationChat_DoesNotReturn404(t *testing.T) {
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{DisableImageGeneration: internalconfig.DisableImageGenerationChat}, nil)
	handler := NewOpenAIAPIHandler(base)
	body := strings.NewReader(`{"model":"gpt-5.4-mini","prompt":"edit this","images":[{"image_url":"data:image/png;base64,AA=="}]}`)

	resp := performImagesEndpointRequest(t, imagesEditsPath, "application/json", body, handler.ImagesEdits)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusBadRequest, resp.Body.String())
	}
}
