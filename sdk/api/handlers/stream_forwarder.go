package handlers

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"golang.org/x/net/context"
)

const continuityWaitSignalContextKey = "_cpa_continuity_wait_signal"

type continuityWaitSignal struct {
	mu          sync.Mutex
	activeCount int
	notify      chan struct{}
}

func continuityWaitSignalFromGin(c *gin.Context) *continuityWaitSignal {
	if c == nil {
		return nil
	}
	raw, ok := c.Get(continuityWaitSignalContextKey)
	if !ok || raw == nil {
		return nil
	}
	sig, _ := raw.(*continuityWaitSignal)
	return sig
}

func newContinuityWaitSignal() *continuityWaitSignal {
	return &continuityWaitSignal{notify: make(chan struct{}, 1)}
}

func (s *continuityWaitSignal) OnContinuityWaitStart() {
	s.updateCount(1)
}

func (s *continuityWaitSignal) OnContinuityWaitEnd() {
	s.updateCount(-1)
}

func (s *continuityWaitSignal) updateCount(delta int) {
	s.mu.Lock()
	wasActive := s.activeCount > 0
	s.activeCount += delta
	if s.activeCount < 0 {
		s.activeCount = 0
	}
	nowActive := s.activeCount > 0
	s.mu.Unlock()
	if wasActive != nowActive {
		select {
		case s.notify <- struct{}{}:
		default:
		}
	}
}

func (s *continuityWaitSignal) isActive() bool {
	s.mu.Lock()
	active := s.activeCount > 0
	s.mu.Unlock()
	return active
}

// StartContinuityWaitKeepAliveForPeek forwards continuity wait keep-alive ticks
// while waiting for the first complete SSE data frame when regular pre-FT keep-alives are disabled.
func (h *BaseAPIHandler) StartContinuityWaitKeepAliveForPeek(c *gin.Context, existingKeepAliveC <-chan time.Time) (<-chan time.Time, func() bool, func()) {
	if existingKeepAliveC != nil {
		return nil, func() bool { return false }, func() {}
	}
	sig := continuityWaitSignalFromGin(c)
	if sig == nil {
		return nil, func() bool { return false }, func() {}
	}
	tickOut := make(chan time.Time, 1)
	done := make(chan struct{})
	exited := make(chan struct{})
	var once sync.Once
	stop := func() {
		once.Do(func() {
			close(done)
			<-exited
		})
	}
	canWrite := func() bool {
		return sig.isActive()
	}
	go func() {
		defer close(exited)
		var waitC <-chan time.Time
		stopTicker := func() {}
		defer func() {
			stopTicker()
		}()
		syncTicker := func() {
			if sig.isActive() {
				if waitC == nil {
					waitC, stopTicker = NewContinuityKeepAliveTicker(h.CurrentConfig())
				}
			} else {
				stopTicker()
				waitC = nil
				stopTicker = func() {}
				select {
				case <-tickOut:
				default:
				}
			}
		}
		syncTicker()
		for {
			select {
			case <-done:
				return
			case <-sig.notify:
				syncTicker()
			case tick, ok := <-waitC:
				if !ok {
					stopTicker()
					waitC = nil
					stopTicker = func() {}
					continue
				}
				if !sig.isActive() {
					continue
				}
				select {
				case tickOut <- tick:
				default:
				}
			}
		}
	}()
	return tickOut, canWrite, stop
}

// ExecuteStreamWithPreFTKeepAlive executes a streaming request via ExecuteStreamWithAuthManager.
// When keepAliveC is non-nil (streaming.send-keepalive-before-ft is enabled and keepalive-seconds > 0),
// it runs the synchronous bootstrap in a goroutine and invokes writeKeepAlive on each tick while waiting.
// When keepAliveC is nil for an SSE request (alt == "") and an active continuity scheduler is attached,
// it emits wait-only keep-alives strictly while a continuity wait is active.
// It returns (dataChan, upstreamHeaders, errChan, ok), where ok is false if the request context was canceled
// or writeKeepAlive returned false.
func (h *BaseAPIHandler) ExecuteStreamWithPreFTKeepAlive(
	c *gin.Context,
	cliCtx context.Context,
	cliCancel func(error),
	handlerType, modelName string,
	rawJSON []byte,
	alt string,
	keepAliveC <-chan time.Time,
	writeKeepAlive func() bool,
) (<-chan []byte, http.Header, <-chan *interfaces.ErrorMessage, bool) {
	waitOnlyKeepAlive := keepAliveC == nil && alt == "" && writeKeepAlive != nil && h != nil && h.HasSchedulerContinuity()
	if keepAliveC == nil && !waitOnlyKeepAlive {
		dataChan, upstreamHeaders, errChan := h.ExecuteStreamWithAuthManager(cliCtx, handlerType, modelName, rawJSON, alt)
		return dataChan, upstreamHeaders, errChan, true
	}

	var waitSignal *continuityWaitSignal
	var waitNotify <-chan struct{}
	var waitKeepAliveC <-chan time.Time
	stopWaitKeepAlive := func() {}
	defer func() {
		stopWaitKeepAlive()
	}()
	if waitOnlyKeepAlive {
		waitSignal = newContinuityWaitSignal()
		waitNotify = waitSignal.notify
		c.Set(continuityWaitSignalContextKey, waitSignal)
		cliCtx = coreexecutor.WithContinuityWaitObserver(cliCtx, waitSignal)
	}

	type streamBootstrapResult struct {
		data    <-chan []byte
		headers http.Header
		errs    <-chan *interfaces.ErrorMessage
	}
	bootstrapResult := make(chan streamBootstrapResult, 1)
	go func() {
		data, headers, errs := h.ExecuteStreamWithAuthManager(cliCtx, handlerType, modelName, rawJSON, alt)
		bootstrapResult <- streamBootstrapResult{data: data, headers: headers, errs: errs}
	}()

	for {
		select {
		case <-c.Request.Context().Done():
			if cliCancel != nil {
				cliCancel(c.Request.Context().Err())
			}
			<-bootstrapResult
			return nil, nil, nil, false
		case result := <-bootstrapResult:
			return result.data, result.headers, result.errs, true
		case <-waitNotify:
			if waitSignal != nil && waitSignal.isActive() {
				if waitKeepAliveC == nil {
					waitKeepAliveC, stopWaitKeepAlive = NewContinuityKeepAliveTicker(h.CurrentConfig())
				}
			} else {
				stopWaitKeepAlive()
				waitKeepAliveC = nil
				stopWaitKeepAlive = func() {}
			}
		case <-waitKeepAliveC:
			if waitSignal != nil && waitSignal.isActive() && writeKeepAlive != nil && !writeKeepAlive() {
				if cliCancel != nil {
					cliCancel(context.Canceled)
				}
				<-bootstrapResult
				return nil, nil, nil, false
			}
		case <-keepAliveC:
			if writeKeepAlive != nil && !writeKeepAlive() {
				if cliCancel != nil {
					cliCancel(context.Canceled)
				}
				<-bootstrapResult
				return nil, nil, nil, false
			}
		}
	}
}

// PendingStreamError returns an immediately available non-nil stream error.
func PendingStreamError(errs <-chan *interfaces.ErrorMessage) (*interfaces.ErrorMessage, bool) {
	if errs == nil {
		return nil, false
	}
	select {
	case errMsg, ok := <-errs:
		if ok && errMsg != nil {
			return errMsg, true
		}
	default:
	}
	return nil, false
}

type StreamForwardOptions struct {
	// KeepAliveInterval overrides the configured streaming keep-alive interval.
	// If nil, the configured default is used. If set to <= 0, keep-alives are disabled.
	KeepAliveInterval *time.Duration

	// WriteChunk writes a single data chunk to the response body. It should not flush.
	WriteChunk func(chunk []byte)

	// ChunkError optionally reports that WriteChunk emitted a terminal failure.
	// The failure is passed to cancel without writing another terminal payload.
	ChunkError func() *interfaces.ErrorMessage

	// ChunkDone reports successful terminal delivery after WriteChunk and Flush.
	ChunkDone func() bool

	// Flush optionally exposes transport flush errors hidden by http.Flusher.
	Flush func() error

	// NormalizeTerminalError optionally replaces an upstream error before it is
	// written or passed to cancel.
	NormalizeTerminalError func(errMsg *interfaces.ErrorMessage) *interfaces.ErrorMessage

	// WriteTerminalError writes an error payload to the response body when streaming fails
	// after headers have already been committed. It should not flush.
	WriteTerminalError func(errMsg *interfaces.ErrorMessage)

	// CloseError optionally validates a clean upstream channel close before WriteDone.
	// Returning an error surfaces it through WriteTerminalError instead of completing the stream.
	CloseError func() *interfaces.ErrorMessage

	// WriteDone optionally writes a terminal marker when the upstream data channel closes
	// without an error (e.g. OpenAI's `[DONE]`). It should not flush.
	WriteDone func()

	// WriteKeepAlive optionally writes a keep-alive heartbeat. It should not flush.
	// When nil, a standard SSE comment heartbeat is used.
	WriteKeepAlive func()
}

func (h *BaseAPIHandler) ForwardStream(c *gin.Context, flusher http.Flusher, cancel func(error), data <-chan []byte, errs <-chan *interfaces.ErrorMessage, opts StreamForwardOptions) {
	if c == nil {
		return
	}
	if cancel == nil {
		return
	}

	flush := func() bool {
		if opts.Flush != nil {
			if errFlush := opts.Flush(); errFlush != nil {
				cancel(errFlush)
				return false
			}
		} else {
			flusher.Flush()
		}
		return true
	}

	writeChunk := opts.WriteChunk
	if writeChunk == nil {
		writeChunk = func([]byte) {}
	}

	writeKeepAlive := opts.WriteKeepAlive
	if writeKeepAlive == nil {
		writeKeepAlive = func() {
			_, _ = c.Writer.Write([]byte(": keep-alive\n\n"))
		}
	}

	keepAliveInterval := StreamingKeepAliveInterval(h.CurrentConfig())
	if opts.KeepAliveInterval != nil {
		keepAliveInterval = *opts.KeepAliveInterval
	}
	keepAliveC, stopKeepAlive, resetKeepAlive := NewStreamingKeepAliveTickerWithReset(keepAliveInterval)
	defer stopKeepAlive()

	var waitSignal *continuityWaitSignal
	var waitNotify <-chan struct{}
	var waitKeepAliveC <-chan time.Time
	stopWaitKeepAlive := func() {}
	defer func() {
		stopWaitKeepAlive()
	}()
	if keepAliveC == nil && opts.KeepAliveInterval == nil {
		if sig := continuityWaitSignalFromGin(c); sig != nil {
			waitSignal = sig
			waitNotify = sig.notify
			if sig.isActive() {
				waitKeepAliveC, stopWaitKeepAlive = NewContinuityKeepAliveTicker(h.CurrentConfig())
			}
		}
	}

	var terminalErr *interfaces.ErrorMessage
	handleChunk := func(chunk []byte, ok bool) bool {
		if !ok {
			// Prefer surfacing a terminal error if one is pending.
			if terminalErr == nil {
				if errMsg, ok := PendingStreamError(errs); ok {
					terminalErr = errMsg
					if opts.NormalizeTerminalError != nil {
						terminalErr = opts.NormalizeTerminalError(terminalErr)
					}
				}
			}
			if terminalErr == nil && opts.CloseError != nil {
				terminalErr = opts.CloseError()
			}
			if terminalErr != nil {
				if opts.WriteTerminalError != nil {
					opts.WriteTerminalError(terminalErr)
				}
				if !flush() {
					return true
				}
				cancel(terminalErr.Error)
				return true
			}
			if opts.WriteDone != nil {
				opts.WriteDone()
			}
			if !flush() {
				return true
			}
			cancel(nil)
			return true
		}
		beforeSize := c.Writer.Size()
		writeChunk(chunk)
		if c.Writer.Size() > beforeSize {
			resetKeepAlive()
		}
		if !flush() {
			return true
		}
		if opts.ChunkError != nil {
			chunkErr := opts.ChunkError()
			if chunkErr != nil {
				if opts.NormalizeTerminalError != nil {
					chunkErr = opts.NormalizeTerminalError(chunkErr)
				}
				if chunkErr != nil {
					cancel(chunkErr.Error)
				} else {
					cancel(nil)
				}
				return true
			}
		}
		if opts.ChunkDone != nil && opts.ChunkDone() {
			cancel(nil)
			return true
		}
		return false
	}

	for {
		select {
		case <-c.Request.Context().Done():
			cancel(c.Request.Context().Err())
			return
		case chunk, ok := <-data:
			if handleChunk(chunk, ok) {
				return
			}
		case errMsg, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if errMsg != nil {
				terminalErr = errMsg
				if opts.NormalizeTerminalError != nil {
					terminalErr = opts.NormalizeTerminalError(terminalErr)
				}
				if opts.WriteTerminalError != nil {
					opts.WriteTerminalError(terminalErr)
					if !flush() {
						return
					}
				}
			}
			var execErr error
			if terminalErr != nil {
				execErr = terminalErr.Error
			}
			cancel(execErr)
			return
		case <-waitNotify:
			if waitSignal != nil && waitSignal.isActive() {
				if waitKeepAliveC == nil {
					waitKeepAliveC, stopWaitKeepAlive = NewContinuityKeepAliveTicker(h.CurrentConfig())
				}
			} else {
				stopWaitKeepAlive()
				waitKeepAliveC = nil
				stopWaitKeepAlive = func() {}
			}
		case <-waitKeepAliveC:
			if waitSignal != nil && waitSignal.isActive() {
				writeKeepAlive()
				if !flush() {
					return
				}
			}
		case <-keepAliveC:
			writeKeepAlive()
			if !flush() {
				return
			}
		}
	}
}
