package usage

// RingBuffer stores request details in a fixed-size ring, preserving order.
// It is not concurrency-safe and should be guarded by an outer lock.
type RingBuffer struct {
	buf   []RequestDetail
	start int
	size  int
	limit int
}

// NewRingBuffer creates a new ring buffer with the given limit.
func NewRingBuffer(limit int) *RingBuffer {
	if limit < 0 {
		limit = 0
	}
	return &RingBuffer{
		buf:   make([]RequestDetail, limit),
		limit: limit,
	}
}

// Push appends a detail to the buffer. If full, it evicts the oldest entry.
func (rb *RingBuffer) Push(detail RequestDetail) {
	if rb == nil || rb.limit == 0 {
		return
	}
	if rb.size < rb.limit {
		idx := (rb.start + rb.size) % rb.limit
		rb.buf[idx] = detail
		rb.size++
		return
	}
	rb.buf[rb.start] = detail
	rb.start = (rb.start + 1) % rb.limit
	return
}

// Snapshot returns a copy of the buffer contents ordered from oldest to newest.
func (rb *RingBuffer) Snapshot() []RequestDetail {
	if rb == nil || rb.size == 0 || rb.limit == 0 {
		return nil
	}
	out := make([]RequestDetail, rb.size)
	for i := 0; i < rb.size; i++ {
		idx := (rb.start + i) % rb.limit
		out[i] = rb.buf[idx]
	}
	return out
}

// Load replaces the buffer contents with the provided ordered details.
// If the input exceeds capacity, only the newest entries are retained.
func (rb *RingBuffer) Load(details []RequestDetail) {
	if rb == nil {
		return
	}
	rb.start = 0
	rb.size = 0
	if rb.limit == 0 || len(details) == 0 {
		return
	}
	if len(details) > rb.limit {
		details = details[len(details)-rb.limit:]
	}
	for _, detail := range details {
		rb.Push(detail)
	}
}

// Resize changes the buffer capacity and keeps the newest entries.
func (rb *RingBuffer) Resize(limit int) {
	if rb == nil {
		return
	}
	if limit < 0 {
		limit = 0
	}
	if limit == rb.limit {
		return
	}
	snapshot := rb.Snapshot()
	rb.limit = limit
	rb.buf = make([]RequestDetail, limit)
	rb.start = 0
	rb.size = 0
	if limit == 0 {
		return
	}
	if len(snapshot) > limit {
		snapshot = snapshot[len(snapshot)-limit:]
	}
	for _, detail := range snapshot {
		rb.Push(detail)
	}
}

// Len returns the number of entries currently stored.
func (rb *RingBuffer) Len() int {
	if rb == nil {
		return 0
	}
	return rb.size
}

// Oldest returns the oldest entry without removing it.
func (rb *RingBuffer) Oldest() (RequestDetail, bool) {
	if rb == nil || rb.size == 0 || rb.limit == 0 {
		return RequestDetail{}, false
	}
	return rb.buf[rb.start], true
}

// PopOldest removes and returns the oldest entry.
func (rb *RingBuffer) PopOldest() (RequestDetail, bool) {
	if rb == nil || rb.size == 0 || rb.limit == 0 {
		return RequestDetail{}, false
	}
	value := rb.buf[rb.start]
	rb.start = (rb.start + 1) % rb.limit
	rb.size--
	return value, true
}
