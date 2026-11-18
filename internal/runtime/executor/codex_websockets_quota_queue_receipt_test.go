package executor

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestCodexWebsocketQueuedQuotaRetainsSocketReceipt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pongAt := make(chan time.Time, 1)
	serverDone := make(chan struct{})
	quota := []byte("{\"type\":\"codex.rate_limits\",\"rate_limits\":{\"primary\":{\"used_percent\":20,\"window_minutes\":10080,\"reset_after_seconds\":120}}}")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(serverDone)
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		conn.SetPongHandler(func(string) error {
			select {
			case pongAt <- time.Now():
			default:
			}
			return nil
		})
		if _, _, errRead := conn.ReadMessage(); errRead != nil {
			t.Errorf("read request: %v", errRead)
			return
		}
		for _, frame := range [][]byte{
			[]byte("{\"type\":\"response.created\",\"response\":{\"id\":\"receipt-response\"}}"),
			[]byte("{\"type\":\"response.output_text.delta\",\"delta\":\"first\"}"),
			quota,
			[]byte("{\"type\":\"response.completed\",\"response\":{\"id\":\"receipt-response\",\"output\":[],\"usage\":{\"input_tokens\":0,\"output_tokens\":0,\"total_tokens\":0}}}"),
		} {
			if errWrite := conn.WriteMessage(websocket.TextMessage, frame); errWrite != nil {
				t.Errorf("write response: %v", errWrite)
				return
			}
		}
		// The socket reader must pass every preceding frame before it answers
		// this ping, independently of the blocked downstream stream consumer.
		if errPing := conn.WriteControl(websocket.PingMessage, []byte("receipt-barrier"), time.Now().Add(5*time.Second)); errPing != nil {
			t.Errorf("write receipt barrier: %v", errPing)
			return
		}
		for {
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
		}
	}))
	t.Cleanup(upstream.Close)
	exec := NewCodexWebsocketsExecutor(&config.Config{
		SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll},
	})
	const sessionID = "quota-receipt-backpressure"
	t.Cleanup(func() {
		exec.CloseExecutionSession(sessionID)
		select {
		case <-serverDone:
		case <-time.After(5 * time.Second):
			t.Error("upstream handler did not finish")
		}
	})
	manager := cliproxyauth.NewManager(nil, nil, nil)
	selected, err := manager.Register(ctx, &cliproxyauth.Auth{
		ID: "quota-receipt", Provider: "codex", Status: cliproxyauth.StatusActive,
		Attributes: map[string]string{"api_key": "test", "base_url": upstream.URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx = cliproxyexecutor.WithDownstreamWebsocket(internallogging.WithResponseHeadersHolder(ctx))
	result, err := exec.ExecuteStream(ctx, selected, cliproxyexecutor.Request{
		Model: "gpt-5-codex", Payload: []byte("{\"model\":\"gpt-5-codex\",\"input\":[]}"),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FromString("openai-response"),
		ResponseFormat: sdktranslator.FromString("openai-response"),
		Metadata:       map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: sessionID},
	})
	if err != nil {
		t.Fatal(err)
	}
	var barrier time.Time
	select {
	case barrier = <-pongAt:
	case <-ctx.Done():
		t.Fatal("socket reader did not reach the receipt barrier")
	}
	// A canonical usage observation arrives after the queued quota frame, but
	// before downstream releases the worker that will account for that frame.
	freshAt := barrier.Add(time.Nanosecond)
	freshReset := freshAt.Add(7 * 24 * time.Hour)
	if manager.RecordQuotaResetScheduleIfUnchanged(ctx, selected, cliproxyauth.QuotaResetSchedule{
		WeeklyResetAt: freshReset, ObservedAt: freshAt,
	}) == nil {
		t.Fatal("fresh usage observation was not accepted")
	}
	sawRawQuota := false
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
		if bytes.Equal(chunk.Payload, quota) {
			sawRawQuota = true
		}
	}
	if !sawRawQuota {
		t.Error("quota frame was not forwarded unchanged")
	}
	_, receipts := internallogging.GetResponseHeadersWithReceiptTimes(ctx)
	if got := receipts["X-Codex-Primary-Reset-At"]; got.IsZero() || got.After(barrier) {
		t.Errorf("queued quota observation used dequeue time: got=%v barrier=%v", got, barrier)
	}
	manager.MarkResult(ctx, cliproxyauth.Result{AuthID: selected.ID, Provider: "codex", Success: true})
	got, _ := manager.GetByID(selected.ID)
	if !got.QuotaResetSchedule.WeeklyResetAt.Equal(freshReset) || !got.QuotaResetSchedule.ObservedAt.Equal(freshAt) {
		t.Errorf("old queued frame replaced newer usage reset: got=%+v want reset=%v at=%v", got.QuotaResetSchedule, freshReset, freshAt)
	}
}

func TestCodexWebsocketReadPreservesQueuedReceipt(t *testing.T) {
	conn, previous := &websocket.Conn{}, &websocket.Conn{}
	receivedAt := time.Date(2026, time.November, 16, 12, 0, 0, 123, time.UTC)
	payload := []byte("queued frame")
	readCh := make(chan codexWebsocketRead, 2)
	readCh <- codexWebsocketRead{conn: previous, msgType: websocket.TextMessage, payload: []byte("previous connection"), receivedAt: receivedAt.Add(-time.Hour)}
	readCh <- codexWebsocketRead{conn: conn, msgType: websocket.TextMessage, payload: payload, receivedAt: receivedAt}
	kind, got, at, err := readCodexWebsocketMessage(context.Background(), &codexWebsocketSession{}, conn, readCh)
	if err != nil || kind != websocket.TextMessage || !bytes.Equal(got, payload) || !at.Equal(receivedAt) {
		t.Fatalf("queued frame receipt changed: kind=%v payload=%q at=%v err=%v", kind, got, at, err)
	}
}

func TestCodexWebsocketDirectReadReportsReceipt(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte("direct frame")); errWrite != nil {
			t.Errorf("write direct frame: %v", errWrite)
		}
	}))
	defer upstream.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(upstream.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	before := time.Now()
	kind, payload, receivedAt, err := readCodexWebsocketMessage(context.Background(), nil, conn, nil)
	after := time.Now()
	if err != nil || kind != websocket.TextMessage || string(payload) != "direct frame" ||
		receivedAt.IsZero() || receivedAt.Before(before) || receivedAt.After(after) {
		t.Fatalf("invalid direct receipt: kind=%v payload=%q at=%v err=%v", kind, payload, receivedAt, err)
	}
}
