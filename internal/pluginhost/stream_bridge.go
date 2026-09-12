package pluginhost

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type streamBridge struct {
	next    atomic.Uint64
	mu      sync.Mutex
	streams map[string]*streamBridgeStream
}

const streamBridgeBufferSize = 16

var errStreamBridgeClosed = errors.New("stream is not open")

type streamBridgeStream struct {
	chunks    chan pluginapi.ExecutorStreamChunk
	emits     chan streamBridgeEmit
	closes    chan streamBridgeClose
	closed    chan struct{}
	finished  chan struct{}
	abort     chan struct{}
	closeOnce sync.Once
	abortOnce sync.Once
}

type streamBridgeEmit struct {
	ctx   context.Context
	chunk pluginapi.ExecutorStreamChunk
	done  chan error
}

type streamBridgeClose struct {
	errorMessage string
	accepted     chan struct{}
}

type rpcStreamEmitRequest struct {
	StreamID string `json:"stream_id"`
	Payload  []byte `json:"payload,omitempty"`
	Error    string `json:"error,omitempty"`
}

type rpcStreamCloseRequest struct {
	StreamID string `json:"stream_id"`
	Error    string `json:"error,omitempty"`
}

func newStreamBridge() *streamBridge {
	return &streamBridge{streams: make(map[string]*streamBridgeStream)}
}

func newStreamBridgeStream() *streamBridgeStream {
	stream := &streamBridgeStream{
		chunks:   make(chan pluginapi.ExecutorStreamChunk),
		emits:    make(chan streamBridgeEmit),
		closes:   make(chan streamBridgeClose),
		closed:   make(chan struct{}),
		finished: make(chan struct{}),
		abort:    make(chan struct{}),
	}
	go stream.run()
	return stream
}

func (s *streamBridgeStream) run() {
	defer func() {
		s.markClosed()
		close(s.chunks)
		close(s.finished)
	}()

	queue := make([]pluginapi.ExecutorStreamChunk, 0, streamBridgeBufferSize)
	for {
		var emitC <-chan streamBridgeEmit
		if len(queue) < streamBridgeBufferSize {
			emitC = s.emits
		}
		var outputC chan pluginapi.ExecutorStreamChunk
		var next pluginapi.ExecutorStreamChunk
		if len(queue) > 0 {
			outputC = s.chunks
			next = queue[0]
		}

		select {
		case <-s.abort:
			return
		case request := <-s.closes:
			s.markClosed()
			close(request.accepted)
			if request.errorMessage != "" {
				queue = append(queue, pluginapi.ExecutorStreamChunk{Err: fmt.Errorf("%s", request.errorMessage)})
			}
			for len(queue) > 0 {
				select {
				case <-s.abort:
					return
				case s.chunks <- queue[0]:
					queue = queue[1:]
				}
			}
			return
		case request := <-emitC:
			if err := request.ctx.Err(); err != nil {
				request.done <- err
				continue
			}
			queue = append(queue, request.chunk)
			request.done <- nil
		case outputC <- next:
			queue = queue[1:]
		}
	}
}

func (s *streamBridgeStream) markClosed() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() { close(s.closed) })
}

func (s *streamBridgeStream) abortStream() {
	if s == nil {
		return
	}
	s.abortOnce.Do(func() {
		s.markClosed()
		close(s.abort)
	})
}

func (s *streamBridgeStream) emit(ctx context.Context, chunk pluginapi.ExecutorStreamChunk) error {
	if s == nil {
		return errStreamBridgeClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	request := streamBridgeEmit{
		ctx:   ctx,
		chunk: chunk,
		done:  make(chan error, 1),
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.closed:
		return errStreamBridgeClosed
	case s.emits <- request:
	}
	return <-request.done
}

func (s *streamBridgeStream) close(errorMessage string) {
	if s == nil {
		return
	}
	request := streamBridgeClose{
		errorMessage: errorMessage,
		accepted:     make(chan struct{}),
	}
	select {
	case <-s.finished:
		return
	case s.closes <- request:
	}
	select {
	case <-request.accepted:
	case <-s.finished:
	}
}

func (b *streamBridge) open(ctx context.Context) (string, <-chan pluginapi.ExecutorStreamChunk, func()) {
	if b == nil {
		chunks := make(chan pluginapi.ExecutorStreamChunk)
		close(chunks)
		return "", chunks, func() {}
	}
	id := strconv.FormatUint(b.next.Add(1), 10)
	stream := newStreamBridgeStream()
	b.mu.Lock()
	b.streams[id] = stream
	b.mu.Unlock()
	cleanup := func() {
		b.mu.Lock()
		if b.streams[id] == stream {
			delete(b.streams, id)
		}
		b.mu.Unlock()
		stream.abortStream()
	}
	if ctx != nil && ctx.Done() != nil {
		// Abort streams canceled before ExecuteStream can install cleanupWhenStreamDone.
		go func() {
			<-ctx.Done()
			cleanup()
		}()
	}
	return id, stream.chunks, cleanup
}

func (b *streamBridge) emit(ctx context.Context, id string, chunk pluginapi.ExecutorStreamChunk) error {
	if b == nil || id == "" {
		return fmt.Errorf("stream id is required")
	}
	b.mu.Lock()
	stream := b.streams[id]
	b.mu.Unlock()
	if stream == nil {
		return fmt.Errorf("stream %s is not open", id)
	}
	if err := stream.emit(ctx, chunk); err != nil {
		if errors.Is(err, errStreamBridgeClosed) {
			return fmt.Errorf("stream %s is not open", id)
		}
		return err
	}
	return nil
}

func (b *streamBridge) close(id string, errorMessage string) {
	if b == nil || id == "" {
		return
	}
	b.mu.Lock()
	stream := b.streams[id]
	delete(b.streams, id)
	b.mu.Unlock()
	if stream == nil {
		return
	}
	stream.close(errorMessage)
}
