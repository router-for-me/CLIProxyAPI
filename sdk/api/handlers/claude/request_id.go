package claude

import (
	"encoding/json"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/tidwall/gjson"
)

const (
	claudeRequestIDHeader = "Request-Id"
	claudeRequestIDKey    = "__claude_request_id__"
)

// EnsureRequestID assigns a stable Anthropic response ID at request ingress.
// Locally generated IDs retain the logging request ID with a req_ prefix.
// Client-supplied request headers are not used as server response IDs.
func EnsureRequestID(c *gin.Context) string {
	if c == nil {
		return ""
	}
	requestID := c.GetString(claudeRequestIDKey)
	if requestID == "" {
		requestID = logging.GetGinRequestID(c)
		if !validClaudeRequestID(requestID) {
			requestID = logging.GenerateRequestID()
			logging.SetGinRequestID(c, requestID)
		}
		if !strings.HasPrefix(requestID, "req_") {
			requestID = "req_" + requestID
		}
		c.Set(claudeRequestIDKey, requestID)
	}
	if !c.Writer.Written() {
		c.Header(claudeRequestIDHeader, requestID)
	}
	return requestID
}

func setClaudeRequestID(c *gin.Context, upstreamID string) string {
	requestID := EnsureRequestID(c)
	// A keep-alive or SSE flush makes the response header immutable. Errors
	// after that point must use the ID already sent to the client.
	if c == nil || c.Writer.Written() || !validClaudeRequestID(upstreamID) {
		return requestID
	}
	c.Set(claudeRequestIDKey, upstreamID)
	c.Header(claudeRequestIDHeader, upstreamID)
	return upstreamID
}

func claudeRequestIDFromText(text string) string {
	if !json.Valid([]byte(text)) || gjson.Get(text, "type").String() != "error" || !gjson.Get(text, "error").IsObject() {
		return ""
	}
	id := gjson.Get(text, "request_id")
	if id.Type != gjson.String || !validClaudeRequestID(id.String()) {
		return ""
	}
	return id.String()
}

func validClaudeRequestID(id string) bool {
	if len(id) == 0 || len(id) > 256 {
		return false
	}
	for i := range id {
		if id[i] < '!' || id[i] > '~' {
			return false
		}
	}
	return true
}
