package live

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
)

func TestHandleDirectWebsocketRejectsClientSecretModelMismatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewHandler(auth.NewManager(nil, nil, nil), nil)
	router := gin.New()
	router.GET("/v1/realtime", func(c *gin.Context) {
		c.Set(ClientSecretSessionContextKey, json.RawMessage(`{"type":"realtime","model":"gpt-live-1-codex"}`))
		c.Set(ClientSecretPrincipalContextKey, "sess_123")
		c.Next()
	}, handler.HandleRealtimeWebsocket)
	request := httptest.NewRequest(http.MethodGet, "/v1/realtime?model=another-live-model", nil)
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusForbidden, recorder.Body.String())
	}
}

func TestHandleDirectWebsocketForwardsUnauthorizedHomeHandshakeWithoutRefresh(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name              string
		upstreamBody      string
		truncatedResponse bool
	}{
		{name: "response body", upstreamBody: `{"error":{"message":"access token expired"}}`},
		{name: "response body with read error", upstreamBody: `{"error":{"message":"access token expired"}}`, truncatedResponse: true},
		{name: "empty response body"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstreamAuthorization := make(chan string, 1)
			upstreamServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				upstreamAuthorization <- request.Header.Get("Authorization")
				writer.Header().Set("Content-Type", "application/json")
				if tc.truncatedResponse {
					writer.Header().Set("Content-Length", strconv.Itoa(len(tc.upstreamBody)+1))
				}
				writer.WriteHeader(http.StatusUnauthorized)
				_, _ = writer.Write([]byte(tc.upstreamBody))
			}))
			defer upstreamServer.Close()

			runtimeConfig := &config.Config{
				Home:      config.HomeConfig{Enabled: true},
				SDKConfig: config.SDKConfig{RequestLog: true},
			}
			manager := auth.NewManager(nil, nil, nil)
			manager.SetConfig(runtimeConfig)
			manager.PublishHomeDispatch(&homeDispatcher{}, executionregistry.New(), 1)
			executor := &captureExecutor{}
			manager.RegisterExecutor(executor)
			handler := NewHandler(manager, runtimeConfig)
			handler.sidebandAPIBaseURL = "ws" + strings.TrimPrefix(upstreamServer.URL, "http") + "/v1"
			router := gin.New()
			timelineCapture := make(chan []byte, 1)
			router.Use(func(c *gin.Context) {
				c.Next()
				if raw, exists := c.Get("API_WEBSOCKET_TIMELINE"); exists {
					timeline, _ := raw.([]byte)
					timelineCapture <- append([]byte(nil), timeline...)
				}
			})
			router.GET("/v1/realtime", handler.HandleRealtimeWebsocket)
			downstreamServer := httptest.NewServer(router)
			defer downstreamServer.Close()

			wsURL := "ws" + strings.TrimPrefix(downstreamServer.URL, "http") + "/v1/realtime?model=gpt-realtime"
			connection, response, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
			if connection != nil {
				_ = connection.Close()
			}
			if errDial == nil || response == nil {
				t.Fatalf("dial downstream websocket = response %#v err %v, want rejected handshake", response, errDial)
			}
			defer func() { _ = response.Body.Close() }()
			responseBody, errRead := io.ReadAll(response.Body)
			if errRead != nil {
				t.Fatalf("read downstream rejection: %v", errRead)
			}
			if response.StatusCode != http.StatusUnauthorized || string(responseBody) != tc.upstreamBody {
				t.Fatalf("downstream rejection = status %d body %q, want original upstream 401 body %q", response.StatusCode, responseBody, tc.upstreamBody)
			}
			if executor.refreshCalls.Load() != 0 {
				t.Fatalf("refresh calls = %d, want 0", executor.refreshCalls.Load())
			}
			if got := <-upstreamAuthorization; got != "Bearer home-live-token" {
				t.Fatalf("upstream Authorization = %q, want original Home token", got)
			}
			if tc.truncatedResponse {
				select {
				case timeline := <-timelineCapture:
					if !strings.Contains(string(timeline), tc.upstreamBody) {
						t.Fatalf("API_WEBSOCKET_TIMELINE = %q, want original upstream error", timeline)
					}
				case <-time.After(time.Second):
					t.Fatal("timed out waiting for websocket request-log timeline")
				}
			}
		})
	}
}

func TestHandleDirectWebsocketAppliesClientSecretSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstreamUpdate := make(chan []byte, 1)
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		connection, errUpgrade := upgrader.Upgrade(writer, request, nil)
		if errUpgrade != nil {
			return
		}
		defer func() { _ = connection.Close() }()
		_, payload, errRead := connection.ReadMessage()
		if errRead != nil {
			return
		}
		upstreamUpdate <- append([]byte(nil), payload...)
		_ = connection.WriteMessage(websocket.TextMessage, []byte(`{"type":"session.created"}`))
	}))
	defer upstreamServer.Close()

	manager := auth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(&captureExecutor{})
	registerCredential(t, manager, &auth.Auth{
		ID:       "codex-oauth",
		Provider: "codex",
		Status:   auth.StatusActive,
		Metadata: map[string]any{"access_token": "oauth-token"},
	})
	handler := NewHandler(manager, nil)
	handler.sidebandAPIBaseURL = "ws" + strings.TrimPrefix(upstreamServer.URL, "http") + "/v1"
	router := gin.New()
	router.GET("/v1/realtime", func(c *gin.Context) {
		c.Set(ClientSecretSessionContextKey, json.RawMessage(`{"type":"realtime","model":"gpt-live-1-codex","instructions":"help"}`))
		c.Set(ClientSecretPrincipalContextKey, "sess_123")
		c.Next()
	}, handler.HandleRealtimeWebsocket)
	downstreamServer := httptest.NewServer(router)
	defer downstreamServer.Close()

	wsURL := "ws" + strings.TrimPrefix(downstreamServer.URL, "http") + "/v1/realtime?model=gpt-realtime"
	connection, _, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
	if errDial != nil {
		t.Fatalf("dial downstream websocket: %v", errDial)
	}
	defer func() { _ = connection.Close() }()
	_, _, _ = connection.ReadMessage()

	select {
	case update := <-upstreamUpdate:
		var event struct {
			Type    string `json:"type"`
			Session struct {
				Model        string `json:"model"`
				Instructions string `json:"instructions"`
			} `json:"session"`
		}
		if errUnmarshal := json.Unmarshal(update, &event); errUnmarshal != nil {
			t.Fatalf("unmarshal session update: %v", errUnmarshal)
		}
		if event.Type != "session.update" || event.Session.Model != "" || event.Session.Instructions != "help" {
			t.Fatalf("session update = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("session update not captured")
	}
}

func TestHandleDirectWebsocketRelaysStandardRealtimeFrames(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstreamRequest := make(chan *http.Request, 1)
	upstreamMessage := make(chan string, 1)
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		connection, errUpgrade := upgrader.Upgrade(writer, request, nil)
		if errUpgrade != nil {
			return
		}
		defer func() { _ = connection.Close() }()
		upstreamRequest <- request.Clone(request.Context())
		if errWrite := connection.WriteMessage(websocket.TextMessage, []byte(`{"type":"session.created"}`)); errWrite != nil {
			return
		}
		messageType, payload, errRead := connection.ReadMessage()
		if errRead != nil {
			return
		}
		upstreamMessage <- string(payload)
		_ = connection.WriteMessage(messageType, append([]byte("echo:"), payload...))
	}))
	defer upstreamServer.Close()

	manager := auth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(&captureExecutor{})
	registerCredential(t, manager, &auth.Auth{
		ID:       "codex-oauth",
		Provider: "codex",
		Status:   auth.StatusActive,
		Metadata: map[string]any{
			"access_token": "oauth-token",
			"account_id":   "account-123",
		},
	})
	handler := NewHandler(manager, nil)
	handler.sidebandAPIBaseURL = "ws" + strings.TrimPrefix(upstreamServer.URL, "http") + "/v1"

	router := gin.New()
	router.GET("/v1/realtime", handler.HandleRealtimeWebsocket)
	downstreamServer := httptest.NewServer(router)
	defer downstreamServer.Close()

	wsURL := "ws" + strings.TrimPrefix(downstreamServer.URL, "http") + "/v1/realtime?model=gpt-realtime"
	downstreamHeaders := make(http.Header)
	downstreamHeaders.Set("OpenAI-Alpha", "quicksilver=v2")
	connection, _, errDial := websocket.DefaultDialer.Dial(wsURL, downstreamHeaders)
	if errDial != nil {
		t.Fatalf("dial downstream websocket: %v", errDial)
	}
	defer func() { _ = connection.Close() }()

	_, created, errRead := connection.ReadMessage()
	if errRead != nil {
		t.Fatalf("read session.created: %v", errRead)
	}
	if string(created) != `{"type":"session.created"}` {
		t.Fatalf("created event = %s", created)
	}
	const event = `{"type":"response.create"}`
	if errWrite := connection.WriteMessage(websocket.TextMessage, []byte(event)); errWrite != nil {
		t.Fatalf("write downstream event: %v", errWrite)
	}
	_, echoed, errRead := connection.ReadMessage()
	if errRead != nil {
		t.Fatalf("read echoed event: %v", errRead)
	}
	if string(echoed) != "echo:"+event {
		t.Fatalf("echoed event = %s", echoed)
	}

	select {
	case request := <-upstreamRequest:
		if request.Header.Get("Authorization") != "Bearer oauth-token" {
			t.Fatalf("Authorization = %q", request.Header.Get("Authorization"))
		}
		if request.Header.Get("Chatgpt-Account-Id") != "account-123" {
			t.Fatalf("Chatgpt-Account-Id = %q", request.Header.Get("Chatgpt-Account-Id"))
		}
		if request.Header.Get("OpenAI-Alpha") != "" {
			t.Fatalf("OpenAI-Alpha must not be forwarded, got %q", request.Header.Get("OpenAI-Alpha"))
		}
		query, errParse := url.ParseQuery(request.URL.RawQuery)
		if errParse != nil {
			t.Fatalf("parse upstream query: %v", errParse)
		}
		if query.Get("model") != "gpt-realtime" || query.Has("intent") {
			t.Fatalf("upstream query = %v", query)
		}
	case <-time.After(time.Second):
		t.Fatal("upstream request not captured")
	}
	select {
	case payload := <-upstreamMessage:
		if payload != event {
			t.Fatalf("upstream event = %s", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("upstream event not captured")
	}
}
