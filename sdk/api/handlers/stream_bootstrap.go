package handlers

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
)

// StreamBootstrapOptions configures BootstrapStream. Every field that writes bytes to the
// client is a callback because each protocol frames its headers, chunks and errors
// differently, and because handlers such as Claude override WriteErrorResponse (embedding
// gives no virtual dispatch, so the override has to be passed in explicitly).
type StreamBootstrapOptions struct {
	// KeepAliveInterval overrides the configured streaming keep-alive interval.
	// If nil, the configured default is used. If set to <= 0, heartbeats are disabled
	// and Execute runs synchronously, exactly as it did before keep-alives existed.
	KeepAliveInterval *time.Duration

	// Execute performs the blocking stream bootstrap (ExecuteStreamWithAuthManager).
	// It returns only once the upstream produced its first payload chunk or an error.
	Execute func() (<-chan []byte, http.Header, <-chan *interfaces.ErrorMessage)

	// SetSSEHeaders commits the protocol response headers. A heartbeat calls this before
	// the upstream headers are known, so it must not depend on them.
	SetSSEHeaders func()

	// WriteCommittedError writes errMsg into a stream whose headers are already committed
	// (the HTTP status is gone, so the error has to travel in-band). It returns the raw
	// error payload, which BootstrapStream appends to the request log so that errors
	// arriving after a heartbeat are still captured, matching WriteErrorResponse.
	WriteCommittedError func(errMsg *interfaces.ErrorMessage) []byte

	// WriteUncommittedError writes errMsg before anything reached the client, i.e. the
	// handler's normal WriteErrorResponse path with a real HTTP status.
	WriteUncommittedError func(errMsg *interfaces.ErrorMessage)

	// OnFirstChunk commits the headers unless headersCommitted is already true, then
	// writes the first payload chunk.
	OnFirstChunk func(headersCommitted bool, upstreamHeaders http.Header, chunk []byte)

	// OnStreamClosedWithoutData terminates a stream that closed before any payload,
	// again honouring headersCommitted.
	OnStreamClosedWithoutData func(headersCommitted bool, upstreamHeaders http.Header)

	// Forward continues streaming after the first chunk (the existing ForwardStream leg).
	Forward func(data <-chan []byte, errs <-chan *interfaces.ErrorMessage)

	// Cancel cancels the upstream request context and records the terminal error.
	Cancel func(error)

	// DrainPendingErrorOnClose makes a closed data channel check the error channel for a
	// pending terminal error before finishing. Only the Claude handler does this today;
	// enabling it elsewhere would change those handlers' terminal output.
	DrainPendingErrorOnClose bool
}

// BootstrapStream waits for a streaming upstream to produce its first chunk while keeping
// the client connection alive.
//
// A high-effort model reasoning over a large context can take minutes to produce its first
// token. The upstream stays silent that whole time and Execute blocks until the first
// payload chunk arrives, so with no bytes flowing the client treats the stream as idle and
// disconnects, killing the request with "context canceled". The same gap reopens after
// Execute returns on an error chunk: the SDK's background goroutine may perform a bootstrap
// retry against a fresh (and equally silent) upstream while this loop waits.
//
// So Execute runs in its own goroutine and delivers its three return values over a channel,
// while a single select loop here owns every write to the client: it heartbeats on each
// tick, binds the real data/error channels when the result lands, and keeps the same ticker
// running across the bootstrap-retry wait. One writer end to end means no mutex.
//
// The first heartbeat commits the response headers, which spends the HTTP status code; from
// that point errors are reported in-band via WriteCommittedError. Requests that resolve
// before the first tick (the common case, including immediate upstream errors) keep their
// normal status. When the interval is <= 0 no goroutine is started and no ticker runs, so
// the behaviour is byte-for-byte what it was before.
func (h *BaseAPIHandler) BootstrapStream(c *gin.Context, flusher http.Flusher, opts StreamBootstrapOptions) {
	if c == nil || opts.Execute == nil || opts.Cancel == nil {
		return
	}

	keepAliveInterval := StreamingKeepAliveInterval(h.Cfg)
	if opts.KeepAliveInterval != nil {
		keepAliveInterval = *opts.KeepAliveInterval
	}

	type bootstrapResult struct {
		data    <-chan []byte
		headers http.Header
		errs    <-chan *interfaces.ErrorMessage
	}

	var (
		dataChan        <-chan []byte
		errChan         <-chan *interfaces.ErrorMessage
		upstreamHeaders http.Header
		resultChan      chan bootstrapResult
		keepAliveC      <-chan time.Time
	)

	if keepAliveInterval > 0 {
		resultChan = make(chan bootstrapResult, 1)
		go func() {
			data, headers, errs := opts.Execute()
			resultChan <- bootstrapResult{data: data, headers: headers, errs: errs}
		}()

		ticker := time.NewTicker(keepAliveInterval)
		defer ticker.Stop()
		keepAliveC = ticker.C
	} else {
		dataChan, upstreamHeaders, errChan = opts.Execute()
	}

	headersCommitted := false

	writeError := func(errMsg *interfaces.ErrorMessage) {
		if headersCommitted {
			if opts.WriteCommittedError != nil {
				if body := opts.WriteCommittedError(errMsg); len(body) > 0 {
					appendAPIResponse(c, body)
				}
				flusher.Flush()
			}
		} else if opts.WriteUncommittedError != nil {
			opts.WriteUncommittedError(errMsg)
		}
		if errMsg != nil {
			opts.Cancel(errMsg.Error)
		} else {
			opts.Cancel(nil)
		}
	}

	for {
		select {
		case <-c.Request.Context().Done():
			opts.Cancel(c.Request.Context().Err())
			return
		case result := <-resultChan:
			dataChan, upstreamHeaders, errChan = result.data, result.headers, result.errs
			// Stop selecting on the result; the ticker keeps covering the bootstrap-retry wait.
			resultChan = nil
		case <-keepAliveC:
			if !headersCommitted {
				if opts.SetSSEHeaders != nil {
					opts.SetSSEHeaders()
				}
				headersCommitted = true
			}
			_, _ = c.Writer.Write([]byte(KeepAliveSSEComment))
			flusher.Flush()
		case errMsg, ok := <-errChan:
			if !ok {
				// Err channel closed cleanly; wait for the data channel.
				errChan = nil
				continue
			}
			writeError(errMsg)
			return
		case chunk, ok := <-dataChan:
			if !ok {
				if opts.DrainPendingErrorOnClose {
					if errMsg, pending := pendingStreamError(errChan); pending {
						writeError(errMsg)
						return
					}
				}
				if opts.OnStreamClosedWithoutData != nil {
					opts.OnStreamClosedWithoutData(headersCommitted, upstreamHeaders)
				}
				flusher.Flush()
				opts.Cancel(nil)
				return
			}
			if opts.OnFirstChunk != nil {
				opts.OnFirstChunk(headersCommitted, upstreamHeaders, chunk)
			}
			if opts.Forward != nil {
				opts.Forward(dataChan, errChan)
			}
			return
		}
	}
}

// pendingStreamError reports a terminal error that is already queued on errs, without blocking.
func pendingStreamError(errs <-chan *interfaces.ErrorMessage) (*interfaces.ErrorMessage, bool) {
	if errs == nil {
		return nil, false
	}
	select {
	case errMsg, ok := <-errs:
		if !ok {
			return nil, false
		}
		return errMsg, true
	default:
		return nil, false
	}
}
