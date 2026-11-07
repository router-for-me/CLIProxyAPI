package openai

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type rawImageStreamRecorder struct {
	*httptest.ResponseRecorder
	writes chan []byte
}

func (w *rawImageStreamRecorder) Write(data []byte) (int, error) {
	n, err := w.ResponseRecorder.Write(data)
	w.writes <- bytes.Clone(data)
	return n, err
}

func (w *rawImageStreamRecorder) WriteString(data string) (int, error) {
	return w.Write([]byte(data))
}

func rawImageStreamWaitWrite(t *testing.T, w *rawImageStreamRecorder, want []byte) {
	t.Helper()
	select {
	case got := <-w.writes:
		if !bytes.Equal(got, want) {
			t.Fatalf("unexpected stream write: got %d bytes, want %d bytes", len(got), len(want))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream write did not arrive")
	}
}

func rawImageStreamTick(t *testing.T, ticks chan<- time.Time, done <-chan struct{}) {
	t.Helper()
	select {
	case ticks <- time.Time{}:
	case <-done:
		t.Fatal("stream stopped before the keepalive tick")
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not receive the keepalive tick")
	}
}

type rawImageStreamFixture struct {
	t             *testing.T
	writer        *rawImageStreamRecorder
	data          chan []byte
	errs          chan *interfaces.ErrorMessage
	ticks         chan time.Time
	done          chan struct{}
	stopped       chan struct{}
	cancelRequest context.CancelFunc
	cancelStream  context.CancelFunc
	result        error
}

func newRawImageStreamFixture(t *testing.T, first []byte) *rawImageStreamFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	f := &rawImageStreamFixture{t: t, writer: &rawImageStreamRecorder{ResponseRecorder: httptest.NewRecorder(), writes: make(chan []byte, 128)},
		data: make(chan []byte), errs: make(chan *interfaces.ErrorMessage, 1), ticks: make(chan time.Time), done: make(chan struct{}), stopped: make(chan struct{}),
	}
	restore := handlers.SetStreamingKeepAliveTickerFactoryForTest(func(time.Duration) (<-chan time.Time, func()) {
		return f.ticks, func() { close(f.stopped) }
	})
	t.Cleanup(restore)
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	streamCtx, cancelStream := context.WithCancel(context.Background())
	f.cancelRequest, f.cancelStream = cancelRequest, cancelStream
	c, _ := gin.CreateTestContext(f.writer)
	c.Request = httptest.NewRequest(http.MethodPost, imagesGenerationsPath, nil).WithContext(requestCtx)
	setImagesSSEHeaders(c)
	cfg := &sdkconfig.SDKConfig{}
	cfg.Streaming.KeepAliveSeconds = 20
	h := NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(cfg, nil))
	go func() {
		defer close(f.done)
		h.forwardRawImageStream(streamCtx, c, func(err error) { f.result = err }, f.data, f.errs, first)
	}()
	t.Cleanup(func() {
		cancelRequest()
		cancelStream()
		f.waitDone()
	})
	if len(first) > 0 {
		rawImageStreamWaitWrite(t, f.writer, first)
	}
	return f
}

func (f *rawImageStreamFixture) send(chunk []byte) {
	f.t.Helper()
	select {
	case f.data <- chunk:
	case <-f.done:
		f.t.Fatal("stream stopped before receiving its next chunk")
	case <-time.After(5 * time.Second):
		f.t.Fatal("stream did not receive its next chunk")
	}
	rawImageStreamWaitWrite(f.t, f.writer, chunk)
}

func (f *rawImageStreamFixture) waitDone() {
	f.t.Helper()
	select {
	case <-f.done:
	case <-time.After(5 * time.Second):
		f.t.Fatal("stream did not stop")
	}
}

func TestRawImageStreamKeepAliveBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name     string
		chunks   []string
		suffix   string
		boundary bool
	}{
		{"empty", nil, "data: {}\n\n", true},
		{"first_chunk_partial_json", []string{"event: image_generation.completed\ndata: {\"b64_json\":\"QU"}, "JD\"}\n\n", false},
		{"later_chunk_partial_json", []string{"data: {}\n\n", "data: {\"b64_json\":\"QU"}, "JD\"}\n\n", false},
		{"complete_then_partial_in_one_chunk", []string{"data: {}\n\ndata: {\"b64_json\":\"QU"}, "JD\"}\n\n", false},
		{"lf_line_only", []string{"data: {}\n"}, "\n", false},
		{"lf_frame", []string{"data: {}\n\n"}, "data: {}\n\n", true},
		{"split_lf_delimiter", []string{"data: {}\n", "\n"}, "data: {}\n\n", true},
		{"crlf_line_only", []string{"data: {}\r", "\n"}, "\r\n", false},
		{"empty_chunk_within_crlf", []string{"data: {}\r", "", "\n"}, "\r\n", false},
		{"split_crlf_delimiter", []string{"data: {}\r", "\n\r", "\n"}, "data: {}\r\n\r\n", true},
		{"cr_at_frame_boundary", []string{"data: {}\r\n\r"}, "\n", true},
		{"cr_line_only", []string{"data: {}\r"}, "\r", false},
		{"cr_frame", []string{"data: {}\r", "\r"}, "data: {}\r\r", true},
		{"mixed_line_endings", []string{"data: {}\r", "\n\n"}, "data: {}\r\n\r\n", true},
		{"comment_within_frame", []string{"data: {\n:\ndata: \"ok\":true}\n"}, "\n", false},
		{"metadata_preserved", []string{"id: image-42\nretry: 1000\nevent: image_generation.completed\n: upstream-comment\ndata: {}\n\n"}, "data: {}\n\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var first []byte
			if len(tc.chunks) > 0 {
				first = []byte(tc.chunks[0])
			}
			f := newRawImageStreamFixture(t, first)
			for i, chunk := range tc.chunks {
				if i > 0 {
					f.send([]byte(chunk))
				}
			}
			rawImageStreamTick(t, f.ticks, f.done)
			if tc.boundary {
				rawImageStreamWaitWrite(t, f.writer, []byte(": keep-alive\n\n"))
			}
			// Receiving this next chunk also acknowledges completion of the tick
			// branch, so a forbidden heartbeat cannot hide behind scheduling.
			f.send([]byte(tc.suffix))
			close(f.data)
			f.waitDone()
			if f.result != nil {
				t.Fatal(f.result)
			}
			body := f.writer.Body.Bytes()
			want := strings.Join(tc.chunks, "") + tc.suffix
			if got := bytes.ReplaceAll(body, []byte(": keep-alive\n\n"), nil); string(got) != want {
				t.Fatal("raw SSE bytes were changed")
			}
			wantHeartbeats := 0
			if tc.boundary {
				wantHeartbeats = 1
			}
			if got := bytes.Count(body, []byte(": keep-alive\n\n")); got != wantHeartbeats {
				t.Fatalf("heartbeats=%d, want %d", got, wantHeartbeats)
			}
			for _, payload := range rawImageStreamDataFrames(body) {
				if !ojson.ValidBytes(payload) {
					t.Fatal("an SSE event was corrupted at a line-ending boundary")
				}
			}
		})
	}
}

func TestRawImageStreamKeepsUnterminatedEOFUnchanged(t *testing.T) {
	for _, data := range []string{`data: {"b64_json":"QU`, "data: {}\n", "data: {}\r"} {
		f := newRawImageStreamFixture(t, []byte(data))
		rawImageStreamTick(t, f.ticks, f.done)
		close(f.data)
		f.waitDone()
		if f.result != nil || f.writer.Body.String() != data {
			t.Fatalf("EOF changed the upstream stream: err=%v", f.result)
		}
	}
}

func rawImageStreamDataFrames(body []byte) [][]byte {
	normalized := strings.ReplaceAll(string(body), "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	var payloads [][]byte
	for _, frame := range strings.Split(normalized, "\n\n") {
		var lines []string
		for _, line := range strings.Split(frame, "\n") {
			if strings.HasPrefix(line, "data:") {
				lines = append(lines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			}
		}
		if len(lines) > 0 {
			payloads = append(payloads, []byte(strings.Join(lines, "\n")))
		}
	}
	return payloads
}

func TestRawImageStreamSeparatesTerminalErrors(t *testing.T) {
	for _, prefix := range []string{`data: {"b64_json":"QU`, "data: {}\n", "data: {}\r", "data: {}\r\n", "data: {}\n\n", "data: {}\r\n\r\n", "data: {}\r\r"} {
		for _, closeData := range []bool{false, true} {
			f := newRawImageStreamFixture(t, []byte(prefix))
			f.errs <- &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: errors.New("upstream image stream disconnected")}
			if closeData {
				close(f.data)
			}
			f.waitDone()
			payloads := rawImageStreamDataFrames(f.writer.Body.Bytes())
			if f.result == nil || len(payloads) != 2 || !ojson.ValidBytes(payloads[1]) || ojson.GetBytesNoCopy(payloads[1], "error.message").String() != "upstream image stream disconnected" {
				t.Fatalf("terminal error was not a separate SSE event: closeData=%t err=%v body=%q", closeData, f.result, f.writer.Body.String())
			}
		}
	}
}

func TestRawImageStreamCancellationStopsKeepAlive(t *testing.T) {
	for _, cancelRequest := range []bool{false, true} {
		prefix := []byte(`data: {"b64_json":"QU`)
		f := newRawImageStreamFixture(t, prefix)
		rawImageStreamTick(t, f.ticks, f.done)
		if cancelRequest {
			f.cancelRequest()
		} else {
			f.cancelStream()
		}
		f.waitDone()
		if !errors.Is(f.result, context.Canceled) || !bytes.Equal(f.writer.Body.Bytes(), prefix) {
			t.Fatalf("cancellation changed the stream: err=%v", f.result)
		}
		select {
		case <-f.stopped:
		default:
			t.Fatal("keepalive ticker was not stopped")
		}
	}
}

type rawImageStreamExecutor struct {
	xaiImagesCaptureExecutor
	chunks   <-chan coreexecutor.StreamChunk
	provider string
}

func (e *rawImageStreamExecutor) Identifier() string { return e.provider }

func (e *rawImageStreamExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return &coreexecutor.StreamResult{Headers: http.Header{"X-Request-Id": []string{"image-stream-test"}}, Chunks: e.chunks}, nil
}

func TestImagePassthroughKeepAlivePreservesLargeEvent(t *testing.T) {
	for _, provider := range []struct {
		name, model, modelType string
	}{{"codex", defaultImagesToolModel, "codex"}, {"openai-compatibility", "compat-image-stream-test", registry.OpenAIImageModelType}} {
		for _, endpoint := range []string{imagesGenerationsPath, imagesEditsPath} {
			for _, streaming := range []struct {
				name        string
				seconds     int
				beforeFirst bool
				fail        bool
			}{{"disabled", 0, false, false}, {"without_pre_ft", 20, false, false}, {"with_pre_ft", 20, true, false}, {"terminal_error", 20, false, true}} {
				beforeFirstToken := streaming.beforeFirst
				name := provider.name + "/" + strings.TrimPrefix(endpoint, "/v1/images/") + "/" + streaming.name
				t.Run(name, func(t *testing.T) {
					gin.SetMode(gin.TestMode)
					ticks := make(chan time.Time)
					restore := handlers.SetStreamingKeepAliveTickerFactoryForTest(func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} })
					t.Cleanup(restore)
					chunks := make(chan coreexecutor.StreamChunk)
					manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
					manager.RegisterExecutor(&rawImageStreamExecutor{chunks: chunks, provider: provider.name})
					auth := &coreauth.Auth{ID: t.Name(), Provider: provider.name, Status: coreauth.StatusActive,
						Attributes: map[string]string{"plan_type": "plus"}, Metadata: map[string]any{"type": provider.name, "access_token": "synthetic"},
					}
					if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
						t.Fatal(errRegister)
					}
					models := registry.GetGlobalRegistry()
					models.RegisterClient(auth.ID, provider.name, []*registry.ModelInfo{{ID: provider.model, Object: "model", OwnedBy: provider.name, Type: provider.modelType}})
					t.Cleanup(func() { models.UnregisterClient(auth.ID) })
					manager.RefreshSchedulerEntry(auth.ID)
					cfg := &sdkconfig.SDKConfig{PassthroughHeaders: true}
					cfg.Streaming.KeepAliveSeconds = streaming.seconds
					cfg.Streaming.SendKeepAliveBeforeFT = beforeFirstToken
					h := NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(cfg, manager))
					router := gin.New()
					if endpoint == imagesGenerationsPath {
						router.POST(endpoint, h.ImagesGenerations)
					} else {
						router.POST(endpoint, h.ImagesEdits)
					}
					requestDoc := ojson.NewDoc([]byte(`{"model":"gpt-image-2","prompt":"test","stream":true}`))
					_ = requestDoc.Set("model", provider.model)
					if endpoint == imagesEditsPath {
						_ = requestDoc.Set("image", "data:image/png;base64,aW1hZ2U=")
					}
					ctx, cancel := context.WithCancel(context.Background())
					req := httptest.NewRequest(http.MethodPost, endpoint, bytes.NewReader(requestDoc.Bytes())).WithContext(ctx)
					req.Header.Set("Content-Type", "application/json")
					writer := &rawImageStreamRecorder{ResponseRecorder: httptest.NewRecorder(), writes: make(chan []byte, 128)}
					done := make(chan struct{})
					go func() { defer close(done); router.ServeHTTP(writer, req) }()
					t.Cleanup(func() {
						cancel()
						select {
						case <-done:
						case <-time.After(5 * time.Second):
							t.Error("image handler did not stop")
						}
					})
					if beforeFirstToken {
						rawImageStreamTick(t, ticks, done)
						rawImageStreamWaitWrite(t, writer, []byte(": keep-alive\n\n"))
					}
					eventDoc := ojson.NewDoc([]byte(`{"type":"image_generation.completed","size":"1536x1024"}`))
					eventType := "image_generation.completed"
					if endpoint == imagesEditsPath {
						eventType = "image_edit.completed"
						_ = eventDoc.Set("type", eventType)
					}
					_ = eventDoc.Set("b64_json", strings.Repeat("QUJD", 425000))
					frame := append([]byte("event: "+eventType+"\ndata: "), eventDoc.Bytes()...)
					frame = append(frame, '\n', '\n')
					for offset := 0; offset < len(frame); {
						end := min(offset+32*1024, len(frame))
						chunk := frame[offset:end]
						select {
						case chunks <- coreexecutor.StreamChunk{Payload: chunk}:
						case <-done:
							t.Fatalf("image handler stopped early: status=%d", writer.Code)
						case <-time.After(5 * time.Second):
							t.Fatal("image handler did not receive a chunk")
						}
						rawImageStreamWaitWrite(t, writer, chunk)
						offset = end
						// A tick between each pair of 32 KiB chunks must never split
						// the completed event's single large JSON data line.
						if offset < len(frame) && streaming.seconds > 0 {
							rawImageStreamTick(t, ticks, done)
						}
						if streaming.fail {
							select {
							case chunks <- coreexecutor.StreamChunk{Err: errors.New("upstream image stream disconnected")}:
							case <-done:
								t.Fatal("image handler stopped before the upstream error")
							case <-time.After(5 * time.Second):
								t.Fatal("image handler did not receive the upstream error")
							}
							break
						}
					}
					if streaming.seconds > 0 && !streaming.fail {
						rawImageStreamTick(t, ticks, done)
						rawImageStreamWaitWrite(t, writer, []byte(": keep-alive\n\n"))
					}
					close(chunks)
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						t.Fatal("image handler did not finish")
					}
					body := writer.Body.Bytes()
					if writer.Code != http.StatusOK {
						t.Fatalf("status=%d, want 200", writer.Code)
					}
					if !beforeFirstToken && writer.Result().Header.Get("X-Request-Id") != "image-stream-test" {
						t.Fatal("upstream response headers were lost")
					}
					payloads := rawImageStreamDataFrames(body)
					if streaming.fail {
						if !bytes.HasPrefix(body, frame[:32*1024]) || len(payloads) != 2 || !ojson.ValidBytes(payloads[1]) || !strings.Contains(ojson.GetBytesNoCopy(payloads[1], "error.message").String(), "upstream image stream disconnected") {
							t.Fatal("upstream error did not arrive as a separate SSE event")
						}
						return
					}
					if !bytes.Equal(bytes.ReplaceAll(body, []byte(": keep-alive\n\n"), nil), frame) {
						t.Fatal("raw image bytes changed")
					}
					if len(payloads) != 1 || !ojson.ValidBytes(payloads[0]) || ojson.GetBytesNoCopy(payloads[0], "type").String() != eventType || len(ojson.GetBytesNoCopy(payloads[0], "b64_json").String()) != 1700000 {
						t.Fatal("completed image event was corrupted")
					}
				})
			}
		}
	}
}
