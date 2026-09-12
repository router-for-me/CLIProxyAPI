package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	. "github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func shouldUseClaudeResponsesBridge(clientModel, upstreamModel string) bool {
	if clientModel == "" || clientModel == upstreamModel {
		return false
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(upstreamModel)), "gpt-")
}

func (h *ClaudeCodeAPIHandler) handleResponsesBridge(c *gin.Context, rawJSON []byte, clientModel string) {
	compactRequest := isClaudeCompactRequest(rawJSON)
	upstreamModel := gjson.GetBytes(rawJSON, "model").String()
	preparedJSON, _, errPrepare := prepareClaudeCompactionReplay(rawJSON, upstreamModel)
	if errPrepare != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{Message: errPrepare.Error(), Type: "invalid_request_error"},
		})
		return
	}
	if compactRequest {
		h.handleCompactResponsesBridge(c, preparedJSON, clientModel)
		return
	}
	if gjson.GetBytes(rawJSON, "stream").Bool() {
		h.handleStreamingResponsesBridge(c, preparedJSON, clientModel)
		return
	}
	h.handleNonStreamingResponsesBridge(c, preparedJSON, clientModel)
}

func (h *ClaudeCodeAPIHandler) handleNonStreamingResponsesBridge(c *gin.Context, rawJSON []byte, clientModel string) {
	c.Header("Content-Type", "application/json")
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	modelName := gjson.GetBytes(rawJSON, "model").String()
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)

	response, errMsg := h.ExecuteProtocolWithAuthManager(cliCtx, handlers.ProtocolExecutionRequest{
		EntryProtocol:  Claude,
		ExitProtocol:   Claude,
		ForcedProvider: Codex,
		Model:          modelName,
		Body:           rawJSON,
		Headers:        c.Request.Header.Clone(),
		Query:          c.Request.URL.Query(),
		Alt:            ClaudeResponsesBridgeAlt,
	})
	stopKeepAlive()
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errMsg.Error)
		return
	}

	handlers.WriteUpstreamHeaders(c.Writer.Header(), response.Headers)
	_, _ = c.Writer.Write(rewriteClaudeBridgeResponseModel(response.Body, clientModel))
	cliCancel()
}

func (h *ClaudeCodeAPIHandler) handleStreamingResponsesBridge(c *gin.Context, rawJSON []byte, clientModel string) {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{Message: "Streaming not supported", Type: "server_error"},
		})
		return
	}

	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	modelName := gjson.GetBytes(rawJSON, "model").String()
	stream, errMsg := h.ExecuteProtocolStreamWithAuthManager(cliCtx, handlers.ProtocolExecutionRequest{
		EntryProtocol:  Claude,
		ExitProtocol:   Claude,
		ForcedProvider: Codex,
		Model:          modelName,
		Stream:         true,
		Body:           rawJSON,
		Headers:        c.Request.Header.Clone(),
		Query:          c.Request.URL.Query(),
		Alt:            ClaudeResponsesBridgeAlt,
	})
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errMsg.Error)
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("Access-Control-Allow-Origin", "*")
	handlers.WriteUpstreamHeaders(c.Writer.Header(), stream.Headers)

	for {
		select {
		case <-c.Request.Context().Done():
			cliCancel(c.Request.Context().Err())
			return
		case chunk, okChunk := <-stream.Chunks:
			if !okChunk {
				flusher.Flush()
				cliCancel()
				return
			}
			if chunk.Err != nil {
				h.writeResponsesBridgeStreamError(c, chunk.Err)
				flusher.Flush()
				cliCancel(chunk.Err)
				return
			}
			if len(chunk.Payload) == 0 {
				continue
			}
			_, _ = c.Writer.Write(rewriteClaudeBridgeResponseModel(chunk.Payload, clientModel))
			flusher.Flush()
		}
	}
}

func (h *ClaudeCodeAPIHandler) writeResponsesBridgeStreamError(c *gin.Context, streamErr *handlers.ModelExecutionStreamError) {
	if streamErr == nil {
		return
	}
	errMsg := &interfaces.ErrorMessage{StatusCode: streamErr.StatusCode, Error: streamErr}
	errorBytes, errMarshal := json.Marshal(h.toClaudeError(errMsg))
	if errMarshal != nil {
		errorBytes = []byte(`{"type":"error","error":{"type":"api_error","message":"stream failed"}}`)
	}
	_, _ = fmt.Fprintf(c.Writer, "event: error\ndata: %s\n\n", errorBytes)
}

func rewriteClaudeBridgeResponseModel(body []byte, clientModel string) []byte {
	if len(body) == 0 || clientModel == "" {
		return body
	}
	if gjson.ValidBytes(body) && gjson.GetBytes(body, "type").String() == "message" {
		if updated, errSet := sjson.SetBytes(body, "model", clientModel); errSet == nil {
			return updated
		}
		return body
	}

	lines := bytes.Split(body, []byte("\n"))
	changed := false
	for i, line := range lines {
		trimmed := bytes.TrimSpace(line)
		if !bytes.HasPrefix(trimmed, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(trimmed[len("data:"):])
		if !gjson.ValidBytes(payload) || gjson.GetBytes(payload, "type").String() != "message_start" {
			continue
		}
		updated, errSet := sjson.SetBytes(payload, "message.model", clientModel)
		if errSet != nil {
			continue
		}
		lines[i] = append([]byte("data: "), updated...)
		changed = true
	}
	if !changed {
		return body
	}
	return bytes.Join(lines, []byte("\n"))
}
