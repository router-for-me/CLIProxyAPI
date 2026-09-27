package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/tidwall/gjson"
)

type xaiImagesQualityCaptureExecutor struct {
	payload []byte
}

func (*xaiImagesQualityCaptureExecutor) Identifier() string { return "xai" }

func (e *xaiImagesQualityCaptureExecutor) Execute(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	e.payload = bytes.Clone(req.Payload)
	return coreexecutor.Response{Payload: []byte(`{"created":1,"data":[{"b64_json":"AA=="}],"usage":{"cost_in_usd_ticks":600000000}}`)}, nil
}

func (*xaiImagesQualityCaptureExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("unexpected streaming executor call")
}

func (*xaiImagesQualityCaptureExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (*xaiImagesQualityCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("unexpected token counting call")
}

func (*xaiImagesQualityCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected HTTP call")
}

func TestXAIImagesEndpointsPreserveQuality(t *testing.T) {
	for _, endpoint := range []string{"generation", "edit-json", "edit-multipart"} {
		for _, stream := range []bool{false, true} {
			for _, tc := range []struct {
				name    string
				quality string
				present bool
				want    string
			}{
				{name: "omitted"},
				{name: "empty", present: true},
				{name: "blank", present: true, quality: " \t "},
				{name: "low", present: true, quality: "low", want: "low"},
				{name: "medium", present: true, quality: "medium", want: "medium"},
				{name: "auto", present: true, quality: "auto", want: "auto"},
				{name: "trimmed", present: true, quality: " medium ", want: "medium"},
			} {
				t.Run(fmt.Sprintf("%s/stream=%t/%s", endpoint, stream, tc.name), func(t *testing.T) {
					executor := &xaiImagesQualityCaptureExecutor{}
					manager := coreauth.NewManager(nil, nil, nil)
					manager.RegisterExecutor(executor)
					auth := &coreauth.Auth{ID: t.Name(), Provider: "xai", Status: coreauth.StatusActive}
					if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
						t.Fatalf("Register auth: %v", errRegister)
					}
					modelRegistry := registry.GetGlobalRegistry()
					modelRegistry.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: xaiImages20Model}})
					t.Cleanup(func() { modelRegistry.UnregisterClient(auth.ID) })
					h := NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))

					path := imagesGenerationsPath
					var handler gin.HandlerFunc = h.ImagesGenerations
					if endpoint != "generation" {
						path = imagesEditsPath
						handler = h.ImagesEdits
					}
					fields := map[string]any{"model": xaiImages20Model, "prompt": "a blue circle", "response_format": "b64_json", "stream": stream}
					if tc.present {
						fields["quality"] = tc.quality
					}
					var body bytes.Buffer
					contentType := "application/json"
					if endpoint == "edit-multipart" {
						writer := multipart.NewWriter(&body)
						for key, value := range fields {
							if errWrite := writer.WriteField(key, fmt.Sprint(value)); errWrite != nil {
								t.Fatalf("WriteField: %v", errWrite)
							}
						}
						part, errCreate := writer.CreateFormFile("image", "input.png")
						if errCreate != nil {
							t.Fatalf("CreateFormFile: %v", errCreate)
						}
						if _, errWrite := io.WriteString(part, "test image"); errWrite != nil {
							t.Fatalf("Write image: %v", errWrite)
						}
						if errClose := writer.Close(); errClose != nil {
							t.Fatalf("Close multipart writer: %v", errClose)
						}
						contentType = writer.FormDataContentType()
					} else {
						if endpoint == "edit-json" {
							fields["image"] = "data:image/png;base64,AA=="
						}
						if errEncode := json.NewEncoder(&body).Encode(fields); errEncode != nil {
							t.Fatalf("Encode JSON: %v", errEncode)
						}
					}

					resp := performImagesEndpointRequest(t, path, contentType, &body, handler)
					if resp.Code != http.StatusOK {
						t.Fatalf("status = %d, want 200: %s", resp.Code, resp.Body.String())
					}
					if len(executor.payload) == 0 {
						t.Fatal("request did not reach xAI executor")
					}
					quality := gjson.GetBytes(executor.payload, "quality")
					if tc.want == "" {
						if quality.Exists() {
							t.Fatalf("quality = %s, want omitted to preserve upstream defaults", quality.Raw)
						}
					} else if quality.Type != gjson.String || quality.String() != tc.want {
						t.Fatalf("quality = %s, want %q", quality.Raw, tc.want)
					}
				})
			}
		}
	}
}
