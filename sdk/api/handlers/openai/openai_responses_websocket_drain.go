package openai

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/maintenance"
)

// responsesWebsocketDrain ties one Responses WebSocket to the proxy's drain
// for supervised updates. ResponsesWebsocket calls it at a few fixed points
// and only from its own goroutine.
type responsesWebsocketDrain struct {
	session      *maintenance.Session
	writer       *responsesWebsocketWriter
	pendingTools bool
}

func startResponsesWebsocketDrain(ctx context.Context, writer *responsesWebsocketWriter) *responsesWebsocketDrain {
	return &responsesWebsocketDrain{session: maintenance.ManageWebsocket(ctx), writer: writer}
}

// Close releases the socket from the drain. The handler defers it before
// socket cleanup so restart readiness follows upstream cleanup.
func (d *responsesWebsocketDrain) Close() {
	d.session.Close()
}

// Waiting closes when the drain wants this idle socket closed. A socket whose
// last turn left tool results pending waits for them.
func (d *responsesWebsocketDrain) Waiting() <-chan struct{} {
	return d.session.Waiting(d.pendingTools)
}

// CloseSocket sends the replay-required close, so the client reconnects and
// resends the conversation.
func (d *responsesWebsocketDrain) CloseSocket() {
	_, _ = d.writer.closeForUpstreamError(responsesWebsocketHTTPReplayRequiredError())
}

// BeginTurn admits a new turn. While draining it closes the socket instead
// and returns false.
func (d *responsesWebsocketDrain) BeginTurn() bool {
	if d.session.BeginTurn() {
		return true
	}
	d.CloseSocket()
	return false
}

// SetPendingTools records whether the last committed turn left tool results pending.
func (d *responsesWebsocketDrain) SetPendingTools(pending bool) {
	d.pendingTools = pending
}
