package wstunnel

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
)

func dialTunnelURL(serverURL string, query url.Values) string {
	wsBase := "ws" + strings.TrimPrefix(serverURL, "http") + "/v1/tunnel"
	if len(query) > 0 {
		wsBase += "?" + query.Encode()
	}
	return wsBase
}

func closeHTTPResponseBody(t *testing.T, resp *http.Response) {
	t.Helper()
	if resp == nil || resp.Body == nil {
		return
	}
	if errClose := resp.Body.Close(); errClose != nil {
		t.Logf("response body close error: %v", errClose)
	}
}

func registerCleanupConn(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	t.Cleanup(func() {
		if conn != nil {
			_ = conn.Close()
		}
	})
}

func readTextJSONFrame(t *testing.T, conn *websocket.Conn) ojson.Result {
	t.Helper()
	msgType, raw, errRead := readTunnelTestMessage(conn)
	if errRead != nil {
		t.Fatalf("ReadMessage() error = %v", errRead)
	}
	if msgType != websocket.TextMessage {
		t.Fatalf("message type = %d, want TextMessage (%d)", msgType, websocket.TextMessage)
	}
	if !ojson.ValidBytes(raw) {
		t.Fatalf("invalid json frame: %s", string(raw))
	}
	return ojson.ParseBytes(raw)
}

// waitServersVisible drains control frames on conn until every uuid in want
// has been observed on the client (via an initial server_list or incremental
// server_online events). The WebSocket handshake completing does not guarantee
// commitPeer has published a concurrently-dialing server, and a server already
// present in the initial list produces no server_online, so all pending uuids
// must be collected across a single drain rather than one frame per uuid.
func waitServersVisible(t *testing.T, conn *websocket.Conn, want ...string) {
	t.Helper()
	pending := make(map[string]struct{}, len(want))
	for _, uuid := range want {
		pending[uuid] = struct{}{}
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(pending) > 0 && time.Now().Before(deadline) {
		frame := readTextJSONFrame(t, conn)
		switch frame.Get("type").String() {
		case ControlTypeServerList:
			count := int(frame.Get("servers.#").Int())
			for i := 0; i < count; i++ {
				delete(pending, frame.Get(fmt.Sprintf("servers.%d.uuid", i)).String())
			}
		case ControlTypeServerOnline:
			delete(pending, frame.Get("server.uuid").String())
		}
	}
	if len(pending) > 0 {
		t.Fatalf("timed out waiting for servers to become visible: %v", pending)
	}
}

func TestHubAuth_MethodAndCredentials(t *testing.T) {
	hub := NewHub(Options{})
	t.Cleanup(hub.Close)
	srv := httptest.NewServer(hub)
	t.Cleanup(srv.Close)

	t.Run("non-GET method returns 405 with Allow GET", func(t *testing.T) {
		req, errReq := http.NewRequest(http.MethodPost, srv.URL+"/v1/tunnel", nil)
		if errReq != nil {
			t.Fatalf("NewRequest error = %v", errReq)
		}
		resp, errDo := http.DefaultClient.Do(req)
		if errDo != nil {
			t.Fatalf("Do error = %v", errDo)
		}
		defer closeHTTPResponseBody(t, resp)
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusMethodNotAllowed)
		}
		if gotAllow := resp.Header.Get("Allow"); gotAllow != http.MethodGet {
			t.Fatalf("Allow = %q, want %q", gotAllow, http.MethodGet)
		}
	})

	t.Run("empty allowedKeys rejects even with credential", func(t *testing.T) {
		q := url.Values{"role": {"client"}, "uuid": {"c1"}, "name": {"Client 1"}, "key": {"any-key"}}
		_, resp, errDial := websocket.DefaultDialer.Dial(dialTunnelURL(srv.URL, q), nil)
		defer closeHTTPResponseBody(t, resp)
		if errDial == nil {
			t.Fatal("Dial succeeded with empty allowedKeys, want 401")
		}
		if resp == nil || resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %v, want 401", resp)
		}
		body, _ := io.ReadAll(resp.Body)
		if ojson.GetBytes(body, "error").String() != "invalid or missing api key" {
			t.Fatalf("error body = %s, want invalid or missing api key", string(body))
		}
	})

	hub.UpdateAllowedAPIKeys([]string{"  key-alpha  ", "", "key-beta", "key-alpha"})

	t.Run("missing or wrong credential returns 401", func(t *testing.T) {
		for _, badKey := range []string{"", "wrong-key"} {
			q := url.Values{"role": {"client"}, "uuid": {"c1"}, "name": {"Client 1"}}
			if badKey != "" {
				q.Set("key", badKey)
			}
			_, resp, errDial := websocket.DefaultDialer.Dial(dialTunnelURL(srv.URL, q), nil)
			closeHTTPResponseBody(t, resp)
			if errDial == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("Dial(key=%q) = (%v, %v), want 401", badKey, resp, errDial)
			}
		}
	})

	t.Run("all five credential sources and multi-candidate fallback work", func(t *testing.T) {
		cases := []struct {
			name    string
			headers http.Header
			query   url.Values
			uuid    string
		}{
			{
				name:    "Authorization Bearer",
				headers: http.Header{"Authorization": {"Bearer key-alpha"}},
				query:   url.Values{"role": {"server"}, "uuid": {"u-auth-bearer"}, "name": {"S1"}, "type": {"s"}},
				uuid:    "u-auth-bearer",
			},
			{
				name:    "X-Goog-Api-Key",
				headers: http.Header{"X-Goog-Api-Key": {"key-alpha"}},
				query:   url.Values{"role": {"server"}, "uuid": {"u-goog"}, "name": {"S2"}, "type": {"type-2"}},
				uuid:    "u-goog",
			},
			{
				name:    "X-Api-Key",
				headers: http.Header{"X-Api-Key": {"key-alpha"}},
				query:   url.Values{"role": {"server"}, "uuid": {"u-xapi"}, "name": {"S3"}, "type": {"type-3"}},
				uuid:    "u-xapi",
			},
			{
				name:  "query key",
				query: url.Values{"key": {"key-alpha"}, "role": {"server"}, "uuid": {"u-qkey"}, "name": {"S4"}, "type": {"type-4"}},
				uuid:  "u-qkey",
			},
			{
				name:  "query auth_token",
				query: url.Values{"auth_token": {"key-alpha"}, "role": {"server"}, "uuid": {"u-qtoken"}, "name": {"S5"}, "type": {"type-5"}},
				uuid:  "u-qtoken",
			},
			{
				name:    "invalid Authorization falls back to valid query key",
				headers: http.Header{"Authorization": {"Bearer invalid-first"}},
				query:   url.Values{"key": {"key-beta"}, "role": {"client"}, "uuid": {"u-fallback"}, "name": {"C-Fallback"}},
				uuid:    "u-fallback",
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				conn, resp, errDial := websocket.DefaultDialer.Dial(dialTunnelURL(srv.URL, tc.query), tc.headers)
				closeHTTPResponseBody(t, resp)
				if errDial != nil {
					t.Fatalf("Dial() error = %v", errDial)
				}
				registerCleanupConn(t, conn)
				if tc.query.Get("role") == "client" {
					frame := readTextJSONFrame(t, conn)
					if frame.Get("type").String() != ControlTypeServerList || frame.Get("servers.#").Int() != 0 {
						t.Fatalf("fallback client expected empty key-beta server_list, got %s", frame.Raw)
					}
				}
			})
		}
	})

	t.Run("header fallback for role uuid name type and 400 validation errors", func(t *testing.T) {
		headers := http.Header{
			"Authorization": {"Bearer key-beta"},
			"X-Tunnel-Role": {"SERVER"},
			"X-Tunnel-UUID": {"hdr-srv-1"},
			"X-Tunnel-Name": {"Header Server"},
			"X-Tunnel-Type": {"  Header-Type-01  "},
		}
		// Blank query type must fall back to X-Tunnel-Type; non-blank query type overrides header.
		conn, resp, errDial := websocket.DefaultDialer.Dial(dialTunnelURL(srv.URL, url.Values{"type": {"   "}}), headers)
		closeHTTPResponseBody(t, resp)
		if errDial != nil {
			t.Fatalf("Dial with X-Tunnel-* headers error = %v", errDial)
		}
		registerCleanupConn(t, conn)

		overrideConn, overResp, errOver := websocket.DefaultDialer.Dial(
			dialTunnelURL(srv.URL, url.Values{"role": {"server"}, "uuid": {"hdr-srv-2"}, "name": {"Override Server"}, "type": {"Query-Type"}}),
			http.Header{"Authorization": {"Bearer key-beta"}, "X-Tunnel-Type": {"Ignored-Header-Type"}},
		)
		closeHTTPResponseBody(t, overResp)
		if errOver != nil {
			t.Fatalf("Dial with query type override error = %v", errOver)
		}
		registerCleanupConn(t, overrideConn)

		// Client role ignores type (both omitted and blank/arbitrary values allowed).
		cliCheck, cliResp, errCli := websocket.DefaultDialer.Dial(
			dialTunnelURL(srv.URL, url.Values{"role": {"client"}, "uuid": {"hdr-cli-check"}, "name": {"Client Check"}}),
			http.Header{"Authorization": {"Bearer key-beta"}},
		)
		closeHTTPResponseBody(t, cliResp)
		if errCli != nil {
			t.Fatalf("Dial client without type error = %v", errCli)
		}
		registerCleanupConn(t, cliCheck)
		listFrame := readTextJSONFrame(t, cliCheck)
		if listFrame.Get("servers.#").Int() != 2 ||
			listFrame.Get("servers.0.uuid").String() != "hdr-srv-1" ||
			listFrame.Get("servers.0.type").String() != "Header-Type-01" ||
			listFrame.Get("servers.1.uuid").String() != "hdr-srv-2" ||
			listFrame.Get("servers.1.type").String() != "Query-Type" {
			t.Fatalf("unexpected server_list types: %s", listFrame.Raw)
		}

		cliWithBlankType, cliBlankResp, errCliBlank := websocket.DefaultDialer.Dial(
			dialTunnelURL(srv.URL, url.Values{"role": {"client"}, "uuid": {"hdr-cli-blank-type"}, "name": {"Client Blank Type"}, "type": {""}}),
			http.Header{"Authorization": {"Bearer key-beta"}},
		)
		closeHTTPResponseBody(t, cliBlankResp)
		if errCliBlank != nil {
			t.Fatalf("Dial client with empty type error = %v", errCliBlank)
		}
		registerCleanupConn(t, cliWithBlankType)
		_ = readTextJSONFrame(t, cliWithBlankType)

		badCases := []struct {
			query   url.Values
			wantErr string
		}{
			{url.Values{"role": {"invalid"}, "uuid": {"u1"}, "name": {"n1"}}, "invalid or missing role: must be client or server"},
			{url.Values{"role": {"client"}, "uuid": {"bad uuid"}, "name": {"n1"}}, "invalid or missing uuid"},
			{url.Values{"role": {"client"}, "uuid": {"u1"}, "name": {"   "}}, "invalid or missing name"},
			{url.Values{"role": {"server"}, "uuid": {"u-no-type"}, "name": {"n1"}}, "invalid or missing type"},
			{url.Values{"role": {"server"}, "uuid": {"u-no-type"}, "name": {"n1"}, "type": {""}}, "invalid or missing type"},
			{url.Values{"role": {"server"}, "uuid": {"u-no-type"}, "name": {"n1"}, "type": {"   "}}, "invalid or missing type"},
			{url.Values{"role": {"server"}, "uuid": {"u-no-type"}, "name": {"n1"}, "type": {"bad\ttype"}}, "invalid or missing type"},
			{url.Values{"role": {"server"}, "uuid": {"u-no-type"}, "name": {"n1"}, "type": {strings.Repeat("t", MaxServerTypeLen+1)}}, "invalid or missing type"},
		}
		for _, bc := range badCases {
			_, badResp, errBad := websocket.DefaultDialer.Dial(dialTunnelURL(srv.URL, bc.query), http.Header{"Authorization": {"Bearer key-beta"}})
			var errBody []byte
			if badResp != nil && badResp.Body != nil {
				errBody, _ = io.ReadAll(badResp.Body)
			}
			closeHTTPResponseBody(t, badResp)
			if errBad == nil || badResp == nil || badResp.StatusCode != http.StatusBadRequest {
				t.Fatalf("Dial(%v) = (%v, %v), want 400", bc.query, badResp, errBad)
			}
			if gotErr := ojson.GetBytes(errBody, "error").String(); gotErr != bc.wantErr {
				t.Fatalf("Dial(%v) error = %q, want %q", bc.query, gotErr, bc.wantErr)
			}
		}

		// Failed server type validation must not reserve the UUID; single-character type succeeds.
		afterRejectConn, afterRejectResp, errAfterReject := websocket.DefaultDialer.Dial(
			dialTunnelURL(srv.URL, url.Values{"role": {"server"}, "uuid": {"u-no-type"}, "name": {"Recovered"}, "type": {"x"}}),
			http.Header{"Authorization": {"Bearer key-beta"}},
		)
		closeHTTPResponseBody(t, afterRejectResp)
		if errAfterReject != nil {
			t.Fatalf("Dial after 400 type rejection failed (UUID leaked?): %v", errAfterReject)
		}
		registerCleanupConn(t, afterRejectConn)
	})
}

func TestHubAuth_UUIDUniquenessConcurrencyAndDuplicateNames(t *testing.T) {
	unregisteredCh := make(chan string, 16)
	hub := NewHub(Options{})
	hub.testOnPeerUnregistered = func(_, uuid string) {
		select {
		case unregisteredCh <- uuid:
		default:
		}
	}
	hub.UpdateAllowedAPIKeys([]string{"key-1"})
	t.Cleanup(hub.Close)

	srv := httptest.NewServer(hub)
	t.Cleanup(srv.Close)
	authHdr := http.Header{"Authorization": {"Bearer key-1"}}

	t.Run("cross-role and same-role duplicate uuid rejected with 409, reconnect allowed after disconnect", func(t *testing.T) {
		srvConn, resp, errDial := websocket.DefaultDialer.Dial(
			dialTunnelURL(srv.URL, url.Values{"role": {"server"}, "uuid": {"dup-uuid"}, "name": {"Shared Name"}, "type": {"shared-type"}}),
			authHdr,
		)
		closeHTTPResponseBody(t, resp)
		if errDial != nil {
			t.Fatalf("initial server dial error = %v", errDial)
		}

		for _, role := range []string{"server", "client"} {
			dupQuery := url.Values{"role": {role}, "uuid": {"dup-uuid"}, "name": {"Another"}}
			if role == "server" {
				dupQuery.Set("type", "another-type")
			}
			_, dupResp, errDup := websocket.DefaultDialer.Dial(
				dialTunnelURL(srv.URL, dupQuery),
				authHdr,
			)
			closeHTTPResponseBody(t, dupResp)
			if errDup == nil || dupResp == nil || dupResp.StatusCode != http.StatusConflict {
				t.Fatalf("duplicate uuid with role=%s got (%v, %v), want 409", role, dupResp, errDup)
			}
		}

		_ = srvConn.Close()
		select {
		case gotUUID := <-unregisteredCh:
			if gotUUID != "dup-uuid" {
				t.Fatalf("unregistered uuid = %q, want dup-uuid", gotUUID)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for dup-uuid unregister")
		}

		reConn, reResp, errReDial := websocket.DefaultDialer.Dial(
			dialTunnelURL(srv.URL, url.Values{"role": {"client"}, "uuid": {"dup-uuid"}, "name": {"Reconnected"}}),
			authHdr,
		)
		closeHTTPResponseBody(t, reResp)
		if errReDial != nil {
			t.Fatalf("reconnect after disconnect error = %v", errReDial)
		}
		registerCleanupConn(t, reConn)
		_ = readTextJSONFrame(t, reConn)
	})

	t.Run("16 concurrent dials with same uuid admit exactly 1 and reject 15 with 409", func(t *testing.T) {
		const workers = 16
		var (
			wg        sync.WaitGroup
			startGate = make(chan struct{})
			okCount   atomic.Int32
			conflict  atomic.Int32
			winnerMu  sync.Mutex
			winner    *websocket.Conn
		)
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-startGate
				c, resp, errDial := websocket.DefaultDialer.Dial(
					dialTunnelURL(srv.URL, url.Values{"role": {"server"}, "uuid": {"race-uuid"}, "name": {"RaceNode"}, "type": {"race-type"}}),
					authHdr,
				)
				closeHTTPResponseBody(t, resp)
				if errDial == nil {
					okCount.Add(1)
					winnerMu.Lock()
					winner = c
					winnerMu.Unlock()
					return
				}
				if resp != nil && resp.StatusCode == http.StatusConflict {
					conflict.Add(1)
				}
			}()
		}
		close(startGate)
		wg.Wait()
		if winner != nil {
			registerCleanupConn(t, winner)
		}
		if okCount.Load() != 1 || conflict.Load() != workers-1 {
			t.Fatalf("concurrent race results: ok=%d conflict=%d, want ok=1 conflict=%d", okCount.Load(), conflict.Load(), workers-1)
		}
	})

	t.Run("duplicate device names and server types with distinct uuids coexist", func(t *testing.T) {
		for _, id := range []string{"same-name-srv-1", "same-name-srv-2"} {
			c, resp, errDial := websocket.DefaultDialer.Dial(
				dialTunnelURL(srv.URL, url.Values{"role": {"server"}, "uuid": {id}, "name": {"Identical Name"}, "type": {"Identical Type"}}),
				authHdr,
			)
			closeHTTPResponseBody(t, resp)
			if errDial != nil {
				t.Fatalf("dial server %s error = %v", id, errDial)
			}
			registerCleanupConn(t, c)
		}
		for _, id := range []string{"same-name-cli-1", "same-name-cli-2"} {
			c, resp, errDial := websocket.DefaultDialer.Dial(
				dialTunnelURL(srv.URL, url.Values{"role": {"client"}, "uuid": {id}, "name": {"Identical Name"}}),
				authHdr,
			)
			closeHTTPResponseBody(t, resp)
			if errDial != nil {
				t.Fatalf("dial client %s error = %v", id, errDial)
			}
			registerCleanupConn(t, c)
			_ = readTextJSONFrame(t, c)
		}
	})
}

func TestHubAuth_PreCommitKeyRevocationAndClosedHub(t *testing.T) {
	hub := NewHub(Options{})
	hub.UpdateAllowedAPIKeys([]string{"ephemeral-key"})
	hub.testOnAfterReserve = func() {
		hub.UpdateAllowedAPIKeys(nil)
	}
	srv := httptest.NewServer(hub)
	t.Cleanup(srv.Close)

	conn, resp, errDial := websocket.DefaultDialer.Dial(
		dialTunnelURL(srv.URL, url.Values{"role": {"client"}, "uuid": {"revoked-mid-upgrade"}, "name": {"Client"}}),
		http.Header{"Authorization": {"Bearer ephemeral-key"}},
	)
	closeHTTPResponseBody(t, resp)
	if errDial == nil {
		registerCleanupConn(t, conn)

		_, _, errRead := readTunnelTestMessage(conn)
		var closeErr *websocket.CloseError
		if !errors.As(errRead, &closeErr) || closeErr.Code != websocket.ClosePolicyViolation {
			t.Fatalf("expected ClosePolicyViolation (1008) on pre-commit key revocation, got %v", errRead)
		}
	}

	hub.Close()
	_, closedResp, errClosedDial := websocket.DefaultDialer.Dial(
		dialTunnelURL(srv.URL, url.Values{"role": {"client"}, "uuid": {"after-close"}, "name": {"Client"}}),
		http.Header{"Authorization": {"Bearer ephemeral-key"}},
	)
	closeHTTPResponseBody(t, closedResp)
	if errClosedDial == nil || closedResp == nil || closedResp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("Dial after Hub.Close() = (%v, %v), want 503", closedResp, errClosedDial)
	}
}
