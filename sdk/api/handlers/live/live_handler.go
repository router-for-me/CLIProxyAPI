// Package live provides WebSocket handlers for Gemini Live API.
// It implements bidirectional WebSocket relay for real-time audio/video communication.
package live

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	log "github.com/sirupsen/logrus"
)

const (
	// Gemini Live API endpoint
	geminiLiveAPIURL = "wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent"

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
	// APIKey is used for authenticating with Gemini API
	// In production, this would come from auth manager
	APIKey string

	// DefaultModel is the default Live API model
	DefaultModel string

	// DefaultVoice is the default voice for audio output
	DefaultVoice string

	// ThinkingBudget enables Deep Think capability
	ThinkingBudget int
}

// NewLiveHandler creates a new Live API handler.
func NewLiveHandler() *LiveHandler {
	return &LiveHandler{
		DefaultModel:   "gemini-2.5-flash-native-audio-preview",
		DefaultVoice:   "Puck",
		ThinkingBudget: 1024,
	}
}

// SetAPIKey sets the API key for Gemini authentication.
func (h *LiveHandler) SetAPIKey(key string) {
	h.APIKey = key
}

// HandleWebSocket handles the /v1/realtime WebSocket endpoint.
func (h *LiveHandler) HandleWebSocket(c *gin.Context) {
	// Upgrade HTTP connection to WebSocket
	clientConn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Errorf("WebSocket upgrade failed: %v", err)
		return
	}
	defer clientConn.Close()

	log.Info("Client connected to Live API relay")

	// Extract API key from query or header
	apiKey := c.Query("key")
	if apiKey == "" {
		apiKey = c.GetHeader("X-API-Key")
	}
	if apiKey == "" {
		apiKey = h.APIKey
	}

	if apiKey == "" {
		h.sendError(clientConn, "API key required. Provide via ?key= query parameter or X-API-Key header")
		return
	}

	// Connect to Gemini Live API
	geminiURL := fmt.Sprintf("%s?key=%s", geminiLiveAPIURL, apiKey)
	geminiConn, _, err := websocket.DefaultDialer.Dial(geminiURL, nil)
	if err != nil {
		log.Errorf("Failed to connect to Gemini Live API: %v", err)
		h.sendError(clientConn, fmt.Sprintf("Failed to connect to Gemini: %v", err))
		return
	}
	defer geminiConn.Close()

	log.Info("Connected to Gemini Live API")

	// Create context for managing goroutines
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(2)

	// Client -> Gemini relay
	go func() {
		defer wg.Done()
		h.relayMessages(ctx, clientConn, geminiConn, "client->gemini")
	}()

	// Gemini -> Client relay
	go func() {
		defer wg.Done()
		h.relayMessages(ctx, geminiConn, clientConn, "gemini->client")
	}()

	// Wait for either direction to close
	wg.Wait()
	log.Info("Live API session ended")
}

// relayMessages relays WebSocket messages from src to dst.
func (h *LiveHandler) relayMessages(ctx context.Context, src, dst *websocket.Conn, direction string) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			messageType, message, err := src.ReadMessage()
			if err != nil {
				if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
					log.Debugf("[%s] Connection closed normally", direction)
				} else {
					log.Errorf("[%s] Read error: %v", direction, err)
				}
				return
			}

			// Log message type for debugging (truncate large messages)
			logMsg := string(message)
			if len(logMsg) > 200 {
				logMsg = logMsg[:200] + "..."
			}
			log.Debugf("[%s] Message: %s", direction, logMsg)

			// Forward message
			if err := dst.WriteMessage(messageType, message); err != nil {
				log.Errorf("[%s] Write error: %v", direction, err)
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
			"model": fmt.Sprintf("models/%s", model),
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
