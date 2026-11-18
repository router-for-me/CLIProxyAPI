package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/wstunnel"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func newTestTunnelAPIServer(t *testing.T, cfg *config.Config, opts ...ServerOption) *Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	allOpts := append([]ServerOption{WithRequestLoggerFactory(nil)}, opts...)
	return NewServer(cfg, auth.NewManager(nil, nil, nil), sdkaccess.NewManager(), cfgPath, allOpts...)
}

func dialServerTunnel(t *testing.T, baseURL, apiKey string, role wstunnel.Role, uuid, name string) *websocket.Conn {
	t.Helper()
	serverType := ""
	if role == wstunnel.RoleServer {
		serverType = "default-server"
	}
	return dialServerTunnelWithType(t, baseURL, apiKey, role, uuid, name, serverType)
}

func dialServerTunnelWithType(t *testing.T, baseURL, apiKey string, role wstunnel.Role, uuid, name, serverType string) *websocket.Conn {
	t.Helper()
	q := url.Values{
		"role": {string(role)},
		"uuid": {uuid},
		"name": {name},
	}
	if serverType != "" {
		q.Set("type", serverType)
	}
	wsURL := "ws" + strings.TrimPrefix(baseURL, "http") + "/v1/tunnel?" + q.Encode()
	headers := http.Header{}
	if apiKey != "" {
		headers.Set("Authorization", "Bearer "+apiKey)
	}
	conn, resp, errDial := websocket.DefaultDialer.Dial(wsURL, headers)
	if resp != nil && resp.Body != nil {
		if errClose := resp.Body.Close(); errClose != nil {
			t.Logf("response body close error: %v", errClose)
		}
	}
	if errDial != nil {
		t.Fatalf("Dial(%s, %s) error = %v", role, uuid, errDial)
	}
	t.Cleanup(func() {
		_ = conn.Close()
	})
	return conn
}

func readServerTunnelTestMessage(conn *websocket.Conn) (int, []byte, error) {
	type result struct {
		messageType int
		raw         []byte
		err         error
	}
	ch := make(chan result, 1)
	go func() {
		messageType, raw, errRead := conn.ReadMessage()
		ch <- result{messageType, raw, errRead}
	}()
	select {
	case res := <-ch:
		return res.messageType, res.raw, res.err
	case <-time.After(10 * time.Second):
		_ = conn.Close()
		return 0, nil, errors.New("test message read did not complete")
	}
}

func readServerTunnelJSON(t *testing.T, conn *websocket.Conn) ojson.Result {
	t.Helper()
	msgType, raw, errRead := readServerTunnelTestMessage(conn)
	if errRead != nil {
		t.Fatalf("ReadMessage() error = %v", errRead)
	}
	if msgType != websocket.TextMessage {
		t.Fatalf("message type = %d, want TextMessage", msgType)
	}
	return ojson.ParseBytes(raw)
}

func readServerTunnelBinary(t *testing.T, conn *websocket.Conn) wstunnel.DataFrameView {
	t.Helper()
	msgType, raw, errRead := readServerTunnelTestMessage(conn)
	if errRead != nil {
		t.Fatalf("ReadMessage() error = %v", errRead)
	}
	if msgType != websocket.BinaryMessage {
		t.Fatalf("message type = %d (%s), want BinaryMessage", msgType, string(raw))
	}
	view, code, errParse := wstunnel.ParseDataFrameHeader(raw)
	if errParse != nil || code != "" {
		t.Fatalf("ParseDataFrameHeader() = (%+v, %q, %v)", view, code, errParse)
	}
	return view
}

func TestServerWebsocketTunnel_EndToEndAndTenantIsolation(t *testing.T) {
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeys: []string{"tenant-a", "tenant-b"},
		},
	}
	apiSrv := newTestTunnelAPIServer(t, cfg)
	t.Cleanup(func() {
		_ = apiSrv.Stop(context.Background())
	})
	httpSrv := httptest.NewServer(apiSrv.Handler())
	t.Cleanup(httpSrv.Close)

	// Server role without a valid type (>= 1 non-whitespace char) must fail with 400.
	missingTypeURL := "ws" + strings.TrimPrefix(httpSrv.URL, "http") + "/v1/tunnel?role=server&uuid=srv-missing-type&name=ServerMissingType"
	_, badResp, errBadDial := websocket.DefaultDialer.Dial(missingTypeURL, http.Header{"Authorization": {"Bearer tenant-a"}})
	var badBody []byte
	if badResp != nil && badResp.Body != nil {
		badBody, _ = io.ReadAll(badResp.Body)
		if errClose := badResp.Body.Close(); errClose != nil {
			t.Logf("response body close error: %v", errClose)
		}
	}
	if errBadDial == nil || badResp == nil || badResp.StatusCode != http.StatusBadRequest || ojson.GetBytes(badBody, "error").String() != "invalid or missing type" {
		t.Fatalf("Dial server without type = (%v, %v, %s), want 400 invalid or missing type", badResp, errBadDial, string(badBody))
	}

	srvA1 := dialServerTunnelWithType(t, httpSrv.URL, "tenant-a", wstunnel.RoleServer, "srv-a1", "Server A1", "cpa-desktop")
	_ = dialServerTunnelWithType(t, httpSrv.URL, "tenant-b", wstunnel.RoleServer, "srv-b1", "Server B1", "cpa-cloud")

	cliA := dialServerTunnel(t, httpSrv.URL, "tenant-a", wstunnel.RoleClient, "cli-a1", "Client A1")
	initList := readServerTunnelJSON(t, cliA)
	if initList.Get("type").String() != wstunnel.ControlTypeServerList ||
		initList.Get("servers.#").Int() != 1 ||
		initList.Get("servers.0.uuid").String() != "srv-a1" ||
		initList.Get("servers.0.type").String() != "cpa-desktop" {
		t.Fatalf("unexpected initial server_list: %s", initList.Raw)
	}

	srvA2 := dialServerTunnelWithType(t, httpSrv.URL, "tenant-a", wstunnel.RoleServer, "srv-a2", "Server A2", "build-runner")
	online := readServerTunnelJSON(t, cliA)
	if online.Get("type").String() != wstunnel.ControlTypeServerOnline ||
		online.Get("server.uuid").String() != "srv-a2" ||
		online.Get("server.type").String() != "build-runner" {
		t.Fatalf("unexpected server_online event: %s", online.Raw)
	}

	rawPayload := []byte{0x00, 0x10, 0x7f, 0x80, 0xff}
	reqFrame, errEnc := wstunnel.EncodeDataFrame(wstunnel.PayloadTypeBinary, "cli-a1", "srv-a2", rawPayload)
	if errEnc != nil {
		t.Fatalf("EncodeDataFrame error = %v", errEnc)
	}
	if errWrite := cliA.WriteMessage(websocket.BinaryMessage, reqFrame); errWrite != nil {
		t.Fatalf("cliA WriteMessage error = %v", errWrite)
	}

	gotReq := readServerTunnelBinary(t, srvA2)
	if gotReq.SrcUUID != "cli-a1" || gotReq.DstUUID != "srv-a2" || !bytes.Equal(gotReq.Payload, rawPayload) {
		t.Fatalf("srvA2 received unexpected frame: %+v", gotReq)
	}

	respFrame, errEncResp := wstunnel.EncodeDataFrame(wstunnel.PayloadTypeText, "srv-a2", "cli-a1", []byte("pong-from-a2"))
	if errEncResp != nil {
		t.Fatalf("EncodeDataFrame error = %v", errEncResp)
	}
	if errWrite := srvA2.WriteMessage(websocket.BinaryMessage, respFrame); errWrite != nil {
		t.Fatalf("srvA2 WriteMessage error = %v", errWrite)
	}

	gotResp := readServerTunnelBinary(t, cliA)
	if gotResp.SrcUUID != "srv-a2" || gotResp.DstUUID != "cli-a1" || string(gotResp.Payload) != "pong-from-a2" {
		t.Fatalf("cliA received unexpected frame: %+v", gotResp)
	}

	_ = srvA2.Close()
	offline := readServerTunnelJSON(t, cliA)
	if offline.Get("type").String() != wstunnel.ControlTypeServerOffline ||
		offline.Get("server.uuid").String() != "srv-a2" ||
		offline.Get("server.type").String() != "build-runner" {
		t.Fatalf("unexpected server_offline event: %s", offline.Raw)
	}
	_ = srvA1
}

func TestServerWebsocketTunnel_NonGetMethodsReturn405AndPreserveCORSOptions(t *testing.T) {
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeys: []string{"method-key"},
		},
	}
	apiSrv := newTestTunnelAPIServer(t, cfg)
	t.Cleanup(func() {
		_ = apiSrv.Stop(context.Background())
	})
	httpSrv := httptest.NewServer(apiSrv.Handler())
	t.Cleanup(httpSrv.Close)

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodTrace, http.MethodConnect} {
		req, errReq := http.NewRequest(method, httpSrv.URL+"/v1/tunnel", nil)
		if errReq != nil {
			t.Fatalf("NewRequest(%s) error = %v", method, errReq)
		}
		resp, errDo := http.DefaultClient.Do(req)
		if errDo != nil {
			t.Fatalf("Do(%s) error = %v", method, errDo)
		}
		body, _ := io.ReadAll(resp.Body)
		if errClose := resp.Body.Close(); errClose != nil {
			t.Logf("response body close error: %v", errClose)
		}
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("%s /v1/tunnel status = %d, want 405 (body=%s)", method, resp.StatusCode, string(body))
		}
		if gotAllow := resp.Header.Get("Allow"); gotAllow != http.MethodGet {
			t.Fatalf("%s /v1/tunnel Allow = %q, want GET", method, gotAllow)
		}
		if method != http.MethodHead && ojson.GetBytes(body, "error").String() != "method not allowed" {
			t.Fatalf("%s /v1/tunnel body = %s, want method not allowed", method, string(body))
		}
	}

	optReq, errOpt := http.NewRequest(http.MethodOptions, httpSrv.URL+"/v1/tunnel", nil)
	if errOpt != nil {
		t.Fatalf("NewRequest(OPTIONS) error = %v", errOpt)
	}
	optReq.Header.Set("Origin", "https://app.example.com")
	optReq.Header.Set("Access-Control-Request-Method", "GET")
	optResp, errOptDo := http.DefaultClient.Do(optReq)
	if errOptDo != nil {
		t.Fatalf("Do(OPTIONS) error = %v", errOptDo)
	}
	if errClose := optResp.Body.Close(); errClose != nil {
		t.Logf("response body close error: %v", errClose)
	}
	if optResp.StatusCode != http.StatusNoContent {
		t.Fatalf("OPTIONS /v1/tunnel status = %d, want 204", optResp.StatusCode)
	}
}

func TestServerWebsocketTunnel_RejectsWhenAPIKeysEmptyOrSafeMode(t *testing.T) {
	t.Run("empty access.api-keys rejects tunnel upgrade with 401", func(t *testing.T) {
		apiSrv := newTestTunnelAPIServer(t, &config.Config{})
		t.Cleanup(func() {
			_ = apiSrv.Stop(context.Background())
		})
		httpSrv := httptest.NewServer(apiSrv.Handler())
		t.Cleanup(httpSrv.Close)

		wsURL := "ws" + strings.TrimPrefix(httpSrv.URL, "http") + "/v1/tunnel?role=client&uuid=c1&name=Client1"
		_, resp, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
		if resp != nil && resp.Body != nil {
			defer func() {
				if errClose := resp.Body.Close(); errClose != nil {
					t.Logf("response body close error: %v", errClose)
				}
			}()
		}
		if errDial == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("Dial with empty APIKeys = (%v, %v), want 401", resp, errDial)
		}
	})

	t.Run("example api-key safe mode blocks /v1/tunnel with 403", func(t *testing.T) {
		cfg := &config.Config{
			SDKConfig: config.SDKConfig{
				APIKeys: []string{"your-api-key-1"},
			},
		}
		apiSrv := newTestTunnelAPIServer(t, cfg, WithExampleAPIKeySafeMode())
		t.Cleanup(func() {
			_ = apiSrv.Stop(context.Background())
		})
		httpSrv := httptest.NewServer(apiSrv.Handler())
		t.Cleanup(httpSrv.Close)

		wsURL := "ws" + strings.TrimPrefix(httpSrv.URL, "http") + "/v1/tunnel?role=client&uuid=c1&name=Client1"
		_, resp, errDial := websocket.DefaultDialer.Dial(wsURL, http.Header{"Authorization": {"Bearer your-api-key-1"}})
		if resp != nil && resp.Body != nil {
			defer func() {
				if errClose := resp.Body.Close(); errClose != nil {
					t.Logf("response body close error: %v", errClose)
				}
			}()
		}
		if errDial == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
			t.Fatalf("Dial in safe mode = (%v, %v), want 403", resp, errDial)
		}
		body, _ := io.ReadAll(resp.Body)
		if ojson.GetBytes(body, "error").String() != "unsafe_example_api_key" {
			t.Fatalf("safe mode body = %s, want unsafe_example_api_key", string(body))
		}
	})
}

func TestServerWebsocketTunnel_ServiceUnavailableResponseBodies(t *testing.T) {
	previousHome := home.Current()
	home.SetCurrent(nil)
	t.Cleanup(func() { home.SetCurrent(previousHome) })
	for _, tc := range []struct {
		name        string
		homeEnabled bool
		wantBody    string
	}{
		{"closed hub", false, `{"error":"tunnel hub is closed"}`},
		{"unhealthy Home heartbeat gate", true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{
				SDKConfig: config.SDKConfig{APIKeys: []string{"key"}},
				Home:      config.HomeConfig{Enabled: tc.homeEnabled},
			}
			apiSrv := newTestTunnelAPIServer(t, cfg)
			t.Cleanup(func() {
				if errStop := apiSrv.Stop(context.Background()); errStop != nil {
					t.Errorf("stop test server: %v", errStop)
				}
			})
			if !tc.homeEnabled {
				apiSrv.tunnelHub.Close()
			}
			req := httptest.NewRequest(http.MethodGet, "/v1/tunnel?key=key&role=client&uuid=device&name=Device", nil)
			req.Header.Set("Connection", "Upgrade")
			req.Header.Set("Upgrade", "websocket")
			req.Header.Set("Sec-WebSocket-Version", "13")
			req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
			resp := httptest.NewRecorder()
			apiSrv.Handler().ServeHTTP(resp, req)
			if resp.Code != http.StatusServiceUnavailable || resp.Body.String() != tc.wantBody {
				t.Fatalf("handshake = (%d, %q), want (503, %q)", resp.Code, resp.Body.String(), tc.wantBody)
			}
		})
	}
}

func TestServerWebsocketTunnel_HotReloadRevokesRemovedKeyOnly(t *testing.T) {
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeys: []string{"key-keep", "key-revoke"},
		},
	}
	apiSrv := newTestTunnelAPIServer(t, cfg)
	t.Cleanup(func() {
		_ = apiSrv.Stop(context.Background())
	})
	httpSrv := httptest.NewServer(apiSrv.Handler())
	t.Cleanup(httpSrv.Close)

	srvKeep := dialServerTunnel(t, httpSrv.URL, "key-keep", wstunnel.RoleServer, "s-keep", "Keep Server")
	cliKeep := dialServerTunnel(t, httpSrv.URL, "key-keep", wstunnel.RoleClient, "c-keep", "Keep Client")
	_ = readServerTunnelJSON(t, cliKeep)

	cliRevoke := dialServerTunnel(t, httpSrv.URL, "key-revoke", wstunnel.RoleClient, "c-revoke", "Revoke Client")
	_ = readServerTunnelJSON(t, cliRevoke)

	updatedCfg := &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeys: []string{"key-keep"},
		},
	}
	apiSrv.UpdateClients(updatedCfg)

	_, _, errRead := readServerTunnelTestMessage(cliRevoke)
	var closeErr *websocket.CloseError
	if !errors.As(errRead, &closeErr) || closeErr.Code != websocket.ClosePolicyViolation {
		t.Fatalf("expected revoked key tunnel to close with 1008, got %v", errRead)
	}

	pkt, _ := wstunnel.EncodeDataFrame(wstunnel.PayloadTypeText, "c-keep", "s-keep", []byte("survives-reload"))
	if errWrite := cliKeep.WriteMessage(websocket.BinaryMessage, pkt); errWrite != nil {
		t.Fatalf("cliKeep WriteMessage error = %v", errWrite)
	}
	got := readServerTunnelBinary(t, srvKeep)
	if string(got.Payload) != "survives-reload" {
		t.Fatalf("srvKeep payload = %q, want survives-reload", string(got.Payload))
	}
}

func TestServerWebsocketTunnel_ServerStopClosesConnections(t *testing.T) {
	ln, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("net.Listen error = %v", errListen)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if errCloseLn := ln.Close(); errCloseLn != nil {
		t.Fatalf("ln.Close error = %v", errCloseLn)
	}

	cfg := &config.Config{
		Host: "127.0.0.1",
		Port: port,
		SDKConfig: config.SDKConfig{
			APIKeys: []string{"stop-key"},
		},
	}
	apiSrv := newTestTunnelAPIServer(t, cfg)
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- apiSrv.Start()
	}()

	// Wait deterministically for listener readiness by retrying /healthz with short backoffless dial attempts.
	baseURL := "http://" + net.JoinHostPort("127.0.0.1", strings.TrimPrefix(strings.Split(ln.Addr().String(), ":")[1], ""))
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, errHealth := http.Get(baseURL + "/healthz")
		if errHealth == nil {
			if resp.Body != nil {
				_ = resp.Body.Close()
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not start listening on %s: %v", baseURL, errHealth)
		}
	}

	cli := dialServerTunnel(t, baseURL, "stop-key", wstunnel.RoleClient, "c-stop", "Stop Client")
	_ = readServerTunnelJSON(t, cli)

	if errStop := apiSrv.Stop(context.Background()); errStop != nil {
		t.Fatalf("apiSrv.Stop error = %v", errStop)
	}

	if _, _, errRead := readServerTunnelTestMessage(cli); errRead == nil {
		t.Fatal("expected tunnel websocket to close on Server.Stop()")
	}
	<-serveDone
}
