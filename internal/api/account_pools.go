package api

import "net/http"

// accountPoolRouteSupported reports whether an endpoint selects its credential
// in a way that honours account pools: through model execution, which applies
// the pool policy in the scheduler, or as one of the Codex-only routes, whose
// handlers select a Codex login the same way. Model lists and model details,
// OpenAI and Gemini alike, only show models that a credential in the key's
// pools serves.
func accountPoolRouteSupported(method, path string) bool {
	if codexOnlyRoute(method, path) {
		return true
	}
	if method == http.MethodGet {
		switch path {
		case "/v1/models", "/v1/models/*model", "/v1beta/models", "/v1beta/models/*action",
			"/v1/responses", "/backend-api/codex/responses":
			return true
		}
		return false
	}
	if method != http.MethodPost {
		return false
	}
	switch path {
	case "/v1/messages", "/v1/messages/count_tokens", "/v1/chat/completions", "/v1/completions",
		"/v1/responses", "/v1/responses/compact",
		"/backend-api/codex/responses", "/backend-api/codex/responses/compact":
		return true
	}
	return false
}
