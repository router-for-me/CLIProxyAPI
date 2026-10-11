package claudemaster

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"io"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
	"github.com/tidwall/gjson"
)

// tokenCounts is one response's token usage as Anthropic reports it. Input excludes the cache
// fields, which the Messages API reports separately; output includes thinking.
type tokenCounts struct {
	input, output, cacheRead, cacheCreation int64
}

const (
	// usageLineLimit is the longest stream line kept for reading; usage events are far shorter, and a
	// longer line (a large content delta) is passed over without being held.
	usageLineLimit = 64 << 10
	// usageTailLimit is how much of the end of a JSON body is kept: its usage object comes last.
	usageTailLimit = 64 << 10
)

const (
	usageUndecided = iota
	usageSSE
	usageJSON
	usageOff
)

// usageScanner reads the usage Anthropic reports in a response from the bytes on their way to the
// client, without changing them: the native path forwards responses untouched and never parses
// them. In a stream it reads message_start's usage and then message_delta's, which is cumulative
// and so wins; in a JSON body, the top-level usage object.
type usageScanner struct {
	mode     int
	line     []byte
	skipping bool // inside a line longer than usageLineLimit
	tail     []byte
	size     int
	counts   tokenCounts
	seen     bool
	decoded  *decodedUsage // a compressed body: its plain bytes are read by a scanner of their own
}

// decodedUsage reads a compressed body. Anthropic answers compressed when the client allows it, and
// the native path forwards the bytes as sent, so without this the scanner looked for usage in
// compressed bytes and counted nothing: no token series at all for two hours after release
// 09015c6 (2026-10-10). The bytes the client receives go into a pipe, a goroutine decodes them and
// a plain scanner reads the result. The client's bytes are never changed; a body that does not
// decode is drained to the end and counts nothing, so a write never blocks on it.
type decodedUsage struct {
	pipe   *io.PipeWriter
	inner  usageScanner
	done   chan struct{}
	closed bool
}

// start picks how to read the body from the response's status, content type and content encoding.
func (u *usageScanner) start(status int, contentType, contentEncoding string) {
	switch {
	case status < 200 || status >= 300:
		u.mode = usageOff
	case strings.Contains(contentType, "event-stream"):
		u.mode = usageSSE
	case strings.Contains(contentType, "json"):
		u.mode = usageJSON
	default:
		u.mode = usageOff
	}
	if u.mode == usageOff {
		return
	}
	encodings := usageEncodings(contentEncoding)
	if len(encodings) == 0 {
		return
	}
	reader, writer := io.Pipe()
	u.decoded = &decodedUsage{pipe: writer, inner: usageScanner{mode: u.mode}, done: make(chan struct{})}
	go u.decoded.run(reader, encodings)
}

// usageEncodings is the Content-Encoding chain in the order it was applied, without identity.
func usageEncodings(header string) []string {
	var out []string
	for _, enc := range strings.Split(header, ",") {
		if enc = strings.ToLower(strings.TrimSpace(enc)); enc != "" && enc != "identity" {
			out = append(out, enc)
		}
	}
	return out
}

func (d *decodedUsage) run(compressed *io.PipeReader, encodings []string) {
	defer close(d.done)
	// Whatever the decoder does, keep reading until the response ends, so a write never blocks.
	defer func() { _, _ = io.Copy(io.Discard, compressed); _ = compressed.Close() }()
	var closers []func()
	defer func() {
		for i := len(closers) - 1; i >= 0; i-- {
			closers[i]()
		}
	}()
	var r io.Reader = compressed
	for i := len(encodings) - 1; i >= 0; i-- {
		switch encodings[i] {
		case "gzip", "x-gzip":
			gz, err := gzip.NewReader(r)
			if err != nil {
				return
			}
			closers = append(closers, func() { _ = gz.Close() })
			r = gz
		case "deflate":
			// HTTP's deflate is zlib-wrapped; some servers send raw deflate. The zlib header tells.
			buffered := bufio.NewReader(r)
			if head, err := buffered.Peek(2); err == nil && head[0]&0x0f == 8 && (uint16(head[0])<<8|uint16(head[1]))%31 == 0 {
				zr, err := zlib.NewReader(buffered)
				if err != nil {
					return
				}
				closers = append(closers, func() { _ = zr.Close() })
				r = zr
			} else {
				fr := flate.NewReader(buffered)
				closers = append(closers, func() { _ = fr.Close() })
				r = fr
			}
		case "br":
			r = brotli.NewReader(r)
		case "zstd":
			zd, err := zstd.NewReader(r)
			if err != nil {
				return
			}
			closers = append(closers, zd.Close)
			r = zd
		default:
			return
		}
	}
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			d.inner.write(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

func (u *usageScanner) write(b []byte) {
	if d := u.decoded; d != nil {
		if !d.closed {
			_, _ = d.pipe.Write(b)
		}
		return
	}
	switch u.mode {
	case usageSSE:
		for len(b) > 0 {
			end := bytes.IndexByte(b, '\n')
			if end < 0 {
				u.keep(b)
				return
			}
			u.keep(b[:end])
			if !u.skipping {
				u.readLine(bytes.TrimRight(u.line, "\r"))
			}
			u.line, u.skipping = u.line[:0], false
			b = b[end+1:]
		}
	case usageJSON:
		u.size += len(b)
		u.tail = append(u.tail, b...)
		if len(u.tail) > usageTailLimit {
			u.tail = append(u.tail[:0], u.tail[len(u.tail)-usageTailLimit:]...)
		}
	}
}

// keep adds part of the current line, or stops keeping it once it is too long to be a usage event.
func (u *usageScanner) keep(part []byte) {
	if u.skipping {
		return
	}
	if len(u.line)+len(part) > usageLineLimit {
		u.line, u.skipping = u.line[:0], true
		return
	}
	u.line = append(u.line, part...)
}

func (u *usageScanner) readLine(line []byte) {
	payload, ok := bytes.CutPrefix(line, []byte("data:"))
	if !ok {
		return
	}
	switch gjson.GetBytes(payload, "type").String() {
	case "message_start":
		u.apply(gjson.GetBytes(payload, "message.usage"))
	case "message_delta":
		u.apply(gjson.GetBytes(payload, "usage"))
	}
}

// apply takes every count a usage object carries; a later object's counts replace earlier ones.
func (u *usageScanner) apply(usage gjson.Result) {
	if !usage.IsObject() {
		return
	}
	for _, field := range []struct {
		name string
		into *int64
	}{
		{"input_tokens", &u.counts.input},
		{"output_tokens", &u.counts.output},
		{"cache_read_input_tokens", &u.counts.cacheRead},
		{"cache_creation_input_tokens", &u.counts.cacheCreation},
	} {
		if value := usage.Get(field.name); value.Type == gjson.Number {
			*field.into = max(value.Int(), 0)
			u.seen = true
		}
	}
}

// result is the response's usage, once the body is complete.
func (u *usageScanner) result() (tokenCounts, bool) {
	if d := u.decoded; d != nil {
		if !d.closed {
			d.closed = true
			_ = d.pipe.Close()
			<-d.done
		}
		return d.inner.result()
	}
	if u.mode == usageJSON && len(u.tail) > 0 {
		if u.size <= usageTailLimit {
			u.apply(gjson.GetBytes(u.tail, "usage"))
		} else if at := bytes.LastIndex(u.tail, []byte(`"usage":`)); at >= 0 {
			// Only the end of a large body is kept; its usage object is the last one in it.
			u.apply(gjson.ParseBytes(u.tail[at+len(`"usage":`):]))
		}
		u.tail = nil
	}
	return u.counts, u.seen
}
