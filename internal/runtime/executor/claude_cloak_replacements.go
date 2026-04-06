package executor

import (
	"bytes"
	"context"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func shouldApplyClaudeCloak(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) bool {
	cloakCfg := resolveClaudeKeyCloakConfig(cfg, auth)
	attrMode, _, _, _ := getCloakConfigFromAuth(auth)

	cloakMode := attrMode
	if cloakCfg != nil {
		if mode := strings.TrimSpace(cloakCfg.Mode); mode != "" {
			cloakMode = mode
		}
	}

	return helps.ShouldCloak(cloakMode, getClientUserAgent(ctx))
}

func applyTextReplacements(body []byte, replacements []config.TextReplacement) []byte {
	if len(body) == 0 || len(replacements) == 0 {
		return body
	}
	out := body
	for _, replacement := range replacements {
		if replacement.Find == "" {
			continue
		}
		out = bytes.ReplaceAll(out, []byte(replacement.Find), []byte(replacement.Replace))
	}
	return out
}

func applyClaudeCloakRequestReplacements(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, body []byte) []byte {
	if !shouldApplyClaudeCloak(ctx, cfg, auth) {
		return body
	}
	cloakCfg := resolveClaudeKeyCloakConfig(cfg, auth)
	if cloakCfg == nil {
		return body
	}
	return applyTextReplacements(body, cloakCfg.RequestReplacements)
}

func applyClaudeCloakResponseReplacements(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, body []byte) []byte {
	if !shouldApplyClaudeCloak(ctx, cfg, auth) {
		return body
	}
	cloakCfg := resolveClaudeKeyCloakConfig(cfg, auth)
	if cloakCfg == nil {
		return body
	}
	return applyTextReplacements(body, cloakCfg.ResponseReplacements)
}

type claudeCloakResponseStreamKey struct {
	kind  string
	index int
}

type claudeCloakResponseTextStream struct {
	replacements []config.TextReplacement
	pending      []byte
}

func newClaudeCloakResponseTextStream(replacements []config.TextReplacement) *claudeCloakResponseTextStream {
	return &claudeCloakResponseTextStream{replacements: replacements}
}

func (s *claudeCloakResponseTextStream) Consume(fragment []byte) []byte {
	return s.process(fragment, false)
}

func (s *claudeCloakResponseTextStream) Flush() []byte {
	return s.process(nil, true)
}

func (s *claudeCloakResponseTextStream) process(fragment []byte, flush bool) []byte {
	if s == nil {
		return fragment
	}
	combined := make([]byte, 0, len(s.pending)+len(fragment))
	combined = append(combined, s.pending...)
	combined = append(combined, fragment...)
	if len(combined) == 0 {
		return nil
	}

	out := make([]byte, 0, len(combined))
	i := 0
	for i < len(combined) {
		if !flush && couldMatchClaudeCloakReplacementLater(combined[i:], s.replacements) {
			break
		}
		if replacement, matchLen, ok := matchClaudeCloakReplacementAt(combined[i:], s.replacements); ok {
			out = append(out, replacement.Replace...)
			i += matchLen
			continue
		}
		out = append(out, combined[i])
		i++
	}

	s.pending = append(s.pending[:0], combined[i:]...)
	if flush {
		s.pending = s.pending[:0]
	}
	return out
}

func matchClaudeCloakReplacementAt(data []byte, replacements []config.TextReplacement) (config.TextReplacement, int, bool) {
	for _, replacement := range replacements {
		find := []byte(replacement.Find)
		if len(find) == 0 || len(data) < len(find) {
			continue
		}
		if bytes.HasPrefix(data, find) {
			return replacement, len(find), true
		}
	}
	return config.TextReplacement{}, 0, false
}

func couldMatchClaudeCloakReplacementLater(data []byte, replacements []config.TextReplacement) bool {
	if len(data) == 0 {
		return false
	}
	for _, replacement := range replacements {
		find := []byte(replacement.Find)
		if len(find) <= len(data) || len(find) == 0 {
			continue
		}
		if bytes.HasPrefix(find, data) {
			return true
		}
	}
	return false
}

type claudeCloakResponseStreamRewriter struct {
	replacements  []config.TextReplacement
	blockKinds    map[int]string
	streams       map[claudeCloakResponseStreamKey]*claudeCloakResponseTextStream
	dropNextBlank bool
}

func newClaudeCloakResponseStreamRewriter(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) *claudeCloakResponseStreamRewriter {
	if !shouldApplyClaudeCloak(ctx, cfg, auth) {
		return nil
	}
	cloakCfg := resolveClaudeKeyCloakConfig(cfg, auth)
	if cloakCfg == nil || len(cloakCfg.ResponseReplacements) == 0 {
		return nil
	}
	return &claudeCloakResponseStreamRewriter{
		replacements: cloakCfg.ResponseReplacements,
		blockKinds:   make(map[int]string),
		streams:      make(map[claudeCloakResponseStreamKey]*claudeCloakResponseTextStream),
	}
}

func (r *claudeCloakResponseStreamRewriter) RewriteLine(line []byte) [][]byte {
	if r == nil {
		return [][]byte{line}
	}
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		if r.dropNextBlank {
			r.dropNextBlank = false
			return nil
		}
		return [][]byte{line}
	}

	payload := helps.JSONPayload(line)
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		r.dropNextBlank = false
		return [][]byte{line}
	}

	root := gjson.ParseBytes(payload)
	switch root.Get("type").String() {
	case "content_block_start":
		if kind := claudeCloakResponseBlockKind(root.Get("content_block.type").String()); kind != "" {
			r.blockKinds[int(root.Get("index").Int())] = kind
		}
		r.dropNextBlank = false
		return [][]byte{line}
	case "content_block_delta":
		kind, fieldPath, deltaType := claudeCloakResponseDeltaSpec(root.Get("delta.type").String())
		if kind == "" {
			r.dropNextBlank = false
			return [][]byte{line}
		}
		index := int(root.Get("index").Int())
		updatedFragment := r.stream(kind, index).Consume([]byte(root.Get(fieldPath).String()))
		if len(updatedFragment) == 0 {
			r.dropNextBlank = true
			return nil
		}
		updatedPayload, err := sjson.SetBytes(payload, fieldPath, string(updatedFragment))
		if err != nil {
			r.dropNextBlank = false
			return [][]byte{line}
		}
		r.blockKinds[index] = kind
		r.dropNextBlank = false
		_ = deltaType
		return [][]byte{claudeCloakResponseStreamLine(line, updatedPayload)}
	case "content_block_stop":
		index := int(root.Get("index").Int())
		kind := r.blockKinds[index]
		delete(r.blockKinds, index)
		flushed := r.flush(kind, index)
		r.dropNextBlank = false
		if kind == "" || len(flushed) == 0 {
			return [][]byte{line}
		}
		return [][]byte{claudeCloakResponseDeltaLine(line, index, kind, flushed), []byte{}, line}
	default:
		r.dropNextBlank = false
		return [][]byte{line}
	}
}

func (r *claudeCloakResponseStreamRewriter) stream(kind string, index int) *claudeCloakResponseTextStream {
	key := claudeCloakResponseStreamKey{kind: kind, index: index}
	if stream, ok := r.streams[key]; ok {
		return stream
	}
	stream := newClaudeCloakResponseTextStream(r.replacements)
	r.streams[key] = stream
	return stream
}

func (r *claudeCloakResponseStreamRewriter) flush(kind string, index int) []byte {
	if kind == "" {
		return nil
	}
	key := claudeCloakResponseStreamKey{kind: kind, index: index}
	stream, ok := r.streams[key]
	if !ok {
		return nil
	}
	delete(r.streams, key)
	return stream.Flush()
}

func claudeCloakResponseBlockKind(blockType string) string {
	switch blockType {
	case "text":
		return "text"
	case "thinking":
		return "thinking"
	case "tool_use":
		return "input_json"
	default:
		return ""
	}
}

func claudeCloakResponseDeltaSpec(deltaType string) (kind string, fieldPath string, emittedType string) {
	switch deltaType {
	case "text_delta":
		return "text", "delta.text", "text_delta"
	case "thinking_delta":
		return "thinking", "delta.thinking", "thinking_delta"
	case "input_json_delta":
		return "input_json", "delta.partial_json", "input_json_delta"
	default:
		return "", "", ""
	}
}

func claudeCloakResponseDeltaLine(line []byte, index int, kind string, fragment []byte) []byte {
	var payload []byte
	switch kind {
	case "text":
		payload = []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":""}}`)
		payload, _ = sjson.SetBytes(payload, "delta.text", string(fragment))
	case "thinking":
		payload = []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":""}}`)
		payload, _ = sjson.SetBytes(payload, "delta.thinking", string(fragment))
	case "input_json":
		payload = []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":""}}`)
		payload, _ = sjson.SetBytes(payload, "delta.partial_json", string(fragment))
	default:
		return nil
	}
	payload, _ = sjson.SetBytes(payload, "index", index)
	return claudeCloakResponseStreamLine(line, payload)
}

func claudeCloakResponseStreamLine(line []byte, payload []byte) []byte {
	trimmed := bytes.TrimSpace(line)
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		out := make([]byte, 0, len(payload)+6)
		out = append(out, []byte("data: ")...)
		out = append(out, payload...)
		return out
	}
	return append([]byte(nil), payload...)
}
