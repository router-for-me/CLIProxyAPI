package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// websocketCreatedExecutor answers every turn with response.created and response.completed.
type websocketCreatedExecutor struct{}

func (websocketCreatedExecutor) Identifier() string { return "codex" }

func (websocketCreatedExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (websocketCreatedExecutor) ExecuteStream(_ context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	chunks := make(chan coreexecutor.StreamChunk, 2)
	responseID := "resp-" + auth.ID
	chunks <- coreexecutor.StreamChunk{Payload: []byte(fmt.Sprintf(`{"type":"response.created","response":{"id":%q}}`, responseID))}
	chunks <- coreexecutor.StreamChunk{Payload: []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"output":[]}}`, responseID))}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (websocketCreatedExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (websocketCreatedExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (websocketCreatedExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

// A websocket has no response headers per turn, so X-CPA-AUTH-KIND cannot say which kind of credential answered:
// each turn's response.created event carries it instead.
func TestResponsesWebsocketNamesTheAuthKindOnResponseCreated(t *testing.T) {
	gin.SetMode(gin.TestMode)
	model := "auth-kind-websocket-model"
	login := &coreauth.Auth{
		ID:         "auth-kind-websocket-login",
		Provider:   "codex",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{coreauth.AttributeAuthKind: "oauth"},
	}
	key := &coreauth.Auth{
		ID:         "auth-kind-websocket-key",
		Provider:   "codex",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{coreauth.AttributeAPIKey: "secret-key"},
	}
	// The first connection gets the login, the second the API key.
	selector := &orderedWebsocketSelector{order: []string{login.ID, key.ID}}
	manager := coreauth.NewManager(nil, selector, nil)
	manager.RegisterExecutor(websocketCreatedExecutor{})
	for _, auth := range []*coreauth.Auth{login, key} {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register %s: %v", auth.ID, errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
		id := auth.ID
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}

	h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)
	server := httptest.NewServer(router)
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"

	turn := func(conn *websocket.Conn) (created, completed []byte) {
		t.Helper()
		request := fmt.Sprintf(`{"type":"response.create","model":%q,"input":[{"type":"message","id":"msg-1"}]}`, model)
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(request)); errWrite != nil {
			t.Fatalf("write: %v", errWrite)
		}
		for completed == nil {
			_, payload, errRead := conn.ReadMessage()
			if errRead != nil {
				t.Fatalf("read: %v", errRead)
			}
			switch ojson.GetBytesNoCopy(payload, "type").String() {
			case "response.created":
				created = payload
			case "response.completed":
				completed = payload
			}
		}
		return created, completed
	}

	for _, want := range []string{"oauth", "apikey"} {
		conn, _, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
		if errDial != nil {
			t.Fatalf("dial: %v", errDial)
		}
		created, completed := turn(conn)
		if got := ojson.GetBytesNoCopy(created, "cpa_auth_kind").String(); got != want {
			t.Fatalf("response.created cpa_auth_kind = %q, want %q: %s", got, want, created)
		}
		// Only response.created carries it, and nothing that identifies the credential.
		if ojson.GetBytesNoCopy(completed, "cpa_auth_kind").Exists() {
			t.Fatalf("response.completed carries cpa_auth_kind: %s", completed)
		}
		for _, secret := range []string{"secret-key", login.ID, key.ID} {
			if strings.Contains(string(created), secret) && !strings.Contains(ojson.GetBytesNoCopy(created, "response.id").String(), secret) {
				t.Fatalf("response.created exposes %q: %s", secret, created)
			}
		}
		if errClose := conn.Close(); errClose != nil {
			t.Fatalf("close: %v", errClose)
		}
	}
}
