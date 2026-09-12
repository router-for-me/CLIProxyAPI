package grokbuild

import (
	"bytes"
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

var keepaliveSSEComment = []byte(": keepalive\n\n")

// KeepaliveSSEComment returns the standard SSE comment used for keepalive.
func KeepaliveSSEComment() []byte {
	return bytes.Clone(keepaliveSSEComment)
}

// IsGrokClientUserAgent checks if the user agent contains "grok-pager" or "grok-shell".
func IsGrokClientUserAgent(userAgent string) bool {
	ua := strings.ToLower(userAgent)
	return strings.Contains(ua, "grok-pager") || strings.Contains(ua, "grok-shell")
}

// IsGrokClientHeaders checks if the provided HTTP headers indicate a Grok client.
func IsGrokClientHeaders(headers http.Header) bool {
	if headers == nil {
		return false
	}
	for key, values := range headers {
		if strings.EqualFold(key, "User-Agent") {
			if slices.ContainsFunc(values, IsGrokClientUserAgent) {
				return true
			}
		}
	}
	return false
}

// IsGrokClientContext checks if either the context (e.g. Gin context) or headers indicate a Grok client.
func IsGrokClientContext(ctx context.Context, headers http.Header) bool {
	if ctx != nil {
		if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
			if IsGrokClientHeaders(ginCtx.Request.Header) {
				return true
			}
		}
	}
	return IsGrokClientHeaders(headers)
}

// IsKeepalivePayload reports whether a JSON payload has type "keepalive".
func IsKeepalivePayload(payload []byte) bool {
	return gjson.GetBytes(payload, "type").String() == "keepalive"
}

// IsKeepaliveSSELine reports whether an SSE line represents a keepalive event or data frame.
func IsKeepaliveSSELine(line []byte) bool {
	trimmed := bytes.TrimSpace(line)
	if bytes.HasPrefix(trimmed, []byte("event:")) {
		eventName := bytes.TrimSpace(trimmed[6:])
		return bytes.Equal(eventName, []byte("keepalive"))
	}
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		data := bytes.TrimSpace(trimmed[5:])
		return IsKeepalivePayload(data)
	}
	return false
}

// TransformKeepaliveSSELine transforms a keepalive SSE line into an SSE comment line
// when isGrokClient is true. If the line is not a keepalive line or isGrokClient is false,
// it returns the original line and false.
func TransformKeepaliveSSELine(line []byte, isGrokClient bool) ([]byte, bool) {
	if !isGrokClient {
		return line, false
	}
	if IsKeepaliveSSELine(line) {
		return bytes.Clone(keepaliveSSEComment), true
	}
	return line, false
}
