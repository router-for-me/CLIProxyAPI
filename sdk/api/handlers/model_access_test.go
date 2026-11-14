package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
)

func TestModelAccessRejectsBeforeExecution(t *testing.T) {
	handler := &BaseAPIHandler{}
	ctx := context.WithValue(context.Background(), "gin", ginWithPolicy(sdkaccess.NewModelPolicy([]string{"claude-*"}, nil)))
	for name, execute := range map[string]func() *interfaces.ErrorMessage{
		"execute": func() *interfaces.ErrorMessage {
			_, _, errMsg := handler.ExecuteWithAuthManager(ctx, "openai", "gpt-5", nil, "")
			return errMsg
		},
		"count": func() *interfaces.ErrorMessage {
			_, _, errMsg := handler.ExecuteCountWithAuthManager(ctx, "claude", "gpt-5", nil, "")
			return errMsg
		},
		"stream": func() *interfaces.ErrorMessage {
			_, _, errors := handler.ExecuteStreamWithAuthManager(ctx, "openai", "gpt-5", nil, "")
			errMsg := <-errors
			if _, open := <-errors; open {
				t.Fatal("denied stream error channel must close after one error")
			}
			return errMsg
		},
	} {
		t.Run(name, func(t *testing.T) {
			errMsg := execute()
			if errMsg == nil || errMsg.StatusCode != http.StatusForbidden {
				t.Fatalf("execution error = %v, want 403 before any credential lookup", errMsg)
			}
		})
	}
}

func ginWithPolicy(policy *sdkaccess.ModelPolicy) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	c.Set(sdkaccess.ModelPolicyContextKey, policy)
	return c
}

func TestFilterModelListRecognizesCodexSlug(t *testing.T) {
	models := []map[string]any{{"slug": "gpt-5"}, {"slug": "gpt-4"}}
	policy := sdkaccess.NewModelPolicy([]string{"gpt-5"}, nil)

	got := FilterModelList(ginWithPolicy(policy), models)
	if len(got) != 1 || got[0]["slug"] != "gpt-5" {
		t.Fatalf("slug-filtered models = %v, want only gpt-5", got)
	}
}

func TestFilterModelList(t *testing.T) {
	models := []map[string]any{{"id": "gpt-5"}, {"id": "claude-opus"}, {"name": "models/gemini-3"}}
	policy := sdkaccess.NewModelPolicy([]string{"claude-*", "gemini-*"}, nil)

	got := FilterModelList(ginWithPolicy(policy), models)
	if len(got) != 2 || got[0]["id"] != "claude-opus" || got[1]["name"] != "models/gemini-3" {
		t.Fatalf("unexpected filtered list: %v", got)
	}
	if got := FilterModelList(ginWithPolicy(nil), models); len(got) != len(models) {
		t.Fatalf("nil policy must not filter, got %v", got)
	}
	if got := FilterModelList(nil, models); len(got) != len(models) {
		t.Fatalf("nil context must not filter, got %v", got)
	}
}

func TestModelAccessError(t *testing.T) {
	policy := sdkaccess.NewModelPolicy([]string{"claude-*"}, []string{"claude-3-*"})
	ctx := context.WithValue(context.Background(), "gin", ginWithPolicy(policy))

	for model, wantDenied := range map[string]bool{
		"claude-opus":         false,
		"claude-opus(8192)":   false, // thinking suffix is ignored
		"claude-3-haiku":      true,
		"claude-3-haiku(low)": true,
		"gpt-5":               true,
	} {
		errMsg := modelAccessError(ctx, model)
		if (errMsg != nil) != wantDenied {
			t.Fatalf("modelAccessError(%q) = %v, wantDenied=%v", model, errMsg, wantDenied)
		}
		if errMsg != nil && errMsg.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", errMsg.StatusCode)
		}
	}
	if modelAccessError(context.Background(), "gpt-5") != nil {
		t.Fatal("request without a policy must be unrestricted")
	}
}

func TestModelAccessError_AllowsResolvedAutoModel(t *testing.T) {
	registryRef := registry.GetGlobalRegistry()
	clientID := "model-access-auto-test"
	registryRef.RegisterClient(clientID, "openai", []*registry.ModelInfo{{ID: "gpt-5-auto-test", Created: 1}})
	defer registryRef.UnregisterClient(clientID)

	resolved := util.ResolveAutoModel("auto")
	if resolved == "auto" {
		t.Fatal("test requires the registry to resolve auto to a concrete model")
	}
	policy := sdkaccess.NewModelPolicy([]string{resolved}, nil)
	ctx := context.WithValue(context.Background(), "gin", ginWithPolicy(policy))
	if errMsg := modelAccessError(ctx, "auto"); errMsg != nil {
		t.Fatalf("modelAccessError(auto) = %v, want resolved model %q to be allowed", errMsg, resolved)
	}
}
