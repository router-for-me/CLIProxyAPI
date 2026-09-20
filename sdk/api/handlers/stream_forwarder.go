package handlers

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
)

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
					flusher.Flush()
					cancel(terminalErr.Error)
					return
				}
				if opts.WriteDone != nil {
					opts.WriteDone()
				}
				flusher.Flush()
				cancel(nil)
				return
			}
			writeChunk(chunk)
			flusher.Flush()
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
					return
				}
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
					flusher.Flush()
				}
			}
			var execErr error
			if terminalErr != nil {
				execErr = terminalErr.Error
			}
			cancel(execErr)
			return
		case <-keepAliveC:
			writeKeepAlive()
			flusher.Flush()
		}
	}
}
