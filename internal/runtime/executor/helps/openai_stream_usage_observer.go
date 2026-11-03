package helps

import "bytes"

// OpenAIStreamUsageObserver assembles SSE events for accounting while callers
// continue forwarding the original network chunks unchanged.
type OpenAIStreamUsageObserver struct {
	usage         *StreamUsageBuffer
	line          []byte
	data          [][]byte
	dataBytes     int
	maxBytes      int
	lineOverflow  bool
	eventOverflow bool
}

// NewOpenAIStreamUsageObserver bounds both incomplete lines and assembled events.
func NewOpenAIStreamUsageObserver(usage *StreamUsageBuffer, maxBytes int) *OpenAIStreamUsageObserver {
	if maxBytes <= 0 {
		maxBytes = defaultMaxStreamModelBufferBound
	}
	return &OpenAIStreamUsageObserver{usage: usage, maxBytes: maxBytes}
}

// Feed consumes arbitrary network chunks, retaining incomplete lines across reads.
func (o *OpenAIStreamUsageObserver) Feed(chunk []byte) {
	if o == nil || o.usage == nil {
		return
	}
	for len(chunk) > 0 {
		idx := bytes.IndexByte(chunk, '\n')
		if idx < 0 {
			if !o.lineOverflow {
				if len(o.line)+len(chunk) > o.maxBytes {
					o.dropEvent()
					o.line = nil
					o.lineOverflow = true
				} else {
					o.line = append(o.line, chunk...)
				}
			}
			return
		}
		part := chunk[:idx]
		chunk = chunk[idx+1:]
		if o.lineOverflow || len(o.line)+len(part) > o.maxBytes {
			o.dropEvent()
			o.lineOverflow = false
			o.line = nil
			continue
		}
		if len(o.line) > 0 {
			o.line = append(o.line, part...)
			o.observeLine(o.line)
			o.line = o.line[:0]
		} else {
			o.observeLine(part)
		}
	}
}

// Finish observes a trailing event even if the upstream omitted its final newline.
func (o *OpenAIStreamUsageObserver) Finish() {
	if o == nil || o.usage == nil {
		return
	}
	if !o.lineOverflow && len(o.line) > 0 {
		o.observeLine(o.line)
	}
	o.flushEvent()
	o.line = nil
	o.lineOverflow = false
}

func (o *OpenAIStreamUsageObserver) observeLine(line []byte) {
	line = bytes.TrimSuffix(line, []byte("\r"))
	if len(line) == 0 {
		o.flushEvent()
		return
	}
	if o.eventOverflow || !bytes.HasPrefix(line, []byte("data:")) {
		return
	}
	payload := bytes.TrimPrefix(line[len("data:"):], []byte(" "))
	cost := len(payload) + streamEventLineOverhead
	if o.dataBytes+cost > o.maxBytes || len(o.data) >= defaultMaxLinesPerStreamEvent {
		o.dropEvent()
		return
	}
	o.data = append(o.data, bytes.Clone(payload))
	o.dataBytes += cost
}

func (o *OpenAIStreamUsageObserver) dropEvent() {
	o.eventOverflow = true
	o.data = nil
	o.dataBytes = 0
}

func (o *OpenAIStreamUsageObserver) flushEvent() {
	if !o.eventOverflow && len(o.data) > 0 {
		if len(o.data) == 1 {
			o.usage.ObserveOpenAIStream(o.data[0])
		} else {
			o.usage.ObserveOpenAIStream(bytes.Join(o.data, []byte("\n")))
		}
	}
	o.data = nil
	o.dataBytes = 0
	o.eventOverflow = false
}
