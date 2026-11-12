package executor

import (
	"context"
	"errors"
)

// ErrNoActiveUpstreamWebsocket means response.interrupt has no live upstream socket.
var ErrNoActiveUpstreamWebsocket = errors.New("no active upstream websocket for response.interrupt")

// WebsocketInput is a frame from the single downstream reader. Err terminates
// the connection; Payload is owned by the receiver and must not be replayed.
type WebsocketInput struct {
	Payload []byte
	Err     error
}
type websocketInputKey struct{}

func WithWebsocketInput(ctx context.Context, input <-chan WebsocketInput) context.Context {
	return context.WithValue(ctx, websocketInputKey{}, input)
}
func WebsocketInputFromContext(ctx context.Context) <-chan WebsocketInput {
	if ctx == nil {
		return nil
	}
	input, _ := ctx.Value(websocketInputKey{}).(<-chan WebsocketInput)
	return input
}

// WithWebsocketAuthCheck supplies the live account-state check for a bound
// connection. It may reject further frames but never select another account.
type websocketAuthCheckKey struct{}

func WithWebsocketAuthCheck(ctx context.Context, check func(string) bool) context.Context {
	return context.WithValue(ctx, websocketAuthCheckKey{}, check)
}
func WebsocketAuthEnabled(ctx context.Context, authID string) bool {
	if ctx == nil {
		return true
	}
	check, _ := ctx.Value(websocketAuthCheckKey{}).(func(string) bool)
	return check == nil || check(authID)
}

type websocketTurnAuthorizerKey struct{}

// WithWebsocketTurnAuthorizer supplies a per-turn frontend access check for
// follow-up response.create and response.append frames on a duplex socket.
// Returning a non-empty error payload rejects the turn while keeping the socket open.
func WithWebsocketTurnAuthorizer(ctx context.Context, authorize func([]byte) ([]byte, error)) context.Context {
	return context.WithValue(ctx, websocketTurnAuthorizerKey{}, authorize)
}

func AuthorizeWebsocketTurn(ctx context.Context, payload []byte) ([]byte, error) {
	if ctx == nil {
		return nil, nil
	}
	authorize, _ := ctx.Value(websocketTurnAuthorizerKey{}).(func([]byte) ([]byte, error))
	if authorize == nil {
		return nil, nil
	}
	return authorize(payload)
}
