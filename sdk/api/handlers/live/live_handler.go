// Package live provides WebSocket handlers for Gemini Live API.
// It implements bidirectional WebSocket relay for real-time audio/video communication
// through AI Studio Build proxy.
package live

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/wsrelay"
	log "github.com/sirupsen/logrus"
)

const (
	// WebSocket configuration
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = 30 * time.Second
	maxMessageSize = 64 * 1024 * 1024 // 64 MiB
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for development
	},
}

// LiveHandler handles WebSocket connections for Gemini Live API relay.
type LiveHandler struct {
	// WSRelay is the WebSocket relay manager for AI Studio Build connections
	WSRelay *wsrelay.Manager

	// ProviderSelector selects which AI Studio Build provider to use
	ProviderSelector func() string

	// DefaultModel is the default Live API model
	DefaultModel string

	// DefaultVoice is the default voice for audio output
	DefaultVoice string

	// ThinkingBudget enables Deep Think capability
	ThinkingBudget int
}

// NewLiveHandler creates a new Live API handler.
func NewLiveHandler(relay *wsrelay.Manager, providerSelector func() string) *LiveHandler {
	return &LiveHandler{
		WSRelay:          relay,
		ProviderSelector: providerSelector,
		DefaultModel:     "gemini-2.5-flash-native-audio-preview",
		DefaultVoice:     "Puck",
		ThinkingBudget:   1024,
	}
}

// HandleWebSocket handles the /v1/realtime WebSocket endpoint.
// Routes through AI Studio Build proxy instead of direct Gemini connection.
func (h *LiveHandler) HandleWebSocket(c *gin.Context) {
	// Upgrade HTTP connection to WebSocket
	clientConn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Errorf("WebSocket upgrade failed: %v", err)
		return
	}
	defer clientConn.Close()

	log.Info("Client connected to Live API relay")

	// Check if relay is available
	if h.WSRelay == nil {
		h.sendError(clientConn, "Live API relay not configured. AI Studio Build connection required.")
		return
	}

	// Get provider - check query param first, then fall back to selector
	provider := c.Query("provider")
	if provider == "" && h.ProviderSelector != nil {
		provider = h.ProviderSelector()
	}
	log.Infof("Live API provider: '%s' (from query: %v)", provider, c.Query("provider") != "")
	if provider == "" {
		h.sendError(clientConn, "No AI Studio Build provider available. Connect AI Studio Build first.")
		return
	}

	// Extract configuration from query params
	model := c.Query("model")
	if model == "" {
		model = h.DefaultModel
	}
	voice := c.Query("voice")
	if voice == "" {
		voice = h.DefaultVoice
	}

	// Create WebSocket tunnel config
	config := &wsrelay.WSConfig{
		Model:              model,
		Voice:              voice,
		ResponseModalities: []string{"AUDIO"},
	}

	// Parse system instruction if provided
	if sysInstr := c.Query("system_instruction"); sysInstr != "" {
		config.SystemInstruction = sysInstr
	}

	// Create context for managing goroutines
	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()

	// Connect through AI Studio Build
	log.Infof("Connecting to Live API via AI Studio Build (provider=%s, model=%s)", provider, model)
	tunnel, err := h.WSRelay.ConnectWS(ctx, provider, config)
	if err != nil {
		log.Errorf("Failed to establish Live API tunnel: %v", err)
		h.sendError(clientConn, "Failed to connect to Live API via AI Studio Build: "+err.Error())
		return
	}
	defer tunnel.Close()

	log.Info("Live API tunnel established through AI Studio Build")

	var wg sync.WaitGroup
	wg.Add(2)

	// Client -> AI Studio Build relay
	go func() {
		defer wg.Done()
		defer cancel()
		h.relayClientToTunnel(ctx, clientConn, tunnel)
	}()

	// AI Studio Build -> Client relay
	go func() {
		defer wg.Done()
		defer cancel()
		h.relayTunnelToClient(ctx, tunnel, clientConn)
	}()

	// Wait for either direction to close
	wg.Wait()
	log.Info("Live API session ended")
}

// relayClientToTunnel relays messages from client WebSocket to AI Studio Build tunnel.
func (h *LiveHandler) relayClientToTunnel(ctx context.Context, client *websocket.Conn, tunnel *wsrelay.WSTunnel) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			messageType, message, err := client.ReadMessage()
			if err != nil {
				if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
					log.Debug("[client->tunnel] Connection closed normally")
				} else {
					log.Errorf("[client->tunnel] Read error: %v", err)
				}
				return
			}

			// Log message for debugging (truncate large messages)
			logMsg := string(message)
			if len(logMsg) > 200 {
				logMsg = logMsg[:200] + "..."
			}
			log.Debugf("[client->tunnel] Message: %s", logMsg)

			// Forward to tunnel
			var sendErr error
			if messageType == websocket.BinaryMessage {
				sendErr = tunnel.SendBinary(ctx, message)
			} else {
				sendErr = tunnel.SendText(ctx, message)
			}
			if sendErr != nil {
				log.Errorf("[client->tunnel] Send error: %v", sendErr)
				return
			}
		}
	}
}

// relayTunnelToClient relays messages from AI Studio Build tunnel to client WebSocket.
func (h *LiveHandler) relayTunnelToClient(ctx context.Context, tunnel *wsrelay.WSTunnel, client *websocket.Conn) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-tunnel.Receive():
			if !ok {
				// Tunnel closed
				if err := tunnel.Err(); err != nil {
					log.Errorf("[tunnel->client] Tunnel error: %v", err)
				} else {
					log.Debug("[tunnel->client] Tunnel closed normally")
				}
				return
			}

			if msg.Err != nil {
				log.Errorf("[tunnel->client] Message error: %v", msg.Err)
				h.sendError(client, msg.Err.Error())
				return
			}

			// Log message for debugging (truncate large messages)
			logMsg := string(msg.Data)
			if len(logMsg) > 200 {
				logMsg = logMsg[:200] + "..."
			}
			log.Debugf("[tunnel->client] Message: %s", logMsg)

			// Forward to client
			messageType := websocket.TextMessage
			if msg.Type == "binary" {
				messageType = websocket.BinaryMessage
			}
			if err := client.WriteMessage(messageType, msg.Data); err != nil {
				log.Errorf("[tunnel->client] Write error: %v", err)
				return
			}
		}
	}
}

// sendError sends an error message to the client.
func (h *LiveHandler) sendError(conn *websocket.Conn, errMsg string) {
	errorPayload := map[string]any{
		"error": map[string]any{
			"message": errMsg,
			"code":    "relay_error",
		},
	}
	data, _ := json.Marshal(errorPayload)
	conn.WriteMessage(websocket.TextMessage, data)
}

// SetupMessage returns a setup message for initializing a Live API session.
func (h *LiveHandler) SetupMessage(model, voice string, thinkingBudget int) map[string]any {
	if model == "" {
		model = h.DefaultModel
	}
	if voice == "" {
		voice = h.DefaultVoice
	}
	if thinkingBudget == 0 {
		thinkingBudget = h.ThinkingBudget
	}

	return map[string]any{
		"setup": map[string]any{
			"model": "models/" + model,
			"generationConfig": map[string]any{
				"responseModalities": []string{"AUDIO"},
				"speechConfig": map[string]any{
					"voiceConfig": map[string]any{
						"prebuiltVoiceConfig": map[string]any{
							"voiceName": voice,
						},
					},
				},
			},
		},
	}
}
