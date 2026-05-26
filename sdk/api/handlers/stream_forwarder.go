package handlers

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	log "github.com/sirupsen/logrus"
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

	var terminalErr *interfaces.ErrorMessage
	startedAt := time.Now()
	lastActivityAt := startedAt
	chunksForwarded := 0
	bytesForwarded := 0
	logEntry := log.WithField("request_id", "--------")
	if c.Request != nil {
		if reqID := logging.GetRequestID(c.Request.Context()); reqID != "" {
			logEntry = log.WithField("request_id", reqID)
		}
	}
	logProgress := func(reason string, warn bool) {
		msg := "stream forwarder " + reason
		args := []interface{}{
			time.Since(startedAt).Truncate(time.Second),
			time.Since(lastActivityAt).Truncate(time.Second),
			chunksForwarded,
			bytesForwarded,
			c.Writer.Status(),
			c.Request.Context().Err(),
		}
		if warn {
			logEntry.Warnf(msg+" elapsed=%s idle=%s chunks=%d bytes=%d writer_status=%d request_ctx_err=%v", args...)
			return
		}
		logEntry.Infof(msg+" elapsed=%s idle=%s chunks=%d bytes=%d writer_status=%d request_ctx_err=%v", args...)
	}
	for {
		select {
		case <-c.Request.Context().Done():
			logProgress("request context done", true)
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
					logProgress("terminal error before flush", true)
					flusher.Flush()
					logProgress("terminal error flushed", true)
					cancel(terminalErr.Error)
					return
				}
				if opts.WriteDone != nil {
					opts.WriteDone()
				}
				logProgress("data channel closed before final flush", false)
				flusher.Flush()
				logProgress("data channel closed flushed", false)
				cancel(nil)
				return
			}
			lastActivityAt = time.Now()
			chunksForwarded++
			bytesForwarded += len(chunk)
			writeChunk(chunk)
			beforeFlush := time.Now()
			flusher.Flush()
			if flushDuration := time.Since(beforeFlush); flushDuration > time.Second {
				logEntry.Warnf("stream forwarder slow flush duration=%s elapsed=%s chunks=%d bytes=%d writer_status=%d request_ctx_err=%v",
					flushDuration.Truncate(time.Millisecond),
					time.Since(startedAt).Truncate(time.Second),
					chunksForwarded,
					bytesForwarded,
					c.Writer.Status(),
					c.Request.Context().Err(),
				)
			}
		case errMsg, ok := <-errs:
			if !ok {
				continue
			}
			if errMsg != nil {
				terminalErr = errMsg
				if opts.WriteTerminalError != nil {
					opts.WriteTerminalError(errMsg)
					logProgress("error channel received before flush", true)
					flusher.Flush()
					logProgress("error channel received flushed", true)
				}
			}
			var execErr error
			if errMsg != nil {
				execErr = errMsg.Error
			}
			cancel(execErr)
			return
		case <-keepAliveC:
			lastActivityAt = time.Now()
			writeKeepAlive()
			beforeFlush := time.Now()
			flusher.Flush()
			if flushDuration := time.Since(beforeFlush); flushDuration > time.Second {
				logEntry.Warnf("stream forwarder slow keepalive flush duration=%s elapsed=%s chunks=%d bytes=%d writer_status=%d request_ctx_err=%v",
					flushDuration.Truncate(time.Millisecond),
					time.Since(startedAt).Truncate(time.Second),
					chunksForwarded,
					bytesForwarded,
					c.Writer.Status(),
					c.Request.Context().Err(),
				)
			}
		}
	}
}
