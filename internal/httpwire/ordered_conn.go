// Package httpwire contains narrowly scoped HTTP/1.1 wire helpers.
package httpwire

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
)

const maxBufferedRequestHeader = 1 << 20

// RequestHeaderOrder returns the desired header-name order for one HTTP/1.1
// request. Names are compared case-insensitively. Headers omitted from the
// returned list retain their original relative order after the listed headers.
type RequestHeaderOrder func(method, requestTarget string) []string

// NewOrderedRequestConn wraps conn and rewrites only HTTP/1.1 request-header
// order. Request lines, header casing and values, and body bytes remain intact.
func NewOrderedRequestConn(conn net.Conn, order RequestHeaderOrder) net.Conn {
	if conn == nil || order == nil {
		return conn
	}
	return &orderedRequestConn{Conn: conn, order: order}
}

type orderedRequestConn struct {
	net.Conn
	order RequestHeaderOrder

	mu            sync.Mutex
	header        []byte
	bodyRemaining int64
	chunked       *chunkedRequestTracker
}

func (c *orderedRequestConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	originalLength := len(p)
	consumed := 0
	remaining := p
	for len(remaining) > 0 {
		if c.bodyRemaining > 0 {
			bodyBytes := min(int64(len(remaining)), c.bodyRemaining)
			written, errWrite := writeAll(c.Conn, remaining[:bodyBytes])
			consumed += written
			c.bodyRemaining -= int64(written)
			if errWrite != nil {
				return consumed, errWrite
			}
			remaining = remaining[bodyBytes:]
			continue
		}
		if c.chunked != nil {
			preview := c.chunked.clone()
			chunkBytes, _, errChunk := preview.consume(remaining)
			if errChunk != nil {
				return consumed, errChunk
			}
			written, errWrite := writeAll(c.Conn, remaining[:chunkBytes])
			consumed += written
			_, completed, errConsume := c.chunked.consume(remaining[:written])
			if errConsume != nil {
				return consumed, errConsume
			}
			if completed {
				c.chunked = nil
			}
			if errWrite != nil {
				return consumed, errWrite
			}
			remaining = remaining[chunkBytes:]
			continue
		}

		previousHeaderLength := len(c.header)
		c.header = append(c.header, remaining...)
		headerEnd := bytes.Index(c.header, []byte("\r\n\r\n"))
		if headerEnd < 0 {
			if len(c.header) > maxBufferedRequestHeader {
				return consumed, fmt.Errorf("httpwire: request header exceeds %d bytes", maxBufferedRequestHeader)
			}
			return originalLength, nil
		}

		headerEnd += len("\r\n\r\n")
		header := c.header[:headerEnd]
		body := c.header[headerEnd:]
		c.header = nil
		currentHeaderBytes := min(len(remaining), max(0, headerEnd-previousHeaderLength))

		ordered, contentLength, chunked := orderRequestHeader(header, c.order)
		if _, errWrite := writeAll(c.Conn, ordered); errWrite != nil {
			// All caller bytes were accepted into the wrapper before the transformed
			// header write failed. Return the full input count with the terminal
			// connection error so callers do not replay an ambiguous partial header.
			return originalLength, errWrite
		}
		consumed += currentHeaderBytes
		remaining = body
		if chunked {
			c.chunked = newChunkedRequestTracker()
			continue
		}
		c.bodyRemaining = contentLength
	}
	return originalLength, nil
}

func orderRequestHeader(header []byte, order RequestHeaderOrder) ([]byte, int64, bool) {
	lines := bytes.Split(header[:len(header)-len("\r\n\r\n")], []byte("\r\n"))
	if len(lines) == 0 {
		return header, 0, false
	}
	requestParts := strings.SplitN(string(lines[0]), " ", 3)
	if len(requestParts) != 3 {
		return header, requestContentLength(lines[1:]), requestUsesChunkedEncoding(lines[1:])
	}

	desired := order(requestParts[0], requestParts[1])
	if len(desired) == 0 {
		return header, requestContentLength(lines[1:]), requestUsesChunkedEncoding(lines[1:])
	}

	headerLines := lines[1:]
	used := make([]bool, len(headerLines))
	orderedLines := make([][]byte, 0, len(lines))
	orderedLines = append(orderedLines, lines[0])
	for _, name := range desired {
		for index, line := range headerLines {
			if used[index] || !headerLineNamed(line, name) {
				continue
			}
			orderedLines = append(orderedLines, line)
			used[index] = true
		}
	}
	for index, line := range headerLines {
		if !used[index] {
			orderedLines = append(orderedLines, line)
		}
	}

	var output bytes.Buffer
	for _, line := range orderedLines {
		output.Write(line)
		output.WriteString("\r\n")
	}
	output.WriteString("\r\n")
	return output.Bytes(), requestContentLength(headerLines), requestUsesChunkedEncoding(headerLines)
}

func headerLineNamed(line []byte, name string) bool {
	colon := bytes.IndexByte(line, ':')
	return colon > 0 && strings.EqualFold(string(line[:colon]), name)
}

func requestContentLength(lines [][]byte) int64 {
	for _, line := range lines {
		if !headerLineNamed(line, "Content-Length") {
			continue
		}
		colon := bytes.IndexByte(line, ':')
		value := strings.TrimSpace(string(line[colon+1:]))
		length, errParse := strconv.ParseInt(value, 10, 64)
		if errParse == nil && length > 0 {
			return length
		}
		return 0
	}
	return 0
}

func requestUsesChunkedEncoding(lines [][]byte) bool {
	for _, line := range lines {
		if !headerLineNamed(line, "Transfer-Encoding") {
			continue
		}
		colon := bytes.IndexByte(line, ':')
		for _, encoding := range strings.Split(string(line[colon+1:]), ",") {
			if strings.EqualFold(strings.TrimSpace(encoding), "chunked") {
				return true
			}
		}
	}
	return false
}

type chunkedRequestTracker struct {
	state         uint8
	line          []byte
	dataRemaining int64
	crlfPosition  int
	trailers      []byte
}

const (
	chunkedReadingSize uint8 = iota
	chunkedReadingData
	chunkedReadingDataCRLF
	chunkedReadingTrailers
)

func newChunkedRequestTracker() *chunkedRequestTracker {
	return &chunkedRequestTracker{state: chunkedReadingSize}
}

func (tracker *chunkedRequestTracker) clone() *chunkedRequestTracker {
	cloned := *tracker
	cloned.line = append([]byte(nil), tracker.line...)
	cloned.trailers = append([]byte(nil), tracker.trailers...)
	return &cloned
}

func (tracker *chunkedRequestTracker) consume(data []byte) (consumed int, completed bool, err error) {
	for consumed < len(data) {
		switch tracker.state {
		case chunkedReadingSize:
			tracker.line = append(tracker.line, data[consumed])
			consumed++
			if len(tracker.line) > maxBufferedRequestHeader {
				return consumed, false, fmt.Errorf("httpwire: chunk size line exceeds %d bytes", maxBufferedRequestHeader)
			}
			if len(tracker.line) < 2 || !bytes.Equal(tracker.line[len(tracker.line)-2:], []byte("\r\n")) {
				continue
			}
			sizeText := strings.TrimSpace(string(tracker.line[:len(tracker.line)-2]))
			if extension := strings.IndexByte(sizeText, ';'); extension >= 0 {
				sizeText = strings.TrimSpace(sizeText[:extension])
			}
			size, errParse := strconv.ParseInt(sizeText, 16, 64)
			if errParse != nil || size < 0 {
				return consumed, false, fmt.Errorf("httpwire: invalid chunk size %q", sizeText)
			}
			tracker.line = tracker.line[:0]
			if size == 0 {
				tracker.state = chunkedReadingTrailers
				continue
			}
			tracker.dataRemaining = size
			tracker.state = chunkedReadingData
		case chunkedReadingData:
			chunkBytes := min(int64(len(data)-consumed), tracker.dataRemaining)
			consumed += int(chunkBytes)
			tracker.dataRemaining -= chunkBytes
			if tracker.dataRemaining == 0 {
				tracker.crlfPosition = 0
				tracker.state = chunkedReadingDataCRLF
			}
		case chunkedReadingDataCRLF:
			want := []byte("\r\n")
			if data[consumed] != want[tracker.crlfPosition] {
				return consumed, false, fmt.Errorf("httpwire: chunk data is missing CRLF terminator")
			}
			consumed++
			tracker.crlfPosition++
			if tracker.crlfPosition == len(want) {
				tracker.state = chunkedReadingSize
			}
		case chunkedReadingTrailers:
			tracker.trailers = append(tracker.trailers, data[consumed])
			consumed++
			if len(tracker.trailers) > maxBufferedRequestHeader {
				return consumed, false, fmt.Errorf("httpwire: chunk trailers exceed %d bytes", maxBufferedRequestHeader)
			}
			if bytes.Equal(tracker.trailers, []byte("\r\n")) ||
				(len(tracker.trailers) >= 4 && bytes.Equal(tracker.trailers[len(tracker.trailers)-4:], []byte("\r\n\r\n"))) {
				return consumed, true, nil
			}
		default:
			return consumed, false, fmt.Errorf("httpwire: invalid chunk parser state %d", tracker.state)
		}
	}
	return consumed, false, nil
}

func writeAll(writer io.Writer, data []byte) (int, error) {
	total := 0
	for len(data) > 0 {
		written, errWrite := writer.Write(data)
		total += written
		if errWrite != nil {
			return total, errWrite
		}
		if written <= 0 {
			return total, io.ErrShortWrite
		}
		data = data[written:]
	}
	return total, nil
}
