package helps

import (
	"bytes"
)

const (
	defaultMaxStreamModelBufferBound = 64 * 1024
	defaultMaxLinesPerStreamEvent    = 2048
	streamEventLineOverhead          = 32
)

// StreamResponseModelObserver accumulates streaming chunks using a bounded buffer
// and extracts complete SSE lines or events to observe the response model,
// ensuring extraction does not depend on network chunk boundaries.
type StreamResponseModelObserver struct {
	reporter      *UsageReporter
	buf           []byte
	frame         [][]byte
	frameBytes    int
	maxBound      int
	overflow      bool
	eventOverflow bool
}

// NewStreamResponseModelObserver creates an observer for tracking response models
// from arbitrary streaming network chunks.
func NewStreamResponseModelObserver(reporter *UsageReporter) *StreamResponseModelObserver {
	return &StreamResponseModelObserver{
		reporter: reporter,
		maxBound: defaultMaxStreamModelBufferBound,
	}
}

func (o *StreamResponseModelObserver) bound() int {
	if o == nil || o.maxBound <= 0 {
		return defaultMaxStreamModelBufferBound
	}
	return o.maxBound
}

// Feed ingests a raw chunk of bytes from a stream, splitting into lines and events.
func (o *StreamResponseModelObserver) Feed(chunk []byte) {
	if o == nil || o.reporter == nil || len(chunk) == 0 {
		return
	}
	if o.reporter.IsResponseModelFinal() {
		o.buf = nil
		o.frame = nil
		return
	}

	for len(chunk) > 0 {
		if o.overflow {
			idx := bytes.IndexByte(chunk, '\n')
			if idx < 0 {
				return
			}
			chunk = chunk[idx+1:]
			o.overflow = false
			o.buf = o.buf[:0]
			continue
		}

		idx := bytes.IndexByte(chunk, '\n')
		if idx < 0 {
			if len(o.buf)+len(chunk) > o.bound() {
				o.overflow = true
				o.buf = o.buf[:0]
				if len(o.frame) > 0 {
					o.eventOverflow = true
					o.frame = nil
					o.frameBytes = 0
				}
			} else {
				o.buf = append(o.buf, chunk...)
			}
			return
		}

		linePart := chunk[:idx]
		chunk = chunk[idx+1:]
		if len(o.buf)+len(linePart) > o.bound() {
			o.buf = o.buf[:0]
			if len(o.frame) > 0 {
				o.eventOverflow = true
				o.frame = nil
				o.frameBytes = 0
			}
			continue
		}

		var line []byte
		if len(o.buf) > 0 {
			o.buf = append(o.buf, linePart...)
			line = bytes.TrimSuffix(o.buf, []byte("\r"))
			o.buf = o.buf[:0]
		} else {
			line = bytes.TrimSuffix(linePart, []byte("\r"))
		}

		o.handleLine(line)
		if o.reporter.IsResponseModelFinal() {
			o.buf = nil
			o.frame = nil
			return
		}
	}
}

// Finish flushes any pending buffered line or event at the end of the stream.
func (o *StreamResponseModelObserver) Finish() {
	if o == nil || o.reporter == nil {
		return
	}
	if !o.overflow && len(o.buf) > 0 {
		line := bytes.TrimSuffix(o.buf, []byte("\r"))
		o.handleLine(line)
		o.buf = nil
	}
	o.flushEvent()
}

func (o *StreamResponseModelObserver) handleLine(line []byte) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		o.flushEvent()
		return
	}

	if o.eventOverflow {
		return
	}

	// Always attempt to observe the line directly (e.g. data: {"model":"..."}, {"model":"..."}).
	o.reporter.ObserveResponseModel(line)

	if bytes.HasPrefix(trimmed, []byte("data:")) {
		dataPayload := bytes.TrimPrefix(trimmed, []byte("data:"))
		dataPayload = bytes.TrimPrefix(dataPayload, []byte(" "))
		lineCost := len(dataPayload) + streamEventLineOverhead
		if o.frameBytes+lineCost > o.bound() || len(o.frame) >= defaultMaxLinesPerStreamEvent {
			o.eventOverflow = true
			o.frame = nil
			o.frameBytes = 0
			return
		}
		o.frame = append(o.frame, bytes.Clone(dataPayload))
		o.frameBytes += lineCost
	}
}

func (o *StreamResponseModelObserver) flushEvent() {
	if o.eventOverflow {
		o.eventOverflow = false
		o.frame = nil
		o.frameBytes = 0
		return
	}
	if len(o.frame) == 0 {
		return
	}
	if len(o.frame) > 1 {
		joined := bytes.Join(o.frame, []byte("\n"))
		o.reporter.ObserveResponseModel(joined)
	}
	o.frame = nil
	o.frameBytes = 0
}
