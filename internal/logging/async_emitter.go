package logging

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
)

// Default queue capacities. Sized for typical request rates: at ~200 req/s
// and ~1ms-per-write the queue empties faster than it fills, so the
// drop-newest-on-pressure path is rare. Forced-error pressure is much
// lower so the priority lane is shallower.
const (
	asyncNormalQueueDepth   = 256
	asyncPriorityQueueDepth = 64
	asyncFlushPollInterval  = 5 * time.Millisecond
)

// asyncLogArgs bundles the parameters of FileRequestLogger.LogRequest so
// they can be passed across the async emitter's channels without taking 15+
// arguments at every site.
type asyncLogArgs struct {
	url, method          string
	requestHeaders       map[string][]string
	body                 []byte
	statusCode           int
	responseHeaders      map[string][]string
	response             []byte
	websocketTimeline    []byte
	apiRequest           []byte
	apiResponse          []byte
	apiWebsocketTimeline []byte
	apiResponseErrors    []*interfaces.ErrorMessage
	requestID            string
	requestTimestamp     time.Time
	apiResponseTimestamp time.Time
}

type asyncTask struct {
	args  asyncLogArgs
	force bool
}

// asyncEmitter offloads request-log file I/O from the request handler. It
// holds two channels:
//
//   - normal: drop-newest on overflow (counted in droppedCount)
//   - priority: forced-error logs; on overflow we fall back to a synchronous
//     write to honor "forced error logs never drop"
//
// The single worker goroutine drains priority first, then normal. Close()
// signals stop and waits for the worker to drain both queues.
type asyncEmitter struct {
	logger    *FileRequestLogger
	normal    chan asyncTask
	priority  chan asyncTask
	closeCh   chan struct{}
	doneCh    chan struct{}
	startOnce sync.Once
	closeOnce sync.Once
	dropped   atomic.Uint64
}

func newAsyncEmitter(logger *FileRequestLogger) *asyncEmitter {
	return &asyncEmitter{
		logger:   logger,
		normal:   make(chan asyncTask, asyncNormalQueueDepth),
		priority: make(chan asyncTask, asyncPriorityQueueDepth),
		closeCh:  make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
}

func (e *asyncEmitter) start() {
	e.startOnce.Do(func() {
		go e.run()
	})
}

func (e *asyncEmitter) run() {
	defer close(e.doneCh)
	for {
		// Priority drain first: if there's anything in priority, take it.
		select {
		case t := <-e.priority:
			e.execute(t)
			continue
		default:
		}

		select {
		case t := <-e.priority:
			e.execute(t)
		case t := <-e.normal:
			e.execute(t)
		case <-e.closeCh:
			e.drain()
			return
		}
	}
}

// drain empties both queues without blocking. Called on shutdown so queued
// logs are written before the worker exits.
func (e *asyncEmitter) drain() {
	for {
		select {
		case t := <-e.priority:
			e.execute(t)
		default:
			goto drainNormal
		}
	}
drainNormal:
	for {
		select {
		case t := <-e.normal:
			e.execute(t)
		default:
			return
		}
	}
}

func (e *asyncEmitter) execute(t asyncTask) {
	_ = e.logger.writeLogRequest(t.args, t.force)
}

// enqueue routes a task. Returns true when the caller should fall back to a
// synchronous write — only happens for forced-error logs when the priority
// queue is full, since those must never drop.
func (e *asyncEmitter) enqueue(t asyncTask) (syncFallback bool) {
	if t.force {
		select {
		case e.priority <- t:
			return false
		default:
			return true
		}
	}
	select {
	case e.normal <- t:
		return false
	default:
		e.dropped.Add(1)
		return false
	}
}

func (e *asyncEmitter) close() {
	e.closeOnce.Do(func() {
		close(e.closeCh)
	})
	<-e.doneCh
}

// flush blocks until both queues are empty. Callers must externally ensure
// no further tasks are enqueued during the flush window for the wait to
// converge in finite time.
func (e *asyncEmitter) flush() {
	for {
		if len(e.priority) == 0 && len(e.normal) == 0 {
			return
		}
		time.Sleep(asyncFlushPollInterval)
	}
}

func (e *asyncEmitter) droppedCount() uint64 {
	return e.dropped.Load()
}
