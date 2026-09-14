package toolargs

import (
	"bytes"
	"strconv"
	"sync"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// HasFunctionTools is the cheap early-out for the hot path: coercion can only ever apply when
// the client request declared tools, so every other chunk is returned untouched at the cost of
// one gjson probe.
func HasFunctionTools(originalRequest []byte) bool {
	return len(originalRequest) > 0 && gjson.GetBytes(originalRequest, "tools").IsArray()
}

// coerceOne resolves the tool's schema from the original request and rewrites arguments.
// It returns the (possibly unchanged) arguments and the coerced paths.
func coerceOne(originalRequest []byte, toolName, arguments string) (string, []string) {
	if toolName == "" || arguments == "" {
		return arguments, nil
	}
	schema, ok := FindToolParams(originalRequest, toolName)
	if !ok {
		return arguments, nil
	}
	return CoerceIntegralFloats(arguments, schema)
}

// isCompleteObject reports whether a streamed arguments fragment is, by itself, a complete JSON
// object. Only such whole-value chunks are safe to normalize statelessly; a partial fragment
// (as emitted by delta-streaming upstreams) is passed through untouched because the bytes
// already flushed to the client cannot be retracted.
func isCompleteObject(arguments string) bool {
	trimmed := bytes.TrimSpace([]byte(arguments))
	return len(trimmed) > 0 && trimmed[0] == '{' && gjson.ValidBytes(trimmed)
}

// CoerceChatCompletionsBody normalizes a non-streaming Chat Completions response
// (choices[].message.tool_calls[].function.arguments).
func CoerceChatCompletionsBody(body, originalRequest []byte, provider, model string) []byte {
	if !HasFunctionTools(originalRequest) {
		return body
	}
	out := body
	gjson.GetBytes(body, "choices").ForEach(func(ci, choice gjson.Result) bool {
		choice.Get("message.tool_calls").ForEach(func(ti, call gjson.Result) bool {
			name := call.Get("function.name").String()
			args := call.Get("function.arguments")
			if args.Type != gjson.String {
				return true
			}
			fixed, paths := coerceOne(originalRequest, name, args.Str)
			if len(paths) == 0 {
				return true
			}
			path := "choices." + ci.String() + ".message.tool_calls." + ti.String() + ".function.arguments"
			if updated, err := sjson.SetBytes(out, path, fixed); err == nil {
				out = updated
				recordCoercion(provider, model, name, paths)
			}
			return true
		})
		return true
	})
	return out
}

// CoerceChatCompletionsChunk normalizes a streamed Chat Completions chunk. It applies the
// whole-value rule: a delta's function.arguments is rewritten only when that chunk names the
// function AND carries a complete JSON object (Claude/Gemini/Antigravity-style upstreams emit
// the assembled call in one delta). Fragmented argument deltas are left untouched.
func CoerceChatCompletionsChunk(chunk, originalRequest []byte, provider, model string) []byte {
	if !HasFunctionTools(originalRequest) {
		return chunk
	}
	prefix, payload, suffix, ok := splitSSEPayload(chunk)
	if !ok {
		return chunk
	}
	out := payload
	changed := false
	gjson.GetBytes(payload, "choices").ForEach(func(ci, choice gjson.Result) bool {
		choice.Get("delta.tool_calls").ForEach(func(ti, call gjson.Result) bool {
			name := call.Get("function.name").String()
			args := call.Get("function.arguments")
			if name == "" || args.Type != gjson.String || !isCompleteObject(args.Str) {
				return true
			}
			fixed, paths := coerceOne(originalRequest, name, args.Str)
			if len(paths) == 0 {
				return true
			}
			path := "choices." + ci.String() + ".delta.tool_calls." + ti.String() + ".function.arguments"
			if updated, err := sjson.SetBytes(out, path, fixed); err == nil {
				out = updated
				changed = true
				recordCoercion(provider, model, name, paths)
			}
			return true
		})
		return true
	})
	if !changed {
		return chunk
	}
	return joinSSEPayload(prefix, out, suffix)
}

// CoerceResponsesBody normalizes a non-streaming Responses API body
// (output[].type=="function_call" → arguments). Custom (freeform) tool calls are not JSON
// objects of typed parameters and are skipped.
func CoerceResponsesBody(body, originalRequest []byte, provider, model string) []byte {
	if !HasFunctionTools(originalRequest) {
		return body
	}
	out := body
	gjson.GetBytes(body, "output").ForEach(func(oi, item gjson.Result) bool {
		if item.Get("type").String() != "function_call" {
			return true
		}
		name := item.Get("name").String()
		args := item.Get("arguments")
		if args.Type != gjson.String {
			return true
		}
		fixed, paths := coerceOne(originalRequest, name, args.Str)
		if len(paths) == 0 {
			return true
		}
		if updated, err := sjson.SetBytes(out, "output."+oi.String()+".arguments", fixed); err == nil {
			out = updated
			recordCoercion(provider, model, name, paths)
		}
		return true
	})
	return out
}

// responsesStreamNames remembers, per stream, the function name announced by
// response.output_item.added so that the later response.function_call_arguments.done event
// (which carries only item_id) can be resolved to its schema. The stream is identified by the
// translator's per-stream param pointer. Entries are dropped when the item completes and when
// the response reaches a terminal event; a hard cap bounds any leak from aborted streams by
// simply not tracking new streams beyond it (which only disables the args.done rewrite — the
// output_item.done rewrite is stateless and unaffected).
var (
	responsesStreamNames       sync.Map // map[*any]*streamNames
	responsesStreamCount       int64
	responsesStreamMu          sync.Mutex
	maxTrackedResponsesStreams = int64(4096)
)

type streamNames struct {
	mu    sync.Mutex
	names map[string]string // item_id → function name
}

func streamNamesFor(key *any, create bool) *streamNames {
	if key == nil {
		return nil
	}
	if v, ok := responsesStreamNames.Load(key); ok {
		return v.(*streamNames)
	}
	if !create {
		return nil
	}
	responsesStreamMu.Lock()
	defer responsesStreamMu.Unlock()
	if v, ok := responsesStreamNames.Load(key); ok {
		return v.(*streamNames)
	}
	if responsesStreamCount >= maxTrackedResponsesStreams {
		return nil
	}
	sn := &streamNames{names: map[string]string{}}
	responsesStreamNames.Store(key, sn)
	responsesStreamCount++
	return sn
}

func forgetStream(key *any) {
	if key == nil {
		return
	}
	if _, loaded := responsesStreamNames.LoadAndDelete(key); loaded {
		responsesStreamMu.Lock()
		responsesStreamCount--
		responsesStreamMu.Unlock()
	}
}

// CoerceResponsesEvent normalizes one streamed Responses API event. The full arguments string
// is available on response.function_call_arguments.done (arguments) and
// response.output_item.done (item.arguments); both are rewritten so they agree. Deltas are
// never touched. Event order, sequence numbers, item ids, and call ids are unchanged.
// streamKey is the per-stream param pointer (may be nil: then only the stateless
// output_item.done rewrite applies).
func CoerceResponsesEvent(chunk, originalRequest []byte, streamKey *any, provider, model string) []byte {
	if !HasFunctionTools(originalRequest) {
		return chunk
	}
	prefix, payload, suffix, ok := splitSSEPayload(chunk)
	if !ok {
		return chunk
	}
	eventType := gjson.GetBytes(payload, "type").String()
	switch eventType {
	case "response.output_item.added":
		item := gjson.GetBytes(payload, "item")
		if item.Get("type").String() == "function_call" {
			if id, name := item.Get("id").String(), item.Get("name").String(); id != "" && name != "" {
				if sn := streamNamesFor(streamKey, true); sn != nil {
					sn.mu.Lock()
					sn.names[id] = name
					sn.mu.Unlock()
				}
			}
		}
		return chunk
	case "response.function_call_arguments.done":
		sn := streamNamesFor(streamKey, false)
		if sn == nil {
			return chunk
		}
		itemID := gjson.GetBytes(payload, "item_id").String()
		sn.mu.Lock()
		name := sn.names[itemID]
		sn.mu.Unlock()
		args := gjson.GetBytes(payload, "arguments")
		if name == "" || args.Type != gjson.String {
			return chunk
		}
		fixed, paths := coerceOne(originalRequest, name, args.Str)
		if len(paths) == 0 {
			return chunk
		}
		updated, err := sjson.SetBytes(payload, "arguments", fixed)
		if err != nil {
			return chunk
		}
		recordCoercion(provider, model, name, paths)
		return joinSSEPayload(prefix, updated, suffix)
	case "response.output_item.done":
		item := gjson.GetBytes(payload, "item")
		if item.Get("type").String() != "function_call" {
			return chunk
		}
		if sn := streamNamesFor(streamKey, false); sn != nil {
			if id := item.Get("id").String(); id != "" {
				sn.mu.Lock()
				delete(sn.names, id)
				sn.mu.Unlock()
			}
		}
		name := item.Get("name").String()
		args := item.Get("arguments")
		if args.Type != gjson.String {
			return chunk
		}
		fixed, paths := coerceOne(originalRequest, name, args.Str)
		if len(paths) == 0 {
			return chunk
		}
		updated, err := sjson.SetBytes(payload, "item.arguments", fixed)
		if err != nil {
			return chunk
		}
		recordCoercion(provider, model, name, paths)
		return joinSSEPayload(prefix, updated, suffix)
	case "response.completed", "response.failed", "response.incomplete", "error":
		forgetStream(streamKey)
		return chunk
	}
	return chunk
}

// splitSSEPayload isolates the JSON payload of a chunk so only it is rewritten and the framing
// (an optional `event:` line, the `data:` prefix, trailing newlines) is reproduced verbatim.
// Chunks at this boundary arrive either as a bare JSON object (Chat Completions) or as
// `event: name\ndata: {...}` / `data: {...}` (Responses API). Anything else is left alone.
func splitSSEPayload(chunk []byte) (prefix, payload, suffix []byte, ok bool) {
	trimmedStart := bytes.TrimLeft(chunk, " \t\r\n")
	if len(trimmedStart) > 0 && trimmedStart[0] == '{' {
		lead := len(chunk) - len(trimmedStart)
		end := len(chunk)
		for end > lead && (chunk[end-1] == '\n' || chunk[end-1] == '\r' || chunk[end-1] == ' ' || chunk[end-1] == '\t') {
			end--
		}
		return chunk[:lead], chunk[lead:end], chunk[end:], true
	}
	idx := bytes.Index(chunk, []byte("data:"))
	if idx < 0 {
		return nil, nil, nil, false
	}
	start := idx + len("data:")
	for start < len(chunk) && (chunk[start] == ' ' || chunk[start] == '\t') {
		start++
	}
	end := start
	for end < len(chunk) && chunk[end] != '\n' && chunk[end] != '\r' {
		end++
	}
	if end <= start || chunk[start] != '{' {
		return nil, nil, nil, false
	}
	return chunk[:start], chunk[start:end], chunk[end:], true
}

func joinSSEPayload(prefix, payload, suffix []byte) []byte {
	out := make([]byte, 0, len(prefix)+len(payload)+len(suffix))
	out = append(out, prefix...)
	out = append(out, payload...)
	out = append(out, suffix...)
	return out
}

// Observability: an in-process counter keyed by provider/model plus a debug log that names
// only the tool and the parameter paths (never argument values). The repository has no
// metrics backend, so the counter is exposed via Stats for tests/diagnostics.
var (
	statsMu sync.Mutex
	stats   = map[string]uint64{}
)

func recordCoercion(provider, model, tool string, paths []string) {
	key := provider + "/" + model
	statsMu.Lock()
	stats[key] += uint64(len(paths))
	statsMu.Unlock()
	log.WithFields(log.Fields{
		"provider": provider,
		"model":    model,
		"tool":     tool,
		"params":   paths,
		"count":    strconv.Itoa(len(paths)),
	}).Debug("tool-call arguments: coerced integral floats to integers for integer-typed parameters")
}

// Stats returns a snapshot of coercion counts keyed by "provider/model".
func Stats() map[string]uint64 {
	statsMu.Lock()
	defer statsMu.Unlock()
	out := make(map[string]uint64, len(stats))
	for k, v := range stats {
		out[k] = v
	}
	return out
}
