package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

type modelProbeExecutor struct {
	coreauth.ProviderExecutor
	execute func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error)
}

func (e *modelProbeExecutor) Identifier() string { return "codex" }
func (e *modelProbeExecutor) Execute(ctx context.Context, a *coreauth.Auth, r coreexecutor.Request, o coreexecutor.Options) (coreexecutor.Response, error) {
	return e.execute(ctx, a, r, o)
}

func TestAuthFileModelProbe(t *testing.T) {
	for _, tc := range []struct {
		name, model, payload, format, path string
		success                            bool
	}{
		{"text", "gpt-5.5", `{"choices":[{"message":{"content":"OK"}}]}`, "openai", "/v1/chat/completions", true},
		{"image15", "gpt-image-1.5", `{"data":[{"b64_json":"aW1hZ2U="}]}`, "openai-image", "/v1/images/generations", true},
		{"image2", "gpt-image-2", `{"data":[{"url":"https://example.invalid/test.png"}]}`, "openai-image", "/v1/images/generations", true},
		{"empty image", "gpt-image-2", `{"data":[]}`, "openai-image", "/v1/images/generations", false},
		{"empty text", "gpt-5.5", `{"choices":[]}`, "openai", "/v1/chat/completions", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := coreauth.NewManager(nil, nil, nil)
			reg := registry.GetGlobalRegistry()
			for _, id := range []string{"probe-other", "probe-selected"} {
				a := &coreauth.Auth{ID: id, FileName: id + ".json", Provider: "codex", Status: coreauth.StatusActive}
				if _, err := manager.Register(context.Background(), a); err != nil {
					t.Fatal(err)
				}
				reg.RegisterClient(id, "codex", []*registry.ModelInfo{{ID: tc.model, Type: "openai"}})
				t.Cleanup(func() { reg.UnregisterClient(id) })
			}
			calls := 0
			manager.RegisterExecutor(&modelProbeExecutor{execute: func(ctx context.Context, a *coreauth.Auth, r coreexecutor.Request, o coreexecutor.Options) (coreexecutor.Response, error) {
				calls++
				if a.ID != "probe-selected" {
					t.Fatalf("wrong credential: %s", a.ID)
				}
				if o.SourceFormat.String() != tc.format || o.Metadata[coreexecutor.RequestPathMetadataKey] != tc.path {
					t.Fatalf("wrong protocol: %+v", o)
				}
				if r.Model != tc.model || gjson.GetBytes(r.Payload, "model").String() != tc.model {
					t.Fatalf("wrong model: %s", r.Payload)
				}
				if tc.format == "openai-image" && gjson.GetBytes(r.Payload, "n").Int() != 1 {
					t.Fatal("must generate one image")
				}
				if ctx.Err() != nil {
					t.Fatal(ctx.Err())
				}
				return coreexecutor.Response{Payload: []byte(tc.payload)}, nil
			}})
			a, _ := manager.GetByID("probe-selected")
			h := &Handler{authManager: manager}
			router := gin.New()
			router.POST("/test", h.TestAuthFileModel)
			body, _ := json.Marshal(map[string]string{"auth_index": a.EnsureIndex(), "model": tc.model})
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest("POST", "/test", strings.NewReader(string(body))))
			if rec.Code != 200 || gjson.GetBytes(rec.Body.Bytes(), "success").Bool() != tc.success || calls != 1 {
				t.Fatalf("status=%d calls=%d body=%s", rec.Code, calls, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "aW1hZ2U=") {
				t.Fatal("image data should not be returned by a probe")
			}
		})
	}
}

func TestAuthFileModelProbeValidation(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	a := &coreauth.Auth{ID: "probe-validation", Provider: "codex", Status: coreauth.StatusActive}
	_, _ = manager.Register(context.Background(), a)
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(a.ID, "codex", []*registry.ModelInfo{{ID: "allowed"}})
	defer reg.UnregisterClient(a.ID)
	h := &Handler{authManager: manager}
	router := gin.New()
	router.POST("/test", h.TestAuthFileModel)
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{}`, 400}, {`{"auth_index":"unknown","model":"allowed"}`, 404},
		{`{"auth_index":"` + a.EnsureIndex() + `","model":"unregistered"}`, 400},
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(tc.body)))
		if rec.Code != tc.status {
			t.Fatalf("got %d want %d: %s", rec.Code, tc.status, rec.Body.String())
		}
	}
}

func TestAuthFileModelProbeUpstreamFailureAndCancellation(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "upstream 401", true: "cancellation"}[cancelled], func(t *testing.T) {
			manager := coreauth.NewManager(nil, nil, nil)
			a := &coreauth.Auth{ID: "probe-failure", Provider: "codex", Status: coreauth.StatusActive}
			_, _ = manager.Register(context.Background(), a)
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(a.ID, "codex", []*registry.ModelInfo{{ID: "probe-text"}})
			defer reg.UnregisterClient(a.ID)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			manager.RegisterExecutor(&modelProbeExecutor{execute: func(callCtx context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
				calls++
				if cancelled {
					cancel()
					<-callCtx.Done()
					return coreexecutor.Response{}, callCtx.Err()
				}
				return coreexecutor.Response{}, &coreauth.Error{HTTPStatus: 401, Message: "secret-upstream-token"}
			}})
			h := &Handler{authManager: manager}
			router := gin.New()
			router.POST("/test", h.TestAuthFileModel)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/test", strings.NewReader(`{"auth_index":"`+a.EnsureIndex()+`","model":"probe-text"}`)).WithContext(ctx)
			router.ServeHTTP(rec, req)
			status := int(gjson.GetBytes(rec.Body.Bytes(), "error.status_code").Int())
			want := 401
			if cancelled {
				want = 499
			}
			if rec.Code != 200 || status != want || calls != 1 || strings.Contains(rec.Body.String(), "secret-upstream-token") {
				t.Fatalf("%d %s calls=%d", rec.Code, rec.Body.String(), calls)
			}
		})
	}
}

func TestAuthFileModelTestKind(t *testing.T) {
	for _, tc := range []struct {
		info registry.ModelInfo
		want string
	}{
		{registry.ModelInfo{ID: "team/image-alias", Version: "gpt-image-2"}, "image"},
		{registry.ModelInfo{ID: "configured-image", Type: registry.OpenAIImageModelType}, "image"},
		{registry.ModelInfo{ID: "gemini-image", Type: "gemini", SupportedOutputModalities: []string{"TEXT", "IMAGE"}}, "image"},
		{registry.ModelInfo{ID: "grok-imagine-video"}, "unsupported"},
	} {
		if got := authFileModelTestKind(&tc.info); got != tc.want {
			t.Errorf("%s = %s want %s", tc.info.ID, got, tc.want)
		}
	}
	text, count := authFileModelTestOutput([]byte(`{"choices":[{"message":{"content":"image description","images":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}}]}`), "image")
	if text != "" || count != 1 {
		t.Fatalf("image output: %q %d", text, count)
	}
}
