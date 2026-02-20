// Package api provides a WebSocket handler for /v1/responses streaming.
// Codex and other clients expect ws://host/v1/responses for streaming; this handler
// accepts WebSocket upgrades and proxies requests to the HTTP POST /v1/responses endpoint.
package api

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"github.com/gorilla/websocket"
	log "github.com/sirupsen/logrus"
)

const responsesWSPath = "/v1/responses"

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024 * 4,
	WriteBufferSize: 1024 * 4,
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow same-origin and localhost
	},
}

// ResponsesWebSocketHandler returns an http.Handler that upgrades to WebSocket and
// proxies the first message as a POST to /v1/responses, streaming the response back.
func ResponsesWebSocketHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !websocket.IsWebSocketUpgrade(r) {
			http.Error(w, "Expected WebSocket upgrade", http.StatusBadRequest)
			return
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.WithError(err).Warn("responses WebSocket upgrade failed")
			return
		}
		defer func() { _ = conn.Close() }()

		// Read first message (the request body)
		_, msg, err := conn.ReadMessage()
		if err != nil {
			log.WithError(err).Debug("responses WebSocket read failed")
			return
		}

		// Use request Host to build self-request URL (e.g. http://127.0.0.1:8317)
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		baseURL := scheme + "://" + r.Host
		postURL := strings.TrimSuffix(baseURL, "/") + responsesWSPath
		req, err := http.NewRequest(http.MethodPost, postURL, bytes.NewReader(msg))
		if err != nil {
			writeWSError(conn, err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if auth := r.Header.Get("Authorization"); auth != "" {
			req.Header.Set("Authorization", auth)
		}

		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			writeWSError(conn, err)
			return
		}
		defer func() { _ = resp.Body.Close() }()

		// Stream response body to WebSocket
		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				if err := conn.WriteMessage(websocket.TextMessage, buf[:n]); err != nil {
					log.WithError(err).Debug("responses WebSocket write failed")
					return
				}
			}
			if err != nil {
				if err != io.EOF {
					log.WithError(err).Debug("responses WebSocket stream read failed")
				}
				break
			}
		}
	})
}

func writeWSError(conn *websocket.Conn, err error) {
	_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"error":"`+err.Error()+`"}`))
}
