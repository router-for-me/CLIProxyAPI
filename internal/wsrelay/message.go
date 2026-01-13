package wsrelay

// Message represents the JSON payload exchanged with websocket clients.
type Message struct {
	ID      string         `json:"id"`
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload,omitempty"`
}

const (
	// MessageTypeHTTPReq identifies an HTTP-style request envelope.
	MessageTypeHTTPReq = "http_request"
	// MessageTypeHTTPResp identifies a non-streaming HTTP response envelope.
	MessageTypeHTTPResp = "http_response"
	// MessageTypeStreamStart marks the beginning of a streaming response.
	MessageTypeStreamStart = "stream_start"
	// MessageTypeStreamChunk carries a streaming response chunk.
	MessageTypeStreamChunk = "stream_chunk"
	// MessageTypeStreamEnd marks the completion of a streaming response.
	MessageTypeStreamEnd = "stream_end"
	// MessageTypeError carries an error response.
	MessageTypeError = "error"
	// MessageTypePing represents ping messages from clients.
	MessageTypePing = "ping"
	// MessageTypePong represents pong responses back to clients.
	MessageTypePong = "pong"

	// WebSocket tunneling message types for Live API relay
	// MessageTypeWSConnect requests AI Studio Build to open a WebSocket to Gemini Live API.
	MessageTypeWSConnect = "ws_connect"
	// MessageTypeWSConnected confirms WebSocket connection established.
	MessageTypeWSConnected = "ws_connected"
	// MessageTypeWSMessage relays a WebSocket message (text or binary via base64).
	MessageTypeWSMessage = "ws_message"
	// MessageTypeWSClose requests closing the tunneled WebSocket.
	MessageTypeWSClose = "ws_close"
	// MessageTypeWSClosed confirms the tunneled WebSocket was closed.
	MessageTypeWSClosed = "ws_closed"
)
