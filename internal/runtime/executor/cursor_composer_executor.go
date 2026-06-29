package executor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/cursorcomposer"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	cursorComposerProvider       = "cursor-composer"
	cursorComposerDefaultAPIBase = "https://api.cursor.com"
	cursorComposerDefaultModel   = "composer-2.5"
)

type CursorComposerExecutor struct {
	cfg           *config.Config
	identityCache sync.Map
	tokenCache    sync.Map
	tokenMu       sync.Mutex
}

type cursorComposerTokenCacheEntry struct {
	token     string
	expiresAt time.Time
}

func NewCursorComposerExecutor(cfg *config.Config) *CursorComposerExecutor {
	return &CursorComposerExecutor{cfg: cfg}
}

func (e *CursorComposerExecutor) Identifier() string { return cursorComposerProvider }

func (e *CursorComposerExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := resolveCursorComposerModel(thinking.ParseSuffix(req.Model).ModelName)
	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	prepared, translated, err := e.prepare(ctx, auth, req, opts, false)
	if err != nil {
		return resp, err
	}
	var text string
	if bridgeURL := cursorComposerSDKBridgeURL(auth); bridgeURL != "" {
		text, err = e.executeViaSDKBridge(ctx, auth, prepared, false)
	} else {
		var cursorResp *http.Response
		cursorResp, err = e.createCompletion(ctx, auth, prepared)
		if err != nil {
			return resp, err
		}
		defer func() {
			if errClose := cursorResp.Body.Close(); errClose != nil {
				log.Errorf("cursor composer executor: close response body error: %v", errClose)
			}
		}()
		text, err = readCursorComposerText(cursorResp)
		if err != nil {
			return resp, err
		}
	}
	if err != nil {
		return resp, err
	}
	usageDetail := cursorComposerUsage(prepared.prompt, text)
	reporter.Publish(ctx, usageDetail)
	body := buildOpenAIChatResponse(prepared.model, text, usageDetail)
	var param any
	out := sdktranslator.TranslateNonStream(ctx, sdktranslator.FromString("openai"), opts.SourceFormat, req.Model, opts.OriginalRequest, translated, body, &param)
	return cliproxyexecutor.Response{Payload: out}, nil
}

func (e *CursorComposerExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := resolveCursorComposerModel(thinking.ParseSuffix(req.Model).ModelName)
	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	prepared, translated, err := e.prepare(ctx, auth, req, opts, true)
	if err != nil {
		return nil, err
	}
	bridgeURL := cursorComposerSDKBridgeURL(auth)
	var cursorResp *http.Response
	if bridgeURL == "" {
		cursorResp, err = e.createCompletion(ctx, auth, prepared)
		if err != nil {
			return nil, err
		}
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer reporter.EnsurePublished(ctx)
		var param any
		var fullText strings.Builder
		chunkID := "chatcmpl-" + strings.ReplaceAll(uuid.NewString(), "-", "")
		created := time.Now().Unix()
		emitDelta := func(delta string) bool {
			if delta == "" {
				return true
			}
			fullText.WriteString(delta)
			line := buildOpenAIChatStreamLine(chunkID, prepared.model, delta, created, false, nil)
			for _, chunk := range sdktranslator.TranslateStream(ctx, sdktranslator.FromString("openai"), opts.SourceFormat, req.Model, opts.OriginalRequest, translated, line, &param) {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunk}:
				case <-ctx.Done():
					return false
				}
			}
			return true
		}
		if bridgeURL != "" {
			text, bridgeErr := e.executeViaSDKBridge(ctx, auth, prepared, true)
			if bridgeErr != nil {
				helps.RecordAPIResponseError(ctx, e.cfg, bridgeErr)
				reporter.PublishFailure(ctx, bridgeErr)
				select {
				case out <- cliproxyexecutor.StreamChunk{Err: bridgeErr}:
				case <-ctx.Done():
				}
				return
			}
			if !emitDelta(text) {
				return
			}
		} else {
			defer func() {
				if errClose := cursorResp.Body.Close(); errClose != nil {
					log.Errorf("cursor composer executor: close response body error: %v", errClose)
				}
			}()
			for delta, readErr := range streamCursorComposerText(cursorResp) {
				if readErr != nil {
					helps.RecordAPIResponseError(ctx, e.cfg, readErr)
					reporter.PublishFailure(ctx, readErr)
					select {
					case out <- cliproxyexecutor.StreamChunk{Err: readErr}:
					case <-ctx.Done():
					}
					return
				}
				if !emitDelta(delta) {
					return
				}
			}
		}
		usageDetail := cursorComposerUsage(prepared.prompt, fullText.String())
		reporter.Publish(ctx, usageDetail)
		for _, line := range [][]byte{
			buildOpenAIChatStreamLine(chunkID, prepared.model, "", created, true, &usageDetail),
			[]byte("data: [DONE]"),
		} {
			for _, chunk := range sdktranslator.TranslateStream(ctx, sdktranslator.FromString("openai"), opts.SourceFormat, req.Model, opts.OriginalRequest, translated, line, &param) {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunk}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	var headers http.Header
	if cursorResp != nil {
		headers = cursorResp.Header.Clone()
	}
	return &cliproxyexecutor.StreamResult{Headers: headers, Chunks: out}, nil
}

func (e *CursorComposerExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	prepared, _, err := e.prepare(ctx, auth, req, opts, false)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	count := estimateCursorTokens(prepared.prompt)
	usageJSON := helps.BuildOpenAIUsageJSON(int64(count))
	translatedUsage := sdktranslator.TranslateTokenCount(ctx, sdktranslator.FromString("openai"), opts.SourceFormat, int64(count), usageJSON)
	return cliproxyexecutor.Response{Payload: translatedUsage}, nil
}

func (e *CursorComposerExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	return auth, nil
}

func (e *CursorComposerExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("cursor composer executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	key, _, _, _, _ := cursorComposerCredentials(auth)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	util.ApplyCustomHeadersFromAttrs(req, authAttrs(auth))
	return helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(req.WithContext(ctx))
}

type cursorPreparedRequest struct {
	model  string
	prompt string
	mode   string
}

func (e *CursorComposerExecutor) prepare(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool) (cursorPreparedRequest, []byte, error) {
	baseModel := resolveCursorComposerModel(thinking.ParseSuffix(req.Model).ModelName)
	from := opts.SourceFormat
	translated := sdktranslator.TranslateRequest(from, sdktranslator.FromString("openai"), baseModel, req.Payload, stream)
	translated, err := thinking.ApplyThinking(translated, req.Model, from.String(), "openai", e.Identifier())
	if err != nil {
		return cursorPreparedRequest{}, nil, err
	}
	if !json.Valid(translated) {
		return cursorPreparedRequest{}, nil, statusErr{code: http.StatusBadRequest, msg: "cursor composer requires a JSON chat request"}
	}
	messages := gjson.GetBytes(translated, "messages")
	if !messages.Exists() || !messages.IsArray() || len(messages.Array()) == 0 {
		return cursorPreparedRequest{}, nil, statusErr{code: http.StatusBadRequest, msg: "cursor composer request has no messages"}
	}
	prompt := cursorPromptFromOpenAI(translated)
	if strings.TrimSpace(prompt) == "" {
		return cursorPreparedRequest{}, nil, statusErr{code: http.StatusBadRequest, msg: "cursor composer request has no prompt content"}
	}
	_ = ctx
	_ = auth
	return cursorPreparedRequest{model: baseModel, prompt: prompt, mode: "ask"}, translated, nil
}

func (e *CursorComposerExecutor) createCompletion(ctx context.Context, auth *cliproxyauth.Auth, prepared cursorPreparedRequest) (*http.Response, error) {
	apiKey, apiBase, backendBase, endpoint, clientVersion := cursorComposerCredentials(auth)
	if apiKey == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "missing Cursor API key"}
	}
	if backendBase == "" {
		return nil, statusErr{code: http.StatusInternalServerError, msg: "missing Cursor backend base URL"}
	}
	if endpoint == "" {
		return nil, statusErr{code: http.StatusInternalServerError, msg: "missing Cursor chat endpoint"}
	}
	identity, err := e.cursorIdentity(ctx, auth, apiKey, apiBase)
	if err != nil {
		return nil, err
	}
	token, err := e.exchangeAPIKey(ctx, auth, apiKey, backendBase)
	if err != nil {
		return nil, err
	}
	requestID := uuid.NewString()
	body := encodeConnectFrame(encodeCursorChatRequest(prepared.prompt, prepared.mode, prepared.model, requestID, uuid.NewString(), uuid.NewString()))
	url := strings.TrimRight(backendBase, "/") + "/" + strings.TrimLeft(endpoint, "/")
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range cursorInternalHeaders(token, identity, requestID, clientVersion) {
		httpReq.Header.Set(k, v)
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	util.ApplyCustomHeadersFromAttrs(httpReq, authAttrs(auth))
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{URL: url, Method: http.MethodPost, Headers: httpReq.Header.Clone(), Body: body, Provider: e.Identifier()})
	client := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	resp, err := client.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, resp.StatusCode, resp.Header.Clone())
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		return nil, statusErr{code: cursorHTTPStatus(resp.StatusCode), msg: cursorErrorMessage(b, resp.StatusCode)}
	}
	return resp, nil
}

func (e *CursorComposerExecutor) cursorIdentity(ctx context.Context, auth *cliproxyauth.Auth, apiKey, apiBase string) (string, error) {
	if val, ok := e.identityCache.Load(apiKey); ok {
		if identity, ok := val.(string); ok && identity != "" {
			return identity, nil
		}
	}
	url := strings.TrimRight(apiBase, "/") + "/v1/me"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("x-cursor-client-type", "sdk")
	req.Header.Set("x-cursor-client-version", "cli-proxy-api")
	req.Header.Set("x-ghost-mode", "true")
	resp, err := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", statusErr{code: cursorHTTPStatus(resp.StatusCode), msg: cursorErrorMessage(body, resp.StatusCode)}
	}
	identity := ""
	if id := gjson.GetBytes(body, "userId"); id.Exists() {
		identity = "cursor-user:" + id.String()
	} else if email := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "userEmail").String())); email != "" {
		identity = "cursor-email:" + email
	} else {
		identity = "cursor-key:" + sha256Hex(apiKey)
	}
	e.identityCache.Store(apiKey, identity)
	return identity, nil
}

func (e *CursorComposerExecutor) exchangeAPIKey(ctx context.Context, auth *cliproxyauth.Auth, apiKey, backendBase string) (string, error) {
	if val, ok := e.tokenCache.Load(apiKey); ok {
		if entry, ok := val.(cursorComposerTokenCacheEntry); ok && entry.token != "" && time.Now().Before(entry.expiresAt) {
			return entry.token, nil
		}
	}
	e.tokenMu.Lock()
	defer e.tokenMu.Unlock()
	if val, ok := e.tokenCache.Load(apiKey); ok {
		if entry, ok := val.(cursorComposerTokenCacheEntry); ok && entry.token != "" && time.Now().Before(entry.expiresAt) {
			return entry.token, nil
		}
	}
	url := strings.TrimRight(backendBase, "/") + "/auth/exchange_user_api_key"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader("{}"))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", statusErr{code: cursorHTTPStatus(resp.StatusCode), msg: cursorErrorMessage(body, resp.StatusCode)}
	}
	token := strings.TrimSpace(gjson.GetBytes(body, "accessToken").String())
	if token == "" {
		return "", statusErr{code: http.StatusBadGateway, msg: "Cursor did not return an internal access token"}
	}
	e.tokenCache.Store(apiKey, cursorComposerTokenCacheEntry{
		token:     token,
		expiresAt: time.Now().Add(5 * time.Minute),
	})
	return token, nil
}

func cursorComposerCredentials(auth *cliproxyauth.Auth) (apiKey, apiBase, backendBase, endpoint, clientVersion string) {
	apiBase = cursorComposerDefaultAPIBase
	var attrBackend, attrEndpoint, attrVersion string
	if auth != nil && auth.Attributes != nil {
		apiKey = strings.TrimSpace(auth.Attributes["api_key"])
		if v := strings.TrimSpace(auth.Attributes["base_url"]); v != "" {
			apiBase = v
		}
		attrBackend = strings.TrimSpace(auth.Attributes["backend_base_url"])
		attrEndpoint = strings.TrimSpace(auth.Attributes["chat_endpoint"])
		attrVersion = strings.TrimSpace(auth.Attributes["client_version"])
	}
	backendBase = cursorcomposer.ResolveBackendBase(attrBackend)
	endpoint = cursorcomposer.ResolveChatEndpoint(attrEndpoint)
	clientVersion = cursorcomposer.ResolveClientVersion(attrVersion)
	return
}

func cursorPromptFromOpenAI(payload []byte) string {
	var lines []string
	lines = append(lines, "You are serving an OpenAI-compatible API request through Cursor Composer.", "Answer the user directly in chat style.", "", "Conversation:")
	for _, msg := range gjson.GetBytes(payload, "messages").Array() {
		role := msg.Get("role").String()
		if role == "" {
			role = "user"
		}
		text := openAIContentText(msg.Get("content"))
		if text == "" {
			text = "[empty]"
		}
		lines = append(lines, strings.ToUpper(role)+": "+text)
		if tc := msg.Get("tool_calls"); tc.Exists() {
			lines = append(lines, strings.ToUpper(role)+" TOOL_CALLS: "+tc.Raw)
		}
	}
	return strings.Join(lines, "\n")
}

func openAIContentText(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.String()
	}
	if !content.IsArray() {
		return strings.TrimSpace(content.Raw)
	}
	var out []string
	for _, part := range content.Array() {
		switch part.Get("type").String() {
		case "text":
			out = append(out, part.Get("text").String())
		case "input_text":
			out = append(out, part.Get("text").String())
		case "image_url", "input_image":
			out = append(out, "[image omitted]")
		default:
			if s := part.Get("text").String(); s != "" {
				out = append(out, s)
			}
		}
	}
	return strings.Join(out, "\n")
}

func readCursorComposerText(resp *http.Response) (string, error) {
	var b strings.Builder
	for delta, err := range streamCursorComposerText(resp) {
		if err != nil {
			return "", err
		}
		b.WriteString(delta)
	}
	return b.String(), nil
}

func streamCursorComposerText(resp *http.Response) func(func(string, error) bool) {
	return func(yield func(string, error) bool) {
		if !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "application/connect+proto") {
			reader := bufio.NewReader(resp.Body)
			for {
				line, readErr := reader.ReadString('\n')
				if line != "" {
					line = strings.TrimSpace(line)
					if strings.HasPrefix(line, "data:") {
						raw := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
						if raw == "[DONE]" {
							return
						}
						if text := gjson.Get(raw, "text").String(); text != "" && !yield(stripComposerControlTokens(text), nil) {
							return
						}
					}
				}
				if readErr != nil {
					if errors.Is(readErr, io.EOF) {
						return
					}
					yield("", readErr)
					return
				}
			}
		}
		for frame, err := range parseConnectProtoFrames(resp.Body) {
			if err != nil {
				yield("", err)
				return
			}
			if text, ok := decodeCursorTextFrame(frame); ok && text != "" && !yield(stripComposerControlTokens(text), nil) {
				return
			}
		}
	}
}

func encodeCursorChatRequest(prompt, mode, model, requestID, conversationID, messageID string) []byte {
	if mode == "agent" {
		mode = "Agent"
	} else {
		mode = "Ask"
	}
	userMessage := protoMessage(
		protoField(1, 2, []byte(prompt)),
		protoField(2, 0, uint64(1)),
		protoField(13, 2, []byte(messageID)),
		protoField(47, 0, uint64(1)),
	)
	modelMsg := protoMessage(protoField(1, 2, []byte(model)), protoField(4, 2, []byte{}))
	cursorSetting := protoMessage(
		protoField(1, 2, []byte("cursor\\aisettings")),
		protoField(3, 2, []byte{}),
		protoField(6, 2, protoMessage(protoField(1, 2, []byte{}), protoField(2, 2, []byte{}))),
		protoField(8, 0, uint64(1)),
		protoField(9, 0, uint64(1)),
	)
	metadata := protoMessage(
		protoField(1, 2, []byte("linux")),
		protoField(2, 2, []byte("x64")),
		protoField(3, 2, []byte("unknown")),
		protoField(4, 2, []byte("cli-proxy-api")),
		protoField(5, 2, []byte(time.Now().UTC().Format(time.RFC3339Nano))),
	)
	messageIDRecord := protoMessage(protoField(1, 2, []byte(messageID)), protoField(3, 0, uint64(1)))
	request := protoMessage(
		protoField(1, 2, userMessage),
		protoField(2, 0, uint64(1)),
		protoField(3, 2, []byte{}),
		protoField(4, 0, uint64(1)),
		protoField(5, 2, modelMsg),
		protoField(8, 2, []byte{}),
		protoField(13, 0, uint64(1)),
		protoField(15, 2, cursorSetting),
		protoField(19, 0, uint64(1)),
		protoField(23, 2, []byte(conversationID)),
		protoField(26, 2, metadata),
		protoField(27, 0, uint64(0)),
		protoField(30, 2, messageIDRecord),
		protoField(35, 0, uint64(0)),
		protoField(38, 0, uint64(0)),
		protoField(46, 0, uint64(1)),
		protoField(47, 2, []byte{}),
		protoField(48, 0, uint64(0)),
		protoField(49, 0, uint64(0)),
		protoField(51, 0, uint64(0)),
		protoField(53, 0, uint64(1)),
		protoField(54, 2, []byte(mode)),
	)
	_ = requestID
	return protoMessage(protoField(1, 2, request))
}

func encodeConnectFrame(payload []byte) []byte {
	frame := make([]byte, 5+len(payload))
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(payload)))
	copy(frame[5:], payload)
	return frame
}

func parseConnectProtoFrames(r io.Reader) func(func([]byte, error) bool) {
	return func(yield func([]byte, error) bool) {
		reader := bufio.NewReader(r)
		for {
			header := make([]byte, 5)
			if _, err := io.ReadFull(reader, header); err != nil {
				if err != io.EOF {
					yield(nil, err)
				}
				return
			}
			if header[0]&1 == 1 {
				yield(nil, statusErr{code: http.StatusBadGateway, msg: "Cursor returned a compressed Connect frame"})
				return
			}
			length := binary.BigEndian.Uint32(header[1:5])
			if length > 52_428_800 {
				yield(nil, statusErr{code: http.StatusBadGateway, msg: "Cursor Connect frame is too large"})
				return
			}
			payload := make([]byte, length)
			if _, err := io.ReadFull(reader, payload); err != nil {
				yield(nil, err)
				return
			}
			if header[0]&2 == 2 {
				if err := handleConnectEndStreamFrame(payload); err != nil {
					yield(nil, err)
				}
				return
			}
			if !yield(payload, nil) {
				return
			}
		}
	}
}

func handleConnectEndStreamFrame(payload []byte) error {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("{}")) {
		return nil
	}
	errorValue := gjson.GetBytes(trimmed, "error")
	if !errorValue.Exists() {
		return nil
	}
	message := strings.TrimSpace(errorValue.Get("message").String())
	if message == "" {
		message = strings.TrimSpace(errorValue.String())
	}
	if message == "" {
		message = "Cursor stream failed"
	}
	return statusErr{code: http.StatusBadGateway, msg: message}
}

func decodeCursorTextFrame(payload []byte) (string, bool) {
	fields, err := decodeProtoFields(payload)
	if err != nil {
		return "", false
	}
	for _, field := range fields {
		if field.no != 2 || field.wt != 2 {
			continue
		}
		inner, err := decodeProtoFields(field.bytes)
		if err != nil {
			continue
		}
		var text string
		for _, f := range inner {
			if f.no == 1 && f.wt == 2 {
				text += string(f.bytes)
			}
			if f.no == 25 && f.wt == 2 {
				thinking, _ := decodeProtoFields(f.bytes)
				for _, tf := range thinking {
					if tf.no == 1 && tf.wt == 2 {
						text += string(tf.bytes)
					}
				}
			}
		}
		if text != "" {
			return text, true
		}
	}
	return "", false
}

type protoDecodedField struct {
	no    int
	wt    int
	bytes []byte
}

func decodeProtoFields(data []byte) ([]protoDecodedField, error) {
	var out []protoDecodedField
	for offset := 0; offset < len(data); {
		tag, next, err := readProtoVarint(data, offset)
		if err != nil {
			return nil, err
		}
		offset = next
		no := int(tag >> 3)
		wt := int(tag & 7)
		switch wt {
		case 0:
			_, next, err := readProtoVarint(data, offset)
			if err != nil {
				return nil, err
			}
			offset = next
			out = append(out, protoDecodedField{no: no, wt: wt})
		case 2:
			l, next, err := readProtoVarint(data, offset)
			if err != nil {
				return nil, err
			}
			offset = next
			if l > uint64(len(data)-offset) {
				return nil, io.ErrUnexpectedEOF
			}
			out = append(out, protoDecodedField{no: no, wt: wt, bytes: data[offset : offset+int(l)]})
			offset += int(l)
		case 1:
			if offset+8 > len(data) {
				return nil, io.ErrUnexpectedEOF
			}
			offset += 8
		case 5:
			if offset+4 > len(data) {
				return nil, io.ErrUnexpectedEOF
			}
			offset += 4
		default:
			return nil, fmt.Errorf("unsupported protobuf wire type %d", wt)
		}
	}
	return out, nil
}

func readProtoVarint(data []byte, offset int) (uint64, int, error) {
	var value uint64
	for shift := uint(0); offset < len(data); shift += 7 {
		if shift >= 64 {
			return 0, offset, errors.New("varint overflow")
		}
		b := data[offset]
		offset++
		value |= uint64(b&0x7f) << shift
		if b&0x80 == 0 {
			return value, offset, nil
		}
	}
	return 0, offset, io.ErrUnexpectedEOF
}

func protoMessage(parts ...[]byte) []byte {
	return bytes.Join(parts, nil)
}

func protoField(no, wt int, value any) []byte {
	tag := encodeProtoVarint(uint64(no<<3 | wt))
	if wt == 0 {
		return append(tag, encodeProtoVarint(value.(uint64))...)
	}
	raw := value.([]byte)
	out := append(tag, encodeProtoVarint(uint64(len(raw)))...)
	return append(out, raw...)
}

func encodeProtoVarint(value uint64) []byte {
	var out []byte
	for value >= 0x80 {
		out = append(out, byte(value&0x7f|0x80))
		value >>= 7
	}
	return append(out, byte(value))
}

func cursorInternalHeaders(token, identity, requestID, clientVersion string) map[string]string {
	return map[string]string{
		"Content-Type":                "application/connect+proto",
		"Connect-Protocol-Version":    "1",
		"User-Agent":                  "connect-es/1.6.1",
		"x-amzn-trace-id":             "Root=" + requestID,
		"x-client-key":                sha256Hex(token),
		"x-cursor-checksum":           cursorChecksum(identity),
		"x-cursor-client-version":     clientVersion,
		"x-cursor-client-type":        "ide",
		"x-cursor-client-os":          "linux",
		"x-cursor-client-arch":        "x64",
		"x-cursor-client-os-version":  "unknown",
		"x-cursor-client-device-type": "desktop",
		"x-cursor-config-version":     stableUUID("cursor-config", identity),
		"x-cursor-timezone":           "UTC",
		"x-ghost-mode":                "false",
		"x-new-onboarding-completed":  "false",
		"x-request-id":                requestID,
		"x-session-id":                cursorSessionID(token),
	}
}

func cursorChecksum(identity string) string {
	machineID := sha256Hex("composer-api:cursor-machine:" + identity)
	timestamp := uint64(time.Now().Unix() / 1_000_000)
	raw := []byte{byte(timestamp >> 40), byte(timestamp >> 32), byte(timestamp >> 24), byte(timestamp >> 16), byte(timestamp >> 8), byte(timestamp)}
	t := byte(165)
	for i := range raw {
		raw[i] = ((raw[i] ^ t) + byte(i%256)) & 255
		t = raw[i]
	}
	return base64.RawURLEncoding.EncodeToString(raw) + machineID
}

func stableUUID(namespace, value string) string {
	hash := sha256Hex(namespace + ":" + value)
	return fmt.Sprintf("%s-%s-%s-%s-%s", hash[:8], hash[8:12], hash[12:16], hash[16:20], hash[20:32])
}

func cursorSessionID(accessToken string) string {
	hash := sha256Hex(accessToken)
	if len(hash) < 32 {
		return hash
	}
	hash = hash[:32]
	return fmt.Sprintf("%s-%s-%s-%s-%s", hash[:8], hash[8:12], hash[12:16], hash[16:20], hash[20:32])
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func resolveCursorComposerModel(model string) string {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "", "auto", "default", "composer-latest", "composer-2-5", "composer-2.5-sdk":
		return cursorComposerDefaultModel
	case "composer-2-5-fast":
		return "composer-2.5-fast"
	default:
		return strings.TrimSpace(model)
	}
}

func cursorComposerUsage(prompt, text string) usage.Detail {
	input := estimateCursorTokens(prompt)
	output := estimateCursorTokens(text)
	return usage.Detail{InputTokens: int64(input), OutputTokens: int64(output), TotalTokens: int64(input + output)}
}

func estimateCursorTokens(s string) int {
	if s == "" {
		return 0
	}
	return int(math.Ceil(float64(utf8.RuneCountInString(s)) / 4.0))
}

func buildOpenAIChatResponse(model, text string, detail usage.Detail) []byte {
	payload := map[string]any{
		"id":      "chatcmpl-" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": text}, "finish_reason": "stop"}},
		"usage":   map[string]any{"prompt_tokens": detail.InputTokens, "completion_tokens": detail.OutputTokens, "total_tokens": detail.TotalTokens},
	}
	out, _ := json.Marshal(payload)
	return out
}

func buildOpenAIChatStreamLine(id, model, delta string, created int64, done bool, detail *usage.Detail) []byte {
	finish := any(nil)
	if done {
		finish = "stop"
	}
	payload := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []map[string]any{{"index": 0, "delta": map[string]any{"content": delta}, "finish_reason": finish}},
	}
	if detail != nil {
		payload["usage"] = map[string]any{"prompt_tokens": detail.InputTokens, "completion_tokens": detail.OutputTokens, "total_tokens": detail.TotalTokens}
	}
	raw, _ := json.Marshal(payload)
	return append([]byte("data: "), raw...)
}

func stripComposerControlTokens(value string) string {
	for _, token := range []string{"</think>", "<|final|>", "<｜final｜>", "< | final | >"} {
		if idx := strings.LastIndex(value, token); idx >= 0 {
			value = value[idx+len(token):]
		}
	}
	return strings.TrimLeft(value, "\r\n\t ")
}

func cursorHTTPStatus(status int) int {
	if status == 401 {
		return http.StatusUnauthorized
	}
	if status == 429 {
		return http.StatusTooManyRequests
	}
	if status >= 500 || status == 464 {
		return http.StatusBadGateway
	}
	return http.StatusBadRequest
}

func cursorErrorMessage(body []byte, status int) string {
	if msg := gjson.GetBytes(body, "error.message").String(); msg != "" {
		return msg
	}
	if msg := gjson.GetBytes(body, "message").String(); msg != "" {
		return msg
	}
	if len(bytes.TrimSpace(body)) > 0 {
		return string(body)
	}
	return fmt.Sprintf("Cursor API request failed with status %d", status)
}

func authAttrs(auth *cliproxyauth.Auth) map[string]string {
	if auth == nil {
		return nil
	}
	return auth.Attributes
}
