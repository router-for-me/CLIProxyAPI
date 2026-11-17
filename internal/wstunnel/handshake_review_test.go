package wstunnel

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"
)

func TestHubAuth_StaleReservationReleasePreservesReplacement(t *testing.T) {
	hub := NewHub(Options{})
	t.Cleanup(hub.Close)
	hub.UpdateAllowedAPIKeys([]string{"key"})
	stale, status := hub.reserveUUID("key", "device")
	if status != 0 {
		t.Fatalf("initial reservation status = %d", status)
	}
	hub.UpdateAllowedAPIKeys(nil)
	if len(stale.realm.reserved) != 0 || len(hub.realms) != 0 {
		t.Fatal("revocation retained pending reservations")
	}
	hub.UpdateAllowedAPIKeys([]string{"key"})
	replacement, replacementStatus := hub.reserveUUID("key", "device")
	if replacementStatus != 0 {
		t.Fatalf("replacement reservation status = %d, want success", replacementStatus)
	}
	hub.releaseReservation("key", "device", stale)
	if hub.realms["key"].reserved["device"] != replacement {
		t.Fatal("stale release deleted the replacement reservation")
	}
	if _, duplicateStatus := hub.reserveUUID("key", "device"); duplicateStatus != http.StatusConflict {
		t.Fatalf("duplicate reservation status = %d, want 409", duplicateStatus)
	}
	hub.releaseReservation("key", "device", replacement)
	if len(hub.realms) != 0 {
		t.Fatal("matching release did not reclaim the empty realm")
	}
}

func TestHubAuth_PreRevocationHandshakeCannotCommitAfterKeyIsReadded(t *testing.T) {
	hub := NewHub(Options{})
	hub.UpdateAllowedAPIKeys([]string{"key"})
	reserved := make(chan struct{})
	resume := make(chan struct{})
	var reservations atomic.Int32
	hub.testOnAfterReserve = func() {
		if reservations.Add(1) == 1 {
			close(reserved)
			<-resume
		}
	}
	srv := httptest.NewServer(hub)
	t.Cleanup(srv.Close)
	t.Cleanup(hub.Close)
	var resumeOnce sync.Once
	resumeHandshake := func() { resumeOnce.Do(func() { close(resume) }) }
	staleDone := make(chan struct{})
	var stale *websocket.Conn
	var errDial error
	go func() {
		var resp *http.Response
		stale, resp, errDial = websocket.DefaultDialer.Dial(dialTunnelURL(srv.URL, url.Values{
			"key": {"key"}, "role": {"client"}, "uuid": {"device"}, "name": {"Stale"},
		}), nil)
		closeHTTPResponseBody(t, resp)
		close(staleDone)
	}()
	t.Cleanup(func() {
		resumeHandshake()
		waitTunnelSignal(t, staleDone, "stale handshake completion")
		if stale != nil {
			if errClose := stale.Close(); errClose != nil {
				t.Logf("stale connection close error: %v", errClose)
			}
		}
	})
	waitTunnelSignal(t, reserved, "pre-revocation reservation")
	hub.UpdateAllowedAPIKeys(nil)
	hub.UpdateAllowedAPIKeys([]string{"key"})

	// The new connection must succeed while the stale handshake is still paused.
	current := dialRole(t, srv.URL, "key", RoleClient, "device", "Current")
	_ = readTextJSONFrame(t, current)
	currentPeer := lookupTunnelPeer(t, hub, "key", "device")
	resumeHandshake()
	waitTunnelSignal(t, staleDone, "stale WebSocket upgrade")
	if errDial != nil {
		t.Fatalf("stale upgrade failed before commit: %v", errDial)
	}
	assertTunnelClose(t, stale, websocket.ClosePolicyViolation)
	if lookupTunnelPeer(t, hub, "key", "device") != currentPeer {
		t.Fatal("stale commit clobbered the replacement peer")
	}
	if errWrite := current.WriteMessage(websocket.TextMessage, []byte(`{"type":"list_servers","request_id":"still-current"}`)); errWrite != nil {
		t.Fatal(errWrite)
	}
	if got := readTextJSONFrame(t, current); got.Get("request_id").String() != "still-current" {
		t.Fatalf("replacement connection stopped working: %s", got.Raw)
	}
}
