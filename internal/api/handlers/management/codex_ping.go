package management

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	codexPingDefaultModel     = "gpt-5.4-mini"
	codexPingDefaultReasoning = "none"
	codexPingDefaultPrompt    = "ping"
)

type codexPingUsage struct {
	InputTokens       int `json:"input_tokens"`
	CachedInputTokens int `json:"cached_input_tokens"`
	OutputTokens      int `json:"output_tokens"`
	ReasoningTokens   int `json:"reasoning_tokens"`
	TotalTokens       int `json:"total_tokens"`
}

type codexPingResponse struct {
	Message string         `json:"message"`
	Usage   codexPingUsage `json:"usage"`
}

// CodexPing sends a request-scoped stateless ping through a selected Codex auth file.
func (h *Handler) CodexPing(c *gin.Context) {
	var req struct {
		AuthIndexSnake  *string `json:"auth_index"`
		AuthIndexCamel  *string `json:"authIndex"`
		AuthIndexPascal *string `json:"AuthIndex"`
	}
	if errBindJSON := c.ShouldBindJSON(&req); errBindJSON != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	authIndex := firstNonEmptyString(req.AuthIndexSnake, req.AuthIndexCamel, req.AuthIndexPascal)
	if authIndex == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing auth_index"})
		return
	}
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "auth manager unavailable"})
		return
	}

	auth := h.authByIndex(authIndex)
	if auth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth file not found"})
		return
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth file is not codex"})
		return
	}
	if auth.Disabled {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth file is disabled"})
		return
	}
	if isCodexAPIKeyConfigAuth(auth) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "codex api key configs are not supported"})
		return
	}

	logger := log.WithFields(log.Fields{
		"auth_index":    authIndex,
		"auth_id":       auth.ID,
		"model":         codexPingDefaultModel,
		"prompt_length": len(codexPingDefaultPrompt),
		"prompt_sha256": codexPingPromptHash(codexPingDefaultPrompt),
	})
	logger.Info("management codex ping sending request")

	result, errPing := h.sendCodexPing(c.Request.Context(), auth)
	if errPing != nil {
		logger = logger.WithError(errPing)
		if upstreamStatus := codexPingUpstreamStatus(errPing); upstreamStatus > 0 {
			logger = logger.WithField("upstream_status", upstreamStatus)
		}
		logger.Warn("management codex ping failed")
		c.JSON(http.StatusBadGateway, gin.H{"error": safeCodexPingError(errPing)})
		return
	}

	logger.WithFields(log.Fields{
		"input_tokens":        result.Usage.InputTokens,
		"cached_input_tokens": result.Usage.CachedInputTokens,
		"output_tokens":       result.Usage.OutputTokens,
		"reasoning_tokens":    result.Usage.ReasoningTokens,
		"total_tokens":        result.Usage.TotalTokens,
	}).Info("management codex ping succeeded")

	c.JSON(http.StatusOK, result)
}

func (h *Handler) sendCodexPing(ctx context.Context, auth *coreauth.Auth) (codexPingResponse, error) {
	body, errMarshal := json.Marshal(map[string]any{
		"model":               codexPingDefaultModel,
		"input":               codexPingDefaultPrompt,
		"stream":              true,
		"store":               false,
		"parallel_tool_calls": true,
		"reasoning": map[string]string{
			"effort": codexPingDefaultReasoning,
		},
	})
	if errMarshal != nil {
		return codexPingResponse{}, errMarshal
	}

	executor, okExecutor := h.authManager.Executor("codex")
	if !okExecutor || executor == nil {
		return codexPingResponse{}, fmt.Errorf("codex executor not registered")
	}

	streamResult, errStream := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   codexPingDefaultModel,
		Payload: body,
		Format:  sdktranslator.FromString("openai-response"),
	}, cliproxyexecutor.Options{
		Stream:          true,
		SourceFormat:    sdktranslator.FromString("openai-response"),
		OriginalRequest: body,
		Metadata: map[string]any{
			cliproxyexecutor.PinnedAuthMetadataKey:     auth.ID,
			cliproxyexecutor.RequestedModelMetadataKey: codexPingDefaultModel,
			cliproxyexecutor.RequestPathMetadataKey:    "/v0/management/codex/ping",
		},
	})
	if errStream != nil {
		return codexPingResponse{}, errStream
	}
	if streamResult == nil || streamResult.Chunks == nil {
		return codexPingResponse{}, fmt.Errorf("codex executor returned empty stream")
	}

	var stream bytes.Buffer
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			return codexPingResponse{}, chunk.Err
		}
		if len(chunk.Payload) == 0 {
			continue
		}
		stream.Write(chunk.Payload)
		if !bytes.HasSuffix(chunk.Payload, []byte("\n")) {
			stream.WriteByte('\n')
		}
	}

	message, usage, completed := parseCodexPingStream(stream.Bytes())
	if !completed {
		return codexPingResponse{}, fmt.Errorf("stream closed before response.completed")
	}
	if strings.TrimSpace(message) == "" {
		message = "Pong"
	}
	return codexPingResponse{Message: strings.TrimSpace(message), Usage: usage}, nil
}

func isCodexAPIKeyConfigAuth(auth *coreauth.Auth) bool {
	if auth == nil || auth.Attributes == nil {
		return false
	}
	apiKey := strings.TrimSpace(auth.Attributes["api_key"])
	path := strings.TrimSpace(auth.Attributes["path"])
	return apiKey != "" && path == ""
}

func parseCodexPingStream(data []byte) (string, codexPingUsage, bool) {
	var text strings.Builder
	var fallbackText string
	var usage codexPingUsage
	completed := false

	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		raw := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if raw == "" || raw == "[DONE]" {
			continue
		}

		event := gjson.Parse(raw)
		eventType := event.Get("type").String()
		switch eventType {
		case "response.output_text.delta":
			text.WriteString(event.Get("delta").String())
		case "response.output_text.done":
			if fallbackText == "" {
				fallbackText = event.Get("text").String()
			}
		case "response.output_item.done":
			if fallbackText == "" {
				fallbackText = codexPingTextFromOutputItem(event.Get("item"))
			}
		case "response.completed":
			completed = true
			if fallbackText == "" {
				fallbackText = codexPingTextFromCompletedResponse(event.Get("response"))
			}
			if usageResult := event.Get("response.usage"); usageResult.Exists() {
				usage = codexPingUsageFromResult(usageResult)
			}
		}
	}

	message := text.String()
	if strings.TrimSpace(message) == "" {
		message = fallbackText
	}
	return message, usage, completed
}

func codexPingTextFromCompletedResponse(response gjson.Result) string {
	if text := response.Get("output_text").String(); strings.TrimSpace(text) != "" {
		return text
	}
	var out strings.Builder
	response.Get("output").ForEach(func(_, item gjson.Result) bool {
		text := codexPingTextFromOutputItem(item)
		if strings.TrimSpace(text) != "" {
			out.WriteString(text)
		}
		return true
	})
	return out.String()
}

func codexPingTextFromOutputItem(item gjson.Result) string {
	var out strings.Builder
	item.Get("content").ForEach(func(_, content gjson.Result) bool {
		if text := content.Get("text").String(); strings.TrimSpace(text) != "" {
			out.WriteString(text)
			return true
		}
		if text := content.Get("transcript").String(); strings.TrimSpace(text) != "" {
			out.WriteString(text)
		}
		return true
	})
	return out.String()
}

func codexPingUsageFromResult(usage gjson.Result) codexPingUsage {
	input, _ := codexPingInt(usage, "input_tokens", "inputTokens")
	output, _ := codexPingInt(usage, "output_tokens", "outputTokens")
	total, okTotal := codexPingInt(usage, "total_tokens", "totalTokens")
	cached, _ := codexPingInt(usage, "input_tokens_details.cached_tokens", "inputTokensDetails.cachedTokens", "cached_input_tokens", "cachedInputTokens")
	reasoning, _ := codexPingInt(usage, "output_tokens_details.reasoning_tokens", "outputTokensDetails.reasoningTokens", "reasoning_tokens", "reasoningTokens")
	if !okTotal {
		total = input + output
	}
	return codexPingUsage{
		InputTokens:       input,
		CachedInputTokens: cached,
		OutputTokens:      output,
		ReasoningTokens:   reasoning,
		TotalTokens:       total,
	}
}

func codexPingInt(result gjson.Result, keys ...string) (int, bool) {
	for _, key := range keys {
		value := result.Get(key)
		if !value.Exists() {
			continue
		}
		return int(value.Int()), true
	}
	return 0, false
}

func codexPingPromptHash(prompt string) string {
	sum := sha256.Sum256([]byte(prompt))
	return hex.EncodeToString(sum[:])
}

func codexPingUpstreamStatus(err error) int {
	var statusErr cliproxyexecutor.StatusError
	if errors.As(err, &statusErr) && statusErr != nil {
		return statusErr.StatusCode()
	}
	return 0
}

func safeCodexPingError(err error) string {
	if upstreamStatus := codexPingUpstreamStatus(err); upstreamStatus > 0 {
		return fmt.Sprintf("codex ping failed (upstream status %d)", upstreamStatus)
	}
	return "codex ping failed"
}
