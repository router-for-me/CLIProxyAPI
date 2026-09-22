package helps

import (
	"net/http"
	"strings"
)

// ApplyVertexRequestHeaders forwards the client's explicit Vertex serving tier.
// Missing headers leave the default tier unchanged; credentials are never copied.
// Apply configured auth headers afterwards so administrator overrides still win.
func ApplyVertexRequestHeaders(req *http.Request, incoming http.Header) {
	if req == nil {
		return
	}
	for _, name := range []string{
		"X-Vertex-AI-LLM-Request-Type",
		"X-Vertex-AI-LLM-Shared-Request-Type",
	} {
		if value := strings.TrimSpace(incoming.Get(name)); value != "" {
			if req.Header == nil {
				req.Header = make(http.Header)
			}
			req.Header.Set(name, value)
		}
	}
}
