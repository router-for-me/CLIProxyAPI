package claude

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/ccsadvisor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
)

const advisorToolType = "advisor_20260301"

// tryCCSAdvisor returns the original bytes for ordinary requests. The one
// exception is the local worker alias, which also applies to native streams.
func (h *ClaudeCodeAPIHandler) tryCCSAdvisor(c *gin.Context, body []byte) ([]byte, bool) {
	advisor := strings.TrimSpace(os.Getenv("CCS_CODEX_ADVISOR_MODEL"))
	if advisor == "" {
		return body, false
	}
	local := c.GetHeader("X-CCS-Local-Advisor") == "1"
	model := ojson.GetBytesNoCopy(body, "model").String()
	worker := model
	if local && model == "claude-sonnet-5-5" {
		worker = strings.TrimSpace(os.Getenv("CCS_CODEX_WORKER_MODEL"))
	}
	if !strings.HasPrefix(worker, "gpt-") || !h.HasExclusiveModelProvider(worker, "codex") {
		return body, false
	}
	if worker != model {
		doc := ojson.NewDoc(body)
		if err := doc.Set("model", worker); err != nil {
			return body, false
		}
		body = doc.Bytes()
	}
	tools := ojson.GetBytesNoCopy(body, "tools")
	declared := false
	for _, tool := range tools.Array() {
		if tool.Get("name").String() == "advisor" {
			if tool.Get("type").String() != advisorToolType {
				return body, false
			}
			declared = true
		}
	}
	if !declared && (!local || !interactiveAdvisorTurn(c.Request.Header, body, tools)) {
		return body, false
	}
	var req ccsadvisor.Message
	if err := json.Unmarshal(body, &req); err != nil {
		h.WriteErrorResponse(c, &interfaces.ErrorMessage{StatusCode: http.StatusBadRequest, Error: err})
		return nil, true
	}
	if !declared {
		defs, _ := req["tools"].([]any)
		req["tools"] = append(defs, ccsadvisor.Message{"name": "advisor", "type": advisorToolType, "model": advisor})
	}
	if err := ccsadvisor.ValidateDeclaration(req); err != nil {
		h.WriteErrorResponse(c, &interfaces.ErrorMessage{StatusCode: http.StatusBadRequest, Error: err})
		return nil, true
	}
	h.runCCSAdvisor(c, req, worker, advisor)
	return nil, true
}

func interactiveAdvisorTurn(headers http.Header, body []byte, tools ojson.Result) bool {
	ua := strings.ToLower(headers.Get("User-Agent"))
	if headers.Get("X-App") == "cli-bg" || strings.Contains(ua, "request-phase/background") || strings.Contains(ua, "workload/") {
		return false
	}
	if !tools.IsArray() || helps.IsClaudeProbeOrHelperRequest(body) || helps.IsClaudeSubagentRequest(headers, body) || ojson.GetBytesNoCopy(body, "output_config.format").Exists() || ojson.GetBytesNoCopy(body, "tool_choice.type").String() == "tool" {
		return false
	}
	// The local header may be present on every request from the gateway. Only
	// accept Claude Code's positive billing attribution for a human turn.
	billing := ojson.GetBytesNoCopy(body, "system.0.text").String()
	attrs, ok := strings.CutPrefix(billing, "x-anthropic-billing-header:")
	if !ok {
		return false
	}
	human, cliEntry := false, false
	for field := range strings.SplitSeq(attrs, ";") {
		field = strings.TrimSpace(field)
		if strings.HasPrefix(field, "cc_workload=") {
			return false
		}
		if field == "cc_turn_origin=human" {
			human = true
		} else if strings.HasPrefix(field, "cc_turn_origin=") {
			return false
		} else if field == "cc_entrypoint=sdk-cli" || field == "cc_entrypoint=cli" {
			cliEntry = true
		}
	}
	if !human {
		// 2.1.327 may omit cc_turn_origin on primary turns. Require its
		// session header and main agent toolset, never a bare classifier call.
		mainTools := map[string]bool{}
		for _, tool := range tools.Array() {
			mainTools[tool.Get("name").String()] = true
		}
		if !cliEntry || headers.Get("X-Claude-Code-Session-Id") == "" || !(mainTools["Agent"] && mainTools["Read"] && mainTools["Bash"]) {
			return false
		}
	}
	msgs := ojson.GetBytesNoCopy(body, "messages").Array()
	// Newer CLI builds append system reminders after the actual user message.
	for len(msgs) > 0 && msgs[len(msgs)-1].Get("role").String() == "system" {
		msgs = msgs[:len(msgs)-1]
	}
	if len(msgs) == 0 {
		return false
	}
	last := msgs[len(msgs)-1]
	if last.Get("role").String() != "user" {
		return false
	}
	content := last.Get("content")
	hasText := content.Type == ojson.String && strings.TrimSpace(content.String()) != ""
	for _, b := range content.Array() {
		if b.Get("type").String() == "tool_result" || (b.Get("type").String() == "text" && strings.TrimSpace(b.Get("text").String()) != "") {
			hasText = true
		}
	}
	if !hasText {
		return false
	}
	for _, tool := range tools.Array() {
		if tool.Get("input_schema").IsObject() && tool.Get("name").String() != "" {
			return true
		}
	}
	return false
}

func (h *ClaudeCodeAPIHandler) runCCSAdvisor(c *gin.Context, req ccsadvisor.Message, worker, advisor string) {
	ctx, cancel := h.GetContextWithCancel(h, c, c.Request.Context())
	defer cancel()
	stream, _ := req["stream"].(bool)
	req["stream"] = false
	alt := h.GetAlt(c)
	var upstreamErr *interfaces.ErrorMessage
	var headers http.Header
	stopKeepAlive := h.startAdvisorKeepAlive(c, ctx, stream)
	defer stopKeepAlive()
	result, err := ccsadvisor.Run(ctx, req, advisor, func(callCtx context.Context, step ccsadvisor.Message) (ccsadvisor.Message, error) {
		if err := callCtx.Err(); err != nil {
			return nil, err
		}
		model, _ := step["model"].(string)
		if (model != worker && model != advisor) || !h.HasExclusiveModelProvider(model, "codex") {
			return nil, errors.New("advisor execution requires an exclusive Codex route")
		}
		payload, err := json.Marshal(step)
		if err != nil {
			return nil, err
		}
		resp, errMsg := h.ExecuteProtocolWithAuthManager(callCtx, handlers.ProtocolExecutionRequest{
			EntryProtocol: h.HandlerType(), ExitProtocol: h.HandlerType(), Model: model,
			ForcedProvider: "codex", Body: payload, Alt: alt,
		})
		if errMsg != nil {
			upstreamErr = errMsg
			if errMsg.Error != nil {
				return nil, errMsg.Error
			}
			return nil, errors.New("advisor upstream failed")
		}
		var message ccsadvisor.Message
		if err := json.Unmarshal(resp.Body, &message); err != nil {
			return nil, fmt.Errorf("invalid upstream message: %w", err)
		}
		if message["type"] != "message" {
			return nil, errors.New("expected a Messages API response")
		}
		blocks, ok := message["content"].([]any)
		if !ok {
			return nil, errors.New("upstream message has no content array")
		}
		for i, value := range blocks {
			block, _ := value.(ccsadvisor.Message)
			if kind, _ := block["type"].(string); kind == "" {
				return nil, fmt.Errorf("invalid upstream content block at index %d", i)
			}
		}
		headers = resp.Headers
		return message, nil
	})
	stopKeepAlive() // Join the heartbeat writer before accessing the response again.
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		if upstreamErr == nil {
			upstreamErr = &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: err}
		}
		if !c.Writer.Written() {
			h.WriteErrorResponse(c, upstreamErr)
			return
		}
		body, _ := json.Marshal(h.toClaudeError(upstreamErr))
		if stream {
			h.LoggingAPIResponseError(c, upstreamErr)
			_, _ = fmt.Fprintf(c.Writer, "event: error\ndata: %s\n\n", body)
			c.Writer.Flush()
		} else {
			appendClaudeAPIResponse(c, body)
			_, _ = c.Writer.Write(body)
		}
		return
	}
	if !c.Writer.Written() {
		if stream {
			setAdvisorSSEHeaders(c)
		}
		handlers.WriteUpstreamHeaders(c.Writer.Header(), headers)
	}
	if !stream {
		c.JSON(http.StatusOK, result)
		return
	}
	if err := writeAdvisorEvents(ctx, c.Writer, result); err != nil && ctx.Err() == nil {
		h.LoggingAPIResponseError(c, &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: err})
	}
}

func setAdvisorSSEHeaders(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
}

func (h *ClaudeCodeAPIHandler) startAdvisorKeepAlive(c *gin.Context, ctx context.Context, stream bool) func() {
	if !stream {
		c.Header("Content-Type", "application/json")
		return h.StartNonStreamingKeepAlive(c, ctx)
	}
	ticks, stopTicker := handlers.NewPreFirstTokenKeepAliveTicker(h.CurrentConfig())
	if ticks == nil {
		return stopTicker
	}
	ctx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer stopTicker()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticks:
				if !c.Writer.Written() {
					setAdvisorSSEHeaders(c)
				}
				if _, err := c.Writer.WriteString(": keep-alive\n\n"); err != nil {
					return
				}
				c.Writer.Flush()
			}
		}
	}()
	return func() {
		stop()
		<-done
	}
}
