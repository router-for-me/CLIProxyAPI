package executor

import (
	"bytes"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// Claude counterpart of the Codex stream bootstrap buffering. Anthropic can
// answer a 200 stream with message_start and ping and then a rate_limit_error
// event. While nothing has reached the caller, that error can still fail over
// to another account; once one byte went out it cannot.
const (
	claudeBootstrapHoldMaxLines = 48
	claudeBootstrapHoldMaxBytes = 1 << 20
)

// claudeBootstrapHoldTimeBound caps how long the caller's first byte may wait,
// measured from the first held frame. Tests shorten it.
var claudeBootstrapHoldTimeBound atomic.Int64

func init() {
	claudeBootstrapHoldTimeBound.Store(int64(5 * time.Second))
}

func claudeStreamBootstrapHoldEnabled(cfg *config.Config) bool {
	return cfg == nil || cfg.Claude.StreamBootstrapHoldEnabled()
}

type claudeBootstrapEventKind int

const (
	claudeBootstrapEventHoldable claudeBootstrapEventKind = iota
	claudeBootstrapEventGeneration
	claudeBootstrapEventRateLimit
)

// classifyClaudeBootstrapEvent sorts one complete SSE event. Only events that
// cannot have produced output or started server-side work are holdable; the
// list is closed, so anything unknown releases the stream. server_tool_use and
// web_search_tool_result starts are never held: failing over after them would
// run the search again on another account. An error other than a rate limit
// releases too, so it reaches the caller in-stream exactly as without the hold.
// A rate limit's payload is returned as its own copy, so it outlives the buffer
// rawEvent came from.
func classifyClaudeBootstrapEvent(rawEvent []byte) (claudeBootstrapEventKind, []byte) {
	var dataLines [][]byte
	sawField := false
	for _, line := range bytes.Split(rawEvent, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		switch {
		case len(trimmed) == 0, trimmed[0] == ':':
			continue
		case bytes.HasPrefix(trimmed, []byte("data:")):
			if payload := bytes.TrimSpace(trimmed[len("data:"):]); len(payload) > 0 {
				dataLines = append(dataLines, payload)
			}
		case startsWithSSEField(trimmed):
			sawField = true
		default:
			return claudeBootstrapEventGeneration, nil
		}
	}
	if len(dataLines) == 0 {
		if sawField {
			// A named event without data is not something this list knows.
			return claudeBootstrapEventGeneration, nil
		}
		return claudeBootstrapEventHoldable, nil
	}
	payload := bytes.TrimSpace(bytes.Join(dataLines, []byte("\n")))
	if !ojson.ValidBytes(payload) {
		return claudeBootstrapEventGeneration, nil
	}
	root := ojson.ParseBytes(payload)
	switch root.Get("type").String() {
	case "message_start", "ping":
		return claudeBootstrapEventHoldable, nil
	case "content_block_start":
		if isClaudeEmptyBootstrapBlock(root.Get("content_block")) {
			return claudeBootstrapEventHoldable, nil
		}
	case "error":
		if root.Get("error.type").String() == claudeRateLimitErrorType {
			return claudeBootstrapEventRateLimit, payload
		}
	}
	return claudeBootstrapEventGeneration, nil
}

// isClaudeSSEFieldLine reports whether a line belongs to an SSE event that is
// still being assembled, so it can wait for the event's classification.
func isClaudeSSEFieldLine(line []byte) bool {
	trimmed := bytes.TrimSpace(line)
	return len(trimmed) == 0 || startsWithSSEField(trimmed)
}

func isClaudeEmptyBootstrapBlock(block ojson.Result) bool {
	switch block.Get("type").String() {
	case "text":
		return block.Get("text").String() == ""
	case "thinking":
		return block.Get("thinking").String() == "" && block.Get("signature").String() == ""
	default:
		return false
	}
}

type claudeStreamAction int

const (
	claudeStreamNext claudeStreamAction = iota
	claudeStreamBreak
	claudeStreamReturn
)

const (
	claudeHoldOpen int32 = iota
	claudeHoldReleased
	// claudeHoldStopped means the stream goroutine must return: a release hit a
	// cancelled caller, or the held frames were dropped for a failover.
	claudeHoldStopped
)

// claudeBootstrapHold keeps the frames that precede generation. The stream
// goroutine does every per-line step inside Do while the hold is open, and the
// time bound releases from a timer goroutine under the same lock, so the bound
// fires even while the scanner blocks on an upstream that sends nothing.
type claudeBootstrapHold struct {
	mu    sync.Mutex
	state atomic.Int32
	items [][]byte
	lines int
	bytes int
	bound time.Duration
	timer *time.Timer
	// release sends items in order, through the same code live frames use, and
	// returns claudeStreamReturn when the stream goroutine must stop.
	release func([][]byte) claudeStreamAction
	// failover reports the account's rate limit in place of everything held.
	failover func(errorPayload []byte) claudeStreamAction
}

func newClaudeBootstrapHold(enabled bool, release func([][]byte) claudeStreamAction, failover func([]byte) claudeStreamAction) *claudeBootstrapHold {
	if !enabled {
		return nil
	}
	return &claudeBootstrapHold{bound: time.Duration(claudeBootstrapHoldTimeBound.Load()), release: release, failover: failover}
}

// Do runs one step of the stream goroutine. While the hold is open it runs
// under the lock the timer release takes; once released, without it.
func (h *claudeBootstrapHold) Do(step func() claudeStreamAction) claudeStreamAction {
	if h == nil {
		return step()
	}
	switch h.state.Load() {
	case claudeHoldReleased:
		return step()
	case claudeHoldStopped:
		return claudeStreamReturn
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.state.Load() == claudeHoldStopped {
		return claudeStreamReturn
	}
	return step()
}

// Holding reports whether frames are still being held. Call it inside Do.
func (h *claudeBootstrapHold) Holding() bool {
	return h != nil && h.state.Load() == claudeHoldOpen
}

// Admit decides one unit seen while holding: an event on the passthrough path,
// a line on the translated one. A rate limit drops what is held and fails over;
// a holdable unit is kept unless the stream already settled (completed or
// failed), since nothing can generate after that; anything else releases what
// is held. consumed is false only on a release, after which the caller sends
// the unit itself. Call it inside Do while Holding.
func (h *claudeBootstrapHold) Admit(kind claudeBootstrapEventKind, errorPayload, item []byte, lines int, settled bool) (action claudeStreamAction, consumed bool) {
	switch {
	case kind == claudeBootstrapEventRateLimit:
		h.stop()
		return h.failover(errorPayload), true
	case kind == claudeBootstrapEventHoldable && !settled:
		return h.hold(item, lines), true
	default:
		return h.Release(), false
	}
}

func (h *claudeBootstrapHold) hold(item []byte, lines int) claudeStreamAction {
	h.items = append(h.items, item)
	h.lines += lines
	h.bytes += len(item)
	if h.lines >= claudeBootstrapHoldMaxLines || h.bytes >= claudeBootstrapHoldMaxBytes {
		return h.Release()
	}
	if h.timer == nil && h.bound > 0 {
		h.timer = time.AfterFunc(h.bound, h.releaseOnTimeout)
	}
	return claudeStreamNext
}

// Release sends what is held and stops holding. Call it inside Do.
func (h *claudeBootstrapHold) Release() claudeStreamAction {
	if !h.Holding() {
		return claudeStreamNext
	}
	h.stopTimer()
	items := h.items
	h.items = nil
	action := claudeStreamNext
	if len(items) > 0 {
		action = h.release(items)
	}
	// The state is published last, so a step that sees the hold released also
	// sees everything the release wrote.
	if action == claudeStreamReturn {
		h.state.Store(claudeHoldStopped)
	} else {
		h.state.Store(claudeHoldReleased)
	}
	return action
}

// Finish releases what is still held once the upstream stopped, before the
// end-of-stream handling runs. Call it outside Do: it takes the lock itself.
func (h *claudeBootstrapHold) Finish() claudeStreamAction {
	return h.Do(h.Release)
}

// Close keeps a pending timer from sending after the stream goroutine returns.
func (h *claudeBootstrapHold) Close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.stop()
	h.mu.Unlock()
}

func (h *claudeBootstrapHold) stop() {
	h.stopTimer()
	h.items = nil
	h.state.Store(claudeHoldStopped)
}

func (h *claudeBootstrapHold) releaseOnTimeout() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.Release()
}

func (h *claudeBootstrapHold) stopTimer() {
	if h.timer != nil {
		h.timer.Stop()
		h.timer = nil
	}
}
