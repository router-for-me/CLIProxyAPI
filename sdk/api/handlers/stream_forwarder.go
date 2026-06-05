package handlers

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
)

type StreamForwardOptions struct {
	// KeepAliveInterval overrides the configured streaming keep-alive interval.
	// If nil, the configured default is used. If set to <= 0, keep-alives are disabled.
	KeepAliveInterval *time.Duration

	// WriteChunk writes a single data chunk to the response body. It should not flush.
	WriteChunk func(chunk []byte)

	// WriteTerminalError writes an error payload to the response body when streaming fails
	// after headers have already been committed. It should not flush.
	WriteTerminalError func(errMsg *interfaces.ErrorMessage)

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

	keepAliveInterval := StreamingKeepAliveInterval(h.Cfg)
	if opts.KeepAliveInterval != nil {
		keepAliveInterval = *opts.KeepAliveInterval
	}
	var keepAlive *time.Ticker
	var keepAliveC <-chan time.Time
	if keepAliveInterval > 0 {
		keepAlive = time.NewTicker(keepAliveInterval)
		defer keepAlive.Stop()
		keepAliveC = keepAlive.C
	}

	flushInterval := StreamingFlushInterval(h.Cfg)
	var flushTimer *time.Timer
	var flushC <-chan time.Time
	pendingFlush := false
	stopFlushTimer := func() {
		if flushTimer == nil {
			return
		}
		if !flushTimer.Stop() {
			select {
			case <-flushTimer.C:
			default:
			}
		}
		flushTimer = nil
		flushC = nil
	}
	defer stopFlushTimer()
	flushImmediate := func() {
		stopFlushTimer()
		pendingFlush = false
		flusher.Flush()
	}
	flushChunk := func() {
		if flushInterval <= 0 {
			flusher.Flush()
			return
		}
		pendingFlush = true
		if flushTimer != nil {
			return
		}
		flushTimer = time.NewTimer(flushInterval)
		flushC = flushTimer.C
	}

	var terminalErr *interfaces.ErrorMessage
	for {
		select {
		case <-c.Request.Context().Done():
			cancel(c.Request.Context().Err())
			return
		case chunk, ok := <-data:
			if !ok {
				// Prefer surfacing a terminal error if one is pending.
				if terminalErr == nil {
					select {
					case errMsg, ok := <-errs:
						if ok && errMsg != nil {
							terminalErr = errMsg
						}
					default:
					}
				}
				if terminalErr != nil {
					if opts.WriteTerminalError != nil {
						opts.WriteTerminalError(terminalErr)
					}
					flushImmediate()
					cancel(terminalErr.Error)
					return
				}
				if opts.WriteDone != nil {
					opts.WriteDone()
				}
				flushImmediate()
				cancel(nil)
				return
			}
			writeChunk(chunk)
			flushChunk()
		case errMsg, ok := <-errs:
			if !ok {
				continue
			}
			if errMsg != nil {
				terminalErr = errMsg
				if opts.WriteTerminalError != nil {
					opts.WriteTerminalError(errMsg)
					flushImmediate()
				}
			}
			var execErr error
			if errMsg != nil {
				execErr = errMsg.Error
			}
			cancel(execErr)
			return
		case <-keepAliveC:
			writeKeepAlive()
			flushImmediate()
		case <-flushC:
			if pendingFlush {
				flusher.Flush()
			}
			pendingFlush = false
			flushTimer = nil
			flushC = nil
		}
	}
}
