package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type sequentialRoutingResponse struct {
	Sequential bool `json:"sequential"`
	Current    map[string]struct {
		ID        string `json:"id"`
		AuthIndex string `json:"auth_index"`
		Name      string `json:"name"`
		Provider  string `json:"provider"`
	} `json:"current"`
}

func getSequentialRouting(t *testing.T, manager *coreauth.Manager) sequentialRoutingResponse {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := NewHandler(&config.Config{AuthDir: t.TempDir()}, "", manager)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v8/management/routing/sequential", nil)
	h.GetSequentialRouting(c)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	var body sequentialRoutingResponse
	if errDecode := json.Unmarshal(recorder.Body.Bytes(), &body); errDecode != nil {
		t.Fatalf("decode: %v", errDecode)
	}
	return body
}

func TestGetSequentialRoutingReportsCurrentAccount(t *testing.T) {
	selector := coreauth.NewSequentialSelector(5, "")
	manager := coreauth.NewManager(nil, selector, nil)
	ctx := context.Background()
	first, errFirst := manager.Register(ctx, &coreauth.Auth{ID: "codex-a.json", FileName: "codex-a.json", Provider: "codex", Status: coreauth.StatusActive})
	second, errSecond := manager.Register(ctx, &coreauth.Auth{ID: "codex-b.json", FileName: "codex-b.json", Provider: "codex", Status: coreauth.StatusActive})
	if errFirst != nil || errSecond != nil {
		t.Fatalf("register: %v, %v", errFirst, errSecond)
	}

	if body := getSequentialRouting(t, manager); !body.Sequential || len(body.Current) != 0 {
		t.Fatalf("before any pick = %+v, want sequential with no current account", body)
	}

	picked, errPick := selector.Pick(ctx, "codex", "", cliproxyexecutor.Options{}, []*coreauth.Auth{first, second})
	if errPick != nil {
		t.Fatalf("pick: %v", errPick)
	}
	body := getSequentialRouting(t, manager)
	got := body.Current["codex"]
	if got.ID != picked.ID || got.Name != picked.ID || got.Provider != "codex" || got.AuthIndex == "" {
		t.Fatalf("current codex = %+v, want %s with an auth index", got, picked.ID)
	}
}

func TestGetSequentialRoutingOtherStrategy(t *testing.T) {
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	if body := getSequentialRouting(t, manager); body.Sequential || len(body.Current) != 0 {
		t.Fatalf("round-robin = %+v, want sequential false and no accounts", body)
	}
}
