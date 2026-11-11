package openai

import (
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
)

// interruptAfterLossScript drives a fake upstream: connection 1 announces
// response r1 and then dies (EOF) with the outcome unknown; later connections
// complete their single create.
func interruptAfterLossScript(index int, c *websocket.Conn, read func() []byte) {
	read()
	if index == 1 {
		_ = c.WriteMessage(websocket.TextMessage, []byte(lossCreated))
		return
	}
	_ = c.WriteMessage(websocket.TextMessage, []byte(lossCompleted))
	read()
}

// A client that interrupts r1 after r1's upstream socket was lost is asking
// for something that already happened. Answering it with a 400 error frame
// ("no active upstream websocket for response.interrupt") is fatal for
// clients that treat any error frame as a failed run, although the loss was
// already reported once and the connection is healthy. The interrupt must be
// absorbed, and the next response.create must be forwarded exactly once and
// complete without an error frame in between.
func TestResponsesInterruptAfterUpstreamLossIsAbsorbed(t *testing.T) {
	f := newLossFixture(t, false, interruptAfterLossScript)
	f.create("")
	lost := f.nextError()
	if got := ojson.GetBytesNoCopy(lost, "error.code").String(); got != "upstream_websocket_lost" {
		t.Fatalf("loss error = %s", lost)
	}

	f.send(`{"type":"response.interrupt","response_id":"r1"}`)
	f.create("")

	for {
		p := f.next()
		switch ojson.GetBytesNoCopy(p, "type").String() {
		case "response.completed":
			if f.creates.Load() != 2 || f.connections.Load() != 2 {
				t.Fatalf("creates=%d connections=%d, want 2 and 2 (each create forwarded once)", f.creates.Load(), f.connections.Load())
			}
			return
		case "error":
			if strings.Contains(string(p), "no active upstream websocket") {
				t.Fatalf("interrupt for an already-lost response surfaced as an error frame: %s", p)
			}
			t.Fatalf("unexpected error frame: %s", p)
		}
	}
}

// A genuine interrupt for a response that never existed keeps its semantics:
// it is rejected, and the connection stays usable for the next create.
func TestResponsesInterruptUnknownResponseAfterLossStillRejected(t *testing.T) {
	f := newLossFixture(t, false, interruptAfterLossScript)
	f.create("")
	_ = f.nextError()

	f.send(`{"type":"response.interrupt","response_id":"never-existed"}`)
	rejected := f.nextError()
	if !strings.Contains(string(rejected), "no active upstream websocket") {
		t.Fatalf("unknown interrupt was not rejected as before: %s", rejected)
	}
	f.create("")
	f.awaitCompleted()
	if f.creates.Load() != 2 {
		t.Fatalf("creates=%d, want 2", f.creates.Load())
	}
}
