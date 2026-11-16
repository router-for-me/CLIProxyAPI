package api

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

type transientEMFILEListener struct {
	net.Listener
	remainingEMFILE atomic.Int32
}

func (l *transientEMFILEListener) Accept() (net.Conn, error) {
	for {
		remaining := l.remainingEMFILE.Load()
		if remaining <= 0 {
			return l.Listener.Accept()
		}
		if l.remainingEMFILE.CompareAndSwap(remaining, remaining-1) {
			return nil, &net.OpError{
				Op:   "accept",
				Net:  "tcp",
				Addr: l.Listener.Addr(),
				Err:  os.NewSyscallError("accept4", syscall.EMFILE),
			}
		}
	}
}

func TestAcceptMuxRecoversFromEMFILE(t *testing.T) {
	baseListener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("failed to listen: %v", errListen)
	}
	defer func() {
		_ = baseListener.Close()
	}()

	listener := &transientEMFILEListener{Listener: baseListener}
	listener.remainingEMFILE.Store(3)

	var routed atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		routed.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewUnstartedServer(handler)
	defer srv.Close()

	muxLn := newMuxListener(listener.Addr(), 1024)
	defer func() {
		_ = muxLn.Close()
	}()
	server := &Server{managementRoutesEnabled: atomic.Bool{}}
	server.managementRoutesEnabled.Store(false)

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.acceptMuxConnections(listener, muxLn)
	}()

	srv.Listener = muxLn
	srv.Start()

	client := &http.Client{Timeout: 2 * time.Second}
	resp, errGet := client.Get("http://" + listener.Addr().String() + "/")
	if errGet != nil {
		_ = baseListener.Close()
		t.Fatalf("HTTP request failed after transient EMFILE: %v", errGet)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			t.Errorf("close response body: %v", errClose)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if routed.Load() != 1 {
		t.Fatalf("routed = %d, want 1", routed.Load())
	}
}

func TestAcceptMuxUnauthenticatedRESPDoesNotStarveHTTP(t *testing.T) {
	rawCfg := "config-version: 8\naccess:\n  api-keys:\n    - test-key\nmanagement:\n  secret-key: mgmt-secret\n"
	cfg := parseTestServerConfigYAML(t, rawCfg)
	srv := newTestServerWithConfig(t, cfg)

	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen: %v", errListen)
	}
	defer func() {
		_ = listener.Close()
	}()

	muxLn := newMuxListener(listener.Addr(), 1024)
	defer func() {
		_ = muxLn.Close()
	}()

	go func() {
		_ = srv.acceptMuxConnections(listener, muxLn)
	}()
	go func() {
		_ = srv.server.Serve(muxLn)
	}()
	defer func() {
		_ = srv.server.Close()
	}()

	addr := listener.Addr().String()
	const stalledRESPCount = 128
	stalledRESPConns := make([]net.Conn, 0, stalledRESPCount)
	defer func() {
		for _, c := range stalledRESPConns {
			_ = c.Close()
		}
	}()

	for i := 0; i < stalledRESPCount; i++ {
		conn, errDial := net.DialTimeout("tcp", addr, time.Second)
		if errDial != nil {
			t.Fatalf("dial stalled RESP conn %d: %v", i, errDial)
		}
		if _, errWrite := conn.Write([]byte("*")); errWrite != nil {
			_ = conn.Close()
			t.Fatalf("write RESP prefix on conn %d: %v", i, errWrite)
		}
		stalledRESPConns = append(stalledRESPConns, conn)
	}

	httpClient := &http.Client{Timeout: 2 * time.Second}
	resp, errGet := httpClient.Get("http://" + addr + "/healthz")
	if errGet != nil {
		t.Fatalf("HTTP request failed while %d unauthenticated RESP connections were stalled: %v", stalledRESPCount, errGet)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200", resp.StatusCode)
	}
}

func TestServerRejectsIncompleteUnauthenticatedRequestBodiesPromptly(t *testing.T) {
	modes := []struct {
		name           string
		requestLog     bool
		commercialMode bool
	}{
		{name: "request_log_false", requestLog: false, commercialMode: false},
		{name: "request_log_true", requestLog: true, commercialMode: false},
		{name: "commercial_mode_true", requestLog: false, commercialMode: true},
	}
	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			logsDir := t.TempDir()
			rawCfg := fmt.Sprintf(
				"config-version: 8\nserver:\n  commercial-mode: %t\n  max-request-body-bytes: 0\n  response-compression:\n    enable: true\n    min-bytes: 16\naccess:\n  api-keys:\n    - test-key\nobservability:\n  logs:\n    request-log: %t\n",
				mode.commercialMode,
				mode.requestLog,
			)
			cfg := parseTestServerConfigYAML(t, rawCfg)
			srv := newTestServerWithConfig(t, cfg, WithRequestLoggerFactory(func(c *config.Config, configPath string) logging.RequestLogger {
				return logging.NewFileRequestLogger(c.RequestLog, logsDir, filepath.Dir(configPath), c.ErrorLogsMaxFiles)
			}))

			listener, errListen := net.Listen("tcp", "127.0.0.1:0")
			if errListen != nil {
				t.Fatalf("listen: %v", errListen)
			}
			defer func() {
				_ = listener.Close()
			}()

			muxLn := newMuxListener(listener.Addr(), 1024)
			defer func() {
				_ = muxLn.Close()
			}()

			go func() {
				_ = srv.acceptMuxConnections(listener, muxLn)
			}()
			go func() {
				_ = srv.server.Serve(muxLn)
			}()
			defer func() {
				_ = srv.server.Close()
			}()

			addr := listener.Addr().String()
			cases := []struct {
				name       string
				rawRequest string
				wantPrefix string
			}{
				{
					name: "1MiB boundary connection close",
					rawRequest: fmt.Sprintf(
						"POST /v1/chat/completions HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: 1048576\r\nConnection: close\r\n\r\n{",
						addr,
					),
					wantPrefix: "HTTP/1.1 401 ",
				},
				{
					name: "1MiB plus 1 connection close",
					rawRequest: fmt.Sprintf(
						"POST /v1/chat/completions HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: 1048577\r\nConnection: close\r\n\r\n{",
						addr,
					),
					wantPrefix: "HTTP/1.1 401 ",
				},
				{
					name: "1MiB boundary keep-alive",
					rawRequest: fmt.Sprintf(
						"POST /v1/chat/completions HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: 1048576\r\n\r\n{",
						addr,
					),
					wantPrefix: "HTTP/1.1 401 ",
				},
				{
					name: "small body keep-alive",
					rawRequest: fmt.Sprintf(
						"POST /v1/chat/completions HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: 128\r\n\r\n{",
						addr,
					),
					wantPrefix: "HTTP/1.1 401 ",
				},
				{
					name: "small multipart body keep-alive",
					rawRequest: fmt.Sprintf(
						"POST /v1/chat/completions HTTP/1.1\r\nHost: %s\r\nContent-Type: multipart/form-data; boundary=abc\r\nContent-Length: 128\r\n\r\n--abc\r\n",
						addr,
					),
					wantPrefix: "HTTP/1.1 401 ",
				},
				{
					name: "compressed response small body keep-alive",
					rawRequest: fmt.Sprintf(
						"POST /v1/chat/completions HTTP/1.1\r\nHost: %s\r\nAccept-Encoding: gzip\r\nContent-Type: application/json\r\nContent-Length: 128\r\n\r\n{",
						addr,
					),
					wantPrefix: "HTTP/1.1 401 ",
				},
				{
					name: "chunked incomplete body",
					rawRequest: fmt.Sprintf(
						"POST /v1/chat/completions HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nTransfer-Encoding: chunked\r\n\r\n1\r\n{",
						addr,
					),
					wantPrefix: "HTTP/1.1 401 ",
				},
				{
					name: "options small incomplete body keep-alive",
					rawRequest: fmt.Sprintf(
						"OPTIONS /v1/chat/completions HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: 128\r\n\r\n{",
						addr,
					),
					wantPrefix: "HTTP/1.1 204 ",
				},
				{
					name: "options chunked incomplete body keep-alive",
					rawRequest: fmt.Sprintf(
						"OPTIONS /v1/chat/completions HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nTransfer-Encoding: chunked\r\n\r\n1\r\n{",
						addr,
					),
					wantPrefix: "HTTP/1.1 204 ",
				},
				{
					name: "healthz small incomplete body keep-alive",
					rawRequest: fmt.Sprintf(
						"GET /healthz HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: 128\r\n\r\n{",
						addr,
					),
					wantPrefix: "HTTP/1.1 200 ",
				},
				{
					name: "forged websocket upgrade header on healthz keep-alive",
					rawRequest: fmt.Sprintf(
						"GET /healthz HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nContent-Length: 128\r\n\r\n{",
						addr,
					),
					wantPrefix: "HTTP/1.1 200 ",
				},
				{
					name: "trailing slash redirect with incomplete body keep-alive",
					rawRequest: fmt.Sprintf(
						"POST /v1/chat/completions/ HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: 128\r\n\r\n{",
						addr,
					),
					wantPrefix: "HTTP/1.1 307 ",
				},
			}

			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					conn, errDial := net.DialTimeout("tcp", addr, time.Second)
					if errDial != nil {
						t.Fatalf("dial: %v", errDial)
					}
					defer func() {
						_ = conn.Close()
					}()

					if errDeadline := conn.SetDeadline(time.Now().Add(500 * time.Millisecond)); errDeadline != nil {
						t.Fatalf("set deadline: %v", errDeadline)
					}
					if _, errWrite := conn.Write([]byte(tc.rawRequest)); errWrite != nil {
						t.Fatalf("write partial request: %v", errWrite)
					}

					rawResp, errRead := io.ReadAll(conn)
					if errRead != nil {
						t.Fatalf("server did not close connection promptly (err=%v, partial=%q)", errRead, string(rawResp))
					}
					respText := string(rawResp)
					if !strings.HasPrefix(respText, tc.wantPrefix) {
						t.Fatalf("response = %q, want %q prefix", respText, tc.wantPrefix)
					}
					if !strings.Contains(strings.ToLower(respText), "connection: close") {
						t.Fatalf("response missing Connection: close header: %q", respText)
					}
				})
			}

			// Verify authenticated requests and /healthz still work and capture bodies on errors.
			httpClient := &http.Client{Timeout: 2 * time.Second}
			healthResp, errHealth := httpClient.Get("http://" + addr + "/healthz")
			if errHealth != nil {
				t.Fatalf("healthz failed: %v", errHealth)
			}
			_ = healthResp.Body.Close()
			if healthResp.StatusCode != http.StatusOK {
				t.Fatalf("healthz status = %d, want 200", healthResp.StatusCode)
			}

			authPayload := `{"model":"gpt-5","messages":[{"role":"user","content":"authenticated-test-marker"}]}`
			authReq, errNewReq := http.NewRequest(http.MethodPost, "http://"+addr+"/v1/chat/completions", strings.NewReader(authPayload))
			if errNewReq != nil {
				t.Fatalf("new request: %v", errNewReq)
			}
			authReq.Header.Set("Authorization", "Bearer test-key")
			authReq.Header.Set("Content-Type", "application/json")
			authResp, errDo := httpClient.Do(authReq)
			if errDo != nil {
				t.Fatalf("authenticated request failed: %v", errDo)
			}
			_ = authResp.Body.Close()
			if authResp.StatusCode != http.StatusBadRequest {
				t.Fatalf("authenticated request status = %d, want 400", authResp.StatusCode)
			}

			if !mode.commercialMode {
				entries, errReadDir := os.ReadDir(logsDir)
				if errReadDir != nil {
					t.Fatalf("read logs dir: %v", errReadDir)
				}
				foundBodyInLog := false
				for _, entry := range entries {
					if !strings.HasSuffix(entry.Name(), ".log") {
						continue
					}
					content, errReadFile := os.ReadFile(filepath.Join(logsDir, entry.Name()))
					if errReadFile != nil {
						t.Fatalf("read log file: %v", errReadFile)
					}
					if strings.Contains(string(content), "authenticated-test-marker") {
						foundBodyInLog = true
						break
					}
				}
				if !foundBodyInLog {
					t.Fatalf("expected authenticated request body to be captured in logs")
				}
			}
			if srv.server == nil || srv.server.ReadHeaderTimeout <= 0 {
				t.Fatalf("expected positive http.Server.ReadHeaderTimeout, got %+v", srv.server)
			}
		})
	}
}

func TestAcceptMuxNotBlockedByIdleConnection(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer listener.Close()

	var routed atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		routed.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewUnstartedServer(handler)
	defer srv.Close()

	muxLn := newMuxListener(listener.Addr(), 1024)
	server := &Server{managementRoutesEnabled: atomic.Bool{}}
	server.managementRoutesEnabled.Store(false)

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.acceptMuxConnections(listener, muxLn)
	}()

	srv.Listener = muxLn
	srv.Start()

	// Open an idle TCP connection that never sends any bytes.
	idleConn, err := net.DialTimeout("tcp", listener.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("failed to dial idle connection: %v", err)
	}
	defer idleConn.Close()

	// Give the accept loop time to pick up the idle connection.
	time.Sleep(50 * time.Millisecond)

	// Send a real HTTP request. Before the fix, the accept loop would be
	// blocked on Peek(1) for the idle connection, causing this request to
	// time out.
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + listener.Addr().String() + "/")
	if err != nil {
		listener.Close()
		t.Fatalf("HTTP request failed (accept loop may be blocked by idle connection): %v", err)
	}
	resp.Body.Close()

	listener.Close()

	if routed.Load() == 0 {
		t.Error("expected at least one request to be routed")
	}
}
