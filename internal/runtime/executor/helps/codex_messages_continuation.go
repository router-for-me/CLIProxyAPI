package helps

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// CodexMessagesHandshakeFingerprint digests the effective handshake headers.
func CodexMessagesHandshakeFingerprint(headers http.Header) [32]byte {
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, key := range keys {
		_, _ = io.WriteString(h, key)
		_, _ = h.Write([]byte{0})
		for _, value := range headers[key] {
			_, _ = io.WriteString(h, value)
			_, _ = h.Write([]byte{0})
		}
		_, _ = h.Write([]byte{0})
	}
	var digest [32]byte
	h.Sum(digest[:0])
	return digest
}

// CodexMessagesContinuation records a tool turn for matching a subsequent request.
type CodexMessagesContinuation struct {
	id string

	shape  [32]byte
	count  int
	prefix [32]byte
	output [][32]byte
	calls  int
}

// CodexMessagesTurn represents the full normalized business payload of a turn.
type CodexMessagesTurn struct {
	input []ojson.Result
	shape [32]byte
}

// CaptureCodexMessagesTurn snapshots the full input before any delta is applied.
func CaptureCodexMessagesTurn(doc *ojson.Doc, imageMode config.DisableImageGenerationMode) (*CodexMessagesTurn, error) {
	// Only called for requests without user payload rules.
	if doc.Get("previous_response_id").Exists() {
		return nil, nil
	}
	input := doc.Get("input")
	if !input.IsArray() {
		return nil, nil
	}
	// Hash the envelope without materializing the complete outgoing request.
	// Restore input on the same Doc before matching or finalizing it.
	if err := doc.Delete("input"); err != nil {
		return nil, err
	}
	hash := sha256.New()
	_, _ = fmt.Fprintln(hash, imageMode)
	if _, err := doc.WriteTo(hash); err != nil {
		return nil, err
	}
	if err := doc.SetRaw("input", input.Raw); err != nil {
		return nil, err
	}
	var shape [32]byte
	hash.Sum(shape[:0])
	return &CodexMessagesTurn{input: input.Array(), shape: shape}, nil
}

func hashCodexHTTPMessagesInput(input []ojson.Result) [32]byte {
	h := sha256.New()
	for _, item := range input {
		_, _ = io.WriteString(h, item.Raw)
		_, _ = h.Write([]byte{0})
	}
	var sum [32]byte
	h.Sum(sum[:0])
	return sum
}

// Match only output types with an unambiguous replay in streaming Messages.
func codexHTTPMessagesOutput(item ojson.Result) (digest [32]byte, call bool, ok bool) {
	switch item.Get("type").String() {
	case "function_call":
		callID := item.Get("call_id").String()
		name := item.Get("name").String()
		args := item.Get("arguments").String()
		if callID == "" || name == "" || args == "" || strings.ContainsRune(callID, 0) || strings.ContainsRune(name, 0) {
			return digest, false, false
		}
		return sha256.Sum256([]byte("call\x00" + callID + "\x00" + name + "\x00" + args)), true, true
	case "reasoning":
		sig := item.Get("encrypted_content").String()
		if sig != "" {
			return sha256.Sum256([]byte("reasoning\x00" + sig)), false, true
		}
	}
	return digest, false, false
}

// ApplyContinuation replaces the full input with a verified tool-result suffix.
func (t *CodexMessagesTurn) ApplyContinuation(doc *ojson.Doc, previous CodexMessagesContinuation) (bool, error) {
	if t == nil || previous.shape != t.shape || previous.calls == 0 || len(t.input) != previous.count+len(previous.output)+previous.calls ||
		hashCodexHTTPMessagesInput(t.input[:previous.count]) != previous.prefix {
		return false, nil
	}
	n := previous.count
	outputs := make(map[string]bool, previous.calls)
	for i, want := range previous.output {
		item := t.input[n+i]
		got, call, ok := codexHTTPMessagesOutput(item)
		if !ok || got != want {
			return false, nil
		}
		if call {
			outputs[item.Get("call_id").String()] = true
		}
	}
	n += len(previous.output)
	var suffix strings.Builder
	suffix.WriteByte('[')
	for i, item := range t.input[n:] {
		if item.Get("type").String() != "function_call_output" {
			return false, nil
		}
		id := item.Get("call_id").String()
		if !outputs[id] {
			return false, nil
		}
		delete(outputs, id)
		if i > 0 {
			suffix.WriteByte(',')
		}
		suffix.WriteString(item.Raw)
	}
	if len(outputs) != 0 {
		return false, nil
	}
	suffix.WriteByte(']')
	if err := doc.SetRaw("input", suffix.String()); err != nil {
		return false, err
	}
	if err := doc.Set("previous_response_id", previous.id); err != nil {
		return false, err
	}
	return true, nil
}

// Complete captures output that can be matched losslessly on the next turn.
func (t *CodexMessagesTurn) Complete(payload []byte) CodexMessagesContinuation {
	if t == nil {
		return CodexMessagesContinuation{}
	}
	root := ojson.ParseBytes(payload)
	id := root.Get("response.id").String()
	if id == "" {
		return CodexMessagesContinuation{}
	}
	items := root.Get("response.output").Array()
	output := make([][32]byte, 0, len(items))
	calls := 0
	for _, item := range items {
		digest, call, ok := codexHTTPMessagesOutput(item)
		if !ok {
			return CodexMessagesContinuation{}
		}
		output = append(output, digest)
		if call {
			calls++
		}
	}
	if calls == 0 {
		return CodexMessagesContinuation{}
	}
	return CodexMessagesContinuation{id: strings.Clone(id), shape: t.shape, count: len(t.input), prefix: hashCodexHTTPMessagesInput(t.input), output: output, calls: calls}
}
