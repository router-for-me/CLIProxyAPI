package live

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/MoeForge/moego/base64"
	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const dictationFrameLimit = 4 << 20

// HandleDictationStream pins a runtime OAuth credential for one desktop dictation session.
func (h *Handler) HandleDictationStream(c *gin.Context) {
	if !websocket.IsWebSocketUpgrade(c.Request) {
		writeTranscriptionError(c, 426, "websocket_upgrade_required", "WebSocket upgrade required")
		return
	}
	if model := c.Query("model"); model != "" && model != TranscriptionModel {
		writeTranscriptionError(c, 400, "invalid_model", "Use model codex-transcribe")
		return
	}
	if h == nil || h.authManager == nil {
		writeTranscriptionError(c, 503, "dictation_unavailable", "Codex OAuth authentication is unavailable")
		return
	}
	ctx := context.WithValue(c.Request.Context(), "gin", c)
	opts := coreexecutor.Options{Headers: liveSelectionHeaders(c)}
	ctx = handlers.EnrichContextWithSessionHierarchy(ctx, opts.Headers, nil, nil)
	selection, selected, errSelect := h.selectOAuth(ctx, TranscriptionModel, opts)
	if errSelect != nil || selected == nil {
		if selection != nil {
			selection.End("selection_failed")
		}
		writeTranscriptionError(c, 503, "dictation_auth_unavailable", "No Codex OAuth credential is currently available")
		return
	}
	if selection != nil {
		defer selection.End("dictation_finished")
		attemptCtx, release, errAttempt := selection.AttemptContext(ctx)
		if errAttempt != nil {
			writeTranscriptionError(c, 503, "dictation_unavailable", "Unable to acquire a Codex OAuth execution slot")
			return
		}
		ctx = attemptCtx
		defer release()
	}
	endpoint, errURL := dictationUpstreamURL(selected.Attributes["base_url"])
	if errURL != nil {
		writeTranscriptionError(c, 502, "dictation_unavailable", "Unable to prepare dictation transport")
		return
	}
	request, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, websocketHTTPURL(endpoint), nil)
	if errRequest != nil {
		writeTranscriptionError(c, 502, "dictation_unavailable", "Unable to prepare dictation transport")
		return
	}
	request.Header.Set("Origin", "https://chatgpt.com")
	request.Header.Set("User-Agent", transcriptionUserAgent)
	request.Header.Set("Originator", "Codex Desktop")
	setAccountHeader(request.Header, selected)
	if errPrepare := h.authManager.PrepareHttpRequest(ctx, selected, request); errPrepare != nil {
		writeTranscriptionError(c, 502, "dictation_unavailable", "Unable to prepare dictation authentication")
		return
	}
	token := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
	if token == "" || token == request.Header.Get("Authorization") {
		writeTranscriptionError(c, 503, "dictation_auth_unavailable", "Codex OAuth authentication is unavailable")
		return
	}
	// Match the installed app's browser authentication without logging protocols.
	request.Header.Del("Authorization")
	dialer := newProxyAwareSidebandDialer(h.currentConfig(), selected)
	dialer.Subprotocols = []string{"chatgpt-dictation", "openai-bearer." + token, "codex-desktop"}
	upstream, response, errDial := dialer.DialContext(ctx, endpoint, request.Header)
	if errDial != nil {
		status := http.StatusBadGateway
		if response != nil {
			status = response.StatusCode
		}
		closeHandshakeBody(response, "dictation handshake")
		if status == http.StatusUnauthorized && selection != nil {
			h.authManager.ReportHomeUnauthorized(ctx, selected, "codex", TranscriptionModel)
		}
		writeTranscriptionError(c, status, "dictation_upstream_error", "Unable to connect to ChatGPT dictation")
		return
	}
	closeUpstream := sync.OnceValue(upstream.Close)
	defer func() { _ = closeUpstream() }()
	if selection != nil {
		if errBind := selection.Bind(closeUpstream); errBind != nil {
			writeTranscriptionError(c, 503, "dictation_canceled", "Dictation was canceled")
			return
		}
	}
	upgrader := sidebandUpgrader
	upgrader.Subprotocols = nil
	downstream, errUpgrade := upgrader.Upgrade(c.Writer, c.Request, nil)
	if errUpgrade != nil {
		return
	}
	defer func() { _ = downstream.Close() }()
	upstream.SetReadLimit(dictationFrameLimit)
	downstream.SetReadLimit(dictationFrameLimit)
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = downstream.Close()
			_ = closeUpstream()
		case <-done:
		}
	}()
	results := make(chan error, 2)
	go func() { results <- copyDictationInput(upstream, downstream) }()
	go func() { results <- copyDictationEvents(downstream, upstream) }()
	<-results
	_ = downstream.Close()
	_ = closeUpstream()
	<-results
}

func dictationUpstreamURL(base string) (string, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		base = "https://chatgpt.com/backend-api"
	}
	base = strings.TrimSuffix(base, "/codex")
	u, err := url.Parse(base + "/dictation/stream")
	if err != nil || u.Host == "" {
		return "", errors.New("invalid dictation base URL")
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return "", errors.New("invalid dictation transport")
	}
	u.RawQuery = "dictation_surface=composer"
	return u.String(), nil
}

func copyDictationInput(destination, source *websocket.Conn) error {
	started, closing := false, false
	for {
		kind, payload, errRead := source.ReadMessage()
		if errRead != nil {
			return errRead
		}
		if kind != websocket.TextMessage {
			return errors.New("dictation requires JSON text frames")
		}
		frame, errFrame := dictationInput(payload, &started, &closing)
		if errFrame != nil {
			return errFrame
		}
		if errWrite := destination.WriteMessage(websocket.TextMessage, frame); errWrite != nil {
			return errWrite
		}
	}
}

func dictationInput(payload []byte, started, closing *bool) ([]byte, error) {
	var frame struct {
		Type               string `json:"type"`
		DictationSessionID string `json:"dictation_session_id,omitempty"`
		AttemptID          string `json:"attempt_id,omitempty"`
		Audio              string `json:"audio,omitempty"`
		Config             struct {
			Language         string `json:"language,omitempty"`
			InputAudioFormat string `json:"input_audio_format"`
			SampleRate       int    `json:"sample_rate_hz"`
			Channels         int    `json:"num_channels"`
		} `json:"config"`
	}
	if json.Unmarshal(payload, &frame) != nil || *closing {
		return nil, errors.New("invalid dictation frame")
	}
	var output any
	switch frame.Type {
	case "session.start":
		if *started || frame.Config.InputAudioFormat != "pcm16" || frame.Config.Channels != 1 ||
			frame.Config.SampleRate < 8000 || frame.Config.SampleRate > 48000 || len(frame.Config.Language) > 128 ||
			len(frame.DictationSessionID) > 128 || len(frame.AttemptID) > 128 {
			return nil, errors.New("invalid dictation session configuration")
		}
		*started = true
		config := map[string]any{
			"input_audio_format": "pcm16", "sample_rate_hz": frame.Config.SampleRate, "num_channels": 1,
			"max_buffer_size_bytes": 4194304, "max_utterance_duration_ms": 30000, "session_ttl_ms": 300000,
			"provider_mode": "streaming_sse", "transcript_delivery_mode": "segment",
			"vad": map[string]any{"type": "server_vad", "threshold": 0.5, "prefix_padding_ms": 300, "silence_duration_ms": 500},
		}
		if frame.Config.Language != "" {
			config["language"] = frame.Config.Language
		}
		output = map[string]any{"type": "session.start", "dictation_session_id": frame.DictationSessionID, "attempt_id": frame.AttemptID, "config": config}
	case "audio.append":
		audio, errAudio := base64.StdEncoding.DecodeString(frame.Audio)
		if !*started || errAudio != nil || len(audio)%2 != 0 || len(audio) > 1<<20 {
			return nil, errors.New("invalid PCM16 audio")
		}
		output = map[string]any{"type": "audio.append", "audio": frame.Audio}
	case "session.close":
		if !*started {
			return nil, errors.New("dictation session has not started")
		}
		*closing = true
		output = map[string]any{"type": "session.close"}
	default:
		return nil, errors.New("unsupported dictation frame")
	}
	return json.Marshal(output)
}

func copyDictationEvents(destination, source *websocket.Conn) error {
	for {
		kind, payload, errRead := source.ReadMessage()
		if errRead != nil {
			return errRead
		}
		if kind != websocket.TextMessage {
			return errors.New("invalid dictation event")
		}
		event, errEvent := sanitizedDictationEvent(payload)
		if errEvent != nil {
			return errEvent
		}
		if event == nil {
			continue
		}
		if errWrite := destination.WriteMessage(websocket.TextMessage, event); errWrite != nil {
			return errWrite
		}
	}
}

// Error messages and arbitrary upstream fields never cross this boundary.
func sanitizedDictationEvent(payload []byte) ([]byte, error) {
	var event struct {
		Type      string `json:"type"`
		Sequence  int64  `json:"sequence_no"`
		Utterance string `json:"utterance_id,omitempty"`
		Revision  int64  `json:"revision"`
		Text      string `json:"text"`
		Session   *struct {
			Status string `json:"status"`
		} `json:"session,omitempty"`
	}
	if json.Unmarshal(payload, &event) != nil {
		return nil, errors.New("invalid dictation event")
	}
	switch event.Type {
	case "session.started", "session.updated", "speech.started", "speech.stopped",
		"transcript.segment", "transcript.final", "transcript.delta":
		return json.Marshal(event)
	case "session.error", "transcript.failed":
		return json.Marshal(map[string]any{"type": event.Type, "fatal": true, "error": map[string]any{
			"code": "dictation_failed", "message": "ChatGPT dictation failed", "retryable": true,
		}})
	default:
		return nil, nil
	}
}
