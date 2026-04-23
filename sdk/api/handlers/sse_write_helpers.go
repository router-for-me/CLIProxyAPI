package handlers

import (
	"io"
	"sync"
)

var sseFrameBufferPool = sync.Pool{
	New: func() any {
		return make([]byte, 0, 1024)
	},
}

func WriteRawSSEChunk(w io.Writer, chunk []byte) bool {
	if w == nil || len(chunk) == 0 {
		return false
	}
	buf := getSSEFrameBuffer(len(chunk) + 2)
	defer putSSEFrameBuffer(buf)

	buf = append(buf, chunk...)
	if len(chunk) >= 2 && chunk[len(chunk)-2] == '\n' && chunk[len(chunk)-1] == '\n' {
		return writeSSEFrameBuffer(w, buf)
	}
	if len(chunk) >= 4 && chunk[len(chunk)-4] == '\r' && chunk[len(chunk)-3] == '\n' && chunk[len(chunk)-2] == '\r' && chunk[len(chunk)-1] == '\n' {
		return writeSSEFrameBuffer(w, buf)
	}
	if len(chunk) > 1 && chunk[len(chunk)-2] == '\r' && chunk[len(chunk)-1] == '\n' {
		buf = append(buf, '\r', '\n')
	} else if len(chunk) > 0 && chunk[len(chunk)-1] == '\n' {
		buf = append(buf, '\n')
	} else {
		buf = append(buf, '\n', '\n')
	}
	return writeSSEFrameBuffer(w, buf)
}

func WriteSSEDataFrame(w io.Writer, payload []byte) bool {
	return WriteSSEEventDataFrame(w, "", payload)
}

func WriteSSEEventDataFrame(w io.Writer, event string, payload []byte) bool {
	return writeSSEStructuredFrame(w, event, payload, false)
}

func WriteSSEEventDataFrameWithLeadingNewline(w io.Writer, event string, payload []byte) bool {
	return writeSSEStructuredFrame(w, event, payload, true)
}

func writeSSEStructuredFrame(w io.Writer, event string, payload []byte, leadingNewline bool) bool {
	if w == nil {
		return false
	}
	extra := len(payload) + len(event) + len("data: \n\n")
	if event != "" {
		extra += len("event: \n")
	}
	if leadingNewline {
		extra++
	}
	buf := getSSEFrameBuffer(extra)
	defer putSSEFrameBuffer(buf)

	if leadingNewline {
		buf = append(buf, '\n')
	}
	if event != "" {
		buf = append(buf, "event: "...)
		buf = append(buf, event...)
		buf = append(buf, '\n')
	}
	buf = append(buf, "data: "...)
	buf = append(buf, payload...)
	buf = append(buf, '\n', '\n')
	return writeSSEFrameBuffer(w, buf)
}

func getSSEFrameBuffer(minCap int) []byte {
	buf := sseFrameBufferPool.Get().([]byte)
	if cap(buf) < minCap {
		return make([]byte, 0, minCap)
	}
	return buf[:0]
}

func putSSEFrameBuffer(buf []byte) {
	if cap(buf) > 64*1024 {
		return
	}
	sseFrameBufferPool.Put(buf[:0])
}

func writeSSEFrameBuffer(w io.Writer, buf []byte) bool {
	if _, err := w.Write(buf); err != nil {
		return false
	}
	return true
}
