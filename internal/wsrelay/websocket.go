package wsrelay

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"

	"github.com/google/uuid"
)

// WSConfig contains configuration for a WebSocket tunnel connection.
type WSConfig struct {
	// Model is the Gemini model to use (e.g., "gemini-2.5-flash-native-audio-preview")
	Model string
	// Voice is the voice name for audio output (e.g., "Puck", "Aoede")
	Voice string
	// ResponseModalities specifies output types (e.g., ["AUDIO"])
	ResponseModalities []string
	// SystemInstruction is the system prompt
	SystemInstruction string
}

// WSTunnel represents a bidirectional WebSocket tunnel through AI Studio Build.
type WSTunnel struct {
	id       string
	provider string
	manager  *Manager
	incoming chan WSMessage
	closed   chan struct{}
	closeOnce sync.Once
	err      error
}

// WSMessage represents a message in the WebSocket tunnel.
type WSMessage struct {
	// Type is "text" or "binary"
	Type string
	// Data contains the message payload (base64 encoded for binary)
	Data []byte
	// Err is set if an error occurred
	Err error
}

// ConnectWS establishes a WebSocket tunnel through AI Studio Build to Gemini Live API.
func (m *Manager) ConnectWS(ctx context.Context, provider string, config *WSConfig) (*WSTunnel, error) {
	if config == nil {
		return nil, fmt.Errorf("wsrelay: config is nil")
	}

	tunnelID := uuid.NewString()

	// Build the connection request payload
	payload := map[string]any{
		"model": config.Model,
	}
	if config.Voice != "" {
		payload["voice"] = config.Voice
	}
	if len(config.ResponseModalities) > 0 {
		payload["response_modalities"] = config.ResponseModalities
	}
	if config.SystemInstruction != "" {
		payload["system_instruction"] = config.SystemInstruction
	}

	msg := Message{
		ID:      tunnelID,
		Type:    MessageTypeWSConnect,
		Payload: payload,
	}

	respCh, err := m.Send(ctx, provider, msg)
	if err != nil {
		return nil, fmt.Errorf("wsrelay: failed to send ws_connect: %w", err)
	}

	// Wait for connection confirmation
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp, ok := <-respCh:
		if !ok {
			return nil, errors.New("wsrelay: connection closed before ws_connected")
		}
		if resp.Type == MessageTypeError {
			return nil, decodeError(resp.Payload)
		}
		if resp.Type != MessageTypeWSConnected {
			return nil, fmt.Errorf("wsrelay: unexpected response type: %s", resp.Type)
		}
	}

	tunnel := &WSTunnel{
		id:       tunnelID,
		provider: provider,
		manager:  m,
		incoming: make(chan WSMessage, 64),
		closed:   make(chan struct{}),
	}

	// Start goroutine to receive messages
	go tunnel.receiveLoop(ctx, respCh)

	return tunnel, nil
}

// receiveLoop processes incoming messages from the tunnel.
func (t *WSTunnel) receiveLoop(ctx context.Context, respCh <-chan Message) {
	defer t.Close()
	for {
		select {
		case <-ctx.Done():
			t.err = ctx.Err()
			return
		case <-t.closed:
			return
		case msg, ok := <-respCh:
			if !ok {
				t.err = errors.New("wsrelay: tunnel channel closed")
				return
			}

			switch msg.Type {
			case MessageTypeWSMessage:
				wsMsg := decodeWSMessage(msg.Payload)
				select {
				case t.incoming <- wsMsg:
				default:
					// Channel full, drop message
				}

			case MessageTypeWSClosed:
				reason, _ := msg.Payload["reason"].(string)
				if reason != "" {
					t.err = fmt.Errorf("wsrelay: tunnel closed: %s", reason)
				}
				return

			case MessageTypeError:
				t.err = decodeError(msg.Payload)
				return
			}
		}
	}
}

// Send sends a message through the WebSocket tunnel.
func (t *WSTunnel) Send(ctx context.Context, msgType string, data []byte) error {
	select {
	case <-t.closed:
		return errors.New("wsrelay: tunnel closed")
	default:
	}

	payload := map[string]any{
		"msg_type": msgType, // "text" or "binary"
	}
	if msgType == "binary" {
		payload["data"] = base64.StdEncoding.EncodeToString(data)
	} else {
		payload["data"] = string(data)
	}

	msg := Message{
		ID:      t.id,
		Type:    MessageTypeWSMessage,
		Payload: payload,
	}

	s := t.manager.session(t.provider)
	if s == nil {
		return fmt.Errorf("wsrelay: provider %s not connected", t.provider)
	}

	return s.send(ctx, msg)
}

// SendText sends a text message through the tunnel.
func (t *WSTunnel) SendText(ctx context.Context, data []byte) error {
	return t.Send(ctx, "text", data)
}

// SendBinary sends a binary message through the tunnel.
func (t *WSTunnel) SendBinary(ctx context.Context, data []byte) error {
	return t.Send(ctx, "binary", data)
}

// Receive returns the channel for incoming messages.
func (t *WSTunnel) Receive() <-chan WSMessage {
	return t.incoming
}

// Close closes the WebSocket tunnel.
func (t *WSTunnel) Close() error {
	t.closeOnce.Do(func() {
		close(t.closed)

		// Send close message to AI Studio Build
		msg := Message{
			ID:      t.id,
			Type:    MessageTypeWSClose,
			Payload: map[string]any{},
		}
		s := t.manager.session(t.provider)
		if s != nil {
			_ = s.send(context.Background(), msg)
		}

		close(t.incoming)
	})
	return t.err
}

// Err returns any error that caused the tunnel to close.
func (t *WSTunnel) Err() error {
	return t.err
}

// ID returns the tunnel's unique identifier.
func (t *WSTunnel) ID() string {
	return t.id
}

func decodeWSMessage(payload map[string]any) WSMessage {
	msg := WSMessage{}
	if payload == nil {
		msg.Err = errors.New("wsrelay: nil payload")
		return msg
	}

	msg.Type, _ = payload["msg_type"].(string)
	if msg.Type == "" {
		msg.Type = "text"
	}

	if dataStr, ok := payload["data"].(string); ok {
		if msg.Type == "binary" {
			decoded, err := base64.StdEncoding.DecodeString(dataStr)
			if err != nil {
				msg.Err = fmt.Errorf("wsrelay: failed to decode binary data: %w", err)
				return msg
			}
			msg.Data = decoded
		} else {
			msg.Data = []byte(dataStr)
		}
	}

	if errStr, ok := payload["error"].(string); ok && errStr != "" {
		msg.Err = errors.New(errStr)
	}

	return msg
}
