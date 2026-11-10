package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// Decisions handles the native, non-streaming OpenAI Decisions API.
func (h *OpenAIAPIHandler) Decisions(c *gin.Context) {
	body, err := handlers.ReadRequestBody(c)
	if err != nil {
		c.JSON(clienterror.HTTPStatusFromErrorOr(err, http.StatusBadRequest), handlers.ErrorResponse{
			Error: handlers.ErrorDetail{Message: fmt.Sprintf("Invalid request: %v", err), Type: "invalid_request_error"},
		})
		return
	}
	model := strings.Clone(ojson.GetBytesNoCopy(body, "model").String())
	ctx, cancel := h.GetContextWithCancel(h, c, context.Background())
	resp, headers, errMsg := h.ExecuteWithAuthManager(ctx, sdktranslator.FormatOpenAIDecisions.String(), model, body, "")
	if errMsg != nil {
		var upstream *helps.UpstreamResponseError
		if errors.As(errMsg.Error, &upstream) {
			errMsg.DirectResponse = true
			errMsg.Body = upstream.Body
			errMsg.Headers = http.Header{"Content-Type": upstream.Header.Values("Content-Type")}
			if handlers.PassthroughHeadersEnabled(h.CurrentConfig()) {
				handlers.WriteUpstreamHeaders(c.Writer.Header(), handlers.FilterUpstreamHeaders(upstream.Header))
			}
			for _, value := range coreauth.SafeResponseHeaders(errMsg.Error).Values("Retry-After") {
				c.Writer.Header().Set("Retry-After", value)
			}
		}
		h.WriteErrorResponse(c, errMsg)
		cancel(errMsg.Error)
		return
	}
	c.Header("Content-Type", "application/json")
	handlers.WriteUpstreamHeaders(c.Writer.Header(), headers)
	_, _ = c.Writer.Write(resp)
	cancel()
}
