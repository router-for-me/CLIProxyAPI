package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gorilla/websocket"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

// readResponsesWebsocketInput keeps interrupts flowing while a response is active.
// The bounded queue backpressures clients instead of retaining unlimited input.
func readResponsesWebsocketInput(ctx context.Context, cancel context.CancelCauseFunc, conn *websocket.Conn, interrupt func([]byte) error, writer *responsesWebsocketWriter) <-chan cliproxyexecutor.WebsocketInput {
	input := make(chan cliproxyexecutor.WebsocketInput, 16)
	go func() {
		defer close(input)
		for {
			kind, payload, err := conn.ReadMessage()
			if err != nil {
				cancel(err)
				return
			}
			if kind != websocket.TextMessage && kind != websocket.BinaryMessage {
				continue
			}
			if json.Valid(payload) && gjson.GetBytes(payload, "type").String() == "response.interrupt" {
				if errInterrupt := interrupt(payload); errInterrupt != nil {
					body, _ := json.Marshal(map[string]any{"type": "error", "status": 400, "error": map[string]string{"type": "invalid_request_error", "message": errInterrupt.Error()}})
					writer.writeMu.Lock()
					errWrite := conn.WriteMessage(websocket.TextMessage, body)
					writer.writeMu.Unlock()
					if errWrite != nil {
						cancel(errWrite)
						return
					}
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
	return input
}

func (h *OpenAIResponsesAPIHandler) forwardResponsesWebsocketInterrupt(ctx context.Context, sessionID string, payload []byte) error {
	responseID := gjson.GetBytes(payload, "response_id")
	if responseID.Type != gjson.String || strings.TrimSpace(responseID.String()) == "" {
		return fmt.Errorf("response.interrupt requires response_id")
	}
	if h == nil || h.AuthManager == nil {
		return fmt.Errorf("no active upstream websocket for response.interrupt")
	}
	exec, ok := h.AuthManager.Executor("codex")
	if !ok {
		return fmt.Errorf("no active upstream websocket for response.interrupt")
	}
	sender, ok := exec.(interface {
		InterruptExecutionSession(context.Context, string, []byte) error
	})
	if !ok {
		return fmt.Errorf("upstream executor does not support response.interrupt")
	}
	ctx = cliproxyexecutor.WithWebsocketAuthCheck(ctx, func(authID string) bool {
		auth, found := h.AuthManager.GetByID(authID)
		return found && auth != nil && !auth.Disabled && auth.Status != coreauth.StatusDisabled
	})
	return sender.InterruptExecutionSession(ctx, sessionID, payload)
}
