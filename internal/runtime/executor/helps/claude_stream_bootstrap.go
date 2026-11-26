package helps

import (
	"bufio"
	"bytes"
	"errors"
	"io"

	"github.com/MoeForge/moego/ojson"
)

// Budgets for the Claude stream bootstrap hold. They match the Codex bootstrap
// budgets; the hold has no time bound.
const (
	claudeBootstrapMaxLines   = 48
	claudeBootstrapMaxBytes   = 1 << 20
	claudeBootstrapReaderSize = 64 << 10
)

// ClaudeStreamBootstrap is what ReadClaudeStreamBootstrap read from the start of a
// Claude SSE stream.
type ClaudeStreamBootstrap struct {
	// Stream yields the whole stream, held bytes first, exactly as the body would have.
	Stream io.Reader
	// Held is every byte read before ReadClaudeStreamBootstrap returned.
	Held []byte
	// RateLimit is the payload of a rate_limit_error event that arrived before any
	// generating event, or nil.
	RateLimit []byte
}

// ReadClaudeStreamBootstrap reads the events that carry no output yet (message_start,
// ping, comments, and a text or thinking content_block_start that is still empty)
// from the start of body. It stops at the first other event, at the line or byte
// budget, or when body ends or fails. A rate_limit_error event among the held events
// is reported in RateLimit: nothing has reached the client yet, so the caller can
// fail over instead of forwarding it.
//
// The list is closed on purpose. Anything else, including tool_use, server_tool_use
// and web_search_tool_result block starts, ends the hold, since failing over after
// those would repeat work.
func ReadClaudeStreamBootstrap(body io.Reader) ClaudeStreamBootstrap {
	reader := bufio.NewReaderSize(body, claudeBootstrapReaderSize)
	var held bytes.Buffer
	var event bytes.Buffer
	var errRead error
	var rateLimit []byte
	for lines := 0; lines < claudeBootstrapMaxLines && held.Len() < claudeBootstrapMaxBytes; lines++ {
		var line []byte
		line, errRead = reader.ReadSlice('\n')
		held.Write(line)
		event.Write(line)
		if errRead == nil && len(bytes.TrimSpace(line)) != 0 {
			continue
		}
		// An event ends at its blank line, or at the end of the body: a rate limit can be
		// the last event, sent without its blank line.
		var holdable bool
		holdable, rateLimit = classifyClaudeBootstrapEvent(event.Bytes())
		event.Reset()
		if errRead != nil || !holdable {
			break
		}
	}
	heldBytes := held.Bytes()
	// Replay what bufio read ahead, then the body itself, so the read buffer is not kept
	// for the rest of the stream. A read error is replayed as the body would have
	// returned it, every time it is asked.
	rest := []io.Reader{bytes.NewReader(heldBytes)}
	switch {
	case errRead == nil || errors.Is(errRead, bufio.ErrBufferFull):
		buffered, _ := reader.Peek(reader.Buffered())
		rest = append(rest, bytes.NewReader(bytes.Clone(buffered)), body)
	case !errors.Is(errRead, io.EOF):
		rest = append(rest, claudeBootstrapErrReader{errRead})
	}
	return ClaudeStreamBootstrap{Stream: io.MultiReader(rest...), Held: heldBytes, RateLimit: rateLimit}
}

type claudeBootstrapErrReader struct{ err error }

func (r claudeBootstrapErrReader) Read([]byte) (int, error) { return 0, r.err }

// classifyClaudeBootstrapEvent reports whether one complete SSE event can be held,
// or returns its payload when it is a rate_limit_error.
func classifyClaudeBootstrapEvent(rawEvent []byte) (bool, []byte) {
	var data [][]byte
	named := false
	for _, line := range bytes.Split(rawEvent, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		switch {
		case len(trimmed) == 0 || trimmed[0] == ':':
			// Blank lines and comments carry nothing.
		case bytes.HasPrefix(trimmed, []byte("data:")):
			if payload := bytes.TrimSpace(trimmed[len("data:"):]); len(payload) > 0 {
				data = append(data, payload)
			}
		case isSSEControlLine(trimmed):
			named = true
		default:
			return false, nil
		}
	}
	if len(data) == 0 {
		// Comments and blank lines are held; a named event without data is unknown.
		return !named, nil
	}
	payload := bytes.Join(data, []byte("\n"))
	if !ojson.ValidBytes(payload) {
		return false, nil
	}
	root := ojson.ParseBytes(payload)
	switch root.Get("type").String() {
	case "message_start", "ping":
		return true, nil
	case "content_block_start":
		block := root.Get("content_block")
		switch block.Get("type").String() {
		case "text":
			return block.Get("text").String() == "", nil
		case "thinking":
			return block.Get("thinking").String() == "" && block.Get("signature").String() == "", nil
		}
	case "error":
		if root.Get("error.type").String() == "rate_limit_error" {
			return false, payload
		}
	}
	return false, nil
}
