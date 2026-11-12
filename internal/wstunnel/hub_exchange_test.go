package wstunnel

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func dialRole(t *testing.T, serverURL, apiKey string, role Role, uuid, name string) *websocket.Conn {
	t.Helper()
	serverType := ""
	if role == RoleServer {
		serverType = "test-server"
	}
	return dialRoleWithType(t, serverURL, apiKey, role, uuid, name, serverType)
}

func dialRoleWithType(t *testing.T, serverURL, apiKey string, role Role, uuid, name, serverType string) *websocket.Conn {
	t.Helper()
	q := url.Values{
		"role": {string(role)},
		"uuid": {uuid},
		"name": {name},
	}
	if serverType != "" {
		q.Set("type", serverType)
	}
	conn, resp, errDial := websocket.DefaultDialer.Dial(
		dialTunnelURL(serverURL, q),
		http.Header{"Authorization": {"Bearer " + apiKey}},
	)
	closeHTTPResponseBody(t, resp)
	if errDial != nil {
		t.Fatalf("dialRole(%s, %s) error = %v", role, uuid, errDial)
	}
	registerCleanupConn(t, conn)
	return conn
}

func readBinaryDataFrame(t *testing.T, conn *websocket.Conn) DataFrameView {
	t.Helper()
	msgType, raw, errRead := readTunnelTestMessage(conn)
	if errRead != nil {
		t.Fatalf("ReadMessage() error = %v", errRead)
	}
	if msgType != websocket.BinaryMessage {
		t.Fatalf("message type = %d (%s), want BinaryMessage", msgType, string(raw))
	}
	view, code, errParse := ParseDataFrameHeader(raw)
	if errParse != nil || code != "" {
		t.Fatalf("ParseDataFrameHeader() = (%+v, %q, %v)", view, code, errParse)
	}
	return view
}

func TestHubExchange_TenantIsolation(t *testing.T) {
	hub := NewHub(Options{})
	hub.UpdateAllowedAPIKeys([]string{"key-1", "key-2"})
	t.Cleanup(hub.Close)
	srv := httptest.NewServer(hub)
	t.Cleanup(srv.Close)

	// Same UUIDs registered under two distinct api-keys must not collide or leak.
	srv1 := dialRole(t, srv.URL, "key-1", RoleServer, "shared-srv-uuid", "Tenant1-Server")
	srv2 := dialRole(t, srv.URL, "key-2", RoleServer, "shared-srv-uuid", "Tenant2-Server")
	_ = dialRole(t, srv.URL, "key-2", RoleServer, "only-in-tenant-2", "Secret-Server-2")

	cli1 := dialRole(t, srv.URL, "key-1", RoleClient, "shared-cli-uuid", "Tenant1-Client")
	cli2 := dialRole(t, srv.URL, "key-2", RoleClient, "shared-cli-uuid", "Tenant2-Client")

	list1 := readTextJSONFrame(t, cli1)
	if list1.Get("servers.#").Int() != 1 || list1.Get("servers.0.name").String() != "Tenant1-Server" {
		t.Fatalf("tenant 1 server_list leaked or wrong: %s", list1.Raw)
	}
	list2 := readTextJSONFrame(t, cli2)
	if list2.Get("servers.#").Int() != 2 {
		t.Fatalf("tenant 2 server_list count = %d, want 2 (%s)", list2.Get("servers.#").Int(), list2.Raw)
	}

	// Sending to shared-srv-uuid from cli1 must reach srv1 and never srv2.
	pkt1, errEncode := EncodeDataFrame(PayloadTypeText, "shared-cli-uuid", "shared-srv-uuid", []byte("hello-tenant-1"))
	if errEncode != nil {
		t.Fatalf("EncodeDataFrame error = %v", errEncode)
	}
	if errWrite := cli1.WriteMessage(websocket.BinaryMessage, pkt1); errWrite != nil {
		t.Fatalf("cli1 WriteMessage error = %v", errWrite)
	}
	got1 := readBinaryDataFrame(t, srv1)
	if got1.SrcUUID != "shared-cli-uuid" || string(got1.Payload) != "hello-tenant-1" {
		t.Fatalf("srv1 got unexpected packet: %+v", got1)
	}

	// Attempting to address a UUID that exists only in key-2 from key-1 must return target_not_found.
	crossPkt, errCross := EncodeDataFrame(PayloadTypeBinary, "shared-cli-uuid", "only-in-tenant-2", []byte{1, 2, 3})
	if errCross != nil {
		t.Fatalf("EncodeDataFrame error = %v", errCross)
	}
	if errWrite := cli1.WriteMessage(websocket.BinaryMessage, crossPkt); errWrite != nil {
		t.Fatalf("cli1 WriteMessage error = %v", errWrite)
	}
	errFrame := readTextJSONFrame(t, cli1)
	if errFrame.Get("type").String() != ControlTypeError || errFrame.Get("code").String() != ErrCodeTargetNotFound {
		t.Fatalf("expected target_not_found across tenants, got %s", errFrame.Raw)
	}
	_ = srv2
}

func TestHubExchange_DiscoveryEventsAndControlCommands(t *testing.T) {
	hub := NewHub(Options{})
	hub.UpdateAllowedAPIKeys([]string{"key-disc"})
	t.Cleanup(hub.Close)
	srv := httptest.NewServer(hub)
	t.Cleanup(srv.Close)

	// Connect servers out of lexicographical order to verify deterministic sorting and custom types.
	srvB := dialRoleWithType(t, srv.URL, "key-disc", RoleServer, "srv-b", "Server B", "linux-builder")
	_ = dialRoleWithType(t, srv.URL, "key-disc", RoleServer, "srv-a", "Server A", "cpa-desktop")

	cli := dialRole(t, srv.URL, "key-disc", RoleClient, "cli-1", "Client 1")
	initial := readTextJSONFrame(t, cli)
	if initial.Get("type").String() != ControlTypeServerList ||
		initial.Get("servers.#").Int() != 2 ||
		initial.Get("servers.0.uuid").String() != "srv-a" ||
		initial.Get("servers.0.type").String() != "cpa-desktop" ||
		initial.Get("servers.1.uuid").String() != "srv-b" ||
		initial.Get("servers.1.type").String() != "linux-builder" {
		t.Fatalf("unexpected initial server_list: %s", initial.Raw)
	}

	// Dynamic server_online push when srv-c joins with arbitrary custom type.
	srvC := dialRoleWithType(t, srv.URL, "key-disc", RoleServer, "srv-c", "Server C", "GPU 节点 🚀")
	online := readTextJSONFrame(t, cli)
	if online.Get("type").String() != ControlTypeServerOnline ||
		online.Get("server.uuid").String() != "srv-c" ||
		online.Get("server.name").String() != "Server C" ||
		online.Get("server.type").String() != "GPU 节点 🚀" {
		t.Fatalf("unexpected server_online: %s", online.Raw)
	}

	// Explicit list_servers query with request_id echo.
	if errWrite := cli.WriteMessage(websocket.TextMessage, []byte(`{"type":"list_servers","request_id":"req-sync-1"}`)); errWrite != nil {
		t.Fatalf("WriteMessage(list_servers) error = %v", errWrite)
	}
	refreshed := readTextJSONFrame(t, cli)
	if refreshed.Get("type").String() != ControlTypeServerList ||
		refreshed.Get("request_id").String() != "req-sync-1" ||
		refreshed.Get("servers.#").Int() != 3 ||
		refreshed.Get("servers.2.uuid").String() != "srv-c" ||
		refreshed.Get("servers.2.type").String() != "GPU 节点 🚀" {
		t.Fatalf("unexpected refreshed server_list: %s", refreshed.Raw)
	}

	// Server role attempting list_servers gets unsupported_role_action without disconnecting.
	if errWrite := srvB.WriteMessage(websocket.TextMessage, []byte(`{"type":"list_servers"}`)); errWrite != nil {
		t.Fatalf("srvB WriteMessage error = %v", errWrite)
	}
	srvErr := readTextJSONFrame(t, srvB)
	if srvErr.Get("code").String() != ErrCodeUnsupportedRoleAction {
		t.Fatalf("expected unsupported_role_action for server list_servers, got %s", srvErr.Raw)
	}

	// Dynamic server_offline push when srv-c disconnects.
	_ = srvC.Close()
	offline := readTextJSONFrame(t, cli)
	if offline.Get("type").String() != ControlTypeServerOffline ||
		offline.Get("server.uuid").String() != "srv-c" ||
		offline.Get("server.name").String() != "Server C" ||
		offline.Get("server.type").String() != "GPU 节点 🚀" {
		t.Fatalf("unexpected server_offline: %s", offline.Raw)
	}
}

func TestHubExchange_InvalidUTF8ControlNeverExecutesListServers(t *testing.T) {
	hub := NewHub(Options{})
	hub.UpdateAllowedAPIKeys([]string{"key"})
	t.Cleanup(hub.Close)
	srv := httptest.NewServer(hub)
	t.Cleanup(srv.Close)
	client := dialRole(t, srv.URL, "key", RoleClient, "client", "Client")
	_ = readTextJSONFrame(t, client)
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"extra string", "{\"type\":\"list_servers\",\"extra\":\"\xff\"}"},
		{"field name", "{\"type\":\"list_servers\",\"\xff\":\"ignored\"}"},
		{"nested extra string", "{\"type\":\"list_servers\",\"extra\":{\"name\":\"\xc0\xaf\"}}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if errWrite := client.WriteMessage(websocket.TextMessage, []byte(tc.raw)); errWrite != nil {
				t.Fatal(errWrite)
			}
			frame := readTextJSONFrame(t, client)
			if frame.Get("type").String() != ControlTypeError || frame.Get("code").String() != ErrCodeInvalidControlFrame {
				t.Fatalf("invalid UTF-8 control executed instead of returning invalid_control_frame: %s", frame.Raw)
			}
			// A valid barrier proves rejection is non-fatal and left no list response queued.
			if errWrite := client.WriteMessage(websocket.TextMessage, []byte(`{"type":"list_servers","request_id":"after-invalid-utf8"}`)); errWrite != nil {
				t.Fatal(errWrite)
			}
			barrier := readTextJSONFrame(t, client)
			if barrier.Get("type").String() != ControlTypeServerList || barrier.Get("request_id").String() != "after-invalid-utf8" {
				t.Fatalf("unexpected response after invalid UTF-8 control: %s", barrier.Raw)
			}
		})
	}
}

func TestHubExchange_ManyToManyFullDuplexMultiplexing(t *testing.T) {
	hub := NewHub(Options{})
	hub.UpdateAllowedAPIKeys([]string{"key-mesh"})
	t.Cleanup(hub.Close)
	srv := httptest.NewServer(hub)
	t.Cleanup(srv.Close)

	const numPeers = 3
	servers := make([]*websocket.Conn, numPeers)
	serverWriteMu := make([]sync.Mutex, numPeers)
	for s := 0; s < numPeers; s++ {
		servers[s] = dialRole(t, srv.URL, "key-mesh", RoleServer, fmt.Sprintf("srv-%d", s), fmt.Sprintf("Server %d", s))
	}

	clients := make([]*websocket.Conn, numPeers)
	clientWriteMu := make([]sync.Mutex, numPeers)
	for c := 0; c < numPeers; c++ {
		clients[c] = dialRole(t, srv.URL, "key-mesh", RoleClient, fmt.Sprintf("cli-%d", c), fmt.Sprintf("Client %d", c))
		initList := readTextJSONFrame(t, clients[c])
		if initList.Get("servers.#").Int() != numPeers {
			t.Fatalf("client %d saw %d servers, want %d", c, initList.Get("servers.#").Int(), numPeers)
		}
	}

	// Each server reads 2*numPeers packets (one binary + one empty/text from each client) and replies back to the sender client.
	var srvWG sync.WaitGroup
	errCh := make(chan error, 32)
	for s := 0; s < numPeers; s++ {
		srvIdx := s
		srvUUID := fmt.Sprintf("srv-%d", srvIdx)
		srvWG.Add(1)
		go func() {
			defer srvWG.Done()
			for i := 0; i < numPeers*2; i++ {

				msgType, raw, errRead := readTunnelTestMessage(servers[srvIdx])
				if errRead != nil {
					errCh <- fmt.Errorf("server %d read: %w", srvIdx, errRead)
					return
				}
				if msgType != websocket.BinaryMessage {
					errCh <- fmt.Errorf("server %d unexpected msgType %d", srvIdx, msgType)
					return
				}
				view, code, errParse := ParseDataFrameHeader(raw)
				if errParse != nil || code != "" {
					errCh <- fmt.Errorf("server %d parse: %s %v", srvIdx, code, errParse)
					return
				}
				if view.DstUUID != srvUUID {
					errCh <- fmt.Errorf("server %d got DstUUID %q", srvIdx, view.DstUUID)
					return
				}
				var replyPayload []byte
				if len(view.Payload) > 0 {
					replyPayload = append([]byte("ack:"+srvUUID+":"), view.Payload...)
				}
				replyFrame, errEnc := EncodeDataFrame(view.PayloadType, srvUUID, view.SrcUUID, replyPayload)
				if errEnc != nil {
					errCh <- errEnc
					return
				}
				serverWriteMu[srvIdx].Lock()
				errWrite := servers[srvIdx].WriteMessage(websocket.BinaryMessage, replyFrame)
				serverWriteMu[srvIdx].Unlock()
				if errWrite != nil {
					errCh <- errWrite
					return
				}
			}
		}()
	}

	var cliReadWG sync.WaitGroup
	received := make([]map[string][]byte, numPeers)
	var receivedMu sync.Mutex
	for c := 0; c < numPeers; c++ {
		received[c] = make(map[string][]byte)
		cliIdx := c
		cliUUID := fmt.Sprintf("cli-%d", cliIdx)
		cliReadWG.Add(1)
		go func() {
			defer cliReadWG.Done()
			for i := 0; i < numPeers*2; i++ {

				msgType, raw, errRead := readTunnelTestMessage(clients[cliIdx])
				if errRead != nil {
					errCh <- fmt.Errorf("client %d read: %w", cliIdx, errRead)
					return
				}
				if msgType != websocket.BinaryMessage {
					errCh <- fmt.Errorf("client %d unexpected msgType %d: %s", cliIdx, msgType, string(raw))
					return
				}
				view, _, errParse := ParseDataFrameHeader(raw)
				if errParse != nil {
					errCh <- errParse
					return
				}
				if view.DstUUID != cliUUID {
					errCh <- fmt.Errorf("client %d got DstUUID %q", cliIdx, view.DstUUID)
					return
				}
				key := fmt.Sprintf("%s:%d", view.SrcUUID, view.PayloadType)
				receivedMu.Lock()
				received[cliIdx][key] = append([]byte(nil), view.Payload...)
				receivedMu.Unlock()
			}
		}()
	}

	var cliSendWG sync.WaitGroup
	for c := 0; c < numPeers; c++ {
		for s := 0; s < numPeers; s++ {
			cliIdx, srvIdx := c, s
			cliSendWG.Add(1)
			go func() {
				defer cliSendWG.Done()
				cliUUID := fmt.Sprintf("cli-%d", cliIdx)
				srvUUID := fmt.Sprintf("srv-%d", srvIdx)

				binBody := []byte{0x00, byte(cliIdx), byte(srvIdx), 0xff}
				binFrame, errEnc1 := EncodeDataFrame(PayloadTypeBinary, cliUUID, srvUUID, binBody)
				if errEnc1 != nil {
					errCh <- errEnc1
					return
				}
				emptyFrame, errEnc2 := EncodeDataFrame(PayloadTypeText, cliUUID, srvUUID, nil)
				if errEnc2 != nil {
					errCh <- errEnc2
					return
				}

				clientWriteMu[cliIdx].Lock()
				errW1 := clients[cliIdx].WriteMessage(websocket.BinaryMessage, binFrame)
				errW2 := clients[cliIdx].WriteMessage(websocket.BinaryMessage, emptyFrame)
				clientWriteMu[cliIdx].Unlock()
				if errW1 != nil {
					errCh <- errW1
				}
				if errW2 != nil {
					errCh <- errW2
				}
			}()
		}
	}

	cliSendWG.Wait()
	srvWG.Wait()
	cliReadWG.Wait()
	close(errCh)
	for errItem := range errCh {
		if errItem != nil {
			t.Fatalf("mesh error: %v", errItem)
		}
	}

	for c := 0; c < numPeers; c++ {
		for s := 0; s < numPeers; s++ {
			srvUUID := fmt.Sprintf("srv-%d", s)
			wantBin := append([]byte("ack:"+srvUUID+":"), 0x00, byte(c), byte(s), 0xff)
			gotBin := received[c][fmt.Sprintf("%s:%d", srvUUID, PayloadTypeBinary)]
			if !bytes.Equal(gotBin, wantBin) {
				t.Fatalf("client %d <- %s binary payload = %x, want %x", c, srvUUID, gotBin, wantBin)
			}
			gotEmpty, exists := received[c][fmt.Sprintf("%s:%d", srvUUID, PayloadTypeText)]
			if !exists || len(gotEmpty) != 0 {
				t.Fatalf("client %d <- %s empty payload = (%v, len=%d), want empty", c, srvUUID, exists, len(gotEmpty))
			}
		}
	}
}

func TestHubExchange_SecurityGuardsAndBackpressureIsolation(t *testing.T) {
	hub := NewHub(Options{SendQueueSize: 2})
	hub.UpdateAllowedAPIKeys([]string{"key-sec"})
	t.Cleanup(hub.Close)
	srv := httptest.NewServer(hub)
	t.Cleanup(srv.Close)

	srvSlow := dialRole(t, srv.URL, "key-sec", RoleServer, "srv-slow", "Slow Server")
	srvFast := dialRole(t, srv.URL, "key-sec", RoleServer, "srv-fast", "Fast Server")
	cli1 := dialRole(t, srv.URL, "key-sec", RoleClient, "cli-1", "Client 1")
	cli2 := dialRole(t, srv.URL, "key-sec", RoleClient, "cli-2", "Client 2")
	_ = readTextJSONFrame(t, cli1)
	_ = readTextJSONFrame(t, cli2)

	// 1. Source UUID spoofing rejected.
	spoof, _ := EncodeDataFrame(PayloadTypeBinary, "cli-2", "srv-fast", []byte("spoof"))
	if errWrite := cli1.WriteMessage(websocket.BinaryMessage, spoof); errWrite != nil {
		t.Fatalf("WriteMessage(spoof) error = %v", errWrite)
	}
	spoofErr := readTextJSONFrame(t, cli1)
	if spoofErr.Get("code").String() != ErrCodeSourceUUIDMismatch {
		t.Fatalf("expected source_uuid_mismatch, got %s", spoofErr.Raw)
	}

	// 2. Same-role routing (client->client and server->server) rejected.
	c2c, _ := EncodeDataFrame(PayloadTypeBinary, "cli-1", "cli-2", []byte("hi"))
	if errWrite := cli1.WriteMessage(websocket.BinaryMessage, c2c); errWrite != nil {
		t.Fatalf("WriteMessage(c2c) error = %v", errWrite)
	}
	if got := readTextJSONFrame(t, cli1).Get("code").String(); got != ErrCodeInvalidTargetRole {
		t.Fatalf("client->client code = %q, want %q", got, ErrCodeInvalidTargetRole)
	}

	s2s, _ := EncodeDataFrame(PayloadTypeBinary, "srv-fast", "srv-slow", []byte("hi"))
	if errWrite := srvFast.WriteMessage(websocket.BinaryMessage, s2s); errWrite != nil {
		t.Fatalf("WriteMessage(s2s) error = %v", errWrite)
	}
	if got := readTextJSONFrame(t, srvFast).Get("code").String(); got != ErrCodeInvalidTargetRole {
		t.Fatalf("server->server code = %q, want %q", got, ErrCodeInvalidTargetRole)
	}

	// 3. Slow peer backpressure: flood srvSlow (without reading from it) until target_buffer_full arrives.
	gotBufferFull := make(chan struct{})
	syncDone := make(chan error, 1)
	go func() {
		var once sync.Once
		for {
			frame := readTextJSONFrame(t, cli1)
			if frame.Get("type").String() == ControlTypeError && frame.Get("code").String() == ErrCodeTargetBufferFull {
				once.Do(func() { close(gotBufferFull) })
				continue
			}
			if frame.Get("type").String() == ControlTypeServerList && frame.Get("request_id").String() == "post-flood-barrier" {
				syncDone <- nil
				return
			}
		}
	}()

	largeChunk := bytes.Repeat([]byte("z"), 256*1024)
	floodDone := false
	for i := 0; i < 128 && !floodDone; i++ {
		pkt, _ := EncodeDataFrame(PayloadTypeBinary, "cli-1", "srv-slow", largeChunk)
		if errWrite := cli1.WriteMessage(websocket.BinaryMessage, pkt); errWrite != nil {
			t.Fatalf("WriteMessage(flood %d) error = %v", i, errWrite)
		}
		select {
		case <-gotBufferFull:
			floodDone = true
		default:
		}
	}
	select {
	case <-gotBufferFull:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for target_buffer_full from slow server")
	}

	// Barrier sync via list_servers to deterministically drain all in-flight error frames on cli1.
	if errWrite := cli1.WriteMessage(websocket.TextMessage, []byte(`{"type":"list_servers","request_id":"post-flood-barrier"}`)); errWrite != nil {
		t.Fatalf("WriteMessage(barrier) error = %v", errWrite)
	}
	if errSync := <-syncDone; errSync != nil {
		t.Fatalf("barrier sync error = %v", errSync)
	}

	// Verify srvFast still exchanges traffic with cli1 without being blocked by srvSlow.
	fastPkt, _ := EncodeDataFrame(PayloadTypeText, "cli-1", "srv-fast", []byte("fast-ok"))
	if errWrite := cli1.WriteMessage(websocket.BinaryMessage, fastPkt); errWrite != nil {
		t.Fatalf("WriteMessage(srvFast) error = %v", errWrite)
	}
	gotFast := readBinaryDataFrame(t, srvFast)
	if string(gotFast.Payload) != "fast-ok" {
		t.Fatalf("srvFast payload = %q, want fast-ok", string(gotFast.Payload))
	}
	_ = srvSlow
}

func TestHubExchange_ControllablePingAndKeyRevocation(t *testing.T) {
	pingTrig := make(chan struct{}, 1)
	hub := NewHub(Options{
		PingInterval: time.Hour,
		PingTrigger:  pingTrig,
	})
	hub.UpdateAllowedAPIKeys([]string{"key-revoke", "key-keep"})
	t.Cleanup(hub.Close)
	srv := httptest.NewServer(hub)
	t.Cleanup(srv.Close)

	cliRevoke := dialRole(t, srv.URL, "key-revoke", RoleClient, "c-rev", "Revoke Client")
	_ = readTextJSONFrame(t, cliRevoke)

	srvKeep := dialRole(t, srv.URL, "key-keep", RoleServer, "s-keep", "Keep Server")
	cliKeep := dialRole(t, srv.URL, "key-keep", RoleClient, "c-keep", "Keep Client")
	_ = readTextJSONFrame(t, cliKeep)

	pingReceived := make(chan struct{}, 4)
	cliKeep.SetPingHandler(func(appData string) error {
		select {
		case pingReceived <- struct{}{}:
		default:
		}
		return cliKeep.WriteControl(websocket.PongMessage, []byte(appData), time.Time{})
	})

	// Read loop in background on cliKeep to process control frames and receive data frames.
	keepReadCh := make(chan DataFrameView, 1)
	keepErrCh := make(chan error, 1)
	go func() {
		for {

			msgType, raw, errRead := readTunnelTestMessage(cliKeep)
			if errRead != nil {
				keepErrCh <- errRead
				return
			}
			if msgType == websocket.BinaryMessage {
				view, _, errP := ParseDataFrameHeader(raw)
				if errP != nil {
					keepErrCh <- errP
					return
				}
				keepReadCh <- view
				return
			}
		}
	}()

	// Deterministically trigger Ping without wall-clock sleep.
	pingTrig <- struct{}{}
	select {
	case <-pingReceived:
	case <-time.After(5 * time.Second):
		t.Fatal("did not receive triggered WebSocket Ping frame")
	}

	// Revoke key-revoke via UpdateAllowedAPIKeys while keeping key-keep.
	hub.UpdateAllowedAPIKeys([]string{"key-keep"})

	_, _, errRevoked := readTunnelTestMessage(cliRevoke)
	var closeErr *websocket.CloseError
	if !errors.As(errRevoked, &closeErr) || closeErr.Code != websocket.ClosePolicyViolation {
		t.Fatalf("expected revoked key connection to close with 1008, got %v", errRevoked)
	}

	// Also connect a server under key-revoke before another revocation check below, and verify key-keep bidirectional traffic.
	pkt, _ := EncodeDataFrame(PayloadTypeText, "s-keep", "c-keep", []byte("still-alive"))
	if errWrite := srvKeep.WriteMessage(websocket.BinaryMessage, pkt); errWrite != nil {
		t.Fatalf("srvKeep WriteMessage error = %v", errWrite)
	}
	select {
	case got := <-keepReadCh:
		if got.SrcUUID != "s-keep" || string(got.Payload) != "still-alive" {
			t.Fatalf("unexpected frame on surviving tenant: %+v", got)
		}
	case errRead := <-keepErrCh:
		t.Fatalf("surviving tenant read error = %v", errRead)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for packet on surviving tenant")
	}
}
