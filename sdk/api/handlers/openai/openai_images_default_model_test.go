package openai

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// imageModelCaptureExecutor records the model each routed image request resolved to.
type imageModelCaptureExecutor struct {
	compactCaptureExecutor
	models []string
}

func (e *imageModelCaptureExecutor) Execute(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	e.models = append(e.models, req.Model)
	return coreexecutor.Response{Payload: []byte(`{"created":1,"data":[{"b64_json":"AA=="}]}`)}, nil
}

func TestImagesRequestsDefaultToGPTImage25WhenModelIsOmitted(t *testing.T) {
	executor := &imageModelCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: "images-default-model-auth", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "gpt-image-2"}, {ID: "gpt-image-2.5"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})
	handler := NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))

	multipartEdit := func(model string) (string, string) {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		if model != "" {
			if errWrite := writer.WriteField("model", model); errWrite != nil {
				t.Fatalf("write model field: %v", errWrite)
			}
		}
		if errWrite := writer.WriteField("prompt", "edit this"); errWrite != nil {
			t.Fatalf("write prompt field: %v", errWrite)
		}
		part, errCreate := writer.CreateFormFile("image[]", "source.png")
		if errCreate != nil {
			t.Fatalf("create image field: %v", errCreate)
		}
		if _, errWrite := part.Write([]byte("png-data")); errWrite != nil {
			t.Fatalf("write image data: %v", errWrite)
		}
		if errClose := writer.Close(); errClose != nil {
			t.Fatalf("close multipart writer: %v", errClose)
		}
		return writer.FormDataContentType(), body.String()
	}
	defaultType, defaultBody := multipartEdit("")
	explicitType, explicitBody := multipartEdit("gpt-image-2")

	const editJSON = `"prompt":"edit this","images":[{"image_url":"data:image/png;base64,AA=="}]`
	for _, tc := range []struct {
		name        string
		path        string
		contentType string
		body        string
		handler     gin.HandlerFunc
		want        string
	}{
		{"generations without model", imagesGenerationsPath, "application/json", `{"prompt":"draw"}`, handler.ImagesGenerations, "gpt-image-2.5"},
		{"generations with gpt-image-2", imagesGenerationsPath, "application/json", `{"model":"gpt-image-2","prompt":"draw"}`, handler.ImagesGenerations, "gpt-image-2"},
		{"edits json without model", imagesEditsPath, "application/json", `{` + editJSON + `}`, handler.ImagesEdits, "gpt-image-2.5"},
		{"edits json with gpt-image-2", imagesEditsPath, "application/json", `{"model":"gpt-image-2",` + editJSON + `}`, handler.ImagesEdits, "gpt-image-2"},
		{"edits multipart without model", imagesEditsPath, defaultType, defaultBody, handler.ImagesEdits, "gpt-image-2.5"},
		{"edits multipart with gpt-image-2", imagesEditsPath, explicitType, explicitBody, handler.ImagesEdits, "gpt-image-2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(executor.models)
			resp := performImagesEndpointRequest(t, tc.path, tc.contentType, strings.NewReader(tc.body), tc.handler)
			if resp.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusOK, resp.Body.String())
			}
			if len(executor.models) != before+1 {
				t.Fatalf("executor calls = %v, want one new call", executor.models)
			}
			if got := executor.models[len(executor.models)-1]; got != tc.want {
				t.Fatalf("executor model = %q, want %q", got, tc.want)
			}
		})
	}
}
