package gemini

import (
	"context"
	"net/http"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
)

// Decisions handles POST /v1beta/decisions.
//
// The Decisions API is a native, non-streaming Gemini endpoint that carries the
// model name in the request body rather than the URL path, so it follows the same
// body-model parsing shape as the Interactions endpoint while executing through the
// action-aware auth manager path so executors can dispatch on the action.
func (h *GeminiAPIHandler) Decisions(c *gin.Context) {
	rawJSON, errRead := c.GetRawData()
	if errRead != nil {
		c.JSON(clienterror.HTTPStatusFromErrorOr(errRead, http.StatusBadRequest), handlers.ErrorResponse{Error: handlers.ErrorDetail{Message: errRead.Error(), Type: "invalid_request_error"}})
		return
	}
	if !ojson.ValidBytes(rawJSON) {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{Error: handlers.ErrorDetail{Message: "invalid JSON body", Type: "invalid_request_error"}})
		return
	}
	root := util.ParseGJSONBytesNoCopy(rawJSON)
	if streamNode := root.Get("stream"); streamNode.IsBool() && streamNode.Bool() {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{Error: handlers.ErrorDetail{Message: "decisions requests are non-streaming: stream must be false", Type: "invalid_request_error"}})
		return
	}
	modelName := normalizeGeminiModelResourceName(root.Get("model").String())
	if modelName == "" {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{Error: handlers.ErrorDetail{Message: "request requires a model", Type: "invalid_request_error"}})
		return
	}

	alt := h.GetAlt(c)
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	defer cliCancel(nil)

	c.Header("Content-Type", "application/json")
	resp, upstreamHeaders, errMsg := h.ExecuteGeminiActionWithAuthManager(cliCtx, h.HandlerType(), modelName, helps.GeminiDecisionsAction, rawJSON, alt)
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errMsg.Error)
		return
	}
	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
	_, _ = c.Writer.Write(resp)
}
