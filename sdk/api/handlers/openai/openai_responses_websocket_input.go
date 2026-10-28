package openai

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

// responsesLocalInterrupt cancels an in-flight HTTP turn. A websocket upstream
// does not use it; that interrupt is written to the existing socket instead.
// A claimed frame is never silently dropped: the forward loop consumes it, or
// deliver returns false so the reader answers the interrupt itself.
//
// wsTurnActive is the socket-scoped ownership gate: while the current turn
// runs on an upstream websocket, no frame may claim the local path at all,
// not even in the window after that socket was lost. "No live websocket
// channel right now" must never be read as "this turn is HTTP".
type responsesLocalInterrupt struct {
	mu           sync.Mutex
	active       bool
	wsTurnActive bool
	frames       chan []byte
}

func newResponsesLocalInterrupt() *responsesLocalInterrupt {
	return &responsesLocalInterrupt{frames: make(chan []byte, 1)}
}

// begin activates claims for a turn and records its ownership in the same
// critical section: a websocket-owned turn must never expose an instant in
// which claims are active but ownership is not yet visible to deliver.
func (s *responsesLocalInterrupt) begin(websocketOwned bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.active = true
	s.wsTurnActive = websocketOwned
	s.mu.Unlock()
}

// reactivate reopens claims for the next response on the same turn (duplex),
// preserving the ownership recorded by begin.
func (s *responsesLocalInterrupt) reactivate() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.active = true
	s.mu.Unlock()
}

func (s *responsesLocalInterrupt) endWebsocketTurn() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.wsTurnActive = false
	s.mu.Unlock()
}

// stopClaims stops accepting new frames without draining the queue: a frame
// already claimed by deliver stays consumable so the forward loop answers it
// instead of silently dropping it when the turn ends.
func (s *responsesLocalInterrupt) stopClaims() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.active = false
	s.mu.Unlock()
}

// end stops new claims at turn teardown; claimed frames stay queued for
// takeClaimed so they can still be answered.
func (s *responsesLocalInterrupt) end() {
	s.stopClaims()
}

// deactivate stops new claims once the turn completed.
func (s *responsesLocalInterrupt) deactivate() {
	s.stopClaims()
}

// isActive reports whether new frames may still be claimed for the turn.
func (s *responsesLocalInterrupt) isActive() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// deliver claims a frame only if it is actually retained: a full queue
// rejects so the reader answers the frame itself instead of the client
// waiting for an answer that only ever addresses the first claim.
func (s *responsesLocalInterrupt) deliver(payload []byte) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active || s.wsTurnActive {
		return false
	}
	select {
	case s.frames <- bytes.Clone(payload):
		return true
	default:
		return false
	}
}

func (s *responsesLocalInterrupt) framesChan() <-chan []byte {
	if s == nil {
		return nil
	}
	return s.frames
}

// takeClaimed consumes a queued claimed frame without blocking. The caller
// becomes responsible for answering it exactly once.
func (s *responsesLocalInterrupt) takeClaimed() []byte {
	if s == nil {
		return nil
	}
	select {
	case frame := <-s.frames:
		return frame
	default:
		return nil
	}
}

// readResponsesWebsocketInput is the only downstream reader. Interrupts are
// handled immediately so they are not queued behind an active response.
// The bounded queue backpressures clients instead of retaining unlimited input.
func readResponsesWebsocketInput(ctx context.Context, cancel context.CancelCauseFunc, conn *websocket.Conn, interrupt func([]byte) error, local *responsesLocalInterrupt, writer *responsesWebsocketWriter, timeline websocketTimelineAppender, epoch *atomic.Uint64) (<-chan cliproxyexecutor.WebsocketInput, <-chan struct{}) {
	input := make(chan cliproxyexecutor.WebsocketInput, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(input)
		for {
			kind, payload, errRead := conn.ReadMessage()
			if errRead != nil {
				cancel(errRead)
				return
			}
			msgEpoch := epoch.Load()
			if kind != websocket.TextMessage && kind != websocket.BinaryMessage {
				continue
			}
			// Control frames bypass response.create normalization. The original
			// response_id, mode, and extension fields must reach upstream unchanged.
			if json.Valid(payload) && gjson.GetBytes(payload, "type").String() == "response.interrupt" {
				appendResponsesInterruptDiagnostic(timeline, payload, "")
				errInterrupt := interrupt(payload)
				switch {
				case errInterrupt == nil:
					appendResponsesInterruptDiagnostic(timeline, payload, "handled")
					continue
				case errors.Is(errInterrupt, cliproxyexecutor.ErrNoActiveUpstreamWebsocket) && local.deliver(payload):
					appendResponsesInterruptDiagnostic(timeline, payload, "local_http")
					continue
				default:
					appendResponsesInterruptDiagnostic(timeline, payload, "rejected")
					// The sanitized outcome replaces raw error logging here: errors
					// from transport dependencies may contain credentials or URLs.
					_, errWrite := writeResponsesWebsocketError(writer, nil, &interfaces.ErrorMessage{
						StatusCode: http.StatusBadRequest,
						Error:      errInterrupt,
					})
					if errWrite != nil {
						cancel(errWrite)
						return
					}
					continue
				}
			}
			message := cliproxyexecutor.WebsocketInput{Payload: payload, Epoch: msgEpoch}
			select {
			case input <- message:
			case <-ctx.Done():
				return
			}
		}
	}()
	return input, done
}

func appendResponsesInterruptDiagnostic(timeline websocketTimelineAppender, payload []byte, outcome string) {
	if timeline == nil {
		return
	}
	// Hash client-controlled IDs and omit all arbitrary fields, including mode.
	// Only correlation metadata is needed to diagnose control-frame timing.
	idHash := sha256.Sum256([]byte(gjson.GetBytes(payload, "response_id").String()))
	diagnostic := struct {
		Type           string `json:"type"`
		ResponseIDHash string `json:"response_id_hash"`
		Outcome        string `json:"outcome,omitempty"`
	}{Type: "response.interrupt", ResponseIDHash: fmt.Sprintf("%x", idHash[:8]), Outcome: outcome}
	data, errMarshal := json.Marshal(diagnostic)
	if errMarshal != nil {
		return
	}
	event := "interrupt"
	if outcome != "" {
		event = "interrupt.outcome"
	}
	timeline.Append(event, data, time.Now())
}

func (h *OpenAIResponsesAPIHandler) forwardResponsesWebsocketInterrupt(ctx context.Context, sessionID string, payload []byte) error {
	responseID := gjson.GetBytes(payload, "response_id")
	if responseID.Type != gjson.String || strings.TrimSpace(responseID.String()) == "" {
		return fmt.Errorf("response.interrupt requires response_id")
	}
	if h == nil || h.AuthManager == nil {
		return cliproxyexecutor.ErrNoActiveUpstreamWebsocket
	}
	exec, ok := h.AuthManager.Executor("codex")
	if !ok || exec == nil {
		return cliproxyexecutor.ErrNoActiveUpstreamWebsocket
	}
	sender, ok := exec.(interface {
		InterruptExecutionSession(context.Context, string, []byte) error
	})
	if !ok || sender == nil {
		return fmt.Errorf("upstream executor does not support response.interrupt")
	}
	ctx = cliproxyexecutor.WithWebsocketAuthCheck(ctx, func(authID string) bool {
		// Home runtime credentials live on the execution session, not in the global map.
		auth, found := h.AuthManager.GetByID(authID)
		if !found || auth == nil {
			auth, found = h.AuthManager.GetExecutionSessionAuthByID(sessionID, authID)
		}
		return found && auth != nil && !auth.Disabled && auth.Status != coreauth.StatusDisabled
	})
	return sender.InterruptExecutionSession(ctx, sessionID, payload)
}
