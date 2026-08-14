package helps

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// Factory/Droid native tool markup that Grok sometimes leaks into assistant
// text instead of emitting OpenAI tool_calls. Factory's first-party Grok
// adapter parses this; generic OpenAI chat-completion clients do not, so the
// turn ends as plain text (worker_exited_without_handoff).
const (
	xaiNativeToolCallsBegin = "<|tool_calls_begin|>"
	xaiNativeToolCallBegin  = "<|tool_call_begin|>"
	xaiNativeToolCallEnd    = "<|tool_call_end|>"
	xaiNativeToolCallsEnd   = "<|tool_calls_end|>"
	xaiNativeToolSep        = "<|tool_sep|>"
)

type xaiNativeToolCall struct {
	Name      string
	Arguments map[string]any
}

type xaiNativeToolMarkup struct {
	Prefix string
	Calls  []xaiNativeToolCall
}

// parseXAINativeToolMarkup extracts Factory/Droid tool markup from assistant
// text. It returns false when the text has no usable calls. Truncated markup
// that still contains a complete name/argument block is accepted so a stalled
// stream can be recovered.
func parseXAINativeToolMarkup(text string) (xaiNativeToolMarkup, bool) {
	begin := strings.Index(text, xaiNativeToolCallsBegin)
	if begin < 0 {
		return xaiNativeToolMarkup{}, false
	}

	prefix := text[:begin]
	cursor := begin + len(xaiNativeToolCallsBegin)
	var calls []xaiNativeToolCall

	for cursor < len(text) {
		if strings.HasPrefix(text[cursor:], xaiNativeToolCallsEnd) {
			break
		}
		rel := strings.Index(text[cursor:], xaiNativeToolCallBegin)
		if rel < 0 {
			break
		}
		cursor += rel + len(xaiNativeToolCallBegin)
		limit := nextXAINativeMarker(text, cursor, []string{xaiNativeToolCallEnd, xaiNativeToolCallsEnd, xaiNativeToolCallBegin})
		if limit < 0 {
			limit = len(text)
		}
		call, end, ok := parseXAINativeToolCall(text, cursor, limit)
		if !ok {
			cursor = limit
			if cursor < len(text) && strings.HasPrefix(text[cursor:], xaiNativeToolCallEnd) {
				cursor += len(xaiNativeToolCallEnd)
			}
			continue
		}
		calls = append(calls, call)
		cursor = end
		if cursor < len(text) && strings.HasPrefix(text[cursor:], xaiNativeToolCallEnd) {
			cursor += len(xaiNativeToolCallEnd)
		}
	}
	if len(calls) == 0 {
		return xaiNativeToolMarkup{}, false
	}
	return xaiNativeToolMarkup{Prefix: prefix, Calls: calls}, true
}

func parseXAINativeToolCall(text string, start, limit int) (xaiNativeToolCall, int, bool) {
	cursor := skipXAINativeWhitespace(text, start, limit)
	if cursor >= limit {
		return xaiNativeToolCall{}, cursor, false
	}

	nameEnd := nextXAINativeMarkerUntil(text, cursor, []string{xaiNativeToolSep, xaiNativeToolCallEnd, xaiNativeToolCallsEnd, xaiNativeToolCallBegin}, limit)
	if nameEnd < 0 {
		nameEnd = nextXAINativeNewline(text, cursor, limit)
	}
	if nameEnd < 0 {
		nameEnd = limit
	}
	name := strings.TrimSpace(text[cursor:nameEnd])
	if name == "" {
		return xaiNativeToolCall{}, cursor, false
	}

	cursor = nameEnd
	if cursor < limit && text[cursor] == '\r' {
		cursor++
	}
	if cursor < limit && text[cursor] == '\n' {
		cursor++
	}

	arguments := make(map[string]any)
	for cursor < limit && strings.HasPrefix(text[cursor:], xaiNativeToolSep) {
		cursor += len(xaiNativeToolSep)
		cursor = skipXAINativeHorizontalWhitespace(text, cursor, limit)
		if cursor < limit && text[cursor] == '\r' {
			cursor++
		}
		if cursor < limit && text[cursor] == '\n' {
			cursor++
		}

		keyEnd := nextXAINativeNewline(text, cursor, limit)
		if keyEnd < 0 {
			keyEnd = nextXAINativeMarkerUntil(text, cursor, []string{xaiNativeToolSep, xaiNativeToolCallEnd, xaiNativeToolCallsEnd}, limit)
		}
		if keyEnd < 0 {
			keyEnd = limit
		}
		key := strings.TrimSpace(text[cursor:keyEnd])
		cursor = keyEnd
		if cursor < limit && text[cursor] == '\r' {
			cursor++
		}
		if cursor < limit && text[cursor] == '\n' {
			cursor++
		}

		valueEnd := nextXAINativeMarkerUntil(text, cursor, []string{xaiNativeToolSep, xaiNativeToolCallEnd, xaiNativeToolCallsEnd, xaiNativeToolCallBegin}, limit)
		if valueEnd < 0 {
			valueEnd = limit
		}
		value := text[cursor:valueEnd]
		if strings.HasSuffix(value, "\r\n") {
			value = value[:len(value)-2]
		} else if strings.HasSuffix(value, "\n") || strings.HasSuffix(value, "\r") {
			value = value[:len(value)-1]
		}
		if colon := strings.IndexByte(key, ':'); colon >= 0 {
			inline := strings.TrimSpace(key[colon+1:])
			key = strings.TrimSpace(key[:colon])
			if inline != "" && strings.TrimSpace(value) == "" {
				value = inline
			}
		}
		if key != "" {
			arguments[key] = coerceXAINativeJSONValue(value)
		}
		cursor = valueEnd
	}

	return xaiNativeToolCall{Name: name, Arguments: arguments}, cursor, true
}

func coerceXAINativeJSONValue(raw string) any {
	trimmed := strings.TrimSpace(raw)
	switch trimmed {
	case "true":
		return true
	case "false":
		return false
	case "null":
		return nil
	}
	if intVal, err := strconv.ParseInt(trimmed, 10, 64); err == nil && strconv.FormatInt(intVal, 10) == trimmed {
		return intVal
	}
	if strings.Contains(trimmed, ".") {
		if floatVal, err := strconv.ParseFloat(trimmed, 64); err == nil {
			return floatVal
		}
	}
	if ((strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}")) ||
		(strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]"))) &&
		gjson.Valid(trimmed) {
		var decoded any
		dec := json.NewDecoder(strings.NewReader(trimmed))
		dec.UseNumber()
		if err := dec.Decode(&decoded); err == nil {
			return decoded
		}
	}
	return raw
}

// rewriteXAINativeToolMarkupChatJSON lifts markup out of choices[0].message.content
// into OpenAI tool_calls. It returns false when the body is unchanged, including
// when tool_calls are already present.
func rewriteXAINativeToolMarkupChatJSON(body []byte) ([]byte, bool) {
	if !gjson.ValidBytes(body) {
		return nil, false
	}
	choice := gjson.GetBytes(body, "choices.0")
	if !choice.Exists() {
		return nil, false
	}
	message := choice.Get("message")
	if !message.Exists() || xaiNativeHasToolCalls(message.Get("tool_calls")) {
		return nil, false
	}
	content := message.Get("content")
	if content.Type != gjson.String {
		return nil, false
	}
	parsed, ok := parseXAINativeToolMarkup(content.String())
	if !ok {
		return nil, false
	}

	out := body
	if parsed.Prefix == "" {
		out, _ = sjson.SetRawBytes(out, "choices.0.message.content", []byte("null"))
	} else {
		out, _ = sjson.SetBytes(out, "choices.0.message.content", parsed.Prefix)
	}
	out, _ = sjson.SetRawBytes(out, "choices.0.message.tool_calls", encodeXAINativeToolCalls(parsed.Calls, false))
	out, _ = sjson.SetBytes(out, "choices.0.finish_reason", "tool_calls")
	if choice.Get("native_finish_reason").Exists() {
		out, _ = sjson.SetBytes(out, "choices.0.native_finish_reason", "tool_calls")
	}
	return out, true
}

// rewriteXAINativeToolMarkupSSE reassembles an OpenAI chat-completion SSE body
// and replaces leaked markup with tool_calls deltas. Markup split across
// content deltas is handled because the body is assembled first. Returns false
// when unchanged, including when any chunk already carries tool_calls.
func rewriteXAINativeToolMarkupSSE(sse []byte) ([]byte, bool) {
	events := xaiNativeSSEDataPayloads(sse)
	if len(events) == 0 {
		return nil, false
	}

	var content strings.Builder
	alreadyHasToolCalls := false
	id := "chatcmpl-grok-native"
	model := "grok-4.6"
	created := time.Now().Unix()
	var usage rawJSON

	for i, payload := range events {
		if payload == "[DONE]" {
			continue
		}
		if !gjson.Valid(payload) {
			continue
		}
		obj := gjson.Parse(payload)
		if i == 0 || id == "chatcmpl-grok-native" {
			if value := obj.Get("id"); value.Exists() && value.Type == gjson.String {
				id = value.String()
			}
			if value := obj.Get("model"); value.Exists() && value.Type == gjson.String {
				model = value.String()
			}
			if value := obj.Get("created"); value.Exists() {
				created = value.Int()
			}
		}
		if value := obj.Get("usage"); value.Exists() {
			usage = rawJSON(value.Raw)
		}
		choice := obj.Get("choices.0")
		if !choice.Exists() {
			continue
		}
		if message := choice.Get("message"); message.Exists() {
			if xaiNativeHasToolCalls(message.Get("tool_calls")) {
				alreadyHasToolCalls = true
			}
			if text := message.Get("content"); text.Type == gjson.String {
				content.WriteString(text.String())
			}
		}
		if delta := choice.Get("delta"); delta.Exists() {
			if xaiNativeHasToolCalls(delta.Get("tool_calls")) {
				alreadyHasToolCalls = true
			}
			if text := delta.Get("content"); text.Type == gjson.String {
				content.WriteString(text.String())
			}
		}
	}
	if alreadyHasToolCalls {
		return nil, false
	}
	parsed, ok := parseXAINativeToolMarkup(content.String())
	if !ok {
		return nil, false
	}

	var out bytes.Buffer
	if parsed.Prefix != "" {
		out.Write(xaiNativeSSELine(xaiNativeChatChunk(id, model, created, map[string]any{
			"role":    "assistant",
			"content": parsed.Prefix,
		}, "", nil)))
	} else {
		out.Write(xaiNativeSSELine(xaiNativeChatChunk(id, model, created, map[string]any{
			"role": "assistant",
		}, "", nil)))
	}

	toolCalls := encodeXAINativeToolCalls(parsed.Calls, true)
	out.Write(xaiNativeSSELine(xaiNativeChatChunk(id, model, created, map[string]any{
		"tool_calls": json.RawMessage(toolCalls),
	}, "", nil)))
	out.Write(xaiNativeSSELine(xaiNativeChatChunk(id, model, created, map[string]any{}, "tool_calls", usage)))
	out.WriteString("data: [DONE]\n\n")
	return out.Bytes(), true
}

type rawJSON []byte

func (r rawJSON) MarshalJSON() ([]byte, error) {
	if len(r) == 0 {
		return []byte("null"), nil
	}
	return r, nil
}

// ApplyXAINativeToolMarkupChatJSON rewrites an OpenAI chat-completion body when
// the client format is OpenAI chat and assistant content contains native Grok
// tool markup. Other formats are returned unchanged.
func ApplyXAINativeToolMarkupChatJSON(format sdktranslator.Format, body []byte) []byte {
	if format != sdktranslator.FormatOpenAI {
		return body
	}
	rewritten, ok := rewriteXAINativeToolMarkupChatJSON(body)
	if !ok {
		return body
	}
	log.Debugf("xai: lifted native tool markup in chat completion content into tool_calls")
	return rewritten
}

// XAINativeToolMarkupChatStream buffers OpenAI chat-completion chunks once
// native markup appears (or a chunk boundary might be splitting a marker) and
// rewrites the assembled SSE at Flush. Chunks before the marker are forwarded
// immediately so ordinary text streams stay low-latency.
type XAINativeToolMarkupChatStream struct {
	enabled   bool
	passthru  bool
	buffering bool
	confirmed bool
	held      [][]byte
}

// NewXAINativeToolMarkupChatStream returns a rewriter that is active only for
// OpenAI chat-completion client streams.
func NewXAINativeToolMarkupChatStream(format sdktranslator.Format) *XAINativeToolMarkupChatStream {
	return &XAINativeToolMarkupChatStream{enabled: format == sdktranslator.FormatOpenAI}
}

// Ingest forwards a translated chat chunk or holds it until Flush when markup
// may be present. Callers must emit the returned payloads immediately.
func (s *XAINativeToolMarkupChatStream) Ingest(chunk []byte) [][]byte {
	if s == nil || !s.enabled || s.passthru {
		if len(chunk) == 0 {
			return nil
		}
		return [][]byte{chunk}
	}
	if xaiNativeChunkHasToolCalls(chunk) {
		s.passthru = true
		if len(s.held) == 0 {
			return [][]byte{chunk}
		}
		out := append(append([][]byte{}, s.held...), chunk)
		s.held = nil
		s.buffering = false
		s.confirmed = false
		return out
	}
	if s.buffering || xaiNativeChunkMayContainMarkup(chunk) {
		s.held = append(s.held, bytes.Clone(chunk))
		s.buffering = true
		if s.confirmed {
			return nil
		}
		switch xaiNativeHeldMarkupState(s.held) {
		case xaiNativeMarkupConfirmed:
			s.confirmed = true
			return nil
		case xaiNativeMarkupPossible:
			return nil
		default:
			out := s.held
			s.held = nil
			s.buffering = false
			return out
		}
	}
	return [][]byte{chunk}
}

// Flush emits any buffered chat chunks, rewriting assembled markup into
// tool_calls when parsing succeeds.
func (s *XAINativeToolMarkupChatStream) Flush() [][]byte {
	if s == nil || !s.enabled || len(s.held) == 0 {
		return nil
	}
	held := s.held
	s.held = nil
	s.buffering = false
	s.confirmed = false
	if s.passthru {
		return held
	}
	rewritten, ok := rewriteXAINativeToolMarkupChatChunks(held)
	if !ok {
		return held
	}
	log.Debugf("xai: lifted native tool markup in streamed chat completion content into tool_calls")
	return rewritten
}

func rewriteXAINativeToolMarkupChatChunks(chunks [][]byte) ([][]byte, bool) {
	var assembled bytes.Buffer
	for _, chunk := range chunks {
		payload := bytes.TrimSpace(chunk)
		if len(payload) == 0 {
			continue
		}
		if bytes.HasPrefix(payload, []byte("data:")) {
			assembled.Write(payload)
			if !bytes.HasSuffix(payload, []byte("\n\n")) {
				assembled.WriteString("\n\n")
			}
			continue
		}
		assembled.WriteString("data: ")
		assembled.Write(payload)
		assembled.WriteString("\n\n")
	}
	rewritten, ok := rewriteXAINativeToolMarkupSSE(assembled.Bytes())
	if !ok {
		return nil, false
	}
	return xaiNativeSSEToChatChunks(rewritten), true
}

func xaiNativeSSEToChatChunks(sse []byte) [][]byte {
	var chunks [][]byte
	for _, payload := range xaiNativeSSEDataPayloads(sse) {
		if payload == "[DONE]" {
			continue
		}
		chunks = append(chunks, []byte(payload))
	}
	return chunks
}

func encodeXAINativeToolCalls(calls []xaiNativeToolCall, indexed bool) []byte {
	out := []byte("[]")
	for i, call := range calls {
		item := []byte(`{"id":"","type":"function","function":{"name":"","arguments":""}}`)
		item, _ = sjson.SetBytes(item, "id", xaiNativeToolCallID(call.Name, i))
		item, _ = sjson.SetBytes(item, "function.name", call.Name)
		item, _ = sjson.SetBytes(item, "function.arguments", xaiNativeArgumentsJSON(call.Arguments))
		if indexed {
			item, _ = sjson.SetBytes(item, "index", i)
		}
		out, _ = sjson.SetRawBytes(out, "-1", item)
	}
	return out
}

func xaiNativeToolCallID(name string, index int) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
		}
	}
	return "call_grok_native_" + strconv.Itoa(index) + "_" + b.String()
}

func xaiNativeArgumentsJSON(arguments map[string]any) string {
	if len(arguments) == 0 {
		return "{}"
	}
	data, err := json.Marshal(arguments)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func xaiNativeChatChunk(id, model string, created int64, delta map[string]any, finishReason string, usage rawJSON) []byte {
	chunk := []byte(`{"id":"","object":"chat.completion.chunk","created":0,"model":"","choices":[{"index":0,"delta":{},"finish_reason":null}]}`)
	chunk, _ = sjson.SetBytes(chunk, "id", id)
	chunk, _ = sjson.SetBytes(chunk, "created", created)
	chunk, _ = sjson.SetBytes(chunk, "model", model)
	if len(delta) > 0 {
		raw, err := json.Marshal(delta)
		if err == nil {
			chunk, _ = sjson.SetRawBytes(chunk, "choices.0.delta", raw)
		}
	}
	if finishReason != "" {
		chunk, _ = sjson.SetBytes(chunk, "choices.0.finish_reason", finishReason)
	}
	if len(usage) > 0 {
		chunk, _ = sjson.SetRawBytes(chunk, "usage", usage)
	}
	return chunk
}

func xaiNativeSSELine(object []byte) []byte {
	var b bytes.Buffer
	b.WriteString("data: ")
	b.Write(object)
	b.WriteString("\n\n")
	return b.Bytes()
}

func xaiNativeSSEDataPayloads(sse []byte) []string {
	var payloads []string
	for _, rawLine := range bytes.Split(sse, []byte("\n")) {
		line := bytes.TrimSuffix(rawLine, []byte("\r"))
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(line[len("data:"):])
		payloads = append(payloads, string(payload))
	}
	return payloads
}

func xaiNativeChunkJSON(chunk []byte) gjson.Result {
	payload := bytes.TrimSpace(chunk)
	if bytes.HasPrefix(payload, []byte("data:")) {
		payload = bytes.TrimSpace(payload[len("data:"):])
	}
	if bytes.Equal(payload, []byte("[DONE]")) || !gjson.ValidBytes(payload) {
		return gjson.Result{}
	}
	return gjson.ParseBytes(payload)
}

func xaiNativeChunkHasToolCalls(chunk []byte) bool {
	obj := xaiNativeChunkJSON(chunk)
	if !obj.Exists() {
		return false
	}
	choice := obj.Get("choices.0")
	return xaiNativeHasToolCalls(choice.Get("message.tool_calls")) || xaiNativeHasToolCalls(choice.Get("delta.tool_calls"))
}

func xaiNativeHasToolCalls(value gjson.Result) bool {
	if !value.Exists() || value.Type == gjson.Null {
		return false
	}
	if value.IsArray() {
		return len(value.Array()) > 0
	}
	return true
}

func xaiNativeChunkContent(chunk []byte) string {
	obj := xaiNativeChunkJSON(chunk)
	if !obj.Exists() {
		return ""
	}
	choice := obj.Get("choices.0")
	var b strings.Builder
	if text := choice.Get("message.content"); text.Type == gjson.String {
		b.WriteString(text.String())
	}
	if text := choice.Get("delta.content"); text.Type == gjson.String {
		b.WriteString(text.String())
	}
	return b.String()
}

type xaiNativeMarkupState int

const (
	xaiNativeMarkupNone xaiNativeMarkupState = iota
	xaiNativeMarkupPossible
	xaiNativeMarkupConfirmed
)

func xaiNativeChunkMayContainMarkup(chunk []byte) bool {
	return xaiNativeContentMarkupState(xaiNativeChunkContent(chunk)) != xaiNativeMarkupNone
}

func xaiNativeHeldMarkupState(chunks [][]byte) xaiNativeMarkupState {
	var content strings.Builder
	for _, chunk := range chunks {
		content.WriteString(xaiNativeChunkContent(chunk))
	}
	return xaiNativeContentMarkupState(content.String())
}

func xaiNativeContentMarkupState(content string) xaiNativeMarkupState {
	if content == "" {
		return xaiNativeMarkupNone
	}
	if strings.Contains(content, xaiNativeToolCallsBegin) || strings.Contains(content, xaiNativeToolCallBegin) {
		return xaiNativeMarkupConfirmed
	}
	if xaiHasMarkerPrefixSuffix(content, xaiNativeToolCallsBegin) || xaiHasMarkerPrefixSuffix(content, xaiNativeToolCallBegin) {
		return xaiNativeMarkupPossible
	}
	return xaiNativeMarkupNone
}

func xaiHasMarkerPrefixSuffix(text, marker string) bool {
	max := len(marker) - 1
	if max > len(text) {
		max = len(text)
	}
	for n := max; n > 0; n-- {
		if strings.HasPrefix(marker, text[len(text)-n:]) {
			return true
		}
	}
	return false
}

func nextXAINativeMarker(text string, start int, markers []string) int {
	return nextXAINativeMarkerUntil(text, start, markers, len(text))
}

func nextXAINativeMarkerUntil(text string, start int, markers []string, limit int) int {
	if start > limit {
		return -1
	}
	best := -1
	window := text[start:limit]
	for _, marker := range markers {
		if idx := strings.Index(window, marker); idx >= 0 {
			abs := start + idx
			if best < 0 || abs < best {
				best = abs
			}
		}
	}
	return best
}

func nextXAINativeNewline(text string, start, limit int) int {
	for i := start; i < limit; i++ {
		if text[i] == '\n' || text[i] == '\r' {
			return i
		}
	}
	return -1
}

func skipXAINativeWhitespace(text string, start, limit int) int {
	i := start
	for i < limit {
		r, size := utf8.DecodeRuneInString(text[i:limit])
		if r == utf8.RuneError && size == 1 {
			break
		}
		if !unicode.IsSpace(r) {
			break
		}
		i += size
	}
	return i
}

func skipXAINativeHorizontalWhitespace(text string, start, limit int) int {
	i := start
	for i < limit && (text[i] == ' ' || text[i] == '\t') {
		i++
	}
	return i
}
