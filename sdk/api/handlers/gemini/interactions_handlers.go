package gemini

import (
	"bytes"
	"context"
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

const interactionsAgentAuthSelectionModel = "gemini-2.5-flash"

type interactionsRequestTarget struct {
	Model  string
	Agent  string
	Stream bool
}

func parseInteractionsRequestTarget(rawJSON []byte) (interactionsRequestTarget, error) {
	if !gjson.ValidBytes(rawJSON) {
		return interactionsRequestTarget{}, fmt.Errorf("invalid JSON body")
	}
	root := gjson.ParseBytes(rawJSON)
	model := strings.TrimSpace(root.Get("model").String())
	agent := strings.TrimSpace(root.Get("agent").String())
	if model == "" && agent == "" {
		return interactionsRequestTarget{}, fmt.Errorf("request requires exactly one of model or agent")
	}
	if model != "" && agent != "" {
		return interactionsRequestTarget{}, fmt.Errorf("request requires exactly one of model or agent")
	}
	streamNode := root.Get("stream")
	stream := false
	if streamNode.Exists() {
		if !streamNode.IsBool() {
			return interactionsRequestTarget{}, fmt.Errorf("stream must be a boolean")
		}
		stream = streamNode.Bool()
	}
	return interactionsRequestTarget{Model: model, Agent: agent, Stream: stream}, nil
}

func prepareInteractionsExecutionTarget(rawJSON []byte, target interactionsRequestTarget) (string, []byte) {
	if target.Agent != "" {
		return target.Agent, rawJSON
	}
	model := normalizeGeminiModelResourceName(target.Model)
	if model == target.Model {
		return model, rawJSON
	}
	updatedRawJSON, errSet := sjson.SetBytes(rawJSON, "model", model)
	if errSet != nil {
		return model, rawJSON
	}
	return model, updatedRawJSON
}

func normalizeGeminiModelResourceName(model string) string {
	model = strings.TrimSpace(model)
	if strings.HasPrefix(model, "models/") && len(model) > len("models/") {
		return strings.TrimPrefix(model, "models/")
	}
	return model
}

func buildInteractionsExecutionRequest(target interactionsRequestTarget, modelName string, rawJSON []byte, alt string) handlers.ProtocolExecutionRequest {
	forcedProvider := ""
	authSelectionModel := ""
	if target.Agent != "" {
		forcedProvider = GeminiInteractions
		authSelectionModel = interactionsAgentAuthSelectionModel
	}
	return handlers.ProtocolExecutionRequest{
		EntryProtocol:      Interactions,
		ExitProtocol:       Interactions,
		ForcedProvider:     forcedProvider,
		AuthSelectionModel: authSelectionModel,
		Model:              modelName,
		Stream:             target.Stream,
		Body:               rawJSON,
		Alt:                alt,
	}
}

// Interactions handles POST /v1beta/interactions.
func (h *GeminiAPIHandler) Interactions(c *gin.Context) {
	rawJSON, errRead := c.GetRawData()
	if errRead != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{Error: handlers.ErrorDetail{Message: errRead.Error(), Type: "invalid_request_error"}})
		return
	}
	target, errParse := parseInteractionsRequestTarget(rawJSON)
	if errParse != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{Error: handlers.ErrorDetail{Message: errParse.Error(), Type: "invalid_request_error"}})
		return
	}

	modelName, resolvedRawJSON := prepareInteractionsExecutionTarget(rawJSON, target)
	rawJSON = resolvedRawJSON

	alt := h.GetAlt(c)
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	defer cliCancel(nil)

	req := buildInteractionsExecutionRequest(target, modelName, rawJSON, alt)
	if target.Stream {
		h.handleInteractionsStream(c, cliCtx, cliCancel, req)
		return
	}
	h.handleInteractionsNonStream(c, cliCtx, cliCancel, req)
}

func (h *GeminiAPIHandler) handleInteractionsNonStream(c *gin.Context, cliCtx context.Context, cliCancel handlers.APIHandlerCancelFunc, req handlers.ProtocolExecutionRequest) {
	c.Header("Content-Type", "application/json")
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)
	resp, errMsg := h.ExecuteProtocolWithAuthManager(cliCtx, req)
	stopKeepAlive()
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errMsg.Error)
		return
	}
	handlers.WriteUpstreamHeaders(c.Writer.Header(), resp.Headers)
	_, _ = c.Writer.Write(resp.Body)
}

func (h *GeminiAPIHandler) handleInteractionsStream(c *gin.Context, cliCtx context.Context, cliCancel handlers.APIHandlerCancelFunc, req handlers.ProtocolExecutionRequest) {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, handlers.ErrorResponse{Error: handlers.ErrorDetail{Message: "Streaming not supported", Type: "server_error"}})
		return
	}
	stream, errMsg := h.ExecuteProtocolStreamWithAuthManager(cliCtx, req)
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
	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage, 1)
	go func() {
		defer close(data)
		defer close(errs)
		for chunk := range stream.Chunks {
			if chunk.Err != nil {
				errs <- &interfaces.ErrorMessage{StatusCode: chunk.Err.StatusCode, Error: chunk.Err}
				return
			}
			if len(chunk.Payload) > 0 {
				data <- chunk.Payload
			}
		}
	}()
	h.forwardInteractionsStream(c, flusher, func(err error) { cliCancel(err) }, data, errs)
}

func (h *GeminiAPIHandler) forwardInteractionsStream(c *gin.Context, flusher http.Flusher, cancel func(error), data <-chan []byte, errs <-chan *interfaces.ErrorMessage) {
	h.ForwardStream(c, flusher, cancel, data, errs, handlers.StreamForwardOptions{
		WriteChunk: func(chunk []byte) {
			if len(chunk) == 0 {
				return
			}
			trimmed := bytes.TrimSpace(chunk)
			if bytes.HasPrefix(trimmed, []byte("event:")) || bytes.HasPrefix(trimmed, []byte("data:")) {
				_, _ = c.Writer.Write(chunk)
			} else {
				_, _ = c.Writer.Write([]byte("data: "))
				_, _ = c.Writer.Write(chunk)
			}
			if !bytes.HasSuffix(chunk, []byte("\n\n")) {
				_, _ = c.Writer.Write([]byte("\n\n"))
			}
		},
		WriteTerminalError: func(errMsg *interfaces.ErrorMessage) {
			if errMsg == nil {
				return
			}
			status := http.StatusInternalServerError
			if errMsg.StatusCode > 0 {
				status = errMsg.StatusCode
			}
			errText := http.StatusText(status)
			if errMsg.Error != nil && errMsg.Error.Error() != "" {
				errText = errMsg.Error.Error()
			}
			body := handlers.BuildErrorResponseBody(status, errText)
			_, _ = fmt.Fprintf(c.Writer, "event: error\ndata: %s\n\n", string(body))
		},
	})
}
