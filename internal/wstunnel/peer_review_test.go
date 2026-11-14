package wstunnel

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
)

func readTunnelTestMessage(conn *websocket.Conn) (int, []byte, error) {
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

func waitTunnelSignal(t *testing.T, ch <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

// gatedTunnelConn deterministically models a saturated transport send buffer.
// Arming it after a successful write leaves the WebSocket writer idle while
// the next socket write blocks, independently of OS buffer sizing or scheduling.
type gatedTunnelConn struct {
	net.Conn
	mu      sync.Mutex
	gate    chan struct{}
	blocked chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func (c *gatedTunnelConn) arm() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gate = make(chan struct{})
	c.blocked = make(chan struct{}, 1)
}

func (c *gatedTunnelConn) Write(raw []byte) (int, error) {
	c.mu.Lock()
	gate, blocked := c.gate, c.blocked
	c.mu.Unlock()
	if gate != nil {
		select {
		case blocked <- struct{}{}:
		default:
		}
		select {
		case <-gate:
		case <-c.closed:
			return 0, net.ErrClosed
		}
	}
	return c.Conn.Write(raw)
}

func (c *gatedTunnelConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

type gatedTunnelListener struct{ net.Listener }

func (l gatedTunnelListener) Accept() (net.Conn, error) {
	conn, errAccept := l.Listener.Accept()
	if errAccept != nil {
		return nil, errAccept
	}
	return &gatedTunnelConn{Conn: conn, closed: make(chan struct{})}, nil
}

func newGatedTunnelServer(t *testing.T, hub *Hub) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(hub)
	srv.Listener = gatedTunnelListener{srv.Listener}
	srv.Start()
	t.Cleanup(srv.Close)
	t.Cleanup(hub.Close)
	return srv
}

func lookupTunnelPeer(t *testing.T, hub *Hub, key, uuid string) *peer {
	t.Helper()
	hub.mu.RLock()
	defer hub.mu.RUnlock()
	tenant := hub.realms[key]
	if tenant == nil || tenant.peers[uuid] == nil {
		t.Fatalf("missing peer %s", uuid)
	}
	return tenant.peers[uuid]
}

func assertTunnelClose(t *testing.T, conn *websocket.Conn, code int) {
	t.Helper()
	_, _, errRead := readTunnelTestMessage(conn)
	var closeErr *websocket.CloseError
	if !errors.As(errRead, &closeErr) || closeErr.Code != code {
		t.Fatalf("close = %v, want code %d", errRead, code)
	}
}

func TestPeerClose_SaturatedTransportNeverBlocksCallers(t *testing.T) {
	for _, tc := range []struct{ shutdown, active bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		t.Run(fmt.Sprintf("shutdown=%t/active=%t", tc.shutdown, tc.active), func(t *testing.T) {
			forceClose := make(chan time.Time)
			hub := NewHub(Options{})
			hub.testForceClose = forceClose
			var forceOnce sync.Once
			reclaim := func() { forceOnce.Do(func() { close(forceClose) }) }
			t.Cleanup(reclaim)
			hub.UpdateAllowedAPIKeys([]string{"key", "keep"})
			srv := newGatedTunnelServer(t, hub)
			stalled := dialRole(t, srv.URL, "key", RoleClient, "stalled", "Stalled")
			_ = readTextJSONFrame(t, stalled)
			healthy := dialRole(t, srv.URL, "key", RoleClient, "healthy", "Healthy")
			_ = readTextJSONFrame(t, healthy)
			keep := dialRole(t, srv.URL, "keep", RoleClient, "keep", "Keep")
			_ = readTextJSONFrame(t, keep)

			p := lookupTunnelPeer(t, hub, "key", "stalled")
			transport := p.conn.UnderlyingConn().(*deadlineGuardConn).Conn.(*gatedTunnelConn)
			transport.arm()
			if tc.active {
				if !p.tryEnqueueData([]byte("blocked active write")) {
					t.Fatal("failed to start blocked write")
				}
				waitTunnelSignal(t, transport.blocked, "blocked active write")
			}
			// Without an active write, the close write itself must stall.
			returned := make(chan struct{})
			go func() {
				if tc.shutdown {
					hub.Close()
				} else {
					hub.UpdateAllowedAPIKeys([]string{"keep"})
				}
				close(returned)
			}()
			waitTunnelSignal(t, returned, "non-blocking revoke/shutdown")
			if !tc.active {
				waitTunnelSignal(t, transport.blocked, "close write on saturated transport")
			}
			wantCode := websocket.ClosePolicyViolation
			if tc.shutdown {
				wantCode = websocket.CloseGoingAway
			}
			assertTunnelClose(t, healthy, wantCode)
			hub.mu.RLock()
			tenant := hub.realms["key"]
			registered := tenant != nil && (len(tenant.peers) != 0 || len(tenant.reserved) != 0)
			hub.mu.RUnlock()
			if registered {
				t.Fatal("revoked peers or reservations remain registered")
			}
			if !tc.shutdown {
				if errWrite := keep.WriteMessage(websocket.TextMessage, []byte(`{"type":"list_servers"}`)); errWrite != nil {
					t.Fatal(errWrite)
				}
				_ = readTextJSONFrame(t, keep)
				returnedClose := make(chan struct{})
				go func() { hub.Close(); close(returnedClose) }()
				waitTunnelSignal(t, returnedClose, "shutdown after revocation")
			}
			// Advance the local reclamation clock without waiting for wall time.
			reclaim()
			waitTunnelSignal(t, p.done, "forced socket reclamation")
			waitTunnelSignal(t, p.writerDone, "stalled writer exit")
		})
	}
}

func TestPeerClose_ActiveWriteAndQueuedFramesReceivePolicyClose(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(fmt.Sprintf("revoke=%t", revoke), func(t *testing.T) {
			hub := NewHub(Options{SendQueueSize: 1})
			forceClose := make(chan time.Time)
			hub.testForceClose = forceClose
			t.Cleanup(func() { close(forceClose) })
			hub.UpdateAllowedAPIKeys([]string{"key"})
			srv := newGatedTunnelServer(t, hub)
			client := dialRole(t, srv.URL, "key", RoleClient, "client", "Client")
			_ = readTextJSONFrame(t, client)
			p := lookupTunnelPeer(t, hub, "key", "client")
			transport := p.conn.UnderlyingConn().(*deadlineGuardConn).Conn.(*gatedTunnelConn)
			transport.arm()
			if !p.tryEnqueueData([]byte("active")) {
				t.Fatal("failed to enqueue active write")
			}
			waitTunnelSignal(t, transport.blocked, "active write")
			if !p.tryEnqueueData([]byte("queued")) {
				t.Fatal("failed to enqueue waiting frame")
			}
			if revoke {
				hub.UpdateAllowedAPIKeys(nil)
			} else {
				// A topology event cannot fit behind the active and queued frames.
				_ = dialRole(t, srv.URL, "key", RoleServer, "server", "Server")
			}
			waitTunnelSignal(t, p.closed, "policy close request")
			close(transport.gate)
			kind, raw, errRead := readTunnelTestMessage(client)
			if errRead != nil || kind != websocket.BinaryMessage || string(raw) != "active" {
				t.Fatalf("active write = (%d, %q, %v)", kind, raw, errRead)
			}
			// The close frame must precede the discarded queued frame.
			assertTunnelClose(t, client, websocket.ClosePolicyViolation)
			waitTunnelSignal(t, p.writerDone, "graceful writer exit")
			if p.queuedBytes.Load() != 0 || p.tenant.queuedBytes.Load() != 0 {
				t.Fatal("policy close leaked queued bytes")
			}
		})
	}
}

func TestPeerClose_TLSConnectionReceivesPolicyCloseAndReclaimsSocket(t *testing.T) {
	hub := NewHub(Options{})
	hub.UpdateAllowedAPIKeys([]string{"key"})
	srv := httptest.NewTLSServer(hub)
	t.Cleanup(srv.Close)
	t.Cleanup(hub.Close)
	dialer := websocket.Dialer{TLSClientConfig: srv.Client().Transport.(*http.Transport).TLSClientConfig}
	client, resp, errDial := dialer.Dial(dialTunnelURL(srv.URL, url.Values{
		"key": {"key"}, "role": {"client"}, "uuid": {"client"}, "name": {"Client"},
	}), nil)
	closeHTTPResponseBody(t, resp)
	if errDial != nil {
		t.Fatal(errDial)
	}
	registerCleanupConn(t, client)
	_ = readTextJSONFrame(t, client)
	p := lookupTunnelPeer(t, hub, "key", "client")
	hub.UpdateAllowedAPIKeys(nil)
	assertTunnelClose(t, client, websocket.ClosePolicyViolation)
	waitTunnelSignal(t, p.done, "TLS socket reclamation")
	waitTunnelSignal(t, p.writerDone, "TLS writer exit")
}

func newBudgetTestPeer(hub *Hub, tenant *realm, uuid string, role Role) *peer {
	return &peer{
		hub: hub, tenant: tenant, apiKey: "a", uuid: uuid, role: role,
		sendCh: make(chan outboundFrame, 256), closed: make(chan struct{}), done: make(chan struct{}),
		maxQueuedBytes: hub.maxPeerQueuedBytes,
	}
}

func TestPeerBudget_ControlAndDataSharePeerAndTenantLimits(t *testing.T) {
	hub := NewHub(Options{MaxPeerQueuedBytes: 100, MaxTenantQueuedBytes: 150})
	tenant := newRealm()
	first := newBudgetTestPeer(hub, tenant, "first", RoleClient)
	second := newBudgetTestPeer(hub, tenant, "second", RoleClient)
	for i := 0; i < 2; i++ {
		if !first.tryEnqueueControl(outboundFrame{messageType: websocket.TextMessage, data: make([]byte, 50)}) {
			t.Fatal("control frame unexpectedly rejected")
		}
	}
	if first.tryEnqueueData([]byte("x")) || first.tryEnqueueControl(outboundFrame{data: []byte("x")}) {
		t.Fatal("control/data bypassed per-peer byte budget")
	}
	if !second.tryEnqueueControl(outboundFrame{data: make([]byte, 50)}) || tenant.queuedBytes.Load() != 150 {
		t.Fatal("control frames not accounted against tenant budget")
	}
	if second.tryEnqueueControl(outboundFrame{data: []byte("x")}) || second.queuedBytes.Load() != 50 {
		t.Fatal("tenant exhaustion failed to roll back peer reservation")
	}
	first.shutdownAndDrainQueue()
	second.shutdownAndDrainQueue()
	if tenant.queuedBytes.Load() != 0 || first.queuedBytes.Load() != 0 || second.queuedBytes.Load() != 0 {
		t.Fatal("draining control frames leaked byte reservations")
	}
	if first.tryEnqueueControl(outboundFrame{data: []byte("x")}) {
		t.Fatal("closed peer accepted control frame")
	}
}

func TestHubBudget_ExhaustedTenantDoesNotStarveAnotherTenant(t *testing.T) {
	hub := NewHub(Options{MaxPeerQueuedBytes: 1024, MaxTenantQueuedBytes: 2048})
	hub.UpdateAllowedAPIKeys([]string{"a", "b"})
	srv := httptest.NewServer(hub)
	t.Cleanup(srv.Close)
	t.Cleanup(hub.Close)
	serverB := dialRole(t, srv.URL, "b", RoleServer, "s", "Server")
	clientB := dialRole(t, srv.URL, "b", RoleClient, "c", "Client")
	_ = readTextJSONFrame(t, clientB)

	// Paused writers make exact exhaustion independent of TCP buffer sizes.
	tenantA := newRealm()
	slow := newBudgetTestPeer(hub, tenantA, "slow", RoleServer)
	healthy := newBudgetTestPeer(hub, tenantA, "healthy", RoleServer)
	senderA := newBudgetTestPeer(hub, tenantA, "sender", RoleClient)
	tenantA.peers[slow.uuid] = slow
	tenantA.peers[healthy.uuid] = healthy
	tenantA.peers[senderA.uuid] = senderA
	hub.mu.Lock()
	hub.realms["a"] = tenantA
	hub.mu.Unlock()
	routeA := func(target string, payloadBytes int) {
		t.Helper()
		raw, errEncode := EncodeDataFrame(PayloadTypeBinary, senderA.uuid, target, make([]byte, payloadBytes))
		if errEncode != nil {
			t.Fatal(errEncode)
		}
		hub.handleDataMessage(senderA, raw)
	}
	routeA(slow.uuid, 1024-MinBinaryFrameHeaderLen-len(senderA.uuid)-len(slow.uuid))
	if slow.queuedBytes.Load() != 1024 || slow.tryEnqueueData([]byte("full")) {
		t.Fatal("expected slow peer budget exhaustion")
	}
	routeA(healthy.uuid, 16)
	select {
	case frame := <-healthy.sendCh:
		healthy.releaseBytes(frame.accountedBytes)
	default:
		t.Fatal("slow peer starved healthy peer in same tenant")
	}
	routeA(healthy.uuid, 1024-MinBinaryFrameHeaderLen-len(senderA.uuid)-len(healthy.uuid))
	if tenantA.queuedBytes.Load() != 2048 {
		t.Fatal("tenant A budget not completely exhausted")
	}
	if healthy.tryEnqueueData([]byte("full")) {
		t.Fatal("tenant-wide budget was not enforced")
	}
	for _, direction := range []bool{false, true} {
		src, dst, sender, receiver := "c", "s", clientB, serverB
		if direction {
			src, dst, sender, receiver = "s", "c", serverB, clientB
		}
		raw, errEncode := EncodeDataFrame(PayloadTypeText, src, dst, []byte("tenant-b-healthy"))
		if errEncode != nil {
			t.Fatal(errEncode)
		}
		if errWrite := sender.WriteMessage(websocket.BinaryMessage, raw); errWrite != nil {
			t.Fatal(errWrite)
		}
		if got := readBinaryDataFrame(t, receiver); string(got.Payload) != "tenant-b-healthy" {
			t.Fatal("exhausted tenant A starved tenant B")
		}
	}
}

func TestControlFrame_LimitsAndBoundedErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		ok   bool
	}{
		{"frame boundary", `{"type":"list_servers"}` + strings.Repeat(" ", MaxControlFrameBytes-len(`{"type":"list_servers"}`)), true},
		{"oversized frame", strings.Repeat(" ", MaxControlFrameBytes+1), false},
		{"unknown type", `{"type":"` + strings.Repeat("x", MaxControlTypeLen) + `"}`, false},
		{"oversized type", `{"type":"` + strings.Repeat("x", MaxControlTypeLen+1) + `"}`, false},
		{"request id boundary", `{"type":"list_servers","request_id":"` + strings.Repeat("r", MaxRequestIDLen) + `"}`, true},
		{"oversized request id", `{"type":"list_servers","request_id":"` + strings.Repeat("r", MaxRequestIDLen+1) + `"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, code, errParse := ParseControlFrame([]byte(tc.raw))
			if tc.ok {
				if errParse != nil || code != "" {
					t.Fatalf("valid control rejected: %s %v", code, errParse)
				}
			} else if errParse == nil || code != ErrCodeInvalidControlFrame || len(errParse.Error()) > 100 || strings.Contains(errParse.Error(), strings.Repeat("x", 64)) {
				t.Fatalf("invalid control produced unbounded or missing error: %s %v", code, errParse)
			}
		})
	}
}

func TestHubLimits_ControlSizeAndFragmentation(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		t.Run(fmt.Sprintf("oversized=%t", oversized), func(t *testing.T) {
			hub := NewHub(Options{})
			hub.UpdateAllowedAPIKeys([]string{"key"})
			srv := httptest.NewServer(hub)
			t.Cleanup(srv.Close)
			t.Cleanup(hub.Close)
			client := dialRole(t, srv.URL, "key", RoleClient, "c", "Client")
			_ = readTextJSONFrame(t, client)
			raw := append([]byte(`{"type":"list_servers"}`), bytes.Repeat([]byte(" "), MaxControlFrameBytes-len(`{"type":"list_servers"}`))...)
			if oversized {
				raw = append(raw, ' ')
			}
			writer, errNextWriter := client.NextWriter(websocket.TextMessage)
			if errNextWriter != nil {
				t.Fatal(errNextWriter)
			}
			if _, errWrite := writer.Write(raw[:2048]); errWrite != nil {
				t.Fatal(errWrite)
			}
			if _, errWrite := writer.Write(raw[2048:]); errWrite != nil {
				t.Fatal(errWrite)
			}
			if errClose := writer.Close(); errClose != nil {
				t.Fatal(errClose)
			}
			if oversized {
				assertTunnelClose(t, client, websocket.CloseMessageTooBig)
			} else if got := readTextJSONFrame(t, client); got.Get("type").String() != ControlTypeServerList {
				t.Fatalf("boundary control rejected: %s", got.Raw)
			}
		})
	}
}

func TestHubLimits_BinaryPayloadAndTransportBoundaries(t *testing.T) {
	hub := NewHub(Options{})
	hub.UpdateAllowedAPIKeys([]string{"key"})
	srv := httptest.NewServer(hub)
	t.Cleanup(srv.Close)
	t.Cleanup(hub.Close)
	server := dialRole(t, srv.URL, "key", RoleServer, "s", "Server")
	client := dialRole(t, srv.URL, "key", RoleClient, "c", "Client")
	_ = readTextJSONFrame(t, client)
	for _, size := range []int{MaxRawPayloadBytes, MaxRawPayloadBytes + 1, MaxInboundFrameBytes - 6, MaxInboundFrameBytes - 5} {
		raw := append([]byte{ProtocolVersion1, PayloadTypeBinary, 1, 1, 'c', 's'}, make([]byte, size)...)
		written := make(chan error, 1)
		go func() { written <- client.WriteMessage(websocket.BinaryMessage, raw) }()
		if len(raw) > MaxInboundFrameBytes {
			assertTunnelClose(t, client, websocket.CloseMessageTooBig)
			select {
			case <-written:
			case <-time.After(5 * time.Second):
				t.Fatal("oversized transport writer did not exit")
			}
			continue
		}
		if errWrite := <-written; errWrite != nil {
			t.Fatal(errWrite)
		}
		if size == MaxRawPayloadBytes {
			if got := readBinaryDataFrame(t, server); len(got.Payload) != MaxRawPayloadBytes {
				t.Fatalf("payload boundary length = %d", len(got.Payload))
			}
			continue
		}
		if got := readTextJSONFrame(t, client); got.Get("code").String() != ErrCodeInvalidDataFrame {
			t.Fatalf("protocol overflow = %s", got.Raw)
		}
		if errWrite := client.WriteMessage(websocket.TextMessage, []byte(`{"type":"list_servers","request_id":"alive"}`)); errWrite != nil {
			t.Fatal(errWrite)
		}
		if got := readTextJSONFrame(t, client); got.Get("request_id").String() != "alive" {
			t.Fatal("protocol payload overflow terminated connection")
		}
	}
}

func TestHubClose_RevokedSessionCannotRouteAfterKeyIsReadded(t *testing.T) {
	hub := NewHub(Options{})
	hub.UpdateAllowedAPIKeys([]string{"a"})
	t.Cleanup(hub.Close)
	oldTenant := newRealm()
	stale := newBudgetTestPeer(hub, oldTenant, "client", RoleClient)
	oldTenant.peers[stale.uuid] = stale
	hub.realms["a"] = oldTenant
	hub.UpdateAllowedAPIKeys(nil)
	hub.UpdateAllowedAPIKeys([]string{"a"})
	newTenant := newRealm()
	current := newBudgetTestPeer(hub, newTenant, "client", RoleClient)
	target := newBudgetTestPeer(hub, newTenant, "server", RoleServer)
	newTenant.peers[current.uuid] = current
	newTenant.peers[target.uuid] = target
	hub.realms["a"] = newTenant
	raw, errEncode := EncodeDataFrame(PayloadTypeText, "client", "server", []byte("not authorized"))
	if errEncode != nil {
		t.Fatal(errEncode)
	}
	hub.handleDataMessage(stale, raw)
	hub.handleControlMessage(stale, []byte(`{"type":"list_servers"}`))
	if len(target.sendCh) != 0 || len(stale.sendCh) != 0 {
		t.Fatal("revoked session accessed a new tenant generation")
	}
	hub.handleDataMessage(current, raw)
	if len(target.sendCh) != 1 {
		t.Fatal("current authorized session could not route")
	}
}

func TestHubControl_UnknownTypeErrorDoesNotEchoInput(t *testing.T) {
	hub := NewHub(Options{})
	hub.UpdateAllowedAPIKeys([]string{"key"})
	srv := httptest.NewServer(hub)
	t.Cleanup(srv.Close)
	t.Cleanup(hub.Close)
	client := dialRole(t, srv.URL, "key", RoleClient, "c", "Client")
	_ = readTextJSONFrame(t, client)
	doc := ojson.NewDocString("{}")
	if errSet := doc.Set("type", strings.Repeat("x", MaxControlTypeLen)); errSet != nil {
		t.Fatal(errSet)
	}
	if errWrite := client.WriteMessage(websocket.TextMessage, doc.Bytes()); errWrite != nil {
		t.Fatal(errWrite)
	}
	got := readTextJSONFrame(t, client)
	if got.Get("code").String() != ErrCodeInvalidControlFrame || len(got.Raw) > 200 || strings.Contains(got.Raw, strings.Repeat("x", MaxControlTypeLen)) {
		t.Fatalf("unknown type amplified into error: %s", got.Raw)
	}
}
