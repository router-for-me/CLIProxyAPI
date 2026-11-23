package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
)

func TestPendingStreamErrorReturnsBufferedError(t *testing.T) {
	errs := make(chan *interfaces.ErrorMessage, 1)
	want := &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: errors.New("upstream failed")}
	errs <- want
	close(errs)

	got, ok := PendingStreamError(errs)
	if !ok || got != want {
		t.Fatalf("PendingStreamError() = (%#v, %t), want (%#v, true)", got, ok, want)
	}
}

func TestValidateSSEDataJSONAllowsMultilinePayload(t *testing.T) {
	chunk := []byte("event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\n" +
		"data: \"response\":{\"status\":\"completed\"}}\n\n")
	if err := validateSSEDataJSON(chunk); err != nil {
		t.Fatalf("validateSSEDataJSON() error = %v, want nil", err)
	}
}

func TestForwardStreamNormalizesErrorBeforeWriteAndCancel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

	data := make(chan []byte)
	close(data)
	errs := make(chan *interfaces.ErrorMessage, 1)
	errs <- &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: errors.New("raw secret")}
	close(errs)

	var written, canceled string
	disabledKeepAlive := time.Duration(0)
	h := &BaseAPIHandler{}
	h.ForwardStream(c, recorder, func(err error) {
		if err != nil {
			canceled = err.Error()
		}
	}, data, errs, StreamForwardOptions{
		KeepAliveInterval: &disabledKeepAlive,
		NormalizeTerminalError: func(errMsg *interfaces.ErrorMessage) *interfaces.ErrorMessage {
			return &interfaces.ErrorMessage{StatusCode: errMsg.StatusCode, Error: errors.New("safe error")}
		},
		WriteTerminalError: func(errMsg *interfaces.ErrorMessage) {
			written = errMsg.Error.Error()
		},
	})

	if written != "safe error" || canceled != "safe error" {
		t.Fatalf("written=%q canceled=%q, want sanitized error", written, canceled)
	}
}

func TestPendingStreamErrorIgnoresUnavailableErrors(t *testing.T) {
	closed := make(chan *interfaces.ErrorMessage)
	close(closed)

	for name, errs := range map[string]<-chan *interfaces.ErrorMessage{
		"nil":          nil,
		"closed empty": closed,
		"open empty":   make(chan *interfaces.ErrorMessage),
	} {
		t.Run(name, func(t *testing.T) {
			if got, ok := PendingStreamError(errs); ok || got != nil {
				t.Fatalf("PendingStreamError() = (%#v, %t), want (nil, false)", got, ok)
			}
		})
	}
}

func TestSplitCRLFRepro(t *testing.T) {
	state := &sseJSONValidationState{}
	chunks := []string{
		"data: {\"type\":\"response.completed\",\r",
		"\ndata: \"response\":{\"status\":\"completed\"}}\r\n\r\n",
	}
	var output []byte
	for _, chunk := range chunks {
		out, errAdd := state.AddChunk([]byte(chunk))
		if errAdd != nil {
			t.Fatal(errAdd)
		}
		output = append(output, out...)
	}
	if errFinish := state.Finish(); errFinish != nil {
		t.Fatal(errFinish)
	}

	singleState := &sseJSONValidationState{}
	unsplitChunk := "data: {\"type\":\"response.completed\",\r\ndata: \"response\":{\"status\":\"completed\"}}\r\n\r\n"
	singleOutput, errSingle := singleState.AddChunk([]byte(unsplitChunk))
	if errSingle != nil {
		t.Fatal(errSingle)
	}
	if errFinish := singleState.Finish(); errFinish != nil {
		t.Fatal(errFinish)
	}
	if string(output) != string(singleOutput) {
		t.Fatalf("output mismatch: got %q, want %q", string(output), string(singleOutput))
	}
}

func TestSSEJSONValidationStateSplitCRLFVariants(t *testing.T) {
	wantOutput := "data: {\"type\":\"response.completed\",\ndata: \"response\":{\"status\":\"completed\"}}\n\n"
	tests := []struct {
		name   string
		chunks []string
	}{
		{
			name: "intervening empty chunks",
			chunks: []string{
				"data: {\"type\":\"response.completed\",\r",
				"",
				"",
				"\ndata: \"response\":{\"status\":\"completed\"}}\r\n\r\n",
			},
		},
		{
			name: "standalone LF chunk",
			chunks: []string{
				"data: {\"type\":\"response.completed\",\r",
				"\n",
				"data: \"response\":{\"status\":\"completed\"}}\r\n\r\n",
			},
		},
		{
			name: "bare CR chunk followed by regular content",
			chunks: []string{
				"data: {\"type\":\"response.completed\",\r",
				"data: \"response\":{\"status\":\"completed\"}}\r\n\r\n",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			state := &sseJSONValidationState{}
			var output []byte
			for _, chunk := range tc.chunks {
				out, errAdd := state.AddChunk([]byte(chunk))
				if errAdd != nil {
					t.Fatalf("AddChunk failed: %v", errAdd)
				}
				output = append(output, out...)
			}
			if errFinish := state.Finish(); errFinish != nil {
				t.Fatalf("Finish failed: %v", errFinish)
			}
			if string(output) != wantOutput {
				t.Fatalf("output mismatch: got %q, want %q", string(output), wantOutput)
			}
		})
	}
}

func TestForwardStreamResetsKeepAliveOnEachChunk(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

	tickChan := make(chan time.Time)
	data := make(chan []byte, 3)
	errs := make(chan *interfaces.ErrorMessage)
	close(errs)

	resetCount := 0
	restore := SetStreamingKeepAliveTickerWithResetFactoryForTest(func(time.Duration) (<-chan time.Time, func(), func()) {
		return tickChan, func() {}, func() {
			resetCount++
		}
	})
	defer restore()

	data <- []byte("buffered-without-output")
	data <- []byte("data: {\"part\":1}\n\n")
	data <- []byte("data: {\"part\":2}\n\n")
	close(data)

	interval := 20 * time.Second
	h := &BaseAPIHandler{}
	var canceled error
	h.ForwardStream(c, recorder, func(err error) {
		canceled = err
	}, data, errs, StreamForwardOptions{
		KeepAliveInterval: &interval,
		WriteChunk: func(chunk []byte) {
			if string(chunk) == "buffered-without-output" {
				return
			}
			_, _ = c.Writer.Write(chunk)
		},
	})

	if canceled != nil {
		t.Fatalf("unexpected cancel error: %v", canceled)
	}
	if resetCount != 2 {
		t.Fatalf("resetCount = %d, want 2", resetCount)
	}
	if got := recorder.Body.String(); got != "data: {\"part\":1}\n\ndata: {\"part\":2}\n\n" {
		t.Fatalf("body = %q, want two data chunks", got)
	}
}

func TestContinuityWaitSignalPreservesActiveStateAcrossInterleavedWaits(t *testing.T) {
	sig := newContinuityWaitSignal()
	if sig.isActive() {
		t.Fatal("expected initial signal inactive")
	}

	// Wait A starts -> inactive to active notification.
	sig.OnContinuityWaitStart()
	if !sig.isActive() {
		t.Fatal("expected signal active after first start")
	}
	select {
	case <-sig.notify:
	default:
		t.Fatal("expected notify on 0->1 transition")
	}

	// Wait B starts concurrently -> stays active, no spurious transition notify.
	sig.OnContinuityWaitStart()
	if !sig.isActive() {
		t.Fatal("expected signal active after second start")
	}
	select {
	case <-sig.notify:
		t.Fatal("unexpected notify on 1->2 transition")
	default:
	}

	// Wait A ends while Wait B is still active -> MUST remain active and NOT emit stop notify.
	sig.OnContinuityWaitEnd()
	if !sig.isActive() {
		t.Fatal("expected signal to remain active while Wait B is still waiting")
	}
	select {
	case <-sig.notify:
		t.Fatal("unexpected notify on 2->1 transition while Wait B is still active")
	default:
	}

	// Wait B ends -> active to inactive notification.
	sig.OnContinuityWaitEnd()
	if sig.isActive() {
		t.Fatal("expected signal inactive after final wait ends")
	}
	select {
	case <-sig.notify:
	default:
		t.Fatal("expected notify on 1->0 transition")
	}
}

func TestStartContinuityWaitKeepAliveForPeekSuppressesQueuedTickAfterWaitEnds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tickIn := make(chan time.Time)
	tickerCreated := make(chan struct{})
	var tickerOnce sync.Once
	restoreFactory := SetContinuityKeepAliveTickerFactoryForTest(func(time.Duration) (<-chan time.Time, func()) {
		tickerOnce.Do(func() { close(tickerCreated) })
		return tickIn, func() {}
	})
	defer restoreFactory()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	sig := newContinuityWaitSignal()
	c.Set(continuityWaitSignalContextKey, sig)
	sig.OnContinuityWaitStart()

	h := &BaseAPIHandler{}
	waitKeepAliveC, canWriteWaitKeepAlive, stopWaitKeepAlive := h.StartContinuityWaitKeepAliveForPeek(c, nil)
	defer stopWaitKeepAlive()

	// Unbuffered send guarantees the helper goroutine has received the tick;
	// yield until it has pushed into the buffered waitKeepAliveC channel.
	<-tickerCreated
	tickIn <- time.Now()
	for len(waitKeepAliveC) == 0 {
		runtime.Gosched()
	}

	// End wait while a tick is queued in waitKeepAliveC.
	sig.OnContinuityWaitEnd()

	wroteStale := false
	select {
	case <-waitKeepAliveC:
		if canWriteWaitKeepAlive() {
			wroteStale = true
		}
	default:
		// Also acceptable if syncTicker already drained tickOut before consumer read.
	}
	if wroteStale {
		t.Fatal("expected queued peek keepalive tick to be suppressed after continuity wait ended")
	}
}
