package wstunnel

import (
	"bytes"
	"fmt"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestHubReview_CloseAndRevokeDoNotBlockOnBlockedWriteWhenQueueNotFull(t *testing.T) {
	hub := NewHub(Options{
		SendQueueSize: 256,
	})
	hub.UpdateAllowedAPIKeys([]string{"key-revoke", "key-keep"})
	srv := httptest.NewServer(hub)
	t.Cleanup(srv.Close)

	srvStalled := dialRole(t, srv.URL, "key-revoke", RoleServer, "srv-stalled", "Stalled Server")
	cliRevoke := dialRole(t, srv.URL, "key-revoke", RoleClient, "cli-revoke", "Revoke Client")
	_ = readTextJSONFrame(t, cliRevoke)

	srvKeep := dialRole(t, srv.URL, "key-keep", RoleServer, "srv-keep", "Keep Server")
	cliKeep := dialRole(t, srv.URL, "key-keep", RoleClient, "cli-keep", "Keep Client")
	_ = readTextJSONFrame(t, cliKeep)

	// Send two 8 MiB frames to srvStalled without reading from srvStalled:
	// frame 1 blocks inside WriteMessage (filling OS socket buffers) while len(sendCh) == 1 << 256.
	huge := bytes.Repeat([]byte("x"), 8<<20)
	for i := 0; i < 2; i++ {
		pkt, errEnc := EncodeDataFrame(PayloadTypeBinary, "cli-revoke", "srv-stalled", huge)
		if errEnc != nil {
			t.Fatalf("EncodeDataFrame error = %v", errEnc)
		}
		if errWrite := cliRevoke.WriteMessage(websocket.BinaryMessage, pkt); errWrite != nil {
			t.Fatalf("cliRevoke WriteMessage(%d) error = %v", i, errWrite)
		}
	}
	// Barrier via list_servers on cliRevoke ensures cliRevoke's readLoop has processed both 8 MiB frames into srvStalled.
	if errWrite := cliRevoke.WriteMessage(websocket.TextMessage, []byte(`{"type":"list_servers","request_id":"enqueued-8mb"}`)); errWrite != nil {
		t.Fatalf("WriteMessage(list_servers) error = %v", errWrite)
	}
	barrier := readTextJSONFrame(t, cliRevoke)
	if barrier.Get("request_id").String() != "enqueued-8mb" {
		t.Fatalf("unexpected barrier frame: %s", barrier.Raw)
	}

	revokeDone := make(chan struct{})
	go func() {
		hub.UpdateAllowedAPIKeys([]string{"key-keep"})
		close(revokeDone)
	}()

	select {
	case <-revokeDone:
	case <-time.After(1500 * time.Millisecond):
		_ = srvStalled.Close()
		hub.Close()
		t.Fatal("UpdateAllowedAPIKeys deadlocked while peer WriteMessage was blocked with non-full sendCh")
	}

	// Surviving tenant must still exchange packets bidirectionally.
	c2s, _ := EncodeDataFrame(PayloadTypeText, "cli-keep", "srv-keep", []byte("ping-keep"))
	if errWrite := cliKeep.WriteMessage(websocket.BinaryMessage, c2s); errWrite != nil {
		t.Fatalf("cliKeep WriteMessage error = %v", errWrite)
	}
	gotC2S := readBinaryDataFrame(t, srvKeep)
	if string(gotC2S.Payload) != "ping-keep" {
		t.Fatalf("srvKeep got %q, want ping-keep", string(gotC2S.Payload))
	}

	s2c, _ := EncodeDataFrame(PayloadTypeText, "srv-keep", "cli-keep", []byte("pong-keep"))
	if errWrite := srvKeep.WriteMessage(websocket.BinaryMessage, s2c); errWrite != nil {
		t.Fatalf("srvKeep WriteMessage error = %v", errWrite)
	}
	gotS2C := readBinaryDataFrame(t, cliKeep)
	if string(gotS2C.Payload) != "pong-keep" {
		t.Fatalf("cliKeep got %q, want pong-keep", string(gotS2C.Payload))
	}

	closeDone := make(chan struct{})
	go func() {
		hub.Close()
		close(closeDone)
	}()
	select {
	case <-closeDone:
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("Hub.Close() blocked")
	}
}

func TestHubBudget_ReconnectRetainsInFlightTenantBudget(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		for _, drainWithPeers := range []bool{false, true} {
			t.Run(fmt.Sprintf("revoke=%t/drainWithPeers=%t", revoke, drainWithPeers), func(t *testing.T) {
				hub := NewHub(Options{
					MaxPeerQueuedBytes:   4096,
					MaxTenantQueuedBytes: 4096,
					PingInterval:         time.Hour,
				})
				forceClose := make(chan time.Time)
				hub.testForceClose = forceClose
				hub.UpdateAllowedAPIKeys([]string{"key"})
				srv := newGatedTunnelServer(t, hub)
				t.Cleanup(func() { close(forceClose) })
				oldConn := dialRole(t, srv.URL, "key", RoleClient, "client", "Old Client")
				_ = readTextJSONFrame(t, oldConn)
				old := lookupTunnelPeer(t, hub, "key", "client")
				tenant := old.tenant
				transport := old.conn.UnderlyingConn().(*deadlineGuardConn).Conn.(*gatedTunnelConn)
				transport.arm()
				if !old.tryEnqueueData(make([]byte, 3072)) {
					t.Fatal("could not enqueue blocked write")
				}
				waitTunnelSignal(t, transport.blocked, "in-flight write holding tenant budget")

				if revoke {
					hub.UpdateAllowedAPIKeys(nil)
					hub.UpdateAllowedAPIKeys([]string{"key"})
				} else {
					old.close(websocket.CloseNormalClosure, "", true)
				}
				assertRetained := func(wantPeers int) {
					t.Helper()
					hub.mu.RLock()
					defer hub.mu.RUnlock()
					if hub.realms["key"] != tenant || len(tenant.peers) != wantPeers || len(tenant.reserved) != 0 {
						t.Fatal("in-flight tenant realm was replaced or has unexpected membership")
					}
				}
				assertRetained(0)
				if remaining := tenant.queuedBytes.Load(); remaining != 3072 {
					t.Fatalf("disconnected tenant budget = %d, want 3072", remaining)
				}

				// A failed upgrade must not discard the retained in-flight budget either.
				reservation, statusReserve := hub.reserveUUID("key", "aborted")
				if statusReserve != 0 || reservation.realm != tenant {
					t.Fatalf("reconnect reservation did not reuse tenant: status=%d", statusReserve)
				}
				hub.releaseReservation("key", "aborted", reservation)
				assertRetained(0)

				client := dialRole(t, srv.URL, "key", RoleClient, "client", "Replacement Client")
				_ = readTextJSONFrame(t, client)
				server := dialRole(t, srv.URL, "key", RoleServer, "server", "Replacement Server")
				if online := readTextJSONFrame(t, client); online.Get("type").String() != ControlTypeServerOnline {
					t.Fatalf("unexpected topology event: %s", online.Raw)
				}
				currentClient := lookupTunnelPeer(t, hub, "key", "client")
				currentServer := lookupTunnelPeer(t, hub, "key", "server")
				if currentClient.tenant != tenant || currentServer.tenant != tenant {
					t.Fatal("reconnected peers received a fresh tenant byte budget")
				}
				raw, errEncode := EncodeDataFrame(PayloadTypeBinary, "client", "server", make([]byte, 2048))
				if errEncode != nil {
					t.Fatal(errEncode)
				}
				if errWrite := client.WriteMessage(websocket.BinaryMessage, raw); errWrite != nil {
					t.Fatal(errWrite)
				}
				if got := readTextJSONFrame(t, client); got.Get("code").String() != ErrCodeTargetBufferFull || got.Get("target_uuid").String() != "server" {
					t.Fatalf("retained in-flight budget did not apply backpressure: %s", got.Raw)
				}
				select {
				case <-old.writerDone:
					t.Fatal("blocked write exited before its gate was released")
				default:
				}

				if !drainWithPeers {
					currentClient.close(0, "", false)
					currentServer.close(0, "", false)
					waitTunnelSignal(t, currentClient.writerDone, "replacement client exit")
					waitTunnelSignal(t, currentServer.writerDone, "replacement server exit")
					if revoke {
						hub.UpdateAllowedAPIKeys(nil)
					}
					assertRetained(0)
				}

				// Release the write, not the grace reaper, to reclaim its byte reservation.
				close(transport.gate)
				waitTunnelSignal(t, old.writerDone, "old writer exit and budget release")
				if remaining := old.queuedBytes.Load(); remaining != 0 {
					t.Fatalf("old peer budget = %d, want 0", remaining)
				}
				if drainWithPeers {
					assertRetained(2)
					if errWrite := client.WriteMessage(websocket.BinaryMessage, raw); errWrite != nil {
						t.Fatal(errWrite)
					}
					if got := readBinaryDataFrame(t, server); len(got.Payload) != 2048 {
						t.Fatal("reconnected peers could not use the released tenant budget")
					}
					currentClient.close(0, "", false)
					currentServer.close(0, "", false)
					waitTunnelSignal(t, currentClient.writerDone, "replacement client exit")
					waitTunnelSignal(t, currentServer.writerDone, "replacement server exit")
				}
				hub.mu.RLock()
				_, retained := hub.realms["key"]
				hub.mu.RUnlock()
				if retained || tenant.queuedBytes.Load() != 0 {
					t.Fatal("drained idle or revoked tenant leaked its realm or byte budget")
				}
			})
		}
	}
}

func TestHubReview_ByteBudgetBackpressureAndReclaim(t *testing.T) {
	// High frame slot count (256), tight byte budget (32 KiB per peer, 64 KiB tenant-wide).
	hub := NewHub(Options{
		SendQueueSize:        256,
		MaxPeerQueuedBytes:   32 * 1024,
		MaxTenantQueuedBytes: 64 * 1024,
	})
	hub.UpdateAllowedAPIKeys([]string{"key-mem"})
	t.Cleanup(hub.Close)
	srv := httptest.NewServer(hub)
	t.Cleanup(srv.Close)

	srvSlow := dialRole(t, srv.URL, "key-mem", RoleServer, "srv-mem-slow", "Slow Mem Server")
	srvClean := dialRole(t, srv.URL, "key-mem", RoleServer, "srv-mem-clean", "Clean Mem Server")
	cli := dialRole(t, srv.URL, "key-mem", RoleClient, "cli-mem", "Mem Client")
	_ = readTextJSONFrame(t, cli)

	gotFull := make(chan struct{})
	barrierCh := make(chan struct{}, 1)
	go func() {
		var once sync.Once
		for {
			frame := readTextJSONFrame(t, cli)
			if frame.Get("type").String() == ControlTypeError && frame.Get("code").String() == ErrCodeTargetBufferFull {
				once.Do(func() { close(gotFull) })
				continue
			}
			if frame.Get("type").String() == ControlTypeServerList && frame.Get("request_id").String() == "mem-sync" {
				barrierCh <- struct{}{}
				return
			}
		}
	}()

	// Send 24 x 16 KiB = 384 KiB (only 24 frames << 256 slot limit, or enough to fill loopback + 32 KiB budget).
	// Wait: even 64 x 64 KiB = 4 MiB is only 64 frames (far below 256 slots!) so without byte-budget enforcement
	// 64 frames NEVER hit SendQueueSize=256!
	chunk := bytes.Repeat([]byte("m"), 64*1024)
	for i := 0; i < 64; i++ {
		pkt, _ := EncodeDataFrame(PayloadTypeBinary, "cli-mem", "srv-mem-slow", chunk)
		if errWrite := cli.WriteMessage(websocket.BinaryMessage, pkt); errWrite != nil {
			t.Fatalf("WriteMessage(%d) error = %v", i, errWrite)
		}
		select {
		case <-gotFull:
			i = 64
		default:
		}
	}

	select {
	case <-gotFull:
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("expected target_buffer_full from MaxPeerQueuedBytes before reaching 256 frame slots")
	}

	if errWrite := cli.WriteMessage(websocket.TextMessage, []byte(`{"type":"list_servers","request_id":"mem-sync"}`)); errWrite != nil {
		t.Fatalf("WriteMessage(mem-sync) error = %v", errWrite)
	}
	<-barrierCh

	// Close srvSlow so all its queued/in-flight bytes are reclaimed from the hub budget, then verify srvClean works.
	_ = srvSlow.Close()
	for {
		ev := readTextJSONFrame(t, cli)
		if ev.Get("type").String() == ControlTypeServerOffline && ev.Get("server.uuid").String() == "srv-mem-slow" {
			break
		}
	}

	okPkt, _ := EncodeDataFrame(PayloadTypeBinary, "cli-mem", "srv-mem-clean", bytes.Repeat([]byte("k"), 16*1024))
	if errWrite := cli.WriteMessage(websocket.BinaryMessage, okPkt); errWrite != nil {
		t.Fatalf("WriteMessage(srvClean) error = %v", errWrite)
	}
	gotClean := readBinaryDataFrame(t, srvClean)
	if len(gotClean.Payload) != 16*1024 {
		t.Fatalf("srvClean payload len = %d, want %d", len(gotClean.Payload), 16*1024)
	}
	hub.mu.RLock()
	tenant := hub.realms["key-mem"]
	peers := make([]*peer, 0, len(tenant.peers))
	for _, p := range tenant.peers {
		peers = append(peers, p)
	}
	hub.mu.RUnlock()
	hub.Close()
	for _, p := range peers {
		waitTunnelSignal(t, p.writerDone, "writer exit and budget reclamation")
	}
	if rem := tenant.queuedBytes.Load(); rem != 0 {
		t.Fatalf("tenant.queuedBytes after Close = %d, want 0", rem)
	}
}

func TestHubReview_ListServersLinearizableWithServerOffline(t *testing.T) {
	unregistered := make(chan string, 64)
	hub := NewHub(Options{})
	hub.testOnPeerUnregistered = func(_, uuid string) {
		unregistered <- uuid
	}
	hub.UpdateAllowedAPIKeys([]string{"key-lin"})
	t.Cleanup(hub.Close)
	srv := httptest.NewServer(hub)
	t.Cleanup(srv.Close)

	cli := dialRole(t, srv.URL, "key-lin", RoleClient, "cli-lin", "Linearizable Client")
	initFrame := readTextJSONFrame(t, cli)
	if initFrame.Get("servers.#").Int() != 0 {
		t.Fatalf("expected initial empty server_list, got %s", initFrame.Raw)
	}

	localServers := make(map[string]ServerInfo)
	doneReading := make(chan struct{})
	go func() {
		defer close(doneReading)
		for {
			frame := readTextJSONFrame(t, cli)
			switch frame.Get("type").String() {
			case ControlTypeServerList:
				if frame.Get("request_id").String() == "final-drain" {
					return
				}
				clear(localServers)
				count := int(frame.Get("servers.#").Int())
				for i := 0; i < count; i++ {
					id := frame.Get(fmt.Sprintf("servers.%d.uuid", i)).String()
					name := frame.Get(fmt.Sprintf("servers.%d.name", i)).String()
					srvType := frame.Get(fmt.Sprintf("servers.%d.type", i)).String()
					localServers[id] = ServerInfo{UUID: id, Name: name, Type: srvType}
				}
			case ControlTypeServerOnline:
				id := frame.Get("server.uuid").String()
				localServers[id] = ServerInfo{
					UUID: id,
					Name: frame.Get("server.name").String(),
					Type: frame.Get("server.type").String(),
				}
			case ControlTypeServerOffline:
				delete(localServers, frame.Get("server.uuid").String())
			}
		}
	}()

	var writeMu sync.Mutex
	sendListReq := func(reqID string) {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = cli.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":"list_servers","request_id":%q}`, reqID)))
	}

	sConn := dialRole(t, srv.URL, "key-lin", RoleServer, "ephemeral-srv-0", "Ephemeral")
	hookEntered := make(chan struct{})
	var hookOnce sync.Once
	hub.testBeforeListServersEnqueue = func() {
		hookOnce.Do(func() {
			close(hookEntered)
			// Trigger server disconnect concurrently while list_servers has already captured its snapshot.
			_ = sConn.Close()
			// Give unregisterPeer a non-blocking yield window if Hub unlocked too early; if Hub holds RLock,
			// unregisterPeer will block on Lock() until server_list is enqueued first.
			for i := 0; i < 4000; i++ {
				runtime.Gosched()
				select {
				case id := <-unregistered:
					unregistered <- id
					return
				default:
				}
			}
		})
	}
	sendListReq("race-snapshot")
	<-hookEntered
	<-unregistered

	// Send one non-list_servers marker? Wait: to test whether prior poll responses left a ghost server WITHOUT
	// overwriting localServers on final-drain, only exit on final-drain BEFORE clearing localServers!
	sendListReq("final-drain")
	<-doneReading

	if len(localServers) != 0 {
		t.Fatalf("stale server_list overwrote newer server_offline; ghost servers remaining: %+v", localServers)
	}
}
