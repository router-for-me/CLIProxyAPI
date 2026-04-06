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

type claudeCloakReplacementStage struct {
	find    []byte
	replace []byte
	pending []byte
}

func newClaudeCloakReplacementStage(replacement config.TextReplacement) *claudeCloakReplacementStage {
	if replacement.Find == "" {
		return nil
	}
	return &claudeCloakReplacementStage{
		find:    []byte(replacement.Find),
		replace: []byte(replacement.Replace),
	}
}

func (s *claudeCloakReplacementStage) process(fragment []byte, flush bool) []byte {
	if s == nil {
		return fragment
	}
	combined := make([]byte, 0, len(s.pending)+len(fragment))
	combined = append(combined, s.pending...)
	combined = append(combined, fragment...)
	if len(combined) == 0 {
		return nil
	}
	if flush {
		s.pending = s.pending[:0]
		return bytes.ReplaceAll(combined, s.find, s.replace)
	}

	out := make([]byte, 0, len(combined))
	i := 0
	for i < len(combined) {
		if bytes.HasPrefix(combined[i:], s.find) {
			out = append(out, s.replace...)
			i += len(s.find)
			continue
		}
		if len(combined)-i < len(s.find) && bytes.HasPrefix(s.find, combined[i:]) {
			break
		}
		out = append(out, combined[i])
		i++
	}

	s.pending = append(s.pending[:0], combined[i:]...)
	return out
}

type claudeCloakResponseTextStream struct {
	stages []*claudeCloakReplacementStage
}

func newClaudeCloakResponseTextStream(replacements []config.TextReplacement) *claudeCloakResponseTextStream {
	stages := make([]*claudeCloakReplacementStage, 0, len(replacements))
	for _, replacement := range replacements {
		stage := newClaudeCloakReplacementStage(replacement)
		if stage != nil {
			stages = append(stages, stage)
		}
	}
	return &claudeCloakResponseTextStream{stages: stages}
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
	out := fragment
	for _, stage := range s.stages {
		out = stage.process(out, flush)
	}
	return out
}

type claudeCloakResponseStreamRewriter struct {
	replacements []config.TextReplacement
	blockKinds   map[int]string
	streams      map[claudeCloakResponseStreamKey]*claudeCloakResponseTextStream
	frame        [][]byte
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
	cloned := append([]byte(nil), line...)
	if len(bytes.TrimSpace(cloned)) == 0 {
		if len(r.frame) == 0 {
			return [][]byte{cloned}
		}
		out := r.rewriteFrame(r.frame)
		r.frame = nil
		if len(out) == 0 {
			return nil
		}
		return append(out, []byte{})
	}
	r.frame = append(r.frame, cloned)
	return nil
}

func (r *claudeCloakResponseStreamRewriter) FlushPending() [][]byte {
	if r == nil || len(r.frame) == 0 {
		return nil
	}
	out := r.rewriteFrame(r.frame)
	r.frame = nil
	return out
}

func (r *claudeCloakResponseStreamRewriter) rewriteFrame(frame [][]byte) [][]byte {
	if len(frame) == 0 {
		return nil
	}
	dataIdx, payload, ok := claudeCloakResponseFramePayload(frame)
	if !ok || !gjson.ValidBytes(payload) {
		return claudeCloakCloneLines(frame)
	}

	root := gjson.ParseBytes(payload)
	switch root.Get("type").String() {
	case "content_block_start":
		if kind := claudeCloakResponseBlockKind(root.Get("content_block.type").String()); kind != "" {
			r.blockKinds[int(root.Get("index").Int())] = kind
		}
		return claudeCloakCloneLines(frame)
	case "content_block_delta":
		kind, fieldPath, _ := claudeCloakResponseDeltaSpec(root.Get("delta.type").String())
		if kind == "" {
			return claudeCloakCloneLines(frame)
		}
		index := int(root.Get("index").Int())
		updatedFragment := r.stream(kind, index).Consume([]byte(root.Get(fieldPath).String()))
		if len(updatedFragment) == 0 {
			return nil
		}
		updatedPayload, err := sjson.SetBytes(payload, fieldPath, string(updatedFragment))
		if err != nil {
			return claudeCloakCloneLines(frame)
		}
		r.blockKinds[index] = kind
		return claudeCloakResponseReplaceFramePayload(frame, dataIdx, updatedPayload)
	case "content_block_stop":
		index := int(root.Get("index").Int())
		kind := r.blockKinds[index]
		delete(r.blockKinds, index)
		flushed := r.flush(kind, index)
		stopFrame := claudeCloakCloneLines(frame)
		if kind == "" || len(flushed) == 0 {
			return stopFrame
		}
		deltaPayload := claudeCloakResponseDeltaPayload(index, kind, flushed)
		if len(deltaPayload) == 0 {
			return stopFrame
		}
		deltaFrame := claudeCloakResponseSyntheticFrame(frame, deltaPayload, "content_block_delta")
		out := make([][]byte, 0, len(deltaFrame)+1+len(stopFrame))
		out = append(out, deltaFrame...)
		out = append(out, []byte{})
		out = append(out, stopFrame...)
		return out
	default:
		return claudeCloakCloneLines(frame)
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

func claudeCloakResponseDeltaPayload(index int, kind string, fragment []byte) []byte {
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
	return payload
}

func claudeCloakResponseSyntheticFrame(baseFrame [][]byte, payload []byte, eventName string) [][]byte {
	if len(baseFrame) == 0 {
		return nil
	}
	out := make([][]byte, 0, 2)
	if claudeCloakFrameHasEventLine(baseFrame) {
		out = append(out, []byte("event: "+eventName))
	}
	out = append(out, claudeCloakResponseStreamLine(claudeCloakResponseReferenceDataLine(baseFrame), payload))
	return out
}

func claudeCloakResponseReplaceFramePayload(frame [][]byte, dataIdx int, payload []byte) [][]byte {
	out := claudeCloakCloneLines(frame)
	out[dataIdx] = claudeCloakResponseStreamLine(frame[dataIdx], payload)
	return out
}

func claudeCloakResponseFramePayload(frame [][]byte) (int, []byte, bool) {
	for i, line := range frame {
		payload := helps.JSONPayload(line)
		if len(payload) == 0 {
			continue
		}
		return i, payload, true
	}
	return -1, nil, false
}

func claudeCloakFrameHasEventLine(frame [][]byte) bool {
	for _, line := range frame {
		if bytes.HasPrefix(bytes.TrimSpace(line), []byte("event:")) {
			return true
		}
	}
	return false
}

func claudeCloakResponseReferenceDataLine(frame [][]byte) []byte {
	for _, line := range frame {
		trimmed := bytes.TrimSpace(line)
		if bytes.HasPrefix(trimmed, []byte("data:")) {
			return line
		}
	}
	return []byte("data:")
}

func claudeCloakCloneLines(lines [][]byte) [][]byte {
	out := make([][]byte, len(lines))
	for i, line := range lines {
		out[i] = append([]byte(nil), line...)
	}
	return out
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
