package executor

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
)

// UpstreamWebsocketReplayRequiredError indicates that an incremental request
// cannot safely continue because its upstream websocket is no longer reusable.
type UpstreamWebsocketReplayRequiredError struct{}

func (*UpstreamWebsocketReplayRequiredError) Error() string {
	return `{"error":{"message":"upstream transport requires full HTTP replay","type":"server_error","code":"upstream_http_replay_required","status":426}}`
}

func (*UpstreamWebsocketReplayRequiredError) StatusCode() int { return http.StatusUpgradeRequired }

func (*UpstreamWebsocketReplayRequiredError) IsRequestScoped() bool { return true }

// NewUpstreamWebsocketReplayRequiredError creates a request-scoped replay signal.
func NewUpstreamWebsocketReplayRequiredError() error {
	return &UpstreamWebsocketReplayRequiredError{}
}

// IsUpstreamWebsocketReplayRequired reports whether err is the internal replay signal.
func IsUpstreamWebsocketReplayRequired(err error) bool {
	var replayErr *UpstreamWebsocketReplayRequiredError
	return errors.As(err, &replayErr)
}

// UpstreamWebsocketTurnError ends one downstream websocket turn after the upstream
// websocket was lost. The downstream socket stays open and the turn is never replayed.
// Cause is kept for logging and is never sent to the client.
type UpstreamWebsocketTurnError struct {
	Cause error
	// FullContextRequired marks a continuation (previous_response_id, append) that refers to
	// the lost socket: nothing was sent, the client resends the full input.
	FullContextRequired bool
	// NotSent marks an operation that was never written upstream.
	NotSent bool
	EventID string
	// Idle marks a loss with no running turn: only Abandoned is reported, if anything.
	Idle bool
	// Abandoned lists the other operations that lose their outcome with this turn.
	Abandoned []*UpstreamWebsocketTurnError
}

func (e *UpstreamWebsocketTurnError) Error() string {
	if e.FullContextRequired {
		return `{"error":{"message":"Previous response is not available on this websocket; resend the full conversation input without previous_response_id","type":"invalid_request_error","code":"previous_response_not_found","param":"previous_response_id"}}`
	}
	if e.NotSent {
		return `{"error":{"message":"Operation was not sent because the upstream websocket was lost. Send a new response.create with full conversation input.","type":"invalid_request_error","code":"upstream_websocket_not_sent"}}`
	}
	return `{"error":{"message":"Upstream websocket disconnected. The turn was not replayed and its outcome may be unknown. Send a new response.create with full conversation input without previous_response_id.","type":"server_error","code":"upstream_websocket_lost"}}`
}

func (e *UpstreamWebsocketTurnError) Unwrap() error { return e.Cause }
func (e *UpstreamWebsocketTurnError) StatusCode() int {
	if e.FullContextRequired {
		return http.StatusConflict
	}
	return http.StatusBadGateway
}
func (*UpstreamWebsocketTurnError) IsRequestScoped() bool { return true }

// IsUpstreamWebsocketTurnError reports whether err is a recoverable upstream websocket loss.
func IsUpstreamWebsocketTurnError(err error) bool {
	var turnErr *UpstreamWebsocketTurnError
	return errors.As(err, &turnErr)
}

type websocketInputLossState struct {
	epoch        *atomic.Uint64
	messageEpoch uint64
}

type websocketInputLossContextKey struct{}

// WithWebsocketInputEpoch attaches the session's loss epoch counter and the current
// message's read epoch so executors can advance the epoch on upstream loss and reject
// messages that were read before an upstream loss.
func WithWebsocketInputEpoch(ctx context.Context, epoch *atomic.Uint64, messageEpoch uint64) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, websocketInputLossContextKey{}, websocketInputLossState{
		epoch:        epoch,
		messageEpoch: messageEpoch,
	})
}

// NotifyWebsocketInputLoss advances the session's loss epoch if present.
func NotifyWebsocketInputLoss(ctx context.Context) {
	if ctx == nil {
		return
	}
	if state, ok := ctx.Value(websocketInputLossContextKey{}).(websocketInputLossState); ok && state.epoch != nil {
		state.epoch.Add(1)
	}
}

// WebsocketInputPredatesLoss reports whether the current turn's input was read before
// an upstream websocket loss.
func WebsocketInputPredatesLoss(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	state, ok := ctx.Value(websocketInputLossContextKey{}).(websocketInputLossState)
	return ok && state.epoch != nil && state.messageEpoch < state.epoch.Load()
}

// WebsocketInputLossError returns the turn error for a downstream websocket
// frame whose input epoch predates the latest upstream connection loss.
func WebsocketInputLossError(ctx context.Context) error {
	if !WebsocketInputPredatesLoss(ctx) {
		return nil
	}
	if RequiredUpstreamWebsocket(ctx) {
		return &UpstreamWebsocketTurnError{FullContextRequired: true}
	}
	return &UpstreamWebsocketTurnError{NotSent: true}
}
