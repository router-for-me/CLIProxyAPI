package executor

import (
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type countingWebsocketLifecycle struct {
	binds atomic.Int32
}

func (l *countingWebsocketLifecycle) Bind(func() error) error {
	l.binds.Add(1)
	return nil
}

func (*countingWebsocketLifecycle) End(string) {}

func TestCodexWebsocketSessionBindsSameLifecycleAndConnectionOnce(t *testing.T) {
	conn := &websocket.Conn{}
	closer := newWebsocketConnectionCloser(conn)
	sess := &codexWebsocketSession{conn: conn, connCloser: closer}
	lifecycle := &countingWebsocketLifecycle{}
	opts := cliproxyexecutor.Options{ExecutionLifecycle: lifecycle}

	if errBind := sess.bindExecutionLifecycle(opts, conn, closer, "gpt-5-codex"); errBind != nil {
		t.Fatalf("first bindExecutionLifecycle() error = %v", errBind)
	}
	if errBind := sess.bindExecutionLifecycle(opts, conn, closer, "gpt-5-codex"); errBind != nil {
		t.Fatalf("second bindExecutionLifecycle() error = %v", errBind)
	}
	if got := lifecycle.binds.Load(); got != 1 {
		t.Fatalf("lifecycle Bind calls = %d, want 1 for the same lifecycle and connection", got)
	}
}
