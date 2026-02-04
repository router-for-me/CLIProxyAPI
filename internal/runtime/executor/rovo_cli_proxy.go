package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

const rovoCLIDefaultTimeout = 120 * time.Second

func resolveRovoCLIProxyURL(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if proxyStr := strings.TrimSpace(auth.ProxyURL); proxyStr != "" {
		return proxyStr
	}
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["proxy_url"].(string); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func isRovoCLIServerURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func rovoCLIExecute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	proxyURL := resolveRovoCLIProxyURL(auth)
	if proxyURL == "" || !isRovoCLIServerURL(proxyURL) {
		return cliproxyexecutor.Response{}, nil
	}

	openAIReq := rovoCLITranslateToOpenAI(req, opts)
	prompt := buildRovoCLIPrompt(openAIReq)
	if strings.TrimSpace(prompt) == "" {
		return cliproxyexecutor.Response{}, statusErr{code: http.StatusBadRequest, msg: "rovo cli proxy: empty prompt"}
	}

	client := &http.Client{Timeout: rovoCLIDefaultTimeout}
	baseURL := strings.TrimRight(proxyURL, "/")
	if err := rovoCLIPostMessage(ctx, client, baseURL, prompt); err != nil {
		return cliproxyexecutor.Response{}, err
	}

	text, err := rovoCLIReadFullResponse(ctx, client, baseURL)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	openAIResp := buildOpenAIChatCompletionResponse(req.Model, text)
	translated := sdktranslator.TranslateNonStream(ctx, sdktranslator.FromString("openai"), opts.SourceFormat, req.Model, bytes.Clone(opts.OriginalRequest), openAIReq, openAIResp, nil)
	return cliproxyexecutor.Response{Payload: []byte(translated)}, nil
}

func rovoCLIExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
	proxyURL := resolveRovoCLIProxyURL(auth)
	if proxyURL == "" || !isRovoCLIServerURL(proxyURL) {
		return nil, nil
	}

	openAIReq := rovoCLITranslateToOpenAI(req, opts)
	prompt := buildRovoCLIPrompt(openAIReq)
	if strings.TrimSpace(prompt) == "" {
		ch := make(chan cliproxyexecutor.StreamChunk, 1)
		ch <- cliproxyexecutor.StreamChunk{Err: statusErr{code: http.StatusBadRequest, msg: "rovo cli proxy: empty prompt"}}
		close(ch)
		return ch, nil
	}

	client := &http.Client{Timeout: 0}
	baseURL := strings.TrimRight(proxyURL, "/")
	if err := rovoCLIPostMessage(ctx, client, baseURL, prompt); err != nil {
		return nil, err
	}

	stream := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(stream)
		err := rovoCLIStreamResponse(ctx, client, baseURL, func(delta string) {
			if strings.TrimSpace(delta) == "" {
				return
			}
			chunk := buildOpenAIChatCompletionChunk(req.Model, delta, false)
			segments := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("openai"), opts.SourceFormat, req.Model, bytes.Clone(opts.OriginalRequest), openAIReq, []byte(chunk), nil)
			for i := range segments {
				stream <- cliproxyexecutor.StreamChunk{Payload: []byte(segments[i])}
			}
		})
		if err != nil {
			stream <- cliproxyexecutor.StreamChunk{Err: err}
			return
		}

		finalChunk := buildOpenAIChatCompletionChunk(req.Model, "", true)
		segments := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("openai"), opts.SourceFormat, req.Model, bytes.Clone(opts.OriginalRequest), openAIReq, []byte(finalChunk), nil)
		for i := range segments {
			stream <- cliproxyexecutor.StreamChunk{Payload: []byte(segments[i])}
		}
	}()

	return stream, nil
}

func rovoCLIPostMessage(ctx context.Context, client *http.Client, baseURL, prompt string) error {
	payload := map[string]any{"message": prompt}
	body, _ := json.Marshal(payload)
	url := baseURL + "/v3/set_chat_message"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return statusErr{code: resp.StatusCode, msg: string(b)}
	}
	return nil
}

func rovoCLIReadFullResponse(ctx context.Context, client *http.Client, baseURL string) (string, error) {
	url := baseURL + "/v3/stream_chat"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return "", statusErr{code: resp.StatusCode, msg: string(b)}
	}

	var sb strings.Builder
	_ = rovoCLIParseSSE(resp.Body, func(delta string) {
		sb.WriteString(delta)
	})
	return sb.String(), nil
}

func rovoCLIStreamResponse(ctx context.Context, client *http.Client, baseURL string, onDelta func(string)) error {
	url := baseURL + "/v3/stream_chat"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return statusErr{code: resp.StatusCode, msg: string(b)}
	}
	return rovoCLIParseSSE(resp.Body, onDelta)
}

func rovoCLIParseSSE(body io.Reader, onDelta func(string)) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(nil, 8*1024*1024)
	var eventName string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			_ = eventName
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}

		delta := extractRovoCLIDelta(data)
		if delta != "" {
			onDelta(delta)
		}
	}
	return scanner.Err()
}

func extractRovoCLIDelta(raw string) string {
	result := gjson.Parse(raw)
	// part_start -> part.content
	if result.Get("part.part_kind").String() == "text" {
		if content := result.Get("part.content").String(); content != "" {
			return content
		}
	}
	// part_delta -> delta.content_delta
	if result.Get("delta.part_delta_kind").String() == "text" {
		if content := result.Get("delta.content_delta").String(); content != "" {
			return content
		}
	}
	// plain text event
	if result.Get("part_kind").String() == "text" {
		if content := result.Get("content").String(); content != "" {
			return content
		}
	}
	return ""
}

func rovoCLITranslateToOpenAI(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) []byte {
	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	return sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), false)
}

func buildRovoCLIPrompt(openAIReq []byte) string {
	var parts []string
	root := gjson.ParseBytes(openAIReq)

	if system := root.Get("system"); system.Exists() {
		if s := extractOpenAIContent(system); strings.TrimSpace(s) != "" {
			parts = append(parts, "System: "+s)
		}
	}

	messages := root.Get("messages")
	if messages.IsArray() {
		messages.ForEach(func(_, msg gjson.Result) bool {
			role := strings.TrimSpace(msg.Get("role").String())
			content := extractOpenAIContent(msg.Get("content"))
			if strings.TrimSpace(content) == "" {
				return true
			}
			label := "User"
			switch role {
			case "system":
				label = "System"
			case "assistant":
				label = "Assistant"
			case "user":
				label = "User"
			case "tool":
				label = "Tool"
			default:
				if role != "" {
					label = strings.Title(role)
				}
			}
			parts = append(parts, fmt.Sprintf("%s: %s", label, content))
			return true
		})
	}

	return strings.Join(parts, "\n")
}

func extractOpenAIContent(content gjson.Result) string {
	if !content.Exists() {
		return ""
	}
	if content.Type == gjson.String {
		return content.String()
	}
	if content.IsArray() {
		var sb strings.Builder
		content.ForEach(func(_, item gjson.Result) bool {
			if item.Type == gjson.String {
				sb.WriteString(item.String())
				return true
			}
			if item.Get("type").String() == "text" {
				sb.WriteString(item.Get("text").String())
			}
			return true
		})
		return sb.String()
	}
	return ""
}

func buildOpenAIChatCompletionResponse(model, content string) []byte {
	payload := map[string]any{
		"id":      "chatcmpl_rovo_cli",
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{{
			"index": 0,
			"message": map[string]any{
				"role":    "assistant",
				"content": content,
			},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{
			"prompt_tokens":     0,
			"completion_tokens": 0,
			"total_tokens":      0,
		},
	}
	data, _ := json.Marshal(payload)
	return data
}

func buildOpenAIChatCompletionChunk(model, delta string, done bool) string {
	if done {
		chunk := map[string]any{
			"id":      "chatcmpl_rovo_cli",
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   model,
			"choices": []map[string]any{{
				"index":         0,
				"delta":         map[string]any{},
				"finish_reason": "stop",
			}},
		}
		data, _ := json.Marshal(chunk)
		return "data: " + string(data) + "\n\ndata: [DONE]\n\n"
	}
	chunk := map[string]any{
		"id":      "chatcmpl_rovo_cli",
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{{
			"index": 0,
			"delta": map[string]any{
				"content": delta,
			},
			"finish_reason": nil,
		}},
	}
	data, _ := json.Marshal(chunk)
	return "data: " + string(data) + "\n\n"
}
