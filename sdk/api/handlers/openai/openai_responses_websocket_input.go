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
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

const responsesLocalTerminalResponseLimit = 128

type responsesLocalInterruptResult uint8

const (
	responsesLocalInterruptRejected responsesLocalInterruptResult = iota
	responsesLocalInterruptQueued
	responsesLocalInterruptAlreadyTerminal
)

// responsesLocalInterrupt cancels an in-flight HTTP turn. A websocket upstream
// does not use it; that interrupt is written to the existing socket instead.
type responsesLocalInterrupt struct {
	mu            sync.Mutex
	active        bool
	responseID    string
	pending       []byte
	terminalIDs   map[string]struct{}
	terminalOrder []string
	frames        chan struct{}
}

func newResponsesLocalInterrupt() *responsesLocalInterrupt {
	return &responsesLocalInterrupt{
		terminalIDs: make(map[string]struct{}),
		frames:      make(chan struct{}, 1),
	}
}

func (s *responsesLocalInterrupt) begin() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.active = true
	s.responseID = ""
	s.pending = nil
	s.drainFrameSignalLocked()
	s.mu.Unlock()
}

func (s *responsesLocalInterrupt) end() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.active = false
	s.responseID = ""
	s.pending = nil
	s.drainFrameSignalLocked()
	s.mu.Unlock()
}

func (s *responsesLocalInterrupt) observeResponseCreated(responseID string) {
	if s == nil || responseID == "" {
		return
	}
	s.mu.Lock()
	if s.active {
		s.responseID = responseID
		s.pending = nil
		s.drainFrameSignalLocked()
	}
	s.mu.Unlock()
}

func (s *responsesLocalInterrupt) observeResponseTerminal(responseID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if responseID == "" && s.active {
		responseID = s.responseID
	}
	if responseID == "" {
		s.mu.Unlock()
		return
	}
	if _, exists := s.terminalIDs[responseID]; !exists {
		if len(s.terminalOrder) == responsesLocalTerminalResponseLimit {
			delete(s.terminalIDs, s.terminalOrder[0])
			s.terminalOrder = s.terminalOrder[1:]
		}
		s.terminalIDs[responseID] = struct{}{}
		s.terminalOrder = append(s.terminalOrder, responseID)
	}
	if s.active && s.responseID == responseID {
		s.responseID = ""
		s.pending = nil
		s.drainFrameSignalLocked()
	}
	s.mu.Unlock()
}

func (s *responsesLocalInterrupt) deliver(payload []byte) responsesLocalInterruptResult {
	if s == nil {
		return responsesLocalInterruptRejected
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	responseID := gjson.GetBytes(payload, "response_id").String()
	if _, terminal := s.terminalIDs[responseID]; terminal {
		return responsesLocalInterruptAlreadyTerminal
	}
	if !s.active {
		return responsesLocalInterruptRejected
	}
	if s.responseID == "" || responseID != s.responseID {
		return responsesLocalInterruptRejected
	}
	if s.pending == nil {
		s.pending = bytes.Clone(payload)
		select {
		case s.frames <- struct{}{}:
		default:
		}
	}
	return responsesLocalInterruptQueued
}

func (s *responsesLocalInterrupt) take() []byte {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active || s.pending == nil || s.responseID == "" || gjson.GetBytes(s.pending, "response_id").String() != s.responseID {
		s.pending = nil
		return nil
	}
	payload := s.pending
	s.pending = nil
	return payload
}

func (s *responsesLocalInterrupt) drainFrameSignalLocked() {
	select {
	case <-s.frames:
	default:
	}
}

func (s *responsesLocalInterrupt) framesChan() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.frames
}

// readResponsesWebsocketInput is the only downstream reader. Interrupts are
// handled immediately so they are not queued behind an active response.
// The bounded queue backpressures clients instead of retaining unlimited input.
func readResponsesWebsocketInput(ctx context.Context, cancel context.CancelCauseFunc, conn *websocket.Conn, interrupt func([]byte) error, local *responsesLocalInterrupt, writer *responsesWebsocketWriter, timeline websocketTimelineAppender) (<-chan cliproxyexecutor.WebsocketInput, <-chan struct{}) {
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
			if kind != websocket.TextMessage && kind != websocket.BinaryMessage {
				continue
			}
			// Control frames bypass response.create normalization. The original
			// response_id, mode, and extension fields must reach upstream unchanged.
			if json.Valid(payload) && gjson.GetBytes(payload, "type").String() == "response.interrupt" {
				appendResponsesInterruptDiagnostic(timeline, payload, "")
				errInterrupt := interrupt(payload)
				if errInterrupt == nil {
					appendResponsesInterruptDiagnostic(timeline, payload, "handled")
					continue
				}
				if errors.Is(errInterrupt, cliproxyexecutor.ErrNoActiveUpstreamWebsocket) {
					switch local.deliver(payload) {
					case responsesLocalInterruptQueued:
						appendResponsesInterruptDiagnostic(timeline, payload, "local_http")
						continue
					case responsesLocalInterruptAlreadyTerminal:
						appendResponsesInterruptDiagnostic(timeline, payload, "already_terminal")
						continue
					default:
						errInterrupt = fmt.Errorf("response.interrupt does not target an active response")
					}
				}
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
			select {
			case input <- cliproxyexecutor.WebsocketInput{Payload: payload}:
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
