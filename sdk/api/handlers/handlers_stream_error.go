package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/errormessages"
)

// BuildStreamErrorBody renders the registry-backed error body for the given
// status code and detail text, serialized to JSON bytes. It is the streaming
// counterpart to WriteErrorResponse: streaming handlers use it inside their
// WriteTerminalError callbacks (which already own the SSE framing) so that
// mid-stream upstream errors honor operator overrides exactly like their
// non-streaming siblings.
func BuildStreamErrorBody(c *gin.Context, status int, details string) []byte {
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	body := errormessages.BodyFor(c.Request.Context(), status, details)
	raw, err := json.Marshal(body)
	if err != nil {
		// Should not happen for our gin.H shape, but never break the response.
		return BuildErrorResponseBody(status, details)
	}
	return raw
}

// StreamErrorMessageText returns the operator-configured message text for a
// status code (with placeholders substituted) for use inside protocol-
// specific streaming error envelopes whose shape cannot be replaced by the
// registry's default JSON body — e.g. OpenAI Responses SSE chunks with a
// fixed {type,code,message} structure. This lets the `message` field still
// honor operator overrides (custom text, {{details}} expansion) while the
// caller keeps its protocol envelope intact.
func StreamErrorMessageText(c *gin.Context, status int, details string) string {
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	return errormessages.MessageText(c.Request.Context(), status, details)
}
