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
	// closeMu serialises the closed-flag transition against in-flight
	// enqueue check+send. Without it, an enqueue that observed closed=false
	// could be preempted before its channel send while close() ran to
	// completion, leaving a forced task buffered in priority with no worker
	// to drain it (Codex Phase C re-review BLOCKER #3 follow-up).
	//
	// Held only for the brief check+send window in enqueue and the flag
	// transition in close(); contention is bounded by enqueue rate.
	closeMu sync.Mutex
	closed  bool

	// pending tracks every task from the moment enqueue commits to a
	// channel send through the worker's writeLogRequest return. Counted
	// at enqueue (not at execute) so flush() doesn't return in the
	// dequeue-to-execute window where the channel has emptied but the
	// worker hasn't yet entered execute() — Codex Phase C round-4 review
	// IMPORTANT #1. Decremented in execute()'s defer (channel path) and
	// in enqueue's syncFallback path after the synchronous write
	// (handled by the caller in writeLogRequest, see Close path note).
	pending atomic.Int64
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
	defer e.pending.Add(-1)
	_ = e.logger.writeLogRequest(t.args, t.force)
}

// enqueue routes a task. Returns true when the caller should fall back to a
// synchronous write — happens for forced-error logs when the priority queue
// is full or the emitter is closed (since those must never drop).
//
// The closeMu critical section pins the closed-flag check and the channel
// send into one atomic step against close(). Without that, a forced task
// could pass the closed=false check, get preempted while close() drains
// and exits the worker, and then send into priority where no consumer is
// left.
func (e *asyncEmitter) enqueue(t asyncTask) (syncFallback bool) {
	e.closeMu.Lock()
	defer e.closeMu.Unlock()

	if e.closed {
		if t.force {
			return true
		}
		e.dropped.Add(1)
		return false
	}
	if t.force {
		select {
		case e.priority <- t:
			e.pending.Add(1)
			return false
		default:
			return true
		}
	}
	select {
	case e.normal <- t:
		e.pending.Add(1)
		return false
	default:
		e.dropped.Add(1)
		return false
	}
}

func (e *asyncEmitter) close() {
	e.closeOnce.Do(func() {
		// Hold closeMu across the flag transition AND the close(closeCh)
		// signal so enqueue cannot observe closed=false after we've
		// committed to shutdown. Once the lock is released, all
		// subsequent enqueue calls see closed=true and either short
		// circuit to sync (force) or drop (normal).
		e.closeMu.Lock()
		e.closed = true
		close(e.closeCh)
		e.closeMu.Unlock()
	})
	<-e.doneCh
}

// flush blocks until every enqueued task has been written. Callers must
// externally ensure no further tasks are enqueued during the flush
// window for the wait to converge in finite time.
//
// pending is incremented inside enqueue's lock-protected channel send
// and decremented in execute's defer, so flush observing pending==0
// implies every task that left enqueue has also left execute (Codex
// Phase C round-4 review IMPORTANT #1, replacing the round-3 active
// counter that had a dequeue-to-execute race).
func (e *asyncEmitter) flush() {
	for {
		if e.pending.Load() == 0 {
			return
		}
		time.Sleep(asyncFlushPollInterval)
	}
}

func (e *asyncEmitter) droppedCount() uint64 {
	return e.dropped.Load()
}
