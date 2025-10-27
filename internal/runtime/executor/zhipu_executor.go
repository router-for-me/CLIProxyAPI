package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	// sdktranslator removed from this file
	log "github.com/sirupsen/logrus"
)

// ZhipuExecutor is a stateless executor for Zhipu GLM using an
// OpenAI-compatible chat completions interface.
type ZhipuExecutor struct {
	cfg *config.Config
}

// NewZhipuExecutor creates a new ZhipuExecutor instance.
func NewZhipuExecutor(cfg *config.Config) *ZhipuExecutor { return &ZhipuExecutor{cfg: cfg} }

// Identifier implements cliproxyauth.ProviderExecutor.
func (e *ZhipuExecutor) Identifier() string { return "zhipu" }

func (e *ZhipuExecutor) PrepareRequest(_ *http.Request, _ *cliproxyauth.Auth) error { return nil }

func (e *ZhipuExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if fallbackAuth := e.resolveClaudeZhipuAuth(auth); fallbackAuth != nil {
		claude := NewClaudeExecutor(e.cfg)
		return claude.Execute(ctx, fallbackAuth, req, opts)
	}
	compat := NewOpenAICompatExecutor("zhipu", e.cfg)
	resp, err := compat.Execute(ctx, auth, req, opts)
	if err != nil {
		return resp, err
	}
	resp.Payload = stripEmojiFromOpenAIResponseJSON(resp.Payload)
	return resp, nil
}

func (e *ZhipuExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
	// Direct path via compat (Python bridge removed)
	if fallbackAuth := e.resolveClaudeZhipuAuth(auth); fallbackAuth != nil {
		claude := NewClaudeExecutor(e.cfg)
		return claude.ExecuteStream(ctx, fallbackAuth, req, opts)
	}
	compat := NewOpenAICompatExecutor("zhipu", e.cfg)
	upstream, err := compat.ExecuteStream(ctx, auth, req, opts)
	if err != nil {
		return nil, err
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		chunker := newStreamChunker()
		maxSegmentLen := chunker.chunkBytes
		firstBudget := chunker.firstChunkMax
		skipNextBlank := false
		handleLine := func(line []byte) {
			if len(line) == 0 {
				if skipNextBlank {
					skipNextBlank = false
					return
				}
				out <- cliproxyexecutor.StreamChunk{Payload: []byte{'\n'}}
				return
			}
			sanitized := stripEmojiFromOpenAIStreamLine(line)
			payload := make([]byte, 0, len(sanitized)+1)
			payload = append(payload, sanitized...)
			payload = append(payload, '\n')
			out <- cliproxyexecutor.StreamChunk{Payload: payload}
		}
		for chunk := range upstream {
			if chunk.Err != nil {
				out <- cliproxyexecutor.StreamChunk{Err: chunk.Err}
				return
			}
			if len(chunk.Payload) == 0 {
				continue
			}
			scanner := bufio.NewScanner(bytes.NewReader(chunk.Payload))
			buf := make([]byte, 20_971_520)
			scanner.Buffer(buf, 20_971_520)
			for scanner.Scan() {
				line := scanner.Bytes()
				segments := [][]byte{bytes.Clone(line)}
				if len(line) > maxSegmentLen*2 {
					segments = segments[:0]
					maxBuf := maxSegmentLen * 2
					for i := 0; i < len(line); i += maxBuf {
						end := i + maxBuf/2
						if end > len(line) {
							end = len(line)
						}
						segments = append(segments, bytes.Clone(line[i:end]))
					}
				}
				for idx, seg := range segments {
					splitSegments := chunker.splitLine(seg, &firstBudget)
					for _, part := range splitSegments {
						handleLine(part)
						if len(splitSegments) > 1 && len(bytes.TrimSpace(part)) > 0 {
							handleLine([]byte{})
						}
					}
					if len(segments) > 1 && idx == len(segments)-1 {
						skipNextBlank = true
					}
				}
			}
		}
	}()
	return out, nil
	/*
			if e.cfg != nil && !e.cfg.PythonAgent.Enabled {
				if fallbackAuth := e.resolveClaudeZhipuAuth(auth); fallbackAuth != nil {
					claude := NewClaudeExecutor(e.cfg)
					return claude.ExecuteStream(ctx, fallbackAuth, req, opts)
				}
				compat := NewOpenAICompatExecutor("zhipu", e.cfg)
				return compat.ExecuteStream(ctx, auth, req, opts)
			}
			token := zhipuCreds(auth)
			reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
			from := opts.SourceFormat
			to := sdktranslator.FromString("openai")
			body := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), true)
		    // REMOVED: Python bridge. Direct path handled by OpenAI-compat executor via ExecuteStream wrapper below.
		    return NewOpenAICompatExecutor("zhipu", e.cfg).ExecuteStream(ctx, auth, req, opts)

			var authID, authLabel, authType, authValue string
			if auth != nil {
				authID = auth.ID
				authLabel = auth.Label
				authType, authValue = auth.AccountInfo()
			}
			recordAPIRequest(ctx, e.cfg, upstreamRequestLog{URL: url, Method: http.MethodPost, Headers: httpReq.Header.Clone(), Body: body, Provider: e.Identifier(), AuthID: authID, AuthLabel: authLabel, AuthType: authType, AuthValue: authValue})

			httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
			resp, err := httpClient.Do(httpReq)
			if err != nil {
				recordAPIResponseError(ctx, e.cfg, err)
				return nil, err
			}
			recordAPIResponseMetadata(ctx, e.cfg, resp.StatusCode, resp.Header.Clone())
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				defer func() { _ = resp.Body.Close() }()
				b, _ := io.ReadAll(resp.Body)
				appendAPIResponseChunk(ctx, e.cfg, b)
				log.Debugf("request error, error status: %d, error body: %s", resp.StatusCode, string(b))
				return nil, statusErr{code: resp.StatusCode, msg: string(b)}
			}
			out := make(chan cliproxyexecutor.StreamChunk)
			go func() {
				defer close(out)
				defer func() { _ = resp.Body.Close() }()
				scanner := bufio.NewScanner(resp.Body)
				maxBuf := 20_971_520
				if v := strings.TrimSpace(os.Getenv("OPENAI_SSE_MAX_LINE_BYTES")); v != "" {
					if n, err := strconv.Atoi(v); err == nil && n > 1024 {
						maxBuf = n
					}
				}
				buf := make([]byte, maxBuf)
				scanner.Buffer(buf, maxBuf)
				var param any
				var splitCount int
				var maxSegmentLen int
				chunker := newStreamChunker()
				firstBudget := chunker.firstChunkMax
				if firstBudget <= 0 {
					firstBudget = chunker.chunkBytes
				}
				handleLine := func(line []byte) {
					appendAPIResponseChunk(ctx, e.cfg, line)
					if detail, ok := parseOpenAIStreamUsage(line); ok {
						reporter.publish(ctx, detail)
					}
					// Strip emoji from streaming line payload before translation
					sanitized := stripEmojiFromOpenAIStreamLine(line)
					chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), body, bytes.Clone(sanitized), &param)
					for i := range chunks {
						if l := len(chunks[i]); l > maxSegmentLen {
							maxSegmentLen = l
						}
						out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunks[i])}
					}
				}
				skipNextBlank := false
				for scanner.Scan() {
					line := bytes.Clone(scanner.Bytes())
					if skipNextBlank && len(bytes.TrimSpace(line)) == 0 {
						skipNextBlank = false
						continue
					}
					segments := [][]byte{line}
					if len(line) > maxBuf/2 {
						segments = segments[:0]
						for i := 0; i < len(line); i += maxBuf / 2 {
							end := i + maxBuf/2
							if end > len(line) {
								end = len(line)
							}
							segments = append(segments, bytes.Clone(line[i:end]))
						}
					}
					for _, seg := range segments {
						splitSegments := chunker.splitLine(seg, &firstBudget)
						if len(splitSegments) > 1 {
							splitCount += len(splitSegments) - 1
						}
						for idx, part := range splitSegments {
							handleLine(part)
							if len(splitSegments) > 1 && len(bytes.TrimSpace(part)) > 0 {
								handleLine([]byte{})
							}
							if len(splitSegments) > 1 && idx == len(splitSegments)-1 {
								skipNextBlank = true
							}
						}
					}
				}
				if err = scanner.Err(); err != nil {
					recordAPIResponseError(ctx, e.cfg, err)
					out <- cliproxyexecutor.StreamChunk{Err: err}
				}
				log.Debugf("zhipu stream: splitCount=%d maxSegmentLen=%d", splitCount, maxSegmentLen)
			}()
		    return out, nil
	*/
}

func (e *ZhipuExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte{}}, fmt.Errorf("not implemented")
}

func (e *ZhipuExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("zhipu executor: refresh called")
	_ = ctx
	return auth, nil
}

type streamChunker struct {
	chunkBytes    int
	firstChunkMax int
}

func newStreamChunker() *streamChunker {
	// Default to a smaller chunk size to improve perceived streaming smoothness.
	// Operators can override via ZHIPU_SSE_CHUNK_BYTES or SSE_CHUNK_BYTES.
	chunkBytes := intEnvOrDefault([]string{"ZHIPU_SSE_CHUNK_BYTES", "SSE_CHUNK_BYTES"}, 128)
	if chunkBytes < 64 {
		chunkBytes = 64
	}
	firstMax := intEnvOrDefault([]string{"ZHIPU_SSE_FIRST_CHUNK_MAX_BYTES", "SSE_FIRST_CHUNK_MAX_BYTES"}, 2048)
	if firstMax <= 0 {
		firstMax = 2048
	}
	if firstMax > 2048 {
		firstMax = 2048
	}
	return &streamChunker{
		chunkBytes:    chunkBytes,
		firstChunkMax: firstMax,
	}
}

func (c *streamChunker) splitLine(line []byte, firstBudget *int) [][]byte {
	if c == nil {
		return [][]byte{line}
	}
	raw := strings.TrimSpace(string(line))
	if raw == "" {
		return [][]byte{line}
	}
	if !strings.HasPrefix(raw, "data: ") {
		return [][]byte{line}
	}
	payload := strings.TrimSpace(raw[len("data: "):])
	if payload == "" {
		return [][]byte{line}
	}
	if payload == "[DONE]" {
		if firstBudget != nil {
			*firstBudget = 0
		}
		return [][]byte{line}
	}
	var msg streamPayload
	if err := json.Unmarshal([]byte(payload), &msg); err != nil {
		return [][]byte{line}
	}
	if len(msg.Choices) == 0 {
		return [][]byte{line}
	}
	choice := msg.Choices[0]
	contentRaw, ok := choice.Delta["content"]
	if !ok {
		return [][]byte{line}
	}
	var content string
	if err := json.Unmarshal(contentRaw, &content); err != nil {
		return [][]byte{line}
	}
	// Apply server-side emoji stripping for provider=zhipu
	content = stripEmoji(content)
	if content == "" {
		// Re-emit the same structure with empty content to avoid leaking emoji
		chunkCopy := msg.clone()
		deltaCopy := cloneDelta(choice.Delta)
		encodedContent, _ := json.Marshal("")
		deltaCopy["content"] = encodedContent
		chunkCopy.Choices[0].Delta = deltaCopy
		chunkCopy.Choices[0].FinishReason = choice.FinishReason
		encodedChunk, err := json.Marshal(chunkCopy)
		if err != nil {
			return [][]byte{line}
		}
		return [][]byte{[]byte("data: " + string(encodedChunk))}
	}
	segments := c.splitContent(content, firstBudget)
	if len(segments) <= 1 {
		return [][]byte{line}
	}
	result := make([][]byte, 0, len(segments))
	for idx, seg := range segments {
		chunkCopy := msg.clone()
		deltaCopy := cloneDelta(choice.Delta)
		encodedContent, err := json.Marshal(seg)
		if err != nil {
			return [][]byte{line}
		}
		deltaCopy["content"] = encodedContent
		chunkCopy.Choices[0].Delta = deltaCopy
		if idx < len(segments)-1 {
			chunkCopy.Choices[0].FinishReason = nil
		} else {
			chunkCopy.Choices[0].FinishReason = choice.FinishReason
		}
		encodedChunk, err := json.Marshal(chunkCopy)
		if err != nil {
			return [][]byte{line}
		}
		result = append(result, []byte("data: "+string(encodedChunk)))
	}
	return result
}

func (c *streamChunker) splitContent(text string, firstBudget *int) []string {
	if text == "" {
		return nil
	}
	if c.chunkBytes <= 0 {
		return []string{text}
	}
	var segments []string
	remaining := text
	for len(remaining) > 0 {
		limit := c.chunkBytes
		if firstBudget != nil && *firstBudget > 0 && *firstBudget < limit {
			limit = *firstBudget
		}
		if limit <= 0 {
			limit = c.chunkBytes
		}
		cut := safePrefixLen(remaining, limit)
		if cut <= 0 {
			cut = len(remaining)
		}
		segment := remaining[:cut]
		segments = append(segments, segment)
		if firstBudget != nil && *firstBudget > 0 {
			if len(segment) >= *firstBudget {
				*firstBudget = 0
			} else {
				*firstBudget -= len(segment)
			}
		}
		remaining = remaining[cut:]
	}
	return segments
}

type streamPayload struct {
	ID      string         `json:"id,omitempty"`
	Object  string         `json:"object,omitempty"`
	Created int64          `json:"created,omitempty"`
	Model   string         `json:"model,omitempty"`
	Choices []streamChoice `json:"choices"`
}

type streamChoice struct {
	Index        int                        `json:"index"`
	Delta        map[string]json.RawMessage `json:"delta,omitempty"`
	FinishReason *string                    `json:"finish_reason,omitempty"`
}

func (p streamPayload) clone() streamPayload {
	cp := p
	if len(p.Choices) > 0 {
		cp.Choices = make([]streamChoice, len(p.Choices))
		for i := range p.Choices {
			cp.Choices[i].Index = p.Choices[i].Index
			cp.Choices[i].FinishReason = p.Choices[i].FinishReason
			cp.Choices[i].Delta = cloneDelta(p.Choices[i].Delta)
		}
	}
	return cp
}

func cloneDelta(src map[string]json.RawMessage) map[string]json.RawMessage {
	if src == nil {
		return nil
	}
	dst := make(map[string]json.RawMessage, len(src))
	for k, v := range src {
		if v == nil {
			dst[k] = nil
			continue
		}
		cpy := make(json.RawMessage, len(v))
		copy(cpy, v)
		dst[k] = cpy
	}
	return dst
}

func intEnvOrDefault(keys []string, def int) int {
	_ = keys
	return def
}

// stripEmoji removes Unicode emoji/pictographs and related modifiers from s.
// It drops characters in common emoji blocks and combining marks used to form emoji sequences.
func stripEmoji(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if isEmojiRune(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func isEmojiRune(r rune) bool {
	// Variation selectors and joiners
	if r == 0xFE0F || r == 0xFE0E || r == 0x200D {
		return true
	}
	// Skin tone modifiers
	if r >= 0x1F3FB && r <= 0x1F3FF {
		return true
	}
	// Regional indicator symbols (used for flags)
	if r >= 0x1F1E6 && r <= 0x1F1FF {
		return true
	}
	// Tags (used in some flag sequences)
	if r >= 0xE0020 && r <= 0xE007F {
		return true
	}
	// Common emoji blocks
	switch {
	case r >= 0x1F600 && r <= 0x1F64F: // Emoticons
		return true
	case r >= 0x1F300 && r <= 0x1F5FF: // Misc Symbols and Pictographs
		return true
	case r >= 0x1F680 && r <= 0x1F6FF: // Transport & Map
		return true
	case r >= 0x1F900 && r <= 0x1F9FF: // Supplemental Symbols and Pictographs
		return true
	case r >= 0x1FA70 && r <= 0x1FAFF: // Symbols and Pictographs Extended-A
		return true
	case r >= 0x2600 && r <= 0x26FF: // Misc symbols
		return true
	case r >= 0x2700 && r <= 0x27BF: // Dingbats
		return true
	}
	return false
}

// stripEmojiFromOpenAIStreamLine strips emoji from an OpenAI-style SSE line that starts with "data: { ... }"
// If parsing fails, returns the original line.
func stripEmojiFromOpenAIStreamLine(line []byte) []byte {
	raw := strings.TrimSpace(string(line))
	if raw == "" || !strings.HasPrefix(raw, "data: ") {
		return line
	}
	payload := strings.TrimSpace(raw[len("data: "):])
	if payload == "" || payload == "[DONE]" {
		return line
	}
	var msg streamPayload
	if err := json.Unmarshal([]byte(payload), &msg); err != nil {
		return line
	}
	if len(msg.Choices) == 0 {
		return line
	}
	choice := msg.Choices[0]
	contentRaw, ok := choice.Delta["content"]
	if !ok {
		return line
	}
	var content string
	if err := json.Unmarshal(contentRaw, &content); err != nil {
		return line
	}
	sanitized := stripEmoji(content)
	if sanitized == content { // no change
		return line
	}
	chunkCopy := msg.clone()
	deltaCopy := cloneDelta(choice.Delta)
	enc, err := json.Marshal(sanitized)
	if err != nil {
		return line
	}
	deltaCopy["content"] = enc
	chunkCopy.Choices[0].Delta = deltaCopy
	chunkCopy.Choices[0].FinishReason = choice.FinishReason
	encChunk, err := json.Marshal(chunkCopy)
	if err != nil {
		return line
	}
	return []byte("data: " + string(encChunk))
}

// stripEmojiFromOpenAIResponseJSON removes emoji from non-stream OpenAI chat completion JSON payloads.
// If parsing fails, returns the original data.
func stripEmojiFromOpenAIResponseJSON(data []byte) []byte {
	// Minimal shape for OpenAI chat completions
	type message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	type choice struct {
		Message message `json:"message"`
	}
	var obj struct {
		Choices []choice `json:"choices"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return data
	}
	changed := false
	for i := range obj.Choices {
		c := obj.Choices[i].Message.Content
		sc := stripEmoji(c)
		if sc != c {
			obj.Choices[i].Message.Content = sc
			changed = true
		}
	}
	if !changed {
		return data
	}
	enc, err := json.Marshal(obj)
	if err != nil {
		return data
	}
	return enc
}

func safePrefixLen(s string, limit int) int {
	if len(s) <= limit {
		return len(s)
	}
	if limit <= 0 {
		return 0
	}
	if limit >= len(s) {
		return len(s)
	}
	idx := limit
	for idx > 0 && !utf8.RuneStart(s[idx]) {
		idx--
	}
	if idx <= 0 {
		idx = limit
		for idx < len(s) && !utf8.RuneStart(s[idx]) {
			idx++
		}
		if idx >= len(s) {
			return len(s)
		}
	}
	return idx
}

func applyZhipuHeaders(r *http.Request, token string, stream bool) {
	r.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(token) != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.Header.Set("User-Agent", "cli-proxy-zhipu")
	if stream {
		r.Header.Set("Accept", "text/event-stream")
		return
	}
	r.Header.Set("Accept", "application/json")
}

func zhipuCreds(a *cliproxyauth.Auth) (token string) {
	if a == nil {
		return ""
	}
	if a.Attributes != nil {
		if v := a.Attributes["api_key"]; v != "" {
			token = v
		}
	}
	return
}

// resolveClaudeZhipuAuth returns an auth suitable for ClaudeExecutor when
// configuration indicates Zhipu via Claude's Anthropic-compatible endpoint. Priority:
// 1) If incoming auth has a base_url containing "/api/anthropic", use it as-is.
// 2) Else, search cfg.ClaudeKey entries whose BaseURL contains "/api/anthropic" and synthesize an auth.
func (e *ZhipuExecutor) resolveClaudeZhipuAuth(in *cliproxyauth.Auth) *cliproxyauth.Auth {
	if e == nil || e.cfg == nil {
		return nil
	}
	// 1) Incoming auth hints
	if in != nil && in.Attributes != nil {
		base := strings.TrimSpace(in.Attributes["base_url"])
		if base != "" && strings.Contains(strings.ToLower(base), "/api/anthropic") {
			// Ensure api_key exists; otherwise cannot call upstream
			if k := strings.TrimSpace(in.Attributes["api_key"]); k != "" {
				return in
			}
		}
	}
	// 2) Config claude-api-key entries
	var firstCandidate *cliproxyauth.Auth
	for i := range e.cfg.ClaudeKey {
		ck := e.cfg.ClaudeKey[i]
		base := strings.TrimSpace(ck.BaseURL)
		key := strings.TrimSpace(ck.APIKey)
		if key == "" || base == "" {
			continue
		}
		a := &cliproxyauth.Auth{
			ID:       "zhipu-via-claude",
			Provider: "claude",
			Label:    "zhipu-via-claude",
			Status:   cliproxyauth.StatusActive,
			ProxyURL: strings.TrimSpace(ck.ProxyURL),
			Attributes: map[string]string{
				"api_key":  key,
				"base_url": base,
			},
		}
		if firstCandidate == nil {
			firstCandidate = a
		}
		if strings.Contains(strings.ToLower(base), "/api/anthropic") {
			return a
		}
	}
	// If exactly one Claude key configured with baseURL, use it as a fallback
	if firstCandidate != nil && len(e.cfg.ClaudeKey) == 1 {
		return firstCandidate
	}
	return nil
}
