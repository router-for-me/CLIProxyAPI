package openai

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	requestlogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

func TestWriteWebsocketCloseForUpstreamErrorMirrorsMessageTooBig(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		reason string
	}{
		{
			name: "raw close error",
			err: &websocket.CloseError{
				Code: websocket.CloseMessageTooBig,
				Text: "message too big",
			},
			reason: "message too big",
		},
		{
			name: "mapped stream error",
			err: websocketPinnedFailoverStatusError{
				status: http.StatusRequestEntityTooLarge,
				msg:    `{"error":{"message":"upstream websocket message too big","code":"message_too_big"}}`,
			},
			reason: "upstream websocket message too big",
		},
		{
			name: "multibyte reason stays valid",
			err: &websocket.CloseError{
				Code: websocket.CloseMessageTooBig,
				Text: strings.Repeat("🙂", 31),
			},
			reason: strings.Repeat("🙂", 30),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			serverErr := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := responsesWebsocketUpgrader.Upgrade(w, r, nil)
				if err != nil {
					serverErr <- err
					return
				}
				matched, errWrite := writeWebsocketCloseForUpstreamError(conn, tt.err)
				if !matched && errWrite == nil {
					errWrite = errors.New("message-too-big error did not match")
				}
				if errClose := conn.Close(); errWrite == nil {
					errWrite = errClose
				}
				serverErr <- errWrite
			}))
			defer server.Close()

			wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
			conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
			if err != nil {
				t.Fatalf("dial websocket: %v", err)
			}
			defer func() { _ = conn.Close() }()

			if err = conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatalf("set read deadline: %v", err)
			}
			_, _, err = conn.ReadMessage()
			var closeErr *websocket.CloseError
			if !errors.As(err, &closeErr) {
				t.Fatalf("expected websocket close error, got %v", err)
			}
			if closeErr.Code != websocket.CloseMessageTooBig {
				t.Fatalf("expected close code 1009, got %d", closeErr.Code)
			}
			if closeErr.Text != tt.reason {
				t.Fatalf("expected close reason %q, got %q", tt.reason, closeErr.Text)
			}
			if err = <-serverErr; err != nil {
				t.Fatalf("close server websocket: %v", err)
			}
		})
	}
}

func TestResponsesWebsocketWriterCloseDoesNotWaitForActiveDataWriter(t *testing.T) {
	serverErrCh := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := responsesWebsocketUpgrader.Upgrade(w, r, nil)
		if err != nil {
			serverErrCh <- err
			return
		}
		writer := newResponsesWebsocketWriter(conn)

		// Holding writeMu models a data writer blocked inside WriteMessage. The
		// upstream-close path must hard-close the socket instead of waiting for it.
		writer.writeMu.Lock()
		closeDone := make(chan error, 1)
		go func() {
			matched, errClose := writer.closeForUpstreamError(&websocket.CloseError{
				Code: websocket.CloseMessageTooBig,
				Text: "message too big",
			})
			if !matched && errClose == nil {
				errClose = errors.New("message-too-big error did not match")
			}
			closeDone <- errClose
		}()

		select {
		case errClose := <-closeDone:
			writer.writeMu.Unlock()
			serverErrCh <- errClose
		case <-time.After(time.Second):
			writer.writeMu.Unlock()
			serverErrCh <- errors.New("close waited behind active data writer")
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if err = conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	if _, _, err = conn.ReadMessage(); err == nil {
		t.Fatal("client read succeeded, want connection closure")
	}
	if errServer := <-serverErrCh; errServer != nil {
		t.Fatalf("server error: %v", errServer)
	}
}

func TestTruncateWebsocketCloseReason(t *testing.T) {
	tests := []struct {
		name     string
		reason   string
		maxBytes int
		want     string
	}{
		{
			name:     "non-positive limit",
			reason:   "message too big",
			maxBytes: 0,
			want:     "",
		},
		{
			name:     "short valid reason unchanged",
			reason:   "message too big",
			maxBytes: wsCloseReasonMaxBytes,
			want:     "message too big",
		},
		{
			name:     "long ascii reason",
			reason:   strings.Repeat("x", 1<<20),
			maxBytes: wsCloseReasonMaxBytes,
			want:     strings.Repeat("x", wsCloseReasonMaxBytes),
		},
		{
			name:     "long invalid reason",
			reason:   strings.Repeat("\xff", 1<<20),
			maxBytes: wsCloseReasonMaxBytes,
			want:     strings.Repeat("�", wsCloseReasonMaxBytes/utf8.RuneLen(utf8.RuneError)),
		},
		{
			name:     "multibyte rune does not fit",
			reason:   "ab🙂cd",
			maxBytes: 5,
			want:     "ab",
		},
		{
			name:     "invalid bytes become replacement runes",
			reason:   string([]byte{'a', 0xff, 0xfe, 'b'}),
			maxBytes: 8,
			want:     "a��b",
		},
		{
			name:     "invalid replacement does not cross limit",
			reason:   string([]byte{'a', 'b', 0xff, 'c'}),
			maxBytes: 4,
			want:     "ab",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateWebsocketCloseReason(tt.reason, tt.maxBytes)
			if got != tt.want {
				t.Fatalf("truncateWebsocketCloseReason() = %q, want %q", got, tt.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("truncateWebsocketCloseReason() returned invalid UTF-8: %q", got)
			}
			if tt.maxBytes > 0 && len(got) > tt.maxBytes {
				t.Fatalf("truncateWebsocketCloseReason() returned %d bytes, limit %d", len(got), tt.maxBytes)
			}
		})
	}
}

func TestForwardResponsesWebsocketMirrorsMappedMessageTooBig(t *testing.T) {
	gin.SetMode(gin.TestMode)

	serverErrCh := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := responsesWebsocketUpgrader.Upgrade(w, r, nil)
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() { _ = conn.Close() }()

		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = r
		data := make(chan []byte)
		errCh := make(chan *interfaces.ErrorMessage, 1)
		errCh <- &interfaces.ErrorMessage{
			StatusCode: http.StatusRequestEntityTooLarge,
			Error: websocketPinnedFailoverStatusError{
				status: http.StatusRequestEntityTooLarge,
				msg:    `{"error":{"message":"upstream websocket message too big","code":"message_too_big"}}`,
			},
		}

		h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil))
		_, _, _, errMsg, errForward := h.forwardResponsesWebsocket(
			ctx,
			newResponsesWebsocketWriter(conn),
			func(...interface{}) {},
			data,
			errCh,
			newInMemoryWebsocketTimelineLog(),
			"session-1",
		)
		if errMsg == nil || errMsg.StatusCode != http.StatusRequestEntityTooLarge {
			serverErrCh <- fmt.Errorf("forward error message = %#v, want status %d", errMsg, http.StatusRequestEntityTooLarge)
			return
		}
		if !errors.Is(errForward, websocket.ErrCloseSent) {
			serverErrCh <- fmt.Errorf("forward error = %v, want %v", errForward, websocket.ErrCloseSent)
			return
		}
		serverErrCh <- nil
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if err = conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	_, _, err = conn.ReadMessage()
	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) {
		t.Fatalf("expected websocket close error, got %v", err)
	}
	if closeErr.Code != websocket.CloseMessageTooBig {
		t.Fatalf("close code = %d, want %d", closeErr.Code, websocket.CloseMessageTooBig)
	}
	if err = <-serverErrCh; err != nil {
		t.Fatalf("server error: %v", err)
	}
}

type websocketCaptureExecutor struct {
	streamCalls int
	payloads    [][]byte
}

type websocketProviderCaptureExecutor struct {
	provider string
	websocketCaptureExecutor
}

type websocketCompactionCaptureExecutor struct {
	mu             sync.Mutex
	streamPayloads [][]byte
	compactPayload []byte
}

type orderedWebsocketSelector struct {
	mu     sync.Mutex
	order  []string
	cursor int
}

func (s *orderedWebsocketSelector) Pick(_ context.Context, _ string, _ string, _ coreexecutor.Options, auths []*coreauth.Auth) (*coreauth.Auth, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(auths) == 0 {
		return nil, errors.New("no auth available")
	}
	for len(s.order) > 0 && s.cursor < len(s.order) {
		authID := strings.TrimSpace(s.order[s.cursor])
		s.cursor++
		for _, auth := range auths {
			if auth != nil && auth.ID == authID {
				return auth, nil
			}
		}
	}
	for _, auth := range auths {
		if auth != nil {
			return auth, nil
		}
	}
	return nil, errors.New("no auth available")
}

type websocketAuthCaptureExecutor struct {
	mu      sync.Mutex
	authIDs []string
}

type websocketPinnedFailoverExecutor struct {
	mu       sync.Mutex
	authIDs  []string
	calls    map[string]int
	payloads map[string][][]byte
}

type websocketBootstrapFallbackExecutor struct {
	mu       sync.Mutex
	authIDs  []string
	payloads map[string][][]byte
}

type websocketDirectCaptureExecutor struct {
	mu       sync.Mutex
	provider string
	authIDs  []string
	payloads [][]byte
	done     chan struct{}
	doneOnce sync.Once
}

type websocketPinnedFailoverStatusError struct {
	status int
	msg    string
}

func (e websocketPinnedFailoverStatusError) Error() string { return e.msg }

func (e websocketPinnedFailoverStatusError) StatusCode() int { return e.status }

func (e *websocketBootstrapFallbackExecutor) Identifier() string { return "test-provider" }

func (e *websocketBootstrapFallbackExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketBootstrapFallbackExecutor) ExecuteStream(_ context.Context, auth *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	authID := ""
	if auth != nil {
		authID = auth.ID
	}

	e.mu.Lock()
	if e.payloads == nil {
		e.payloads = make(map[string][][]byte)
	}
	e.authIDs = append(e.authIDs, authID)
	e.payloads[authID] = append(e.payloads[authID], bytes.Clone(req.Payload))
	e.mu.Unlock()

	chunks := make(chan coreexecutor.StreamChunk, 1)
	if authID == "auth-ws" {
		chunks <- coreexecutor.StreamChunk{Err: websocketPinnedFailoverStatusError{
			status: http.StatusServiceUnavailable,
			msg:    `{"error":{"message":"websocket bootstrap failed","type":"server_error","code":"ws_failed"}}`,
		}}
		close(chunks)
		return &coreexecutor.StreamResult{Chunks: chunks}, nil
	}

	chunks <- coreexecutor.StreamChunk{Payload: []byte(`{"type":"response.completed","response":{"id":"resp-http","output":[{"type":"message","id":"out-http"}]}}`)}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *websocketBootstrapFallbackExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *websocketBootstrapFallbackExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketBootstrapFallbackExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (e *websocketBootstrapFallbackExecutor) AuthIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.authIDs...)
}

func (e *websocketBootstrapFallbackExecutor) Payloads(authID string) [][]byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	src := e.payloads[authID]
	out := make([][]byte, len(src))
	for i := range src {
		out[i] = bytes.Clone(src[i])
	}
	return out
}

func (e *websocketDirectCaptureExecutor) Identifier() string {
	if e != nil && strings.TrimSpace(e.provider) != "" {
		return strings.TrimSpace(e.provider)
	}
	return "codex"
}

func (e *websocketDirectCaptureExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketDirectCaptureExecutor) ExecuteStream(_ context.Context, auth *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	authID := ""
	if auth != nil {
		authID = auth.ID
	}
	e.mu.Lock()
	e.authIDs = append(e.authIDs, authID)
	e.payloads = append(e.payloads, bytes.Clone(req.Payload))
	count := len(e.payloads)
	e.mu.Unlock()

	chunks := make(chan coreexecutor.StreamChunk, 1)
	responseID := fmt.Sprintf("resp-%d", count)
	chunks <- coreexecutor.StreamChunk{Payload: []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"output":[{"type":"message","id":"out-%d"}]}}`, responseID, count))}
	close(chunks)
	if count >= 2 && e.done != nil {
		e.doneOnce.Do(func() {
			close(e.done)
		})
	}
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *websocketDirectCaptureExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *websocketDirectCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketDirectCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (e *websocketDirectCaptureExecutor) Payloads() [][]byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([][]byte, len(e.payloads))
	for i := range e.payloads {
		out[i] = bytes.Clone(e.payloads[i])
	}
	return out
}

func (e *websocketDirectCaptureExecutor) AuthIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.authIDs...)
}

type websocketUpstreamDisconnectExecutor struct {
	mu         sync.Mutex
	provider   string
	subscribed chan string
	sessions   map[string]chan error
}

func (e *websocketUpstreamDisconnectExecutor) Identifier() string {
	if provider := strings.TrimSpace(e.provider); provider != "" {
		return provider
	}
	return "codex"
}

func (e *websocketUpstreamDisconnectExecutor) UpstreamDisconnectChan(sessionID string) <-chan error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	e.mu.Lock()
	if e.sessions == nil {
		e.sessions = make(map[string]chan error)
	}
	ch, ok := e.sessions[sessionID]
	if !ok {
		ch = make(chan error, 1)
		e.sessions[sessionID] = ch
	}
	subscribed := e.subscribed
	e.mu.Unlock()

	if subscribed != nil {
		select {
		case subscribed <- sessionID:
		default:
		}
	}
	return ch
}

func (e *websocketUpstreamDisconnectExecutor) TriggerDisconnect(sessionID string, err error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	e.mu.Lock()
	ch := e.sessions[sessionID]
	delete(e.sessions, sessionID)
	e.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- err:
	default:
	}
	close(ch)
}

func (e *websocketUpstreamDisconnectExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketUpstreamDisconnectExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (e *websocketUpstreamDisconnectExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *websocketUpstreamDisconnectExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketUpstreamDisconnectExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (e *websocketAuthCaptureExecutor) Identifier() string { return "test-provider" }

func (e *websocketAuthCaptureExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketAuthCaptureExecutor) ExecuteStream(_ context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.mu.Lock()
	if auth != nil {
		e.authIDs = append(e.authIDs, auth.ID)
	}
	e.mu.Unlock()

	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: []byte(`{"type":"response.completed","response":{"id":"resp-upstream","output":[{"type":"message","id":"out-1"}]}}`)}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *websocketAuthCaptureExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *websocketAuthCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketAuthCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (e *websocketAuthCaptureExecutor) AuthIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.authIDs...)
}

func (e *websocketPinnedFailoverExecutor) Identifier() string { return "test-provider" }

func (e *websocketPinnedFailoverExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketPinnedFailoverExecutor) ExecuteStream(_ context.Context, auth *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	authID := ""
	if auth != nil {
		authID = auth.ID
	}

	e.mu.Lock()
	if e.calls == nil {
		e.calls = make(map[string]int)
	}
	if e.payloads == nil {
		e.payloads = make(map[string][][]byte)
	}
	e.authIDs = append(e.authIDs, authID)
	e.calls[authID]++
	call := e.calls[authID]
	e.payloads[authID] = append(e.payloads[authID], bytes.Clone(req.Payload))
	e.mu.Unlock()

	if authID == "auth-a" && call == 2 {
		chunks := make(chan coreexecutor.StreamChunk, 1)
		chunks <- coreexecutor.StreamChunk{Err: websocketPinnedFailoverStatusError{
			status: http.StatusTooManyRequests,
			msg:    `{"error":{"message":"quota exhausted","type":"rate_limit_error","code":"rate_limit_exceeded"}}`,
		}}
		close(chunks)
		return &coreexecutor.StreamResult{Chunks: chunks}, nil
	}

	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp-%s-%d","output":[{"type":"message","id":"out-%s-%d"}]}}`, authID, call, authID, call))}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *websocketPinnedFailoverExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *websocketPinnedFailoverExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketPinnedFailoverExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (e *websocketPinnedFailoverExecutor) AuthIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.authIDs...)
}

func (e *websocketPinnedFailoverExecutor) Payloads(authID string) [][]byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	src := e.payloads[authID]
	out := make([][]byte, len(src))
	for i := range src {
		out[i] = bytes.Clone(src[i])
	}
	return out
}

func (e *websocketCaptureExecutor) Identifier() string { return "test-provider" }

func (e *websocketProviderCaptureExecutor) Identifier() string {
	if e != nil && strings.TrimSpace(e.provider) != "" {
		return strings.TrimSpace(e.provider)
	}
	return "test-provider"
}

func (e *websocketCaptureExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketCaptureExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.streamCalls++
	e.payloads = append(e.payloads, bytes.Clone(req.Payload))
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: []byte(`{"type":"response.completed","response":{"id":"resp-upstream","output":[{"type":"message","id":"out-1"}]}}`)}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *websocketCaptureExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *websocketCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (e *websocketCompactionCaptureExecutor) Identifier() string { return "test-provider" }

func (e *websocketCompactionCaptureExecutor) Execute(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.mu.Lock()
	e.compactPayload = bytes.Clone(req.Payload)
	e.mu.Unlock()
	if opts.Alt != "responses/compact" {
		return coreexecutor.Response{}, fmt.Errorf("unexpected non-compact execute alt: %q", opts.Alt)
	}
	return coreexecutor.Response{Payload: []byte(`{"id":"cmp-1","object":"response.compaction"}`)}, nil
}

func (e *websocketCompactionCaptureExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.mu.Lock()
	callIndex := len(e.streamPayloads)
	e.streamPayloads = append(e.streamPayloads, bytes.Clone(req.Payload))
	e.mu.Unlock()

	var payload []byte
	switch callIndex {
	case 0:
		payload = []byte(`{"type":"response.completed","response":{"id":"resp-1","output":[{"type":"function_call","id":"fc-1","call_id":"call-1","name":"tool"}]}}`)
	case 1:
		payload = []byte(`{"type":"response.completed","response":{"id":"resp-2","output":[{"type":"message","id":"assistant-1"}]}}`)
	default:
		payload = []byte(`{"type":"response.completed","response":{"id":"resp-3","output":[{"type":"message","id":"assistant-2"}]}}`)
	}

	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: payload}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *websocketCompactionCaptureExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *websocketCompactionCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketCompactionCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestNormalizeResponsesWebsocketRequestCreate(t *testing.T) {
	raw := []byte(`{"type":"response.create","model":"test-model","stream":false,"input":[{"type":"message","id":"msg-1"}]}`)

	normalized, last, errMsg := normalizeResponsesWebsocketRequest(raw, nil, nil)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	if gjson.GetBytes(normalized, "type").Exists() {
		t.Fatalf("normalized create request must not include type field")
	}
	if !gjson.GetBytes(normalized, "stream").Bool() {
		t.Fatalf("normalized create request must force stream=true")
	}
	if gjson.GetBytes(normalized, "model").String() != "test-model" {
		t.Fatalf("unexpected model: %s", gjson.GetBytes(normalized, "model").String())
	}
	if !bytes.Equal(last, normalized) {
		t.Fatalf("last request snapshot should match normalized request")
	}
}

func TestNormalizeResponsesWebsocketRequestCreateWithHistory(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"input":[{"type":"message","id":"msg-1"}]}`)
	lastResponseOutput := []byte(`[
		{"type":"function_call","id":"fc-1","call_id":"call-1"},
		{"type":"message","id":"assistant-1"}
	]`)
	raw := []byte(`{"type":"response.create","input":[{"type":"function_call_output","call_id":"call-1","id":"tool-out-1"}]}`)

	normalized, next, errMsg := normalizeResponsesWebsocketRequest(raw, lastRequest, lastResponseOutput)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	if gjson.GetBytes(normalized, "type").Exists() {
		t.Fatalf("normalized subsequent create request must not include type field")
	}
	if gjson.GetBytes(normalized, "model").String() != "test-model" {
		t.Fatalf("unexpected model: %s", gjson.GetBytes(normalized, "model").String())
	}

	input := gjson.GetBytes(normalized, "input").Array()
	if len(input) != 4 {
		t.Fatalf("merged input len = %d, want 4", len(input))
	}
	if input[0].Get("id").String() != "msg-1" ||
		input[1].Get("id").String() != "fc-1" ||
		input[2].Get("id").String() != "assistant-1" ||
		input[3].Get("id").String() != "tool-out-1" {
		t.Fatalf("unexpected merged input order")
	}
	if !bytes.Equal(next, normalized) {
		t.Fatalf("next request snapshot should match normalized request")
	}
}

func TestNormalizeResponsesWebsocketRequestWithPreviousResponseIDIncremental(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"instructions":"be helpful","input":[{"type":"message","id":"msg-1"}]}`)
	lastResponseOutput := []byte(`[
		{"type":"function_call","id":"fc-1","call_id":"call-1"},
		{"type":"message","id":"assistant-1"}
	]`)
	raw := []byte(`{"type":"response.create","previous_response_id":"resp-1","input":[{"type":"function_call_output","call_id":"call-1","id":"tool-out-1"}]}`)

	normalized, next, errMsg := normalizeResponsesWebsocketRequestWithMode(raw, lastRequest, lastResponseOutput, true, false)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	if gjson.GetBytes(normalized, "type").Exists() {
		t.Fatalf("normalized request must not include type field")
	}
	if gjson.GetBytes(normalized, "previous_response_id").String() != "resp-1" {
		t.Fatalf("previous_response_id must be preserved in incremental mode")
	}
	input := gjson.GetBytes(normalized, "input").Array()
	if len(input) != 1 {
		t.Fatalf("incremental input len = %d, want 1", len(input))
	}
	if input[0].Get("id").String() != "tool-out-1" {
		t.Fatalf("unexpected incremental input item id: %s", input[0].Get("id").String())
	}
	if gjson.GetBytes(normalized, "model").String() != "test-model" {
		t.Fatalf("unexpected model: %s", gjson.GetBytes(normalized, "model").String())
	}
	if gjson.GetBytes(normalized, "instructions").String() != "be helpful" {
		t.Fatalf("unexpected instructions: %s", gjson.GetBytes(normalized, "instructions").String())
	}
	if !bytes.Equal(next, normalized) {
		t.Fatalf("next request snapshot should match normalized request")
	}
}

func TestNormalizeResponsesWebsocketRequestInjectsPreviousResponseIDForIncremental(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"instructions":"be helpful","input":[{"type":"message","id":"msg-1"}]}`)
	lastResponseOutput := []byte(`[
		{"type":"function_call","id":"fc-1","call_id":"call-1"},
		{"type":"message","id":"assistant-1"}
	]`)
	raw := []byte(`{"type":"response.create","input":[{"type":"function_call_output","call_id":"call-1","id":"tool-out-1"}]}`)

	normalized, next, errMsg := normalizeResponsesWebsocketRequestWithLastResponseID(raw, lastRequest, lastResponseOutput, "resp-1", true, false)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	if got := gjson.GetBytes(normalized, "previous_response_id").String(); got != "resp-1" {
		t.Fatalf("previous_response_id = %q, want resp-1", got)
	}
	input := gjson.GetBytes(normalized, "input").Array()
	if len(input) != 1 {
		t.Fatalf("incremental input len = %d, want 1: %s", len(input), normalized)
	}
	if input[0].Get("id").String() != "tool-out-1" {
		t.Fatalf("unexpected incremental input item id: %s", input[0].Get("id").String())
	}
	if gjson.GetBytes(normalized, "model").String() != "test-model" {
		t.Fatalf("unexpected model: %s", gjson.GetBytes(normalized, "model").String())
	}
	if gjson.GetBytes(normalized, "instructions").String() != "be helpful" {
		t.Fatalf("unexpected instructions: %s", gjson.GetBytes(normalized, "instructions").String())
	}
	if !bytes.Equal(next, normalized) {
		t.Fatalf("next request snapshot should match normalized request")
	}
}

func TestNormalizeResponsesWebsocketRequestInjectsPreviousResponseIDWhenPendingOutputIsPresent(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"instructions":"be helpful","input":[{"type":"message","id":"msg-1"}]}`)
	lastResponseOutput := []byte(`[]`)
	raw := []byte(`{"type":"response.create","input":[{"type":"function_call_output","call_id":"call-1","id":"tool-out-1"}]}`)

	normalized, _, errMsg := normalizeResponsesWebsocketRequestWithIncrementalState(raw, lastRequest, lastResponseOutput, "resp-1", []string{"call-1"}, true, false)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	if got := gjson.GetBytes(normalized, "previous_response_id").String(); got != "resp-1" {
		t.Fatalf("previous_response_id = %q, want resp-1", got)
	}
	input := gjson.GetBytes(normalized, "input").Array()
	if len(input) != 1 || input[0].Get("id").String() != "tool-out-1" {
		t.Fatalf("unexpected incremental input: %s", normalized)
	}
}

func TestNormalizeResponsesWebsocketRequestSkipsPreviousResponseIDWhenPendingOutputIsMissing(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"instructions":"be helpful","input":[{"type":"message","id":"msg-1"}]}`)
	lastResponseOutput := []byte(`[
		{"type":"function_call","id":"fc-1","call_id":"call-1"}
	]`)
	raw := []byte(`{"type":"response.create","input":[{"type":"message","role":"user","id":"summary-1","content":"compacted summary"}]}`)

	normalized, next, errMsg := normalizeResponsesWebsocketRequestWithIncrementalState(raw, lastRequest, lastResponseOutput, "resp-1", []string{"call-1"}, true, false)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	if gjson.GetBytes(normalized, "previous_response_id").Exists() {
		t.Fatalf("previous_response_id must not be injected when pending tool output is missing: %s", normalized)
	}
	input := gjson.GetBytes(normalized, "input").Array()
	if len(input) != 1 {
		t.Fatalf("replacement input len = %d, want 1: %s", len(input), normalized)
	}
	if input[0].Get("id").String() != "summary-1" {
		t.Fatalf("unexpected replacement input: %s", normalized)
	}
	if !bytes.Equal(next, normalized) {
		t.Fatalf("next request snapshot should match normalized request")
	}
}

func TestNormalizeResponsesWebsocketRequestReplacesCodexLocalCompactionTranscript(t *testing.T) {
	lastRequest := []byte(`{"model":"gpt-5.6-sol","stream":true,"instructions":"be helpful","input":[
		{"type":"message","role":"user","id":"old-user","content":[{"type":"input_text","text":"old prompt"}]},
		{"type":"function_call_output","id":"old-tool-output","call_id":"old-call","output":"old result"}
	]}`)
	lastResponseOutput := []byte(`[
		{"type":"function_call","id":"old-tool-call","call_id":"old-call","name":"lookup","arguments":"{}"},
		{"type":"message","role":"assistant","id":"old-assistant","content":[{"type":"output_text","text":"old answer"}]}
	]`)
	raw := []byte(fmt.Sprintf(`{"type":"response.create","input":[
		{"type":"additional_tools","role":"developer","tools":[]},
		{"role":"developer","id":"initial-context","content":"workspace context"},
		{"type":"message","role":"user","id":"compacted-user","content":[{"type":"input_text","text":"retained context"}]},
		{"role":"user","id":"local-summary","content":%q},
		{"type":"message","role":"developer","id":"turn-context","content":[{"type":"input_text","text":"current workspace context"}]},
		{"role":"user","id":"incoming-user","content":"continue the task"}
	],"parallel_tool_calls":true,"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"}}`, codexLocalCompactionSummaryPrefix+"\nThe compacted summary."))

	normalized, next, errMsg := normalizeResponsesWebsocketRequestWithMode(raw, lastRequest, lastResponseOutput, false, false)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	if gjson.GetBytes(normalized, "previous_response_id").Exists() {
		t.Fatalf("replacement request must not include previous_response_id: %s", normalized)
	}
	if got, want := gjson.GetBytes(normalized, "input").Raw, gjson.GetBytes(raw, "input").Raw; got != want {
		t.Fatalf("replacement input did not preserve the complete new transcript:\n got: %s\nwant: %s", got, want)
	}
	input := gjson.GetBytes(normalized, "input").Array()
	wantIDs := []string{"", "initial-context", "compacted-user", "local-summary", "turn-context", "incoming-user"}
	if len(input) != len(wantIDs) {
		t.Fatalf("replacement input len = %d, want %d: %s", len(input), len(wantIDs), normalized)
	}
	for index, wantID := range wantIDs {
		if got := input[index].Get("id").String(); got != wantID {
			t.Fatalf("replacement input[%d].id = %q, want %q: %s", index, got, wantID, normalized)
		}
	}
	if got := input[0].Get("type").String(); got != "additional_tools" {
		t.Fatalf("input[0].type = %q, want additional_tools: %s", got, normalized)
	}
	if got := input[0].Get("role").String(); got != "developer" {
		t.Fatalf("input[0].role = %q, want developer: %s", got, normalized)
	}
	if tools := input[0].Get("tools"); !tools.IsArray() || len(tools.Array()) != 0 {
		t.Fatalf("input[0] empty tools array was not preserved: %s", normalized)
	}
	for _, staleID := range []string{"old-user", "old-tool-output", "old-tool-call", "old-assistant"} {
		if bytes.Contains(normalized, []byte(staleID)) {
			t.Fatalf("replacement input contains stale item %q: %s", staleID, normalized)
		}
	}
	if got := gjson.GetBytes(normalized, "model").String(); got != "gpt-5.6-sol" {
		t.Fatalf("model = %q, want gpt-5.6-sol", got)
	}
	if got := gjson.GetBytes(normalized, "instructions").String(); got != "be helpful" {
		t.Fatalf("instructions = %q, want be helpful", got)
	}
	if !gjson.GetBytes(normalized, "stream").Bool() {
		t.Fatalf("stream must be enabled: %s", normalized)
	}
	if !gjson.GetBytes(normalized, "parallel_tool_calls").Bool() {
		t.Fatalf("parallel_tool_calls was not preserved: %s", normalized)
	}
	if got := gjson.GetBytes(normalized, "client_metadata.ws_request_header_x_openai_internal_codex_responses_lite").String(); got != "true" {
		t.Fatalf("Responses Lite client metadata = %q, want true: %s", got, normalized)
	}
	if !bytes.Equal(next, normalized) {
		t.Fatalf("next request snapshot should match normalized request")
	}
}

func TestShouldReplaceWebsocketTranscriptCodexLocalCompactionSemantics(t *testing.T) {
	compactedInput := gjson.Parse(fmt.Sprintf(`[
		{"type":"message","role":"developer","content":[{"type":"input_text","text":"initial context"}]},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"retained context"}]},
		{"type":"message","role":"user","content":[{"type":"input_text","text":%q}]}
	]`, codexLocalCompactionSummaryPrefix+"\nSummary body."))
	if !shouldReplaceWebsocketTranscript([]byte(`{"type":"response.create"}`), compactedInput) {
		t.Fatal("Codex local compaction input must replace the websocket transcript")
	}
	for _, request := range []string{
		`{"type":"response.create","previous_response_id":"resp-1"}`,
		`{"type":"response.create","previous_response_id":""}`,
		`{"type":"response.create","previous_response_id":null}`,
	} {
		if shouldReplaceWebsocketTranscript([]byte(request), compactedInput) {
			t.Fatalf("request carrying previous_response_id must not use the local compaction rule: %s", request)
		}
	}
	if shouldReplaceWebsocketTranscript([]byte(`{"type":"response.append"}`), compactedInput) {
		t.Fatal("response.append must not be treated as a full local compaction reset")
	}

	ordinaryInput := gjson.Parse(`[
		{"type":"message","role":"developer","content":"Please summarize future messages."},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"Please create a compacted summary of this text."}]}
	]`)
	if shouldReplaceWebsocketTranscript([]byte(`{"type":"response.create"}`), ordinaryInput) {
		t.Fatal("ordinary user/developer input must not replace the transcript")
	}
}

func TestCodexLocalCompactionSummaryContentShapes(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{name: "string content", content: fmt.Sprintf(`%q`, codexLocalCompactionSummaryPrefix+"\nSummary body."), want: true},
		{name: "multiple input text parts", content: fmt.Sprintf(`[{"type":"input_text","text":%q},{"type":"input_text","text":"\nSummary body."}]`, codexLocalCompactionSummaryPrefix), want: true},
		{name: "non-text part before summary", content: fmt.Sprintf(`[{"type":"input_image","image_url":"data:image/png;base64,AA=="},{"type":"input_text","text":%q}]`, codexLocalCompactionSummaryPrefix+"\nSummary body."), want: true},
		{name: "bare prefix", content: fmt.Sprintf(`%q`, codexLocalCompactionSummaryPrefix), want: false},
		{name: "prefix followed by space", content: fmt.Sprintf(`%q`, codexLocalCompactionSummaryPrefix+" Summary body."), want: false},
		{name: "summary after ordinary text", content: fmt.Sprintf(`[{"type":"input_text","text":"ordinary text"},{"type":"input_text","text":%q}]`, codexLocalCompactionSummaryPrefix+"\nSummary body."), want: false},
		{name: "developer summary", content: fmt.Sprintf(`%q`, codexLocalCompactionSummaryPrefix+"\nSummary body."), want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			role := "user"
			if test.name == "developer summary" {
				role = "developer"
			}
			input := gjson.Parse(fmt.Sprintf(`[{"type":"message","role":%q,"content":%s}]`, role, test.content))
			if got := inputHasCodexLocalCompactionSummary(input); got != test.want {
				t.Fatalf("inputHasCodexLocalCompactionSummary() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestCodexLocalCompactionSummaryAdditionalToolsConstraints(t *testing.T) {
	summary := fmt.Sprintf(`{"role":"user","content":%q}`, codexLocalCompactionSummaryPrefix+"\nSummary body.")
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{name: "Responses Lite tools first", input: fmt.Sprintf(`[{"type":"additional_tools","role":"developer","tools":[{"type":"custom","name":"exec"}]},%s]`, summary), want: true},
		{name: "tools after message", input: fmt.Sprintf(`[%s,{"type":"additional_tools","role":"developer","tools":[{"type":"custom","name":"exec"}]}]`, summary)},
		{name: "tools with user role", input: fmt.Sprintf(`[{"type":"additional_tools","role":"user","tools":[{"type":"custom","name":"exec"}]},%s]`, summary)},
		{name: "tools missing array", input: fmt.Sprintf(`[{"type":"additional_tools","role":"developer"},%s]`, summary)},
		{name: "tools not array", input: fmt.Sprintf(`[{"type":"additional_tools","role":"developer","tools":{}},%s]`, summary)},
		{name: "tools empty", input: fmt.Sprintf(`[{"type":"additional_tools","role":"developer","tools":[]},%s]`, summary), want: true},
		{name: "malformed tool", input: fmt.Sprintf(`[{"type":"additional_tools","role":"developer","tools":[null]},%s]`, summary)},
		{name: "arbitrary input item", input: fmt.Sprintf(`[{"type":"unknown","role":"developer"},%s]`, summary)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := inputHasCodexLocalCompactionSummary(gjson.Parse(test.input)); got != test.want {
				t.Fatalf("inputHasCodexLocalCompactionSummary() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestCodexLocalCompactionSummaryRejectsOrdinaryHistoryItems(t *testing.T) {
	tests := []struct {
		name        string
		historyItem string
		wantReplace bool
	}{
		{name: "reasoning", historyItem: `{"type":"reasoning","id":"reasoning-1"}`},
		{name: "assistant", historyItem: `{"type":"message","role":"assistant","id":"assistant-1"}`, wantReplace: true},
		{name: "function call", historyItem: `{"type":"function_call","call_id":"call-1"}`, wantReplace: true},
		{name: "function call output", historyItem: `{"type":"function_call_output","call_id":"call-1"}`},
		{name: "custom tool call", historyItem: `{"type":"custom_tool_call","call_id":"call-1"}`, wantReplace: true},
		{name: "custom tool call output", historyItem: `{"type":"custom_tool_call_output","call_id":"call-1"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := gjson.Parse(fmt.Sprintf(`[%s,{"type":"message","role":"user","content":[{"type":"input_text","text":%q}]}]`, test.historyItem, codexLocalCompactionSummaryPrefix+"\nSummary body."))
			if inputHasCodexLocalCompactionSummary(input) {
				t.Fatal("ordinary transcript history must not match the local user-summary shape")
			}
			if got := shouldReplaceWebsocketTranscript([]byte(`{"type":"response.create"}`), input); got != test.wantReplace {
				t.Fatalf("shouldReplaceWebsocketTranscript() = %t, want %t", got, test.wantReplace)
			}
		})
	}
}

func TestNormalizeResponsesWebsocketRequestWithPreviousResponseIDMergedWhenIncrementalDisabled(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"input":[{"type":"message","id":"msg-1"}]}`)
	lastResponseOutput := []byte(`[
		{"type":"function_call","id":"fc-1","call_id":"call-1"},
		{"type":"message","id":"assistant-1"}
	]`)
	raw := []byte(`{"type":"response.create","previous_response_id":"resp-1","input":[{"type":"function_call_output","call_id":"call-1","id":"tool-out-1"}]}`)

	normalized, next, errMsg := normalizeResponsesWebsocketRequestWithMode(raw, lastRequest, lastResponseOutput, false, false)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	if gjson.GetBytes(normalized, "previous_response_id").Exists() {
		t.Fatalf("previous_response_id must be removed when incremental mode is disabled")
	}
	input := gjson.GetBytes(normalized, "input").Array()
	if len(input) != 4 {
		t.Fatalf("merged input len = %d, want 4", len(input))
	}
	if input[0].Get("id").String() != "msg-1" ||
		input[1].Get("id").String() != "fc-1" ||
		input[2].Get("id").String() != "assistant-1" ||
		input[3].Get("id").String() != "tool-out-1" {
		t.Fatalf("unexpected merged input order")
	}
	if !bytes.Equal(next, normalized) {
		t.Fatalf("next request snapshot should match normalized request")
	}
}

func TestNormalizeResponsesWebsocketRequestAppend(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"input":[{"type":"message","id":"msg-1"}]}`)
	lastResponseOutput := []byte(`[
		{"type":"message","id":"assistant-1"},
		{"type":"function_call_output","id":"tool-out-1"}
	]`)
	raw := []byte(`{"type":"response.append","input":[{"type":"message","id":"msg-2"},{"type":"message","id":"msg-3"}]}`)

	normalized, next, errMsg := normalizeResponsesWebsocketRequest(raw, lastRequest, lastResponseOutput)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	input := gjson.GetBytes(normalized, "input").Array()
	if len(input) != 5 {
		t.Fatalf("merged input len = %d, want 5", len(input))
	}
	if input[0].Get("id").String() != "msg-1" ||
		input[1].Get("id").String() != "assistant-1" ||
		input[2].Get("id").String() != "tool-out-1" ||
		input[3].Get("id").String() != "msg-2" ||
		input[4].Get("id").String() != "msg-3" {
		t.Fatalf("unexpected merged input order")
	}
	if !bytes.Equal(next, normalized) {
		t.Fatalf("next request snapshot should match normalized append request")
	}
}

func TestNormalizeResponsesWebsocketRequestAppendWithoutCreate(t *testing.T) {
	raw := []byte(`{"type":"response.append","input":[]}`)

	_, _, errMsg := normalizeResponsesWebsocketRequest(raw, nil, nil)
	if errMsg == nil {
		t.Fatalf("expected error for append without previous request")
	}
	if errMsg.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", errMsg.StatusCode, http.StatusBadRequest)
	}
}

func TestWebsocketJSONPayloadsFromChunk(t *testing.T) {
	chunk := []byte("event: response.created\n\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\"}}\n\ndata: [DONE]\n")

	payloads := websocketJSONPayloadsFromChunk(chunk)
	if len(payloads) != 1 {
		t.Fatalf("payloads len = %d, want 1", len(payloads))
	}
	if gjson.GetBytes(payloads[0], "type").String() != "response.created" {
		t.Fatalf("unexpected payload type: %s", gjson.GetBytes(payloads[0], "type").String())
	}
}

func TestWebsocketJSONPayloadsFromPlainJSONChunk(t *testing.T) {
	chunk := []byte(`{"type":"response.completed","response":{"id":"resp-1"}}`)

	payloads := websocketJSONPayloadsFromChunk(chunk)
	if len(payloads) != 1 {
		t.Fatalf("payloads len = %d, want 1", len(payloads))
	}
	if gjson.GetBytes(payloads[0], "type").String() != "response.completed" {
		t.Fatalf("unexpected payload type: %s", gjson.GetBytes(payloads[0], "type").String())
	}
}

func TestResponseCompletedOutputFromPayload(t *testing.T) {
	payload := []byte(`{"type":"response.completed","response":{"id":"resp-1","output":[{"type":"message","id":"out-1"}]}}`)

	output := responseCompletedOutputFromPayload(payload, nil, nil)
	items := gjson.ParseBytes(output).Array()
	if len(items) != 1 {
		t.Fatalf("output len = %d, want 1", len(items))
	}
	if items[0].Get("id").String() != "out-1" {
		t.Fatalf("unexpected output id: %s", items[0].Get("id").String())
	}
}

func TestResponseCompletedOutputFromPayloadDropsIncompleteCollectedToolCalls(t *testing.T) {
	payload := []byte(`{"type":"response.completed","response":{"id":"resp-1","output":[]}}`)
	collector := map[int64][]byte{
		0: []byte(`{"type":"message","id":"msg-1"}`),
		1: []byte(`{"type":"function_call","call_id":"call-1","name":"exec"}`),
		2: []byte(`{"type":"custom_tool_call","call_id":"call-2","name":"exec","input":"pwd"}`),
	}

	output := responseCompletedOutputFromPayload(payload, collector, nil)
	items := gjson.ParseBytes(output).Array()
	if len(items) != 2 {
		t.Fatalf("output len = %d, want 2: %s", len(items), output)
	}
	if items[0].Get("type").String() != "message" || items[0].Get("id").String() != "msg-1" {
		t.Fatalf("unexpected first output item: %s", items[0].Raw)
	}
	if items[1].Get("type").String() != "custom_tool_call" || items[1].Get("call_id").String() != "call-2" {
		t.Fatalf("unexpected second output item: %s", items[1].Raw)
	}
}

func TestRestoreResponsesWebsocketCompletionOutputPreservesNonEmptyOutput(t *testing.T) {
	payload := []byte(`{"type":"response.completed","response":{"id":"resp-1","output":[{"type":"message","id":"out-1"}]}}`)
	collector := map[int64][]byte{0: []byte(`{"type":"function_call","id":"call-1","call_id":"call-1"}`)}

	restored := restoreResponsesWebsocketCompletionOutput(payload, collector, nil)
	if string(restored) != string(payload) {
		t.Fatalf("non-empty completion output was overwritten: %s", restored)
	}
}

func TestRestoreResponsesWebsocketCompletionOutputReconcilesConflictingToolCall(t *testing.T) {
	payload := []byte(`{"type":"response.completed","response":{"id":"resp-1","output":[{"type":"message","id":"msg-1"},{"type":"function_call","call_id":"call-1","name":"exec"}]}}`)
	collector := map[int64][]byte{0: []byte(`{"type":"custom_tool_call","id":"ctc-1","call_id":"call-1","name":"exec","input":"pwd","status":"completed"}`)}

	restored := restoreResponsesWebsocketCompletionOutput(payload, collector, nil)
	output := gjson.GetBytes(restored, "response.output").Array()
	if len(output) != 2 {
		t.Fatalf("restored output len = %d, want 2: %s", len(output), restored)
	}
	if output[0].Get("type").String() != "message" || output[0].Get("id").String() != "msg-1" {
		t.Fatalf("unrelated completion item changed: %s", output[0].Raw)
	}
	if output[1].Get("type").String() != "custom_tool_call" || output[1].Get("call_id").String() != "call-1" {
		t.Fatalf("conflicting tool call was not reconciled: %s", output[1].Raw)
	}
	if input := output[1].Get("input"); input.Type != gjson.String || input.String() != "pwd" {
		t.Fatalf("reconciled custom tool input = %s, want string pwd", input.Raw)
	}

	lastRequest := []byte(`{"model":"gpt-test","stream":true,"input":[{"type":"message","id":"user-1","role":"user","content":"run pwd"}]}`)
	nextRequest := []byte(`{"type":"response.create","previous_response_id":"resp-1","input":[{"type":"custom_tool_call_output","call_id":"call-1","output":"ok"}]}`)
	completedOutput := []byte(gjson.GetBytes(restored, "response.output").Raw)
	normalized, _, errMsg := normalizeResponsesWebsocketRequestWithIncrementalState(
		nextRequest,
		lastRequest,
		completedOutput,
		"resp-1",
		[]string{"call-1"},
		false,
		false,
	)
	if errMsg != nil {
		t.Fatalf("normalize next request: %v", errMsg.Error)
	}
	if gjson.GetBytes(normalized, "previous_response_id").Exists() {
		t.Fatalf("previous_response_id must not be forwarded to HTTP/SSE upstream: %s", normalized)
	}
	input := gjson.GetBytes(normalized, "input").Array()
	if len(input) != 4 {
		t.Fatalf("replayed input len = %d, want 4: %s", len(input), normalized)
	}
	if input[2].Get("type").String() != "custom_tool_call" || input[2].Get("input").String() != "pwd" {
		t.Fatalf("replayed tool call is invalid: %s", input[2].Raw)
	}
	if input[3].Get("type").String() != "custom_tool_call_output" || input[3].Get("call_id").String() != "call-1" {
		t.Fatalf("replayed tool output is invalid: %s", input[3].Raw)
	}

	cache := newWebsocketToolOutputCache(time.Minute, 10)
	donePayload := []byte(`{"type":"response.output_item.done","item":{"type":"custom_tool_call","id":"ctc-1","call_id":"call-1","name":"exec","input":"pwd","status":"completed"}}`)
	recordResponsesWebsocketToolCallsFromPayloadWithCache(cache, "session-1", donePayload)
	recordResponsesWebsocketToolCallsFromPayloadWithCache(cache, "session-1", restored)
	cached, ok := cache.get("session-1", "call-1")
	if !ok {
		t.Fatalf("reconciled custom tool call was not cached")
	}
	if gjson.GetBytes(cached, "type").String() != "custom_tool_call" || gjson.GetBytes(cached, "input").String() != "pwd" {
		t.Fatalf("cached tool call is invalid: %s", cached)
	}
}

func TestRestoreResponsesWebsocketCompletionOutputIgnoresIncompleteCollectedToolCall(t *testing.T) {
	payload := []byte(`{"type":"response.completed","response":{"id":"resp-1","output":[{"type":"function_call","call_id":"call-1","name":"exec"}]}}`)
	collector := map[int64][]byte{0: []byte(`{"type":"custom_tool_call","call_id":"call-1","name":"exec"}`)}

	restored := restoreResponsesWebsocketCompletionOutput(payload, collector, nil)
	if string(restored) != string(payload) {
		t.Fatalf("incomplete collected tool call overwrote completion output: %s", restored)
	}
}

func TestIsCompleteResponsesWebsocketToolCallRequiresStringFields(t *testing.T) {
	tests := []struct {
		name string
		item string
		want bool
	}{
		{name: "numeric call id", item: `{"type":"function_call","call_id":123,"name":"exec","arguments":"{}"}`},
		{name: "boolean name", item: `{"type":"function_call","call_id":"call-1","name":true,"arguments":"{}"}`},
		{name: "numeric arguments", item: `{"type":"function_call","call_id":"call-1","name":"exec","arguments":123}`},
		{name: "object custom input", item: `{"type":"custom_tool_call","call_id":"call-1","name":"exec","input":{}}`},
		{name: "valid function call", item: `{"type":"function_call","call_id":"call-1","name":"exec","arguments":""}`, want: true},
		{name: "valid custom tool call", item: `{"type":"custom_tool_call","call_id":"call-1","name":"exec","input":""}`, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isCompleteResponsesWebsocketToolCall(gjson.Parse(tt.item)); got != tt.want {
				t.Fatalf("isCompleteResponsesWebsocketToolCall() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestAppendWebsocketEvent(t *testing.T) {
	var builder strings.Builder

	appendWebsocketEvent(&builder, "request", []byte("  {\"type\":\"response.create\"}\n"))
	appendWebsocketEvent(&builder, "response", []byte("{\"type\":\"response.created\"}"))

	got := builder.String()
	if !strings.Contains(got, "websocket.request\n{\"type\":\"response.create\"}\n") {
		t.Fatalf("request event not found in body: %s", got)
	}
	if !strings.Contains(got, "websocket.response\n{\"type\":\"response.created\"}\n") {
		t.Fatalf("response event not found in body: %s", got)
	}
}

func TestAppendWebsocketTimelineEvent(t *testing.T) {
	var builder strings.Builder
	ts := time.Date(2026, time.April, 1, 12, 34, 56, 789000000, time.UTC)

	appendWebsocketTimelineEvent(&builder, "request", []byte("  {\"type\":\"response.create\"}\n"), ts)

	got := builder.String()
	if !strings.Contains(got, "Timestamp: 2026-04-01T12:34:56.789Z") {
		t.Fatalf("timeline timestamp not found: %s", got)
	}
	if !strings.Contains(got, "Event: websocket.request") {
		t.Fatalf("timeline event not found: %s", got)
	}
	if !strings.Contains(got, "{\"type\":\"response.create\"}") {
		t.Fatalf("timeline payload not found: %s", got)
	}
}

func TestSetWebsocketTimelineBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	setWebsocketTimelineBody(c, " \n ")
	if _, exists := c.Get(wsTimelineBodyKey); exists {
		t.Fatalf("timeline body key should not be set for empty body")
	}

	setWebsocketTimelineBody(c, "timeline body")
	value, exists := c.Get(wsTimelineBodyKey)
	if !exists {
		t.Fatalf("timeline body key not set")
	}
	bodyBytes, ok := value.([]byte)
	if !ok {
		t.Fatalf("timeline body key type mismatch")
	}
	if string(bodyBytes) != "timeline body" {
		t.Fatalf("timeline body = %q, want %q", string(bodyBytes), "timeline body")
	}
}

func TestWebsocketTimelineLogFallsBackToMemoryWithoutSource(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	ts := time.Date(2026, time.April, 1, 12, 34, 56, 789000000, time.UTC)

	timelineLog := newWebsocketTimelineLog(true, nil)
	timelineLog.BeginRequest()
	timelineLog.Append("request", []byte(`{"type":"response.create"}`), ts)
	timelineLog.SetContext(c)

	value, exists := c.Get(wsTimelineBodyKey)
	if !exists {
		t.Fatalf("timeline body key not set")
	}
	bodyBytes, ok := value.([]byte)
	if !ok {
		t.Fatalf("timeline body key type mismatch")
	}
	got := string(bodyBytes)
	if !strings.Contains(got, "Event: websocket.request") {
		t.Fatalf("timeline event not found: %s", got)
	}
	if !strings.Contains(got, `{"type":"response.create"}`) {
		t.Fatalf("timeline payload not found: %s", got)
	}
}

func TestRepairResponsesWebsocketToolCallsInsertsCachedOutput(t *testing.T) {
	cache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	cacheWarm := []byte(`{"previous_response_id":"resp-1","input":[{"type":"function_call_output","call_id":"call-1","output":"ok"}]}`)
	warmed := repairResponsesWebsocketToolCallsWithCache(cache, sessionKey, cacheWarm)
	if gjson.GetBytes(warmed, "input.0.call_id").String() != "call-1" {
		t.Fatalf("expected warmup output to remain")
	}

	raw := []byte(`{"input":[{"type":"function_call","call_id":"call-1","name":"tool"},{"type":"message","id":"msg-1"}]}`)
	repaired := repairResponsesWebsocketToolCallsWithCache(cache, sessionKey, raw)

	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 3 {
		t.Fatalf("repaired input len = %d, want 3", len(input))
	}
	if input[0].Get("type").String() != "function_call" || input[0].Get("call_id").String() != "call-1" {
		t.Fatalf("unexpected first item: %s", input[0].Raw)
	}
	if input[1].Get("type").String() != "function_call_output" || input[1].Get("call_id").String() != "call-1" {
		t.Fatalf("missing inserted output: %s", input[1].Raw)
	}
	if input[2].Get("type").String() != "message" || input[2].Get("id").String() != "msg-1" {
		t.Fatalf("unexpected trailing item: %s", input[2].Raw)
	}
}

func TestRepairResponsesWebsocketToolCallsDropsOrphanFunctionCall(t *testing.T) {
	cache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	raw := []byte(`{"input":[{"type":"function_call","call_id":"call-1","name":"tool"},{"type":"message","id":"msg-1"}]}`)
	repaired := repairResponsesWebsocketToolCallsWithCache(cache, sessionKey, raw)

	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 1 {
		t.Fatalf("repaired input len = %d, want 1", len(input))
	}
	if input[0].Get("type").String() != "message" || input[0].Get("id").String() != "msg-1" {
		t.Fatalf("unexpected remaining item: %s", input[0].Raw)
	}
}

func TestRepairResponsesWebsocketToolCallsInsertsCachedCallForOrphanOutput(t *testing.T) {
	outputCache := newWebsocketToolOutputCache(time.Minute, 10)
	callCache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	callCache.record(sessionKey, "call-1", []byte(`{"type":"function_call","call_id":"call-1","name":"tool"}`))

	raw := []byte(`{"input":[{"type":"function_call_output","call_id":"call-1","output":"ok"},{"type":"message","id":"msg-1"}]}`)
	repaired := repairResponsesWebsocketToolCallsWithCaches(outputCache, callCache, sessionKey, raw)

	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 3 {
		t.Fatalf("repaired input len = %d, want 3", len(input))
	}
	if input[0].Get("type").String() != "function_call" || input[0].Get("call_id").String() != "call-1" {
		t.Fatalf("missing inserted call: %s", input[0].Raw)
	}
	if input[1].Get("type").String() != "function_call_output" || input[1].Get("call_id").String() != "call-1" {
		t.Fatalf("unexpected output item: %s", input[1].Raw)
	}
	if input[2].Get("type").String() != "message" || input[2].Get("id").String() != "msg-1" {
		t.Fatalf("unexpected trailing item: %s", input[2].Raw)
	}
}

func TestRepairResponsesWebsocketToolCallsKeepsPreviousResponseOutputIncremental(t *testing.T) {
	outputCache := newWebsocketToolOutputCache(time.Minute, 10)
	callCache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	callCache.record(sessionKey, "call-1", []byte(`{"type":"function_call","id":"fc-1","call_id":"call-1","name":"tool"}`))

	raw := []byte(`{"previous_response_id":"resp-latest","input":[{"type":"function_call_output","call_id":"call-1","id":"tool-out-1","output":"ok"},{"type":"message","id":"msg-1"}]}`)
	repaired := repairResponsesWebsocketToolCallsWithCaches(outputCache, callCache, sessionKey, raw)

	if got := gjson.GetBytes(repaired, "previous_response_id").String(); got != "resp-latest" {
		t.Fatalf("previous_response_id = %q, want resp-latest", got)
	}
	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 2 {
		t.Fatalf("repaired input len = %d, want 2: %s", len(input), repaired)
	}
	if input[0].Get("type").String() != "function_call_output" || input[0].Get("call_id").String() != "call-1" {
		t.Fatalf("unexpected output item: %s", input[0].Raw)
	}
	if input[1].Get("type").String() != "message" || input[1].Get("id").String() != "msg-1" {
		t.Fatalf("unexpected trailing item: %s", input[1].Raw)
	}
}

func TestRepairResponsesWebsocketToolCallsKeepsPreviousResponseCallIncremental(t *testing.T) {
	outputCache := newWebsocketToolOutputCache(time.Minute, 10)
	callCache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	outputCache.record(sessionKey, "call-1", []byte(`{"type":"function_call_output","call_id":"call-1","id":"tool-out-1","output":"ok"}`))

	raw := []byte(`{"previous_response_id":"resp-latest","input":[{"type":"function_call","id":"fc-1","call_id":"call-1","name":"tool"},{"type":"message","id":"msg-1"}]}`)
	repaired := repairResponsesWebsocketToolCallsWithCaches(outputCache, callCache, sessionKey, raw)

	if got := gjson.GetBytes(repaired, "previous_response_id").String(); got != "resp-latest" {
		t.Fatalf("previous_response_id = %q, want resp-latest", got)
	}
	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 2 {
		t.Fatalf("repaired input len = %d, want 2: %s", len(input), repaired)
	}
	if input[0].Get("type").String() != "function_call" || input[0].Get("call_id").String() != "call-1" {
		t.Fatalf("unexpected call item: %s", input[0].Raw)
	}
	if input[1].Get("type").String() != "message" || input[1].Get("id").String() != "msg-1" {
		t.Fatalf("unexpected trailing item: %s", input[1].Raw)
	}
}

func TestRepairResponsesWebsocketToolCallsDropsOrphanOutputWhenCallMissing(t *testing.T) {
	outputCache := newWebsocketToolOutputCache(time.Minute, 10)
	callCache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	raw := []byte(`{"input":[{"type":"function_call_output","call_id":"call-1","output":"ok"},{"type":"message","id":"msg-1"}]}`)
	repaired := repairResponsesWebsocketToolCallsWithCaches(outputCache, callCache, sessionKey, raw)

	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 1 {
		t.Fatalf("repaired input len = %d, want 1", len(input))
	}
	if input[0].Get("type").String() != "message" || input[0].Get("id").String() != "msg-1" {
		t.Fatalf("unexpected remaining item: %s", input[0].Raw)
	}
}

func TestRepairResponsesWebsocketToolCallsInsertsCachedCustomToolOutput(t *testing.T) {
	cache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	cacheWarm := []byte(`{"previous_response_id":"resp-1","input":[{"type":"custom_tool_call_output","call_id":"call-1","output":"ok"}]}`)
	warmed := repairResponsesWebsocketToolCallsWithCache(cache, sessionKey, cacheWarm)
	if gjson.GetBytes(warmed, "input.0.call_id").String() != "call-1" {
		t.Fatalf("expected warmup output to remain")
	}

	raw := []byte(`{"input":[{"type":"custom_tool_call","call_id":"call-1","name":"apply_patch"},{"type":"message","id":"msg-1"}]}`)
	repaired := repairResponsesWebsocketToolCallsWithCache(cache, sessionKey, raw)

	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 3 {
		t.Fatalf("repaired input len = %d, want 3", len(input))
	}
	if input[0].Get("type").String() != "custom_tool_call" || input[0].Get("call_id").String() != "call-1" {
		t.Fatalf("unexpected first item: %s", input[0].Raw)
	}
	if input[1].Get("type").String() != "custom_tool_call_output" || input[1].Get("call_id").String() != "call-1" {
		t.Fatalf("missing inserted output: %s", input[1].Raw)
	}
	if input[2].Get("type").String() != "message" || input[2].Get("id").String() != "msg-1" {
		t.Fatalf("unexpected trailing item: %s", input[2].Raw)
	}
}

func TestRepairResponsesWebsocketToolCallsDropsOrphanCustomToolCall(t *testing.T) {
	cache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	raw := []byte(`{"input":[{"type":"custom_tool_call","call_id":"call-1","name":"apply_patch"},{"type":"message","id":"msg-1"}]}`)
	repaired := repairResponsesWebsocketToolCallsWithCache(cache, sessionKey, raw)

	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 1 {
		t.Fatalf("repaired input len = %d, want 1", len(input))
	}
	if input[0].Get("type").String() != "message" || input[0].Get("id").String() != "msg-1" {
		t.Fatalf("unexpected remaining item: %s", input[0].Raw)
	}
}

func TestRepairResponsesWebsocketToolCallsInsertsCachedCustomToolCallForOrphanOutput(t *testing.T) {
	outputCache := newWebsocketToolOutputCache(time.Minute, 10)
	callCache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	callCache.record(sessionKey, "call-1", []byte(`{"type":"custom_tool_call","call_id":"call-1","name":"apply_patch"}`))

	raw := []byte(`{"input":[{"type":"custom_tool_call_output","call_id":"call-1","output":"ok"},{"type":"message","id":"msg-1"}]}`)
	repaired := repairResponsesWebsocketToolCallsWithCaches(outputCache, callCache, sessionKey, raw)

	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 3 {
		t.Fatalf("repaired input len = %d, want 3", len(input))
	}
	if input[0].Get("type").String() != "custom_tool_call" || input[0].Get("call_id").String() != "call-1" {
		t.Fatalf("missing inserted call: %s", input[0].Raw)
	}
	if input[1].Get("type").String() != "custom_tool_call_output" || input[1].Get("call_id").String() != "call-1" {
		t.Fatalf("unexpected output item: %s", input[1].Raw)
	}
	if input[2].Get("type").String() != "message" || input[2].Get("id").String() != "msg-1" {
		t.Fatalf("unexpected trailing item: %s", input[2].Raw)
	}
}

func TestRepairResponsesWebsocketToolCallsKeepsPreviousResponseCustomToolOutputIncremental(t *testing.T) {
	outputCache := newWebsocketToolOutputCache(time.Minute, 10)
	callCache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	callCache.record(sessionKey, "call-1", []byte(`{"type":"custom_tool_call","call_id":"call-1","name":"apply_patch"}`))

	raw := []byte(`{"previous_response_id":"resp-latest","input":[{"type":"custom_tool_call_output","call_id":"call-1","output":"ok"},{"type":"message","id":"msg-1"}]}`)
	repaired := repairResponsesWebsocketToolCallsWithCaches(outputCache, callCache, sessionKey, raw)

	if got := gjson.GetBytes(repaired, "previous_response_id").String(); got != "resp-latest" {
		t.Fatalf("previous_response_id = %q, want resp-latest", got)
	}
	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 2 {
		t.Fatalf("repaired input len = %d, want 2: %s", len(input), repaired)
	}
	if input[0].Get("type").String() != "custom_tool_call_output" || input[0].Get("call_id").String() != "call-1" {
		t.Fatalf("unexpected output item: %s", input[0].Raw)
	}
	if input[1].Get("type").String() != "message" || input[1].Get("id").String() != "msg-1" {
		t.Fatalf("unexpected trailing item: %s", input[1].Raw)
	}
}

func TestRepairResponsesWebsocketToolCallsDropsOrphanCustomToolOutputWhenCallMissing(t *testing.T) {
	outputCache := newWebsocketToolOutputCache(time.Minute, 10)
	callCache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	raw := []byte(`{"input":[{"type":"custom_tool_call_output","call_id":"call-1","output":"ok"},{"type":"message","id":"msg-1"}]}`)
	repaired := repairResponsesWebsocketToolCallsWithCaches(outputCache, callCache, sessionKey, raw)

	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 1 {
		t.Fatalf("repaired input len = %d, want 1", len(input))
	}
	if input[0].Get("type").String() != "message" || input[0].Get("id").String() != "msg-1" {
		t.Fatalf("unexpected remaining item: %s", input[0].Raw)
	}
}

func TestRecordResponsesWebsocketToolCallsIgnoresIncompleteCall(t *testing.T) {
	cache := newWebsocketToolOutputCache(time.Minute, 10)
	pending := make(map[string]struct{})
	payload := []byte(`{"type":"response.output_item.done","item":{"type":"function_call","call_id":"call-1","name":"exec"}}`)

	recordResponsesWebsocketToolCallsFromPayloadWithCache(cache, "session-1", payload)
	recordPendingToolCallIDsFromPayload(pending, payload)

	if cached, ok := cache.get("session-1", "call-1"); ok {
		t.Fatalf("incomplete tool call was cached: %s", cached)
	}
	if len(pending) != 0 {
		t.Fatalf("incomplete tool call was recorded as pending: %v", pending)
	}
}

func TestRecordResponsesWebsocketToolCallsFromPayloadWithCache(t *testing.T) {
	cache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	payload := []byte(`{"type":"response.completed","response":{"id":"resp-1","output":[{"type":"function_call","id":"fc-1","call_id":"call-1","name":"tool","arguments":"{}"}]}}`)
	recordResponsesWebsocketToolCallsFromPayloadWithCache(cache, sessionKey, payload)

	cached, ok := cache.get(sessionKey, "call-1")
	if !ok {
		t.Fatalf("expected cached tool call")
	}
	if gjson.GetBytes(cached, "type").String() != "function_call" || gjson.GetBytes(cached, "call_id").String() != "call-1" {
		t.Fatalf("unexpected cached tool call: %s", cached)
	}
}

func TestRecordResponsesWebsocketCustomToolCallsFromCompletedPayloadWithCache(t *testing.T) {
	cache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	payload := []byte(`{"type":"response.completed","response":{"id":"resp-1","output":[{"type":"custom_tool_call","id":"ctc-1","call_id":"call-1","name":"apply_patch","input":"*** Begin Patch"}]}}`)
	recordResponsesWebsocketToolCallsFromPayloadWithCache(cache, sessionKey, payload)

	cached, ok := cache.get(sessionKey, "call-1")
	if !ok {
		t.Fatalf("expected cached custom tool call")
	}
	if gjson.GetBytes(cached, "type").String() != "custom_tool_call" || gjson.GetBytes(cached, "call_id").String() != "call-1" {
		t.Fatalf("unexpected cached custom tool call: %s", cached)
	}
}

func TestRecordResponsesWebsocketCustomToolCallsFromOutputItemDoneWithCache(t *testing.T) {
	cache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	payload := []byte(`{"type":"response.output_item.done","item":{"type":"custom_tool_call","id":"ctc-1","call_id":"call-1","name":"apply_patch","input":"*** Begin Patch"}}`)
	recordResponsesWebsocketToolCallsFromPayloadWithCache(cache, sessionKey, payload)

	cached, ok := cache.get(sessionKey, "call-1")
	if !ok {
		t.Fatalf("expected cached custom tool call")
	}
	if gjson.GetBytes(cached, "type").String() != "custom_tool_call" || gjson.GetBytes(cached, "call_id").String() != "call-1" {
		t.Fatalf("unexpected cached custom tool call: %s", cached)
	}
}

func TestForwardResponsesWebsocketRestoresAndForwardsCompletedOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)

	serverErrCh := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := responsesWebsocketUpgrader.Upgrade(w, r, nil)
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			errClose := conn.Close()
			if errClose != nil {
				serverErrCh <- errClose
			}
		}()

		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = r

		data := make(chan []byte, 2)
		errCh := make(chan *interfaces.ErrorMessage)
		data <- []byte(`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"call-1","call_id":"call-1","name":"lookup","arguments":"{}"}}`)
		data <- []byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"output\":[]}}\n\n")
		close(data)
		close(errCh)

		timelineLog := newInMemoryWebsocketTimelineLog()
		completedOutput, completedResponseID, pendingToolCallIDs, errMsg, err := (*OpenAIResponsesAPIHandler)(nil).forwardResponsesWebsocket(
			ctx,
			newResponsesWebsocketWriter(conn),
			func(...interface{}) {},
			data,
			errCh,
			timelineLog,
			"session-1",
		)
		if err != nil {
			serverErrCh <- err
			return
		}
		if errMsg != nil {
			serverErrCh <- fmt.Errorf("unexpected websocket error message: %v", errMsg.Error)
			return
		}
		if gjson.GetBytes(completedOutput, "0.id").String() != "call-1" {
			serverErrCh <- errors.New("completed output not restored")
			return
		}
		if completedResponseID != "resp-1" {
			serverErrCh <- fmt.Errorf("completed response id = %q, want resp-1", completedResponseID)
			return
		}
		if len(pendingToolCallIDs) != 1 || pendingToolCallIDs[0] != "call-1" {
			serverErrCh <- fmt.Errorf("pending tool call ids = %v, want [call-1]", pendingToolCallIDs)
			return
		}
		if !strings.Contains(timelineLog.String(), "Event: websocket.response") {
			serverErrCh <- errors.New("websocket timeline did not capture downstream response")
			return
		}
		serverErrCh <- nil
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		errClose := conn.Close()
		if errClose != nil {
			t.Fatalf("close websocket: %v", errClose)
		}
	}()

	_, outputItemPayload, errReadMessage := conn.ReadMessage()
	if errReadMessage != nil {
		t.Fatalf("read output item websocket message: %v", errReadMessage)
	}
	if got := gjson.GetBytes(outputItemPayload, "type").String(); got != "response.output_item.done" {
		t.Fatalf("output item payload type = %s, want response.output_item.done", got)
	}

	_, payload, errReadMessage := conn.ReadMessage()
	if errReadMessage != nil {
		t.Fatalf("read completion websocket message: %v", errReadMessage)
	}
	if gjson.GetBytes(payload, "type").String() != wsEventTypeCompleted {
		t.Fatalf("payload type = %s, want %s", gjson.GetBytes(payload, "type").String(), wsEventTypeCompleted)
	}
	if strings.Contains(string(payload), "response.done") {
		t.Fatalf("payload unexpectedly rewrote completed event: %s", payload)
	}
	if got := gjson.GetBytes(payload, "response.output.0.id").String(); got != "call-1" {
		t.Fatalf("downstream completion output id = %q, want call-1; payload=%s", got, payload)
	}

	if errServer := <-serverErrCh; errServer != nil {
		t.Fatalf("server error: %v", errServer)
	}
}

func TestForwardResponsesWebsocketTreatsResponseDoneAsTerminalWithoutRewriting(t *testing.T) {
	gin.SetMode(gin.TestMode)

	serverErrCh := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := responsesWebsocketUpgrader.Upgrade(w, r, nil)
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			errClose := conn.Close()
			if errClose != nil {
				serverErrCh <- errClose
			}
		}()

		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = r

		data := make(chan []byte, 1)
		errCh := make(chan *interfaces.ErrorMessage)
		data <- []byte(`{"type":"response.done","response":{"id":"resp-1","output":[{"type":"message","id":"out-1"}]}}`)
		close(data)
		close(errCh)

		timelineLog := newInMemoryWebsocketTimelineLog()
		completedOutput, completedResponseID, pendingToolCallIDs, errMsg, err := (*OpenAIResponsesAPIHandler)(nil).forwardResponsesWebsocket(
			ctx,
			newResponsesWebsocketWriter(conn),
			func(...interface{}) {},
			data,
			errCh,
			timelineLog,
			"session-1",
		)
		if err != nil {
			serverErrCh <- err
			return
		}
		if errMsg != nil {
			serverErrCh <- fmt.Errorf("unexpected websocket error message: %v", errMsg.Error)
			return
		}
		if gjson.GetBytes(completedOutput, "0.id").String() != "out-1" {
			serverErrCh <- errors.New("done output not captured")
			return
		}
		if completedResponseID != "resp-1" {
			serverErrCh <- fmt.Errorf("completed response id = %q, want resp-1", completedResponseID)
			return
		}
		if len(pendingToolCallIDs) != 0 {
			serverErrCh <- fmt.Errorf("pending tool call ids = %v, want empty", pendingToolCallIDs)
			return
		}
		serverErrCh <- nil
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		errClose := conn.Close()
		if errClose != nil {
			t.Fatalf("close websocket: %v", errClose)
		}
	}()

	_, payload, errReadMessage := conn.ReadMessage()
	if errReadMessage != nil {
		t.Fatalf("read websocket message: %v", errReadMessage)
	}
	if got := gjson.GetBytes(payload, "type").String(); got != "response.done" {
		t.Fatalf("payload type = %s, want response.done; payload=%s", got, payload)
	}

	if errServer := <-serverErrCh; errServer != nil {
		t.Fatalf("server error: %v", errServer)
	}
}

func TestForwardResponsesWebsocketTreatsErrorPayloadAsTerminal(t *testing.T) {
	gin.SetMode(gin.TestMode)

	serverErrCh := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := responsesWebsocketUpgrader.Upgrade(w, r, nil)
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			errClose := conn.Close()
			if errClose != nil {
				serverErrCh <- errClose
			}
		}()

		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = r

		data := make(chan []byte, 1)
		errCh := make(chan *interfaces.ErrorMessage)
		data <- []byte(`{"type":"error","status":429,"error":{"message":"upstream failed"}}`)
		close(data)
		close(errCh)

		_, _, _, errMsg, err := (*OpenAIResponsesAPIHandler)(nil).forwardResponsesWebsocket(
			ctx,
			newResponsesWebsocketWriter(conn),
			func(...interface{}) {},
			data,
			errCh,
			newInMemoryWebsocketTimelineLog(),
			"session-1",
		)
		if err != nil {
			serverErrCh <- err
			return
		}
		if errMsg == nil {
			serverErrCh <- errors.New("expected websocket error message")
			return
		}
		if errMsg.StatusCode != http.StatusTooManyRequests {
			serverErrCh <- fmt.Errorf("websocket error status = %d, want %d", errMsg.StatusCode, http.StatusTooManyRequests)
			return
		}
		if errMsg.Error == nil || !strings.Contains(errMsg.Error.Error(), "upstream failed") {
			serverErrCh <- fmt.Errorf("websocket error = %v, want upstream failed", errMsg.Error)
			return
		}
		serverErrCh <- nil
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		errClose := conn.Close()
		if errClose != nil {
			t.Fatalf("close websocket: %v", errClose)
		}
	}()

	_, payload, errReadMessage := conn.ReadMessage()
	if errReadMessage != nil {
		t.Fatalf("read websocket message: %v", errReadMessage)
	}
	if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeError {
		t.Fatalf("payload type = %s, want %s; payload=%s", got, wsEventTypeError, payload)
	}

	if errServer := <-serverErrCh; errServer != nil {
		t.Fatalf("server error: %v", errServer)
	}
}

func TestRecordPendingToolCallIDsFromPayloadDropsSatisfiedCalls(t *testing.T) {
	pending := map[string]struct{}{}
	payload := []byte(`{"type":"response.completed","response":{"output":[{"type":"function_call","call_id":"call-1","id":"fc-1"},{"type":"function_call_output","call_id":"call-1","id":"out-1"},{"type":"custom_tool_call","call_id":"call-2","id":"ctc-1"},{"type":"custom_tool_call_output","call_id":"call-2","id":"custom-out-1"}]}}`)

	recordPendingToolCallIDsFromPayload(pending, payload)

	if len(pending) != 0 {
		t.Fatalf("pending tool call ids = %v, want empty", sortedStringSet(pending))
	}
}

func TestForwardResponsesWebsocketLogsAttemptedResponseOnWriteFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)

	serverErrCh := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := responsesWebsocketUpgrader.Upgrade(w, r, nil)
		if err != nil {
			serverErrCh <- err
			return
		}

		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = r

		data := make(chan []byte, 1)
		errCh := make(chan *interfaces.ErrorMessage)
		data <- []byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"output\":[{\"type\":\"message\",\"id\":\"out-1\"}]}}\n\n")
		close(data)
		close(errCh)

		timelineLog := newInMemoryWebsocketTimelineLog()
		if errClose := conn.Close(); errClose != nil {
			serverErrCh <- errClose
			return
		}

		_, _, _, _, err = (*OpenAIResponsesAPIHandler)(nil).forwardResponsesWebsocket(
			ctx,
			newResponsesWebsocketWriter(conn),
			func(...interface{}) {},
			data,
			errCh,
			timelineLog,
			"session-1",
		)
		if err == nil {
			serverErrCh <- errors.New("expected websocket write failure")
			return
		}
		if !strings.Contains(timelineLog.String(), "Event: websocket.response") {
			serverErrCh <- errors.New("websocket timeline did not capture attempted downstream response")
			return
		}
		if !strings.Contains(timelineLog.String(), "\"type\":\"response.completed\"") {
			serverErrCh <- errors.New("websocket timeline did not retain attempted payload")
			return
		}
		serverErrCh <- nil
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		_ = conn.Close()
	}()

	if errServer := <-serverErrCh; errServer != nil {
		t.Fatalf("server error: %v", errServer)
	}
}

func TestResponsesWebsocketTimelineRecordsDisconnectEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(nil, nil, nil)
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{RequestLog: true}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	logsDir := t.TempDir()

	timelineCh := make(chan string, 1)
	router := gin.New()
	router.GET("/v1/responses/ws", func(c *gin.Context) {
		source, errSource := requestlogging.NewFileBodySourceInDir(logsDir, "websocket-timeline-test")
		if errSource != nil {
			timelineCh <- ""
			return
		}
		c.Set(requestlogging.WebsocketTimelineSourceContextKey, source)
		h.ResponsesWebsocket(c)
		timeline := ""
		if value, exists := c.Get(wsTimelineBodyKey); exists {
			if body, ok := value.([]byte); ok {
				timeline = string(body)
			}
		} else if value, exists := c.Get(requestlogging.WebsocketTimelineSourceContextKey); exists {
			if source, ok := value.(*requestlogging.FileBodySource); ok {
				body, _ := source.Bytes()
				timeline = string(body)
				_ = source.Cleanup()
			}
		}
		if value, exists := c.Get(requestlogging.APIWebsocketTimelineSourceContextKey); exists {
			if source, ok := value.(*requestlogging.FileBodySource); ok {
				_ = source.Cleanup()
			}
		}
		timelineCh <- timeline
	})

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}

	closePayload := websocket.FormatCloseMessage(websocket.CloseGoingAway, "client closing")
	if err = conn.WriteControl(websocket.CloseMessage, closePayload, time.Now().Add(time.Second)); err != nil {
		t.Fatalf("write close control: %v", err)
	}
	_ = conn.Close()

	select {
	case timeline := <-timelineCh:
		if !strings.Contains(timeline, "Event: websocket.disconnect") {
			t.Fatalf("websocket timeline missing disconnect event: %s", timeline)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for websocket timeline")
	}
}

func TestResponsesWebsocketMirrorsUpstreamMessageTooBigDisconnect(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, provider := range []string{"codex", "xai"} {
		t.Run(provider, func(t *testing.T) {
			executor := &websocketUpstreamDisconnectExecutor{provider: provider, subscribed: make(chan string, 1)}
			manager := coreauth.NewManager(nil, nil, nil)
			manager.RegisterExecutor(executor)
			base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
			h := NewOpenAIResponsesAPIHandler(base)

			router := gin.New()
			router.GET("/v1/responses/ws", h.ResponsesWebsocket)
			server := httptest.NewServer(router)
			defer server.Close()

			wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
			conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
			if err != nil {
				t.Fatalf("dial websocket: %v", err)
			}
			defer func() { _ = conn.Close() }()

			var sessionID string
			select {
			case sessionID = <-executor.subscribed:
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for upstream disconnect subscription")
			}

			executor.TriggerDisconnect(sessionID, &websocket.CloseError{
				Code: websocket.CloseMessageTooBig,
				Text: "message too big",
			})

			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			_, _, err = conn.ReadMessage()
			var closeErr *websocket.CloseError
			if !errors.As(err, &closeErr) {
				t.Fatalf("expected downstream websocket close error, got %v", err)
			}
			if closeErr.Code != websocket.CloseMessageTooBig {
				t.Fatalf("downstream close code = %d, want %d", closeErr.Code, websocket.CloseMessageTooBig)
			}
			if closeErr.Text != "message too big" {
				t.Fatalf("downstream close reason = %q, want message too big", closeErr.Text)
			}
		})
	}
}

func TestResponsesWebsocketCodexWebsocketPassthroughPassesCompactedRequestWithoutTranscriptMerge(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &websocketDirectCaptureExecutor{done: make(chan struct{})}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{
		ID:         "auth-ws",
		Provider:   "codex",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	firstRequest := []byte(`{"type":"response.create","model":"test-model","input":[{"type":"message","role":"user","content":"first"}]}`)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if errWrite := conn.WriteMessage(websocket.TextMessage, firstRequest); errWrite != nil {
		t.Fatalf("write first websocket message: %v", errWrite)
	}
	if _, _, errRead := conn.ReadMessage(); errRead != nil {
		t.Fatalf("read first websocket response: %v", errRead)
	}

	compactedRequest := []byte(`{"type":"response.create","input":[{"type":"compaction_summary","summary":"compressed history"},{"type":"message","role":"user","content":"after compaction"}]}`)
	if errWrite := conn.WriteMessage(websocket.TextMessage, compactedRequest); errWrite != nil {
		t.Fatalf("write compacted websocket message: %v", errWrite)
	}
	if _, _, errRead := conn.ReadMessage(); errRead != nil {
		t.Fatalf("read compacted websocket response: %v", errRead)
	}

	select {
	case <-executor.done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for websocket passthrough")
	}

	payloads := executor.Payloads()
	if len(payloads) != 2 {
		t.Fatalf("passthrough payload count = %d, want 2", len(payloads))
	}
	if got := gjson.GetBytes(payloads[0], "input").Raw; got != gjson.GetBytes(firstRequest, "input").Raw {
		t.Fatalf("first passthrough input = %s, want %s", got, gjson.GetBytes(firstRequest, "input").Raw)
	}
	if got := gjson.GetBytes(payloads[1], "input").Raw; got != gjson.GetBytes(compactedRequest, "input").Raw {
		t.Fatalf("compacted passthrough input = %s, want %s", got, gjson.GetBytes(compactedRequest, "input").Raw)
	}
	if got := gjson.GetBytes(payloads[1], "model").String(); got != "test-model" {
		t.Fatalf("compacted passthrough model = %s, want test-model", got)
	}
	if bytes.Contains(payloads[1], []byte(`"content":"first"`)) || bytes.Contains(payloads[1], []byte(`"id":"out-1"`)) {
		t.Fatalf("compacted passthrough payload contains stale transcript state: %s", payloads[1])
	}
	authIDs := executor.AuthIDs()
	if len(authIDs) != 2 || authIDs[0] != "auth-ws" || authIDs[1] != "auth-ws" {
		t.Fatalf("passthrough auth IDs = %v, want [auth-ws auth-ws]", authIDs)
	}
}

func TestResponsesWebsocketXAIWebsocketPassthroughCarriesPreviousResponseID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	modelName := "xai-websocket-passthrough-model"
	executor := &websocketDirectCaptureExecutor{provider: "xai", done: make(chan struct{})}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{
		ID:         "auth-xai-ws",
		Provider:   "xai",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: modelName}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() { _ = conn.Close() }()

	firstRequest := []byte(fmt.Sprintf(`{"type":"response.create","model":%q,"input":[{"type":"message","id":"msg-1","role":"user","content":"first"}]}`, modelName))
	if errWrite := conn.WriteMessage(websocket.TextMessage, firstRequest); errWrite != nil {
		t.Fatalf("write first websocket message: %v", errWrite)
	}
	if _, _, errRead := conn.ReadMessage(); errRead != nil {
		t.Fatalf("read first websocket response: %v", errRead)
	}

	secondRequest := []byte(`{"type":"response.create","previous_response_id":"resp-1","input":[{"type":"message","id":"msg-2","role":"user","content":"second"}]}`)
	if errWrite := conn.WriteMessage(websocket.TextMessage, secondRequest); errWrite != nil {
		t.Fatalf("write second websocket message: %v", errWrite)
	}
	if _, _, errRead := conn.ReadMessage(); errRead != nil {
		t.Fatalf("read second websocket response: %v", errRead)
	}

	select {
	case <-executor.done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for websocket passthrough")
	}

	payloads := executor.Payloads()
	if len(payloads) != 2 {
		t.Fatalf("xai websocket payload count = %d, want 2", len(payloads))
	}
	secondPayload := payloads[1]
	if got := gjson.GetBytes(secondPayload, "type").String(); got != wsRequestTypeCreate {
		t.Fatalf("second xai passthrough type = %s, want %s: %s", got, wsRequestTypeCreate, secondPayload)
	}
	if got := gjson.GetBytes(secondPayload, "model").String(); got != modelName {
		t.Fatalf("second xai payload model = %s, want %s", got, modelName)
	}
	if got := gjson.GetBytes(secondPayload, "previous_response_id").String(); got != "resp-1" {
		t.Fatalf("second xai previous_response_id = %s, want resp-1: %s", got, secondPayload)
	}
	input := gjson.GetBytes(secondPayload, "input").Array()
	if len(input) != 1 {
		t.Fatalf("second xai passthrough input len = %d, want 1: %s", len(input), secondPayload)
	}
	if input[0].Get("id").String() != "msg-2" {
		t.Fatalf("second xai passthrough input must contain only the new turn: %s", secondPayload)
	}
	if bytes.Contains(secondPayload, []byte(`"id":"msg-1"`)) || bytes.Contains(secondPayload, []byte(`"id":"out-1"`)) {
		t.Fatalf("second xai passthrough payload contains stale transcript state: %s", secondPayload)
	}
	authIDs := executor.AuthIDs()
	if len(authIDs) != 2 || authIDs[0] != "auth-xai-ws" || authIDs[1] != "auth-xai-ws" {
		t.Fatalf("xai websocket auth IDs = %v, want [auth-xai-ws auth-xai-ws]", authIDs)
	}
}

func TestResponsesWebsocketSwitchesPinnedAuthAcrossProviders(t *testing.T) {
	for _, testCase := range []struct {
		name                      string
		xaiWebsockets             bool
		returnToDifferentXAIModel bool
	}{
		{name: "xai SSE", xaiWebsockets: false},
		{name: "xai websocket", xaiWebsockets: true},
		{name: "xai websocket different model", xaiWebsockets: true, returnToDifferentXAIModel: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)

			xaiModel := "xai-provider-switch-" + strings.ReplaceAll(testCase.name, " ", "-")
			returnXAIModel := xaiModel
			if testCase.returnToDifferentXAIModel {
				returnXAIModel += "-return"
			}
			codexModel := "codex-provider-switch-" + strings.ReplaceAll(testCase.name, " ", "-")
			xaiExecutor := &websocketDirectCaptureExecutor{provider: "xai"}
			codexExecutor := &websocketDirectCaptureExecutor{provider: "codex"}

			xaiAuth := &coreauth.Auth{
				ID:       "auth-" + xaiModel,
				Provider: "xai",
				Status:   coreauth.StatusActive,
			}
			if testCase.xaiWebsockets {
				xaiAuth.Attributes = map[string]string{"websockets": "true"}
			}
			codexAuth := &coreauth.Auth{
				ID:         "auth-" + codexModel,
				Provider:   "codex",
				Status:     coreauth.StatusActive,
				Attributes: map[string]string{"websockets": "true"},
			}
			selector := &orderedWebsocketSelector{order: []string{xaiAuth.ID, codexAuth.ID}}
			manager := coreauth.NewManager(nil, selector, nil)
			manager.RegisterExecutor(xaiExecutor)
			manager.RegisterExecutor(codexExecutor)
			if _, errRegister := manager.Register(context.Background(), xaiAuth); errRegister != nil {
				t.Fatalf("Register xAI auth: %v", errRegister)
			}
			if _, errRegister := manager.Register(context.Background(), codexAuth); errRegister != nil {
				t.Fatalf("Register Codex auth: %v", errRegister)
			}

			registry.GetGlobalRegistry().RegisterClient(xaiAuth.ID, xaiAuth.Provider, []*registry.ModelInfo{{ID: xaiModel}})
			registry.GetGlobalRegistry().RegisterClient(codexAuth.ID, codexAuth.Provider, []*registry.ModelInfo{{ID: codexModel}})
			registeredAuthIDs := []string{xaiAuth.ID, codexAuth.ID}
			xaiAlternateAuthID := ""
			if testCase.xaiWebsockets {
				xaiAlternateAuth := &coreauth.Auth{
					ID:         "auth-alternate-" + xaiModel,
					Provider:   "xai",
					Status:     coreauth.StatusActive,
					Attributes: map[string]string{"websockets": "true"},
				}
				xaiAlternateAuthID = xaiAlternateAuth.ID
				selector.order = append(selector.order, xaiAlternateAuth.ID)
				if _, errRegister := manager.Register(context.Background(), xaiAlternateAuth); errRegister != nil {
					t.Fatalf("Register alternate xAI auth: %v", errRegister)
				}
				alternateModels := []*registry.ModelInfo{{ID: xaiModel}}
				if testCase.returnToDifferentXAIModel {
					alternateModels = []*registry.ModelInfo{{ID: returnXAIModel}}
				}
				registry.GetGlobalRegistry().RegisterClient(xaiAlternateAuth.ID, xaiAlternateAuth.Provider, alternateModels)
				registeredAuthIDs = append(registeredAuthIDs, xaiAlternateAuth.ID)
			}
			t.Cleanup(func() {
				for _, authID := range registeredAuthIDs {
					registry.GetGlobalRegistry().UnregisterClient(authID)
				}
			})

			base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
			h := NewOpenAIResponsesAPIHandler(base)
			router := gin.New()
			router.GET("/v1/responses/ws", h.ResponsesWebsocket)

			server := httptest.NewServer(router)
			defer server.Close()

			wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
			conn, _, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
			if errDial != nil {
				t.Fatalf("dial websocket: %v", errDial)
			}
			defer func() {
				if errClose := conn.Close(); errClose != nil {
					t.Errorf("close websocket: %v", errClose)
				}
			}()

			requests := []string{
				fmt.Sprintf(`{"type":"response.create","model":%q,"input":[{"type":"message","id":"msg-xai-1"}]}`, xaiModel),
				fmt.Sprintf(`{"type":"response.create","model":%q,"input":[{"type":"message","id":"msg-codex-1"}]}`, codexModel),
				fmt.Sprintf(`{"type":"response.create","model":%q,"input":[{"type":"message","id":"msg-xai-2"}]}`, returnXAIModel),
				`{"type":"response.create","input":[{"type":"message","id":"msg-xai-3"}]}`,
			}
			for index, request := range requests {
				if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(request)); errWrite != nil {
					t.Fatalf("write websocket message %d: %v", index+1, errWrite)
				}
				_, payload, errRead := conn.ReadMessage()
				if errRead != nil {
					t.Fatalf("read websocket response %d: %v", index+1, errRead)
				}
				if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeCompleted {
					t.Fatalf("response %d type = %s, want %s: %s", index+1, got, wsEventTypeCompleted, payload)
				}
			}

			wantReturnAuthID := xaiAuth.ID
			if testCase.returnToDifferentXAIModel {
				wantReturnAuthID = xaiAlternateAuthID
			}
			if got := xaiExecutor.AuthIDs(); len(got) != 3 || got[0] != xaiAuth.ID || got[1] != wantReturnAuthID || got[2] != wantReturnAuthID {
				t.Fatalf("xAI auth IDs = %v, want [%s %s %s]", got, xaiAuth.ID, wantReturnAuthID, wantReturnAuthID)
			}
			if got := codexExecutor.AuthIDs(); len(got) != 1 || got[0] != codexAuth.ID {
				t.Fatalf("Codex auth IDs = %v, want [%s]", got, codexAuth.ID)
			}
		})
	}
}

func TestResponsesWebsocketPinnedAuthMatchesModel(t *testing.T) {
	modelA := "xai-pinned-auth-model-a"
	modelB := "xai-pinned-auth-model-b"
	auth := &coreauth.Auth{ID: "xai-pinned-auth", Provider: "xai", Status: coreauth.StatusActive}
	otherAuthID := "xai-pinned-auth-other"
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: modelA}})
	registry.GetGlobalRegistry().RegisterClient(otherAuthID, auth.Provider, []*registry.ModelInfo{{ID: modelB}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
		registry.GetGlobalRegistry().UnregisterClient(otherAuthID)
	})

	if !responsesWebsocketPinnedAuthMatchesModel(auth, modelA, modelA, false) {
		t.Fatal("expected registered auth to match its supported model")
	}
	if responsesWebsocketPinnedAuthMatchesModel(auth, modelB, modelA, false) {
		t.Fatal("registered auth matched an unsupported model from the same provider")
	}

	disabledAuth := auth.Clone()
	disabledAuth.Disabled = true
	if responsesWebsocketPinnedAuthMatchesModel(disabledAuth, modelA, modelA, false) {
		t.Fatal("disabled auth matched a model")
	}

	cooldownAuth := auth.Clone()
	cooldownAuth.ModelStates = map[string]*coreauth.ModelState{
		modelA: {Unavailable: true, NextRetryAfter: time.Now().Add(time.Minute)},
	}
	if responsesWebsocketPinnedAuthMatchesModel(cooldownAuth, modelA, modelA, false) {
		t.Fatal("auth in model cooldown matched a model")
	}

	unregisteredAuth := &coreauth.Auth{ID: "unregistered-auth", Provider: "xai", Status: coreauth.StatusActive}
	if responsesWebsocketPinnedAuthMatchesModel(unregisteredAuth, modelA, modelA, false) {
		t.Fatal("unregistered ordinary auth matched a model")
	}
	if !responsesWebsocketPinnedAuthMatchesModel(unregisteredAuth, modelA, modelA, true) {
		t.Fatal("expected Home runtime auth to match its pinned model")
	}
	if responsesWebsocketPinnedAuthMatchesModel(unregisteredAuth, modelB, modelA, true) {
		t.Fatal("Home runtime auth matched a different model")
	}
}

func TestWebsocketUpstreamSupportsIncrementalInputForModel(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:         "auth-ws",
		Provider:   "test-provider",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	if !h.websocketUpstreamSupportsIncrementalInputForModel("test-model") {
		t.Fatalf("expected websocket-capable upstream for test-model")
	}
}

func TestWebsocketUpstreamSupportsIncrementalInputForXAI(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:         "auth-xai-ws",
		Provider:   "xai",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "xai-test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	if !h.websocketUpstreamSupportsIncrementalInputForModel("xai-test-model") {
		t.Fatalf("expected xai websocket upstream to support previous_response_id incremental input")
	}
}

func TestResponsesWebsocketUsesUpstreamWebsocketPassthroughForXAI(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	executor := &websocketProviderCaptureExecutor{provider: "xai"}
	manager.RegisterExecutor(executor)

	modelName := "xai-passthrough-model"
	auth := &coreauth.Auth{
		ID:         "auth-xai-ws",
		Provider:   "xai",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: modelName}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	if !h.responsesWebsocketUsesUpstreamWebsocketPassthrough(modelName) {
		t.Fatalf("expected xai websocket upstream passthrough for %s", modelName)
	}
}

func TestWebsocketUpstreamSupportsCompactionReplayForModel(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:       "auth-codex",
		Provider: "codex",
		Status:   coreauth.StatusActive,
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	if !h.websocketUpstreamSupportsCompactionReplayForModel("test-model") {
		t.Fatalf("expected codex upstream to support compaction replay")
	}
}

func TestWebsocketUpstreamSupportsCompactionReplayForModelFalseWhenMixedBackends(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	auths := []*coreauth.Auth{
		{ID: "auth-codex", Provider: "codex", Status: coreauth.StatusActive},
		{ID: "auth-claude", Provider: "claude", Status: coreauth.StatusActive},
	}
	for _, auth := range auths {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("Register auth %s: %v", auth.ID, err)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	}
	t.Cleanup(func() {
		for _, auth := range auths {
			registry.GetGlobalRegistry().UnregisterClient(auth.ID)
		}
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	if h.websocketUpstreamSupportsCompactionReplayForModel("test-model") {
		t.Fatalf("expected mixed backend model to disable compaction replay bypass")
	}
}

func TestResponsesWebsocketPrewarmHandledLocallyForSSEUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &websocketCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: "auth-sse", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		errClose := conn.Close()
		if errClose != nil {
			t.Fatalf("close websocket: %v", errClose)
		}
	}()

	errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"test-model","generate":false}`))
	if errWrite != nil {
		t.Fatalf("write prewarm websocket message: %v", errWrite)
	}

	_, createdPayload, errReadMessage := conn.ReadMessage()
	if errReadMessage != nil {
		t.Fatalf("read prewarm created message: %v", errReadMessage)
	}
	if gjson.GetBytes(createdPayload, "type").String() != "response.created" {
		t.Fatalf("created payload type = %s, want response.created", gjson.GetBytes(createdPayload, "type").String())
	}
	prewarmResponseID := gjson.GetBytes(createdPayload, "response.id").String()
	if prewarmResponseID == "" {
		t.Fatalf("prewarm response id is empty")
	}
	if executor.streamCalls != 0 {
		t.Fatalf("stream calls after prewarm = %d, want 0", executor.streamCalls)
	}

	_, completedPayload, errReadMessage := conn.ReadMessage()
	if errReadMessage != nil {
		t.Fatalf("read prewarm completed message: %v", errReadMessage)
	}
	if gjson.GetBytes(completedPayload, "type").String() != wsEventTypeCompleted {
		t.Fatalf("completed payload type = %s, want %s", gjson.GetBytes(completedPayload, "type").String(), wsEventTypeCompleted)
	}
	if gjson.GetBytes(completedPayload, "response.id").String() != prewarmResponseID {
		t.Fatalf("completed response id = %s, want %s", gjson.GetBytes(completedPayload, "response.id").String(), prewarmResponseID)
	}
	if gjson.GetBytes(completedPayload, "response.usage.total_tokens").Int() != 0 {
		t.Fatalf("prewarm total tokens = %d, want 0", gjson.GetBytes(completedPayload, "response.usage.total_tokens").Int())
	}

	secondRequest := fmt.Sprintf(`{"type":"response.create","previous_response_id":%q,"input":[{"type":"message","id":"msg-1"}]}`, prewarmResponseID)
	errWrite = conn.WriteMessage(websocket.TextMessage, []byte(secondRequest))
	if errWrite != nil {
		t.Fatalf("write follow-up websocket message: %v", errWrite)
	}

	_, upstreamPayload, errReadMessage := conn.ReadMessage()
	if errReadMessage != nil {
		t.Fatalf("read upstream completed message: %v", errReadMessage)
	}
	if gjson.GetBytes(upstreamPayload, "type").String() != wsEventTypeCompleted {
		t.Fatalf("upstream payload type = %s, want %s", gjson.GetBytes(upstreamPayload, "type").String(), wsEventTypeCompleted)
	}
	if executor.streamCalls != 1 {
		t.Fatalf("stream calls after follow-up = %d, want 1", executor.streamCalls)
	}
	if len(executor.payloads) != 1 {
		t.Fatalf("captured upstream payloads = %d, want 1", len(executor.payloads))
	}
	forwarded := executor.payloads[0]
	if gjson.GetBytes(forwarded, "previous_response_id").Exists() {
		t.Fatalf("previous_response_id leaked upstream: %s", forwarded)
	}
	if gjson.GetBytes(forwarded, "generate").Exists() {
		t.Fatalf("generate leaked upstream: %s", forwarded)
	}
	if gjson.GetBytes(forwarded, "model").String() != "test-model" {
		t.Fatalf("forwarded model = %s, want test-model", gjson.GetBytes(forwarded, "model").String())
	}
	input := gjson.GetBytes(forwarded, "input").Array()
	if len(input) != 1 || input[0].Get("id").String() != "msg-1" {
		t.Fatalf("unexpected forwarded input: %s", forwarded)
	}
}

func TestResponsesWebsocketMergesTranscriptForNonPassthroughUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &websocketCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{
		ID:         "auth-ws",
		Provider:   executor.Identifier(),
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			t.Fatalf("close websocket: %v", errClose)
		}
	}()

	requests := []string{
		`{"type":"response.create","model":"test-model","input":[{"type":"message","id":"msg-1"}]}`,
		`{"type":"response.create","input":[{"type":"message","id":"msg-2"}]}`,
	}
	for i := range requests {
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(requests[i])); errWrite != nil {
			t.Fatalf("write websocket message %d: %v", i+1, errWrite)
		}
		_, payload, errReadMessage := conn.ReadMessage()
		if errReadMessage != nil {
			t.Fatalf("read websocket message %d: %v", i+1, errReadMessage)
		}
		if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeCompleted {
			t.Fatalf("message %d payload type = %s, want %s", i+1, got, wsEventTypeCompleted)
		}
	}

	if len(executor.payloads) != 2 {
		t.Fatalf("upstream payload count = %d, want 2", len(executor.payloads))
	}
	secondPayload := executor.payloads[1]
	if gjson.GetBytes(secondPayload, "previous_response_id").Exists() {
		t.Fatalf("previous_response_id must not be sent on non-passthrough upstream: %s", secondPayload)
	}
	input := gjson.GetBytes(secondPayload, "input").Array()
	if len(input) != 3 {
		t.Fatalf("second upstream input len = %d, want 3: %s", len(input), secondPayload)
	}
	if input[0].Get("id").String() != "msg-1" || input[1].Get("id").String() != "out-1" || input[2].Get("id").String() != "msg-2" {
		t.Fatalf("unexpected merged upstream input: %s", secondPayload)
	}
}

func TestResponsesWebsocketDoesNotInjectPreviousResponseIDWhenPendingToolOutputMissing(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &websocketCompactionCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{
		ID:         "auth-ws",
		Provider:   executor.Identifier(),
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			t.Fatalf("close websocket: %v", errClose)
		}
	}()

	requests := []string{
		`{"type":"response.create","model":"test-model","input":[{"type":"message","id":"msg-1"}]}`,
		`{"type":"response.create","input":[{"type":"message","role":"user","id":"summary-1","content":"compacted summary"}]}`,
	}
	for i := range requests {
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(requests[i])); errWrite != nil {
			t.Fatalf("write websocket message %d: %v", i+1, errWrite)
		}
		_, payload, errReadMessage := conn.ReadMessage()
		if errReadMessage != nil {
			t.Fatalf("read websocket message %d: %v", i+1, errReadMessage)
		}
		if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeCompleted {
			t.Fatalf("message %d payload type = %s, want %s", i+1, got, wsEventTypeCompleted)
		}
	}

	executor.mu.Lock()
	payloads := append([][]byte(nil), executor.streamPayloads...)
	executor.mu.Unlock()

	if len(payloads) != 2 {
		t.Fatalf("upstream payload count = %d, want 2", len(payloads))
	}
	secondPayload := payloads[1]
	if gjson.GetBytes(secondPayload, "previous_response_id").Exists() {
		t.Fatalf("previous_response_id must not be injected when pending tool output is missing: %s", secondPayload)
	}
	input := gjson.GetBytes(secondPayload, "input").Array()
	if len(input) != 3 {
		t.Fatalf("second upstream input len = %d, want 3: %s", len(input), secondPayload)
	}
	if input[0].Get("id").String() != "msg-1" || input[1].Get("id").String() != "fc-1" || input[2].Get("id").String() != "summary-1" {
		t.Fatalf("unexpected merged upstream input when pending tool output is missing: %s", secondPayload)
	}
}

func TestResponsesWebsocketStripsGenerateWhenWebsocketAttemptFallsBackToHTTP(t *testing.T) {
	gin.SetMode(gin.TestMode)

	selector := &orderedWebsocketSelector{order: []string{"auth-ws", "auth-http"}}
	executor := &websocketBootstrapFallbackExecutor{}
	manager := coreauth.NewManager(nil, selector, nil)
	manager.RegisterExecutor(executor)

	authWS := &coreauth.Auth{
		ID:         "auth-ws",
		Provider:   executor.Identifier(),
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, err := manager.Register(context.Background(), authWS); err != nil {
		t.Fatalf("Register websocket auth: %v", err)
	}
	authHTTP := &coreauth.Auth{ID: "auth-http", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), authHTTP); err != nil {
		t.Fatalf("Register HTTP auth: %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(authWS.ID, authWS.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	registry.GetGlobalRegistry().RegisterClient(authHTTP.ID, authHTTP.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authWS.ID)
		registry.GetGlobalRegistry().UnregisterClient(authHTTP.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			t.Fatalf("close websocket: %v", errClose)
		}
	}()

	request := `{"type":"response.create","model":"test-model","generate":true,"input":[{"type":"message","id":"msg-1"}]}`
	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(request)); errWrite != nil {
		t.Fatalf("write websocket message: %v", errWrite)
	}
	_, payload, errReadMessage := conn.ReadMessage()
	if errReadMessage != nil {
		t.Fatalf("read websocket message: %v", errReadMessage)
	}
	if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeCompleted {
		t.Fatalf("payload type = %s, want %s: %s", got, wsEventTypeCompleted, payload)
	}

	if got := executor.AuthIDs(); len(got) != 2 || got[0] != "auth-ws" || got[1] != "auth-http" {
		t.Fatalf("selected auth IDs = %v, want [auth-ws auth-http]", got)
	}

	wsPayloads := executor.Payloads("auth-ws")
	if len(wsPayloads) != 1 {
		t.Fatalf("auth-ws payload count = %d, want 1", len(wsPayloads))
	}
	if !gjson.GetBytes(wsPayloads[0], "generate").Exists() {
		t.Fatalf("websocket attempt payload unexpectedly stripped generate: %s", wsPayloads[0])
	}

	httpPayloads := executor.Payloads("auth-http")
	if len(httpPayloads) != 1 {
		t.Fatalf("auth-http payload count = %d, want 1", len(httpPayloads))
	}
	if gjson.GetBytes(httpPayloads[0], "generate").Exists() {
		t.Fatalf("generate leaked after HTTP fallback: %s", httpPayloads[0])
	}
}

func TestWebsocketClientAddressUsesGinClientIP(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, engine := gin.CreateTestContext(recorder)
	if err := engine.SetTrustedProxies([]string{"0.0.0.0/0", "::/0"}); err != nil {
		t.Fatalf("SetTrustedProxies: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/responses/ws", nil)
	req.RemoteAddr = "172.18.0.1:34282"
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	c.Request = req

	if got := websocketClientAddress(c); got != strings.TrimSpace(c.ClientIP()) {
		t.Fatalf("websocketClientAddress = %q, ClientIP = %q", got, c.ClientIP())
	}
}

func TestWebsocketClientAddressReturnsEmptyForNilContext(t *testing.T) {
	if got := websocketClientAddress(nil); got != "" {
		t.Fatalf("websocketClientAddress(nil) = %q, want empty", got)
	}
}

func TestResponsesWebsocketPinsOnlyWebsocketCapableAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)

	selector := &orderedWebsocketSelector{order: []string{"auth-sse", "auth-ws"}}
	executor := &websocketAuthCaptureExecutor{}
	manager := coreauth.NewManager(nil, selector, nil)
	manager.RegisterExecutor(executor)

	authSSE := &coreauth.Auth{ID: "auth-sse", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), authSSE); err != nil {
		t.Fatalf("Register SSE auth: %v", err)
	}
	authWS := &coreauth.Auth{
		ID:         "auth-ws",
		Provider:   executor.Identifier(),
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, err := manager.Register(context.Background(), authWS); err != nil {
		t.Fatalf("Register websocket auth: %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(authSSE.ID, authSSE.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	registry.GetGlobalRegistry().RegisterClient(authWS.ID, authWS.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authSSE.ID)
		registry.GetGlobalRegistry().UnregisterClient(authWS.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			t.Fatalf("close websocket: %v", errClose)
		}
	}()

	requests := []string{
		`{"type":"response.create","model":"test-model","input":[{"type":"message","id":"msg-1"}]}`,
		`{"type":"response.create","input":[{"type":"message","id":"msg-2"}]}`,
	}
	for i := range requests {
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(requests[i])); errWrite != nil {
			t.Fatalf("write websocket message %d: %v", i+1, errWrite)
		}
		_, payload, errReadMessage := conn.ReadMessage()
		if errReadMessage != nil {
			t.Fatalf("read websocket message %d: %v", i+1, errReadMessage)
		}
		if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeCompleted {
			t.Fatalf("message %d payload type = %s, want %s", i+1, got, wsEventTypeCompleted)
		}
	}

	if got := executor.AuthIDs(); len(got) != 2 || got[0] != "auth-sse" || got[1] != "auth-ws" {
		t.Fatalf("selected auth IDs = %v, want [auth-sse auth-ws]", got)
	}
}

func TestResponsesWebsocketReleasesPinnedAuthAfterQuotaError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	selector := &orderedWebsocketSelector{order: []string{"auth-a", "auth-b"}}
	executor := &websocketPinnedFailoverExecutor{}
	manager := coreauth.NewManager(nil, selector, nil)
	manager.RegisterExecutor(executor)

	authA := &coreauth.Auth{
		ID:         "auth-a",
		Provider:   executor.Identifier(),
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, err := manager.Register(context.Background(), authA); err != nil {
		t.Fatalf("Register auth A: %v", err)
	}
	authB := &coreauth.Auth{
		ID:         "auth-b",
		Provider:   executor.Identifier(),
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, err := manager.Register(context.Background(), authB); err != nil {
		t.Fatalf("Register auth B: %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(authA.ID, authA.Provider, []*registry.ModelInfo{{ID: "quota-model"}})
	registry.GetGlobalRegistry().RegisterClient(authB.ID, authB.Provider, []*registry.ModelInfo{{ID: "quota-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authA.ID)
		registry.GetGlobalRegistry().UnregisterClient(authB.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			t.Fatalf("close websocket: %v", errClose)
		}
	}()

	requests := []string{
		`{"type":"response.create","model":"quota-model","input":[{"type":"message","id":"msg-1"}]}`,
		`{"type":"response.create","previous_response_id":"resp-auth-a-1","input":[{"type":"message","id":"msg-2"}]}`,
		`{"type":"response.create","previous_response_id":"resp-auth-a-1","input":[{"type":"message","id":"msg-3"}]}`,
	}
	wantTypes := []string{wsEventTypeCompleted, wsEventTypeError, wsEventTypeCompleted}
	for i := range requests {
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(requests[i])); errWrite != nil {
			t.Fatalf("write websocket message %d: %v", i+1, errWrite)
		}
		_, payload, errReadMessage := conn.ReadMessage()
		if errReadMessage != nil {
			t.Fatalf("read websocket message %d: %v", i+1, errReadMessage)
		}
		if got := gjson.GetBytes(payload, "type").String(); got != wantTypes[i] {
			t.Fatalf("message %d payload type = %s, want %s: %s", i+1, got, wantTypes[i], payload)
		}
		if i == 1 && int(gjson.GetBytes(payload, "status").Int()) != http.StatusTooManyRequests {
			t.Fatalf("quota payload status = %d, want %d: %s", gjson.GetBytes(payload, "status").Int(), http.StatusTooManyRequests, payload)
		}
	}

	if got := executor.AuthIDs(); len(got) != 3 || got[0] != "auth-a" || got[1] != "auth-a" || got[2] != "auth-b" {
		t.Fatalf("selected auth IDs = %v, want [auth-a auth-a auth-b]", got)
	}

	authBPayloads := executor.Payloads("auth-b")
	if len(authBPayloads) != 1 {
		t.Fatalf("auth-b payload count = %d, want 1", len(authBPayloads))
	}
	authBPayload := authBPayloads[0]
	if gjson.GetBytes(authBPayload, "previous_response_id").Exists() {
		t.Fatalf("previous_response_id leaked after auth failover: %s", authBPayload)
	}
	authBInput := gjson.GetBytes(authBPayload, "input").Raw
	if !strings.Contains(authBInput, `"id":"msg-1"`) || !strings.Contains(authBInput, `"id":"msg-3"`) {
		t.Fatalf("auth-b replay input missing expected transcript items: %s", authBInput)
	}
}

func TestShouldReleaseResponsesWebsocketPinnedAuth(t *testing.T) {
	cases := []struct {
		name string
		err  *interfaces.ErrorMessage
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "request timeout", err: &interfaces.ErrorMessage{StatusCode: http.StatusRequestTimeout, Error: fmt.Errorf("stream closed before response.completed")}, want: true},
		{name: "service unavailable", err: &interfaces.ErrorMessage{StatusCode: http.StatusServiceUnavailable, Error: fmt.Errorf("websocket bootstrap failed")}, want: true},
		{name: "bad request", err: &interfaces.ErrorMessage{StatusCode: http.StatusBadRequest, Error: fmt.Errorf("invalid request")}, want: false},
		{name: "previous response missing", err: &interfaces.ErrorMessage{StatusCode: http.StatusBadRequest, Error: fmt.Errorf("previous_response_not_found")}, want: true},
		{name: "empty stream", err: &interfaces.ErrorMessage{StatusCode: http.StatusInternalServerError, Error: fmt.Errorf("empty_stream: upstream stream closed before first payload")}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldReleaseResponsesWebsocketPinnedAuth(tc.err); got != tc.want {
				t.Fatalf("shouldReleaseResponsesWebsocketPinnedAuth() = %v, want %v", got, tc.want)
			}
		})
	}
}

type websocketPinnedPrematureCloseExecutor struct {
	mu       sync.Mutex
	authIDs  []string
	calls    map[string]int
	payloads map[string][][]byte
}

func (e *websocketPinnedPrematureCloseExecutor) Identifier() string { return "test-provider" }

func (e *websocketPinnedPrematureCloseExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketPinnedPrematureCloseExecutor) ExecuteStream(_ context.Context, auth *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	authID := ""
	if auth != nil {
		authID = auth.ID
	}

	e.mu.Lock()
	if e.calls == nil {
		e.calls = make(map[string]int)
	}
	if e.payloads == nil {
		e.payloads = make(map[string][][]byte)
	}
	e.authIDs = append(e.authIDs, authID)
	e.calls[authID]++
	call := e.calls[authID]
	e.payloads[authID] = append(e.payloads[authID], bytes.Clone(req.Payload))
	e.mu.Unlock()

	if authID == "auth-a" && call == 2 {
		chunks := make(chan coreexecutor.StreamChunk, 1)
		chunks <- coreexecutor.StreamChunk{Payload: []byte(`{"type":"response.output_item.added","item":{"id":"partial-1","type":"message"}}`)}
		close(chunks)
		return &coreexecutor.StreamResult{Chunks: chunks}, nil
	}

	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp-%s-%d","output":[{"type":"message","id":"out-%s-%d"}]}}`, authID, call, authID, call))}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *websocketPinnedPrematureCloseExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *websocketPinnedPrematureCloseExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketPinnedPrematureCloseExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (e *websocketPinnedPrematureCloseExecutor) AuthIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.authIDs...)
}

func (e *websocketPinnedPrematureCloseExecutor) Payloads(authID string) [][]byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	src := e.payloads[authID]
	out := make([][]byte, len(src))
	for i := range src {
		out[i] = bytes.Clone(src[i])
	}
	return out
}

func TestResponsesWebsocketReleasesPinnedAuthAfterStreamClosed408(t *testing.T) {
	gin.SetMode(gin.TestMode)

	selector := &orderedWebsocketSelector{order: []string{"auth-a", "auth-b"}}
	executor := &websocketPinnedPrematureCloseExecutor{}
	manager := coreauth.NewManager(nil, selector, nil)
	manager.RegisterExecutor(executor)

	authA := &coreauth.Auth{
		ID:         "auth-a",
		Provider:   executor.Identifier(),
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, err := manager.Register(context.Background(), authA); err != nil {
		t.Fatalf("Register auth A: %v", err)
	}
	authB := &coreauth.Auth{
		ID:         "auth-b",
		Provider:   executor.Identifier(),
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, err := manager.Register(context.Background(), authB); err != nil {
		t.Fatalf("Register auth B: %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(authA.ID, authA.Provider, []*registry.ModelInfo{{ID: "stream-model"}})
	registry.GetGlobalRegistry().RegisterClient(authB.ID, authB.Provider, []*registry.ModelInfo{{ID: "stream-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authA.ID)
		registry.GetGlobalRegistry().UnregisterClient(authB.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			t.Fatalf("close websocket: %v", errClose)
		}
	}()

	requests := []string{
		`{"type":"response.create","model":"stream-model","input":[{"type":"message","id":"msg-1"}]}`,
		`{"type":"response.create","previous_response_id":"resp-auth-a-1","input":[{"type":"message","id":"msg-2"}]}`,
		`{"type":"response.create","previous_response_id":"resp-auth-a-1","input":[{"type":"message","id":"msg-3"}]}`,
	}
	wantTypes := []string{wsEventTypeCompleted, wsEventTypeError, wsEventTypeCompleted}
	for i := range requests {
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(requests[i])); errWrite != nil {
			t.Fatalf("write websocket message %d: %v", i+1, errWrite)
		}
		if i == 1 {
			gotError := false
			for {
				_, payload, errReadMessage := conn.ReadMessage()
				if errReadMessage != nil {
					t.Fatalf("read websocket message %d: %v", i+1, errReadMessage)
				}
				got := gjson.GetBytes(payload, "type").String()
				if got == wsEventTypeError {
					if int(gjson.GetBytes(payload, "status").Int()) != http.StatusRequestTimeout {
						t.Fatalf("stream-closed payload status = %d, want %d: %s", gjson.GetBytes(payload, "status").Int(), http.StatusRequestTimeout, payload)
					}
					gotError = true
					break
				}
				if got == wsEventTypeCompleted {
					t.Fatalf("message %d unexpectedly completed: %s", i+1, payload)
				}
			}
			if !gotError {
				t.Fatalf("message %d did not return stream-closed error", i+1)
			}
			continue
		}
		_, payload, errReadMessage := conn.ReadMessage()
		if errReadMessage != nil {
			t.Fatalf("read websocket message %d: %v", i+1, errReadMessage)
		}
		if got := gjson.GetBytes(payload, "type").String(); got != wantTypes[i] {
			t.Fatalf("message %d payload type = %s, want %s: %s", i+1, got, wantTypes[i], payload)
		}
	}

	authIDs := executor.AuthIDs()
	if len(authIDs) != 3 || authIDs[0] != "auth-a" || authIDs[1] != "auth-a" {
		t.Fatalf("selected auth IDs = %v, want auth-a for first two turns", authIDs)
	}

	replayAuthID := authIDs[2]
	replayPayloads := executor.Payloads(replayAuthID)
	if len(replayPayloads) == 0 {
		t.Fatalf("replay auth %s has no payloads", replayAuthID)
	}
	replayPayload := replayPayloads[len(replayPayloads)-1]
	if gjson.GetBytes(replayPayload, "previous_response_id").Exists() {
		t.Fatalf("previous_response_id leaked after stream-closed replay: %s", replayPayload)
	}
	replayInput := gjson.GetBytes(replayPayload, "input").Raw
	if !strings.Contains(replayInput, `"id":"msg-1"`) || !strings.Contains(replayInput, `"id":"msg-3"`) {
		t.Fatalf("replay input missing expected transcript items: %s", replayInput)
	}
}

func TestNormalizeResponsesWebsocketRequestTreatsTranscriptReplacementAsReset(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"input":[{"type":"message","id":"msg-1"},{"type":"function_call","id":"fc-1","call_id":"call-1"},{"type":"function_call_output","id":"tool-out-1","call_id":"call-1"},{"type":"message","id":"assistant-1","role":"assistant"}]}`)
	lastResponseOutput := []byte(`[
		{"type":"message","id":"assistant-1","role":"assistant"}
	]`)
	raw := []byte(`{"type":"response.create","input":[{"type":"function_call","id":"fc-compact","call_id":"call-1","name":"tool"},{"type":"message","id":"msg-2"}]}`)

	normalized, next, errMsg := normalizeResponsesWebsocketRequest(raw, lastRequest, lastResponseOutput)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	if gjson.GetBytes(normalized, "previous_response_id").Exists() {
		t.Fatalf("previous_response_id must not exist in transcript replacement mode")
	}
	items := gjson.GetBytes(normalized, "input").Array()
	if len(items) != 2 {
		t.Fatalf("replacement input len = %d, want 2: %s", len(items), normalized)
	}
	if items[0].Get("id").String() != "fc-compact" || items[1].Get("id").String() != "msg-2" {
		t.Fatalf("replacement transcript was not preserved as-is: %s", normalized)
	}
	if !bytes.Equal(next, normalized) {
		t.Fatalf("next request snapshot should match replacement request")
	}
}

func TestNormalizeResponsesWebsocketRequestDoesNotTreatDeveloperMessageAsReplacement(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"input":[{"type":"message","id":"msg-1"}]}`)
	lastResponseOutput := []byte(`[
		{"type":"message","id":"assistant-1","role":"assistant"}
	]`)
	raw := []byte(`{"type":"response.create","input":[{"type":"message","id":"dev-1","role":"developer"},{"type":"message","id":"msg-2"}]}`)

	normalized, next, errMsg := normalizeResponsesWebsocketRequest(raw, lastRequest, lastResponseOutput)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	items := gjson.GetBytes(normalized, "input").Array()
	if len(items) != 4 {
		t.Fatalf("merged input len = %d, want 4: %s", len(items), normalized)
	}
	if items[0].Get("id").String() != "msg-1" ||
		items[1].Get("id").String() != "assistant-1" ||
		items[2].Get("id").String() != "dev-1" ||
		items[3].Get("id").String() != "msg-2" {
		t.Fatalf("developer follow-up should preserve merge behavior: %s", normalized)
	}
	if !bytes.Equal(next, normalized) {
		t.Fatalf("next request snapshot should match merged request")
	}
}

func TestNormalizeResponsesWebsocketRequestDropsDuplicateFunctionCallsByCallID(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"input":[{"type":"function_call","id":"fc-1","call_id":"call-1"},{"type":"function_call_output","id":"tool-out-1","call_id":"call-1"}]}`)
	lastResponseOutput := []byte(`[
		{"type":"function_call","id":"fc-1","call_id":"call-1","name":"tool"}
	]`)
	raw := []byte(`{"type":"response.create","input":[{"type":"message","id":"msg-2"}]}`)

	normalized, _, errMsg := normalizeResponsesWebsocketRequest(raw, lastRequest, lastResponseOutput)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}

	items := gjson.GetBytes(normalized, "input").Array()
	if len(items) != 3 {
		t.Fatalf("merged input len = %d, want 3: %s", len(items), normalized)
	}
	if items[0].Get("id").String() != "fc-1" ||
		items[1].Get("id").String() != "tool-out-1" ||
		items[2].Get("id").String() != "msg-2" {
		t.Fatalf("unexpected merged input order: %s", normalized)
	}
}

func TestNormalizeResponsesWebsocketRequestDropsDuplicateInputItemsByID(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"input":[{"type":"message","id":"msg-1","role":"user"}]}`)
	lastResponseOutput := []byte(`[
		{"type":"function_call","id":"fc-1","call_id":"call-1","name":"tool"}
	]`)
	raw := []byte(`{"type":"response.create","previous_response_id":"resp-1","input":[{"type":"function_call","id":"fc-1","call_id":"call-2","name":"tool"},{"type":"function_call_output","id":"tool-out-1","call_id":"call-2"}]}`)

	normalized, _, errMsg := normalizeResponsesWebsocketRequestWithMode(raw, lastRequest, lastResponseOutput, false, true)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}

	items := gjson.GetBytes(normalized, "input").Array()
	if len(items) != 3 {
		t.Fatalf("merged input len = %d, want 3: %s", len(items), normalized)
	}
	if items[0].Get("id").String() != "msg-1" ||
		items[1].Get("id").String() != "fc-1" ||
		items[1].Get("call_id").String() != "call-2" ||
		items[2].Get("id").String() != "tool-out-1" {
		t.Fatalf("unexpected merged input order: %s", normalized)
	}
}

func TestNormalizeResponsesWebsocketRequestTreatsCustomToolTranscriptReplacementAsReset(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"input":[{"type":"message","id":"msg-1"},{"type":"custom_tool_call","id":"ctc-1","call_id":"call-1","name":"apply_patch"},{"type":"custom_tool_call_output","id":"tool-out-1","call_id":"call-1"},{"type":"message","id":"assistant-1","role":"assistant"}]}`)
	lastResponseOutput := []byte(`[
		{"type":"message","id":"assistant-1","role":"assistant"}
	]`)
	raw := []byte(`{"type":"response.create","input":[{"type":"custom_tool_call","id":"ctc-compact","call_id":"call-1","name":"apply_patch"},{"type":"custom_tool_call_output","id":"tool-out-compact","call_id":"call-1"},{"type":"message","id":"msg-2"}]}`)

	normalized, next, errMsg := normalizeResponsesWebsocketRequest(raw, lastRequest, lastResponseOutput)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	if gjson.GetBytes(normalized, "previous_response_id").Exists() {
		t.Fatalf("previous_response_id must not exist in transcript replacement mode")
	}
	items := gjson.GetBytes(normalized, "input").Array()
	if len(items) != 3 {
		t.Fatalf("replacement input len = %d, want 3: %s", len(items), normalized)
	}
	if items[0].Get("id").String() != "ctc-compact" ||
		items[1].Get("id").String() != "tool-out-compact" ||
		items[2].Get("id").String() != "msg-2" {
		t.Fatalf("replacement transcript was not preserved as-is: %s", normalized)
	}
	if !bytes.Equal(next, normalized) {
		t.Fatalf("next request snapshot should match replacement request")
	}
}

func TestNormalizeResponsesWebsocketRequestDropsDuplicateCustomToolCallsByCallID(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"input":[{"type":"custom_tool_call","id":"ctc-1","call_id":"call-1","name":"apply_patch"},{"type":"custom_tool_call_output","id":"tool-out-1","call_id":"call-1"}]}`)
	lastResponseOutput := []byte(`[
		{"type":"custom_tool_call","id":"ctc-1","call_id":"call-1","name":"apply_patch"}
	]`)
	raw := []byte(`{"type":"response.create","input":[{"type":"message","id":"msg-2"}]}`)

	normalized, _, errMsg := normalizeResponsesWebsocketRequest(raw, lastRequest, lastResponseOutput)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}

	items := gjson.GetBytes(normalized, "input").Array()
	if len(items) != 3 {
		t.Fatalf("merged input len = %d, want 3: %s", len(items), normalized)
	}
	if items[0].Get("id").String() != "ctc-1" ||
		items[1].Get("id").String() != "tool-out-1" ||
		items[2].Get("id").String() != "msg-2" {
		t.Fatalf("unexpected merged input order: %s", normalized)
	}
}

func TestDedupeResponsesWebsocketInputItemsByIDAfterRepair(t *testing.T) {
	payload := []byte(`{"input":[{"type":"custom_tool_call","id":"ctc-1","call_id":"call-1","name":"tool"},{"type":"custom_tool_call","id":"ctc-1","call_id":"call-2","name":"tool"},{"type":"custom_tool_call_output","id":"tool-out-1","call_id":"call-2"}]}`)

	deduped := dedupeResponsesWebsocketInputItemsByID(payload)

	items := gjson.GetBytes(deduped, "input").Array()
	if len(items) != 2 {
		t.Fatalf("deduped input len = %d, want 2: %s", len(items), deduped)
	}
	if items[0].Get("id").String() != "ctc-1" ||
		items[0].Get("call_id").String() != "call-2" ||
		items[1].Get("id").String() != "tool-out-1" {
		t.Fatalf("unexpected deduped input: %s", deduped)
	}
}

func TestDedupeResponsesWebsocketInputItemsByIDKeepsReferencedToolCall(t *testing.T) {
	// Two function_call items share the same id but carry different call_ids
	// (e.g. the upstream reused the item id across a re-sent/repaired call).
	// Only the first call_id has a matching function_call_output. Deduping by
	// id must keep the referenced call so the output is not orphaned, which
	// previously triggered an upstream 400 "No tool call found for function
	// call output with call_id ...".
	payload := []byte(`{"input":[{"type":"function_call","id":"fc-1","call_id":"call-1","name":"exec_command"},{"type":"function_call","id":"fc-1","call_id":"call-2","name":"exec_command"},{"type":"function_call_output","id":"fco-1","call_id":"call-1"}]}`)

	deduped := dedupeResponsesWebsocketInputItemsByID(payload)

	items := gjson.GetBytes(deduped, "input").Array()
	if len(items) != 2 {
		t.Fatalf("deduped input len = %d, want 2: %s", len(items), deduped)
	}
	if items[0].Get("id").String() != "fc-1" ||
		items[0].Get("call_id").String() != "call-1" ||
		items[1].Get("id").String() != "fco-1" ||
		items[1].Get("call_id").String() != "call-1" {
		t.Fatalf("unexpected deduped input: %s", deduped)
	}
}

func TestResponsesWebsocketCompactionResetsTurnStateOnCustomToolTranscriptReplacement(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &websocketCompactionCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: "auth-sse", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)
	router.POST("/v1/responses/compact", h.Compact)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			t.Fatalf("close websocket: %v", errClose)
		}
	}()

	requests := []string{
		`{"type":"response.create","model":"test-model","input":[{"type":"message","id":"msg-1"}]}`,
		`{"type":"response.create","input":[{"type":"custom_tool_call_output","call_id":"call-1","id":"tool-out-1"}]}`,
	}
	for i := range requests {
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(requests[i])); errWrite != nil {
			t.Fatalf("write websocket message %d: %v", i+1, errWrite)
		}
		_, payload, errReadMessage := conn.ReadMessage()
		if errReadMessage != nil {
			t.Fatalf("read websocket message %d: %v", i+1, errReadMessage)
		}
		if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeCompleted {
			t.Fatalf("message %d payload type = %s, want %s", i+1, got, wsEventTypeCompleted)
		}
	}

	compactResp, errPost := server.Client().Post(
		server.URL+"/v1/responses/compact",
		"application/json",
		strings.NewReader(`{"model":"test-model","input":[{"type":"message","id":"summary-1"}]}`),
	)
	if errPost != nil {
		t.Fatalf("compact request failed: %v", errPost)
	}
	if errClose := compactResp.Body.Close(); errClose != nil {
		t.Fatalf("close compact response body: %v", errClose)
	}
	if compactResp.StatusCode != http.StatusOK {
		t.Fatalf("compact status = %d, want %d", compactResp.StatusCode, http.StatusOK)
	}

	postCompact := `{"type":"response.create","input":[{"type":"custom_tool_call","id":"ctc-compact","call_id":"call-1","name":"apply_patch"},{"type":"custom_tool_call_output","id":"tool-out-compact","call_id":"call-1"},{"type":"message","id":"msg-2"}]}`
	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(postCompact)); errWrite != nil {
		t.Fatalf("write post-compact websocket message: %v", errWrite)
	}
	_, payload, errReadMessage := conn.ReadMessage()
	if errReadMessage != nil {
		t.Fatalf("read post-compact websocket message: %v", errReadMessage)
	}
	if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeCompleted {
		t.Fatalf("post-compact payload type = %s, want %s", got, wsEventTypeCompleted)
	}

	executor.mu.Lock()
	defer executor.mu.Unlock()

	if executor.compactPayload == nil {
		t.Fatalf("compact payload was not captured")
	}
	if len(executor.streamPayloads) != 3 {
		t.Fatalf("stream payload count = %d, want 3", len(executor.streamPayloads))
	}

	merged := executor.streamPayloads[2]
	items := gjson.GetBytes(merged, "input").Array()
	if len(items) != 3 {
		t.Fatalf("merged input len = %d, want 3: %s", len(items), merged)
	}
	if items[0].Get("id").String() != "ctc-compact" ||
		items[1].Get("id").String() != "tool-out-compact" ||
		items[2].Get("id").String() != "msg-2" {
		t.Fatalf("unexpected post-compact input order: %s", merged)
	}
	if items[0].Get("call_id").String() != "call-1" {
		t.Fatalf("post-compact custom tool call id = %s, want call-1", items[0].Get("call_id").String())
	}
}

func TestResponsesWebsocketCompactionResetsTurnStateOnTranscriptReplacement(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &websocketCompactionCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: "auth-sse", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)
	router.POST("/v1/responses/compact", h.Compact)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			t.Fatalf("close websocket: %v", errClose)
		}
	}()

	requests := []string{
		`{"type":"response.create","model":"test-model","input":[{"type":"message","id":"msg-1"}]}`,
		`{"type":"response.create","input":[{"type":"function_call_output","call_id":"call-1","id":"tool-out-1"}]}`,
	}
	for i := range requests {
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(requests[i])); errWrite != nil {
			t.Fatalf("write websocket message %d: %v", i+1, errWrite)
		}
		_, payload, errReadMessage := conn.ReadMessage()
		if errReadMessage != nil {
			t.Fatalf("read websocket message %d: %v", i+1, errReadMessage)
		}
		if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeCompleted {
			t.Fatalf("message %d payload type = %s, want %s", i+1, got, wsEventTypeCompleted)
		}
	}

	compactResp, errPost := server.Client().Post(
		server.URL+"/v1/responses/compact",
		"application/json",
		strings.NewReader(`{"model":"test-model","input":[{"type":"message","id":"summary-1"}]}`),
	)
	if errPost != nil {
		t.Fatalf("compact request failed: %v", errPost)
	}
	if errClose := compactResp.Body.Close(); errClose != nil {
		t.Fatalf("close compact response body: %v", errClose)
	}
	if compactResp.StatusCode != http.StatusOK {
		t.Fatalf("compact status = %d, want %d", compactResp.StatusCode, http.StatusOK)
	}

	// Simulate a post-compaction client turn that replaces local history with a compacted transcript.
	// The websocket handler must treat this as a state reset, not append it to stale pre-compaction state.
	postCompact := `{"type":"response.create","input":[{"type":"function_call","id":"fc-compact","call_id":"call-1","name":"tool"},{"type":"message","id":"msg-2"}]}`
	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(postCompact)); errWrite != nil {
		t.Fatalf("write post-compact websocket message: %v", errWrite)
	}
	_, payload, errReadMessage := conn.ReadMessage()
	if errReadMessage != nil {
		t.Fatalf("read post-compact websocket message: %v", errReadMessage)
	}
	if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeCompleted {
		t.Fatalf("post-compact payload type = %s, want %s", got, wsEventTypeCompleted)
	}

	executor.mu.Lock()
	defer executor.mu.Unlock()

	if executor.compactPayload == nil {
		t.Fatalf("compact payload was not captured")
	}
	if len(executor.streamPayloads) != 3 {
		t.Fatalf("stream payload count = %d, want 3", len(executor.streamPayloads))
	}

	merged := executor.streamPayloads[2]
	items := gjson.GetBytes(merged, "input").Array()
	if len(items) != 2 {
		t.Fatalf("merged input len = %d, want 2: %s", len(items), merged)
	}
	if items[0].Get("id").String() != "fc-compact" ||
		items[1].Get("id").String() != "msg-2" {
		t.Fatalf("unexpected post-compact input order: %s", merged)
	}
	if items[0].Get("call_id").String() != "call-1" {
		t.Fatalf("post-compact function call id = %s, want call-1", items[0].Get("call_id").String())
	}
}

func TestInputContainsFullTranscriptFalseForAssistantMessageOnly(t *testing.T) {
	input := gjson.Parse(`[
		{"type":"message","role":"user","content":"hello"},
		{"type":"message","role":"assistant","content":"hi there"}
	]`)
	if inputContainsFullTranscript(input) {
		t.Fatal("assistant message alone must not be treated as full transcript")
	}
}

func TestInputContainsFullTranscriptDetectsCompactionItem(t *testing.T) {
	for _, typ := range []string{"compaction", "compaction_summary"} {
		input := gjson.Parse(`[{"type":"message","role":"user","content":"hello"},{"type":"` + typ + `","encrypted_content":"summary"}]`)
		if !inputContainsFullTranscript(input) {
			t.Fatalf("expected full transcript for type=%s", typ)
		}
	}
}

func TestInputContainsFullTranscriptFalseForIncremental(t *testing.T) {
	// Normal incremental turns: user messages or function_call_output only.
	for _, raw := range []string{
		`[{"type":"function_call_output","call_id":"call-1","output":"result"}]`,
		`[{"type":"message","role":"user","content":"next question"}]`,
		`[]`,
	} {
		if inputContainsFullTranscript(gjson.Parse(raw)) {
			t.Fatalf("incremental input must not be detected as full transcript: %s", raw)
		}
	}
}

func TestNormalizeSubsequentRequestCompactSkipsMerge(t *testing.T) {
	lastRequest := []byte(`{"model":"gpt-5.4","stream":true,"input":[
		{"type":"message","role":"user","id":"msg-1","content":"original long prompt"},
		{"type":"message","role":"assistant","id":"msg-2","content":"original long response"},
		{"type":"function_call","id":"fc-1","call_id":"call-old","name":"bash","arguments":"{}"},
		{"type":"function_call_output","id":"fco-1","call_id":"call-old","output":"old result"}
	]}`)
	lastResponseOutput := []byte(`[
		{"type":"message","role":"assistant","id":"msg-3","content":"another assistant reply"},
		{"type":"function_call","id":"fc-2","call_id":"call-stale","name":"read","arguments":"{}"}
	]`)

	// Remote compact response: user messages + compaction item, NO assistant message.
	// This is the primary compact scenario from Codex CLI.
	raw := []byte(`{"type":"response.create","input":[
		{"type":"message","role":"user","id":"msg-1c","content":"compacted user msg"},
		{"type":"compaction","encrypted_content":"conversation summary"}
	]}`)

	normalized, _, errMsg := normalizeResponsesWebsocketRequest(raw, lastRequest, lastResponseOutput)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}

	input := gjson.GetBytes(normalized, "input").Array()
	if len(input) != 2 {
		t.Fatalf("input len = %d, want 2 (compacted only); stale state was not skipped", len(input))
	}
	if input[0].Get("id").String() != "msg-1c" {
		t.Fatalf("input[0].id = %q, want %q", input[0].Get("id").String(), "msg-1c")
	}
	if input[1].Get("type").String() != "compaction" {
		t.Fatalf("input[1].type = %q, want %q", input[1].Get("type").String(), "compaction")
	}
}

func TestNormalizeSubsequentRequestReasoningContinuationWithPreviousResponseID(t *testing.T) {
	lastRequest := []byte(`{"model":"gpt-5.6-terra","stream":true,"input":[{"type":"message","role":"user","id":"old-user","content":"long history"}]}`)
	lastResponseOutput := []byte(`[{"type":"function_call","id":"old-call","call_id":"old-call","name":"lookup","arguments":"{}"}]`)

	for _, requestType := range []string{"response.create", "response.append"} {
		t.Run(requestType, func(t *testing.T) {
			raw := []byte(`{"type":"` + requestType + `","previous_response_id":"resp-1","input":[
				{"type":"reasoning","id":"reasoning-1","summary":[]},
				{"type":"function_call_output","id":"output-1","call_id":"old-call","output":"result"}
			]}`)

			normalized, _, errMsg := normalizeResponsesWebsocketRequest(raw, lastRequest, lastResponseOutput)
			if errMsg != nil {
				t.Fatalf("unexpected error: %v", errMsg.Error)
			}
			if got := gjson.GetBytes(normalized, "previous_response_id").String(); got != "resp-1" {
				t.Fatalf("previous_response_id = %q, want resp-1; payload=%s", got, normalized)
			}
			input := gjson.GetBytes(normalized, "input").Array()
			if len(input) != 2 || input[0].Get("id").String() != "reasoning-1" || input[1].Get("id").String() != "output-1" {
				t.Fatalf("incremental continuation was replaced or merged: %s", normalized)
			}
		})
	}
}

func TestResponsesWebsocketOutputCollectorRestoresCompletedOutput(t *testing.T) {
	outputItemsByIndex := make(map[int64][]byte)
	var outputItemsFallback [][]byte
	for _, payload := range [][]byte{
		[]byte(`{"type":"response.output_item.done","output_index":1,"item":{"type":"message","id":"reply-1","role":"assistant"}}`),
		[]byte(`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"summary-1","summary":[]}}`),
		[]byte(`{"type":"response.output_item.done","item":{"type":"function_call","id":"call-1","call_id":"call-1","name":"exec","arguments":"{}"}}`),
	} {
		collectResponsesWebsocketOutputItem(payload, outputItemsByIndex, &outputItemsFallback)
	}

	output := responseCompletedOutputFromPayload(
		[]byte(`{"type":"response.completed","response":{"id":"resp-1","output":[]}}`),
		outputItemsByIndex,
		outputItemsFallback,
	)
	items := gjson.ParseBytes(output).Array()
	if len(items) != 3 {
		t.Fatalf("collected output len = %d, want 3: %s", len(items), output)
	}
	wantIDs := []string{"summary-1", "reply-1", "call-1"}
	for i, wantID := range wantIDs {
		if got := items[i].Get("id").String(); got != wantID {
			t.Fatalf("output[%d].id = %q, want %q: %s", i, got, wantID, output)
		}
	}
}

func TestNormalizeSubsequentRequestCompactMergesWhenCompactionReplayUnsupported(t *testing.T) {
	lastRequest := []byte(`{"model":"gpt-5.4","stream":true,"input":[
		{"type":"message","role":"user","id":"msg-1","content":"original long prompt"},
		{"type":"message","role":"assistant","id":"msg-2","content":"original long response"},
		{"type":"function_call","id":"fc-1","call_id":"call-old","name":"bash","arguments":"{}"},
		{"type":"function_call_output","id":"fco-1","call_id":"call-old","output":"old result"}
	]}`)
	lastResponseOutput := []byte(`[
		{"type":"message","role":"assistant","id":"msg-3","content":"another assistant reply"},
		{"type":"function_call","id":"fc-2","call_id":"call-stale","name":"read","arguments":"{}"}
	]`)
	raw := []byte(`{"type":"response.create","input":[
		{"type":"message","role":"user","id":"msg-1c","content":"compacted user msg"},
		{"type":"compaction","encrypted_content":"conversation summary"}
	]}`)

	normalized, _, errMsg := normalizeResponsesWebsocketRequestWithMode(raw, lastRequest, lastResponseOutput, false, false)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}

	input := gjson.GetBytes(normalized, "input").Array()
	if len(input) != 7 {
		t.Fatalf("input len = %d, want 7 (merged fallback without compaction items)", len(input))
	}
	wantIDs := []string{"msg-1", "msg-2", "fc-1", "fco-1", "msg-3", "fc-2", "msg-1c"}
	for i, want := range wantIDs {
		got := input[i].Get("id").String()
		if got != want {
			t.Fatalf("input[%d].id = %q, want %q", i, got, want)
		}
	}
	for _, item := range input {
		if item.Get("type").String() == "compaction" || item.Get("type").String() == "compaction_summary" {
			t.Fatalf("compaction items must be stripped for unsupported downstream fallback: %s", item.Raw)
		}
	}
}

func TestNormalizeSubsequentRequestIncrementalInputStillMerges(t *testing.T) {
	// Normal incremental flow: user sends function_call_output (no assistant message).
	lastRequest := []byte(`{"model":"gpt-5.4","stream":true,"input":[
		{"type":"message","role":"user","id":"msg-1","content":"hello"}
	]}`)
	lastResponseOutput := []byte(`[
		{"type":"message","role":"assistant","id":"msg-2","content":"let me check"},
		{"type":"function_call","id":"fc-1","call_id":"call-1","name":"bash","arguments":"{}"}
	]`)
	raw := []byte(`{"type":"response.create","input":[
		{"type":"function_call_output","call_id":"call-1","id":"fco-1","output":"done"}
	]}`)

	normalized, _, errMsg := normalizeResponsesWebsocketRequest(raw, lastRequest, lastResponseOutput)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}

	input := gjson.GetBytes(normalized, "input").Array()

	// Should be merged: msg-1 + msg-2 + fc-1 + fco-1 = 4 items
	if len(input) != 4 {
		t.Fatalf("input len = %d, want 4 (merged)", len(input))
	}
	wantIDs := []string{"msg-1", "msg-2", "fc-1", "fco-1"}
	for i, want := range wantIDs {
		got := input[i].Get("id").String()
		if got != want {
			t.Fatalf("input[%d].id = %q, want %q", i, got, want)
		}
	}
}

func TestNormalizeSubsequentRequestAssistantInputTriggersTranscriptReplacement(t *testing.T) {
	// After dev's shouldReplaceWebsocketTranscript, assistant messages in input
	// trigger transcript replacement (no merge with prior state).
	lastRequest := []byte(`{"model":"gpt-5.4","stream":true,"input":[
		{"type":"message","role":"user","id":"msg-1","content":"hello"}
	]}`)
	lastResponseOutput := []byte(`[
		{"type":"message","role":"assistant","id":"msg-2","content":"prior assistant"},
		{"type":"function_call","id":"fc-1","call_id":"call-1","name":"bash","arguments":"{}"}
	]`)
	raw := []byte(`{"type":"response.append","input":[
		{"type":"message","role":"assistant","id":"msg-3","content":"patched assistant turn"}
	]}`)

	normalized, _, errMsg := normalizeResponsesWebsocketRequest(raw, lastRequest, lastResponseOutput)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}

	input := gjson.GetBytes(normalized, "input").Array()
	if len(input) != 1 {
		t.Fatalf("input len = %d, want 1 (transcript replacement, not merge)", len(input))
	}
	if input[0].Get("id").String() != "msg-3" {
		t.Fatalf("input[0].id = %q, want %q", input[0].Get("id").String(), "msg-3")
	}
}
