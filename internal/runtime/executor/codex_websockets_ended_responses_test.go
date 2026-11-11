package executor

import (
	"fmt"
	"testing"

	"github.com/gorilla/websocket"
)

func TestCodexWebsocketSessionEndedResponsesAreBoundedAndConnScoped(t *testing.T) {
	conn := &websocket.Conn{}
	sess := &codexWebsocketSession{
		responseStateConn:   conn,
		terminalResponseIDs: map[string]struct{}{"done": {}},
		activeResponseID:    "running",
	}
	sess.activeMu.Lock()
	sess.rememberEndedResponsesLocked(&websocket.Conn{}) // another socket: ignored
	if sess.responseEndedLocked("running") {
		t.Fatal("ids of a different connection must not be recorded")
	}
	sess.rememberEndedResponsesLocked(conn)
	if !sess.responseEndedLocked("running") {
		t.Fatal("the in-flight response should be remembered after loss")
	}
	if sess.responseEndedLocked("done") {
		t.Fatal("already-terminal responses keep their existing contract")
	}
	if sess.responseEndedLocked("never-existed") || sess.responseEndedLocked("") {
		t.Fatal("unknown or empty ids must not match")
	}
	for i := 0; i < 3*maxEndedResponseIDs; i++ {
		sess.activeResponseID = fmt.Sprintf("r%d", i)
		sess.rememberEndedResponsesLocked(conn)
	}
	if len(sess.endedResponseIDs) > maxEndedResponseIDs {
		t.Fatalf("len=%d, want <= %d", len(sess.endedResponseIDs), maxEndedResponseIDs)
	}
	if !sess.responseEndedLocked(fmt.Sprintf("r%d", 3*maxEndedResponseIDs-1)) || sess.responseEndedLocked("r0") {
		t.Fatal("newest id must be kept and oldest dropped")
	}
	sess.activeMu.Unlock()
}
