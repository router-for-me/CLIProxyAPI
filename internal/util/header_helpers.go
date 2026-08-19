package util

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// ApplyCustomHeadersFromAttrs applies user-defined headers stored in the provided attributes map.
// Custom headers override built-in defaults when conflicts occur.
// If clientHeaders is provided (or if the request context carries a Gin context), any custom header
// whose value starts with "$" (e.g. "$ABC" or "$X-Claude-Code-Session-Id") is dynamically
// resolved from the client's request headers. If the client did not provide that header,
// the custom header is omitted from the outgoing request.
func ApplyCustomHeadersFromAttrs(r *http.Request, attrs map[string]string, clientHeaders ...http.Header) {
	if r == nil {
		return
	}
	var ch http.Header
	if len(clientHeaders) > 0 && clientHeaders[0] != nil {
		ch = clientHeaders[0]
	} else if r.Context() != nil {
		if ginCtx, ok := r.Context().Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
			ch = ginCtx.Request.Header
		} else if ginCtx, ok := r.Context().(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
			ch = ginCtx.Request.Header
		}
	}
	applyCustomHeaders(r, extractCustomHeaders(attrs, ch))
}

func extractCustomHeaders(attrs map[string]string, clientHeaders http.Header) map[string]string {
	if len(attrs) == 0 {
		return nil
	}
	headers := make(map[string]string)
	for k, v := range attrs {
		if !strings.HasPrefix(k, "header:") {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(k, "header:"))
		if name == "" {
			continue
		}
		val := strings.TrimSpace(v)
		if val == "" {
			continue
		}
		if strings.HasPrefix(val, "$") {
			varName := strings.TrimSpace(strings.TrimPrefix(val, "$"))
			if varName == "" || clientHeaders == nil {
				continue
			}
			clientVal := clientHeaders.Get(varName)
			if clientVal == "" {
				for ck, cv := range clientHeaders {
					if strings.EqualFold(ck, varName) && len(cv) > 0 && cv[0] != "" {
						clientVal = cv[0]
						break
					}
				}
			}
			if clientVal == "" {
				continue
			}
			val = clientVal
		}
		headers[name] = val
	}
	if len(headers) == 0 {
		return nil
	}
	return headers
}

func applyCustomHeaders(r *http.Request, headers map[string]string) {
	if r == nil || len(headers) == 0 {
		return
	}
	for k, v := range headers {
		if k == "" || v == "" {
			continue
		}
		// net/http reads Host from req.Host (not req.Header) when writing
		// a real request, so we must mirror it there. Some callers pass
		// synthetic requests (e.g. &http.Request{Header: ...}) and only
		// consume r.Header afterwards, so keep the value in the header
		// map too.
		if http.CanonicalHeaderKey(k) == "Host" {
			r.Host = v
		}
		r.Header.Set(k, v)
	}
}
