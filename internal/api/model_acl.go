// Per-API-key model ACL enforcement.
//
// AuthMiddleware identifies the calling client by api key value. ModelACLMiddleware
// then checks the model targeted by the request against that key's allowed-models
// policy (configured via SDKConfig.APIKeyPolicies / APIKeyDefaultPolicy) and
// rejects the request with HTTP 403 on mismatch.
//
// The middleware extracts the model identifier from whichever location the
// upstream route uses:
//
//   - JSON body field "model" (chat completions, messages, responses, codex)
//   - URL path segment after /v1beta/models/ (Gemini generative endpoints)
//
// When no model can be determined (e.g. listing endpoints, websocket upgrades),
// the middleware allows the request through; access enforcement remains the
// responsibility of the route's own logic in those cases.

package api

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/tidwall/gjson"
)

// modelACLMaxBodyBytes caps the request body the ACL middleware is willing to
// buffer in order to extract the "model" field. Set generously enough to
// accommodate real chat-completion payloads (long prompts, many turns) while
// bounding memory growth per request. Requests above this size are rejected
// with HTTP 413 so they cannot silently bypass policy by being too large to
// inspect.
const modelACLMaxBodyBytes int64 = 10 * 1024 * 1024 // 10 MiB

// modelACLPeekBytes is the size of the initial read the middleware performs
// before committing to buffering the full body. Real-world chat-completion
// payloads place "model" within the first few hundred bytes of JSON, so a
// 16 KiB peek is nearly always sufficient. When "model" is visible in the
// peek, the middleware avoids allocating up to modelACLMaxBodyBytes per
// request — under concurrency this is the difference between a bounded
// constant memory footprint and N*10 MiB.
const modelACLPeekBytes int64 = 16 * 1024 // 16 KiB

// modelACLMultipartMemoryCap is the in-memory portion of a multipart form
// parse. Larger file fields spool to temporary disk (stdlib behavior), so the
// effective per-request memory ceiling for multipart inspection stays small
// even when the upload itself is large. Image-edit endpoints fit here without
// triggering the 10 MiB cap that bounds JSON inspection.
const modelACLMultipartMemoryCap int64 = 1 * 1024 * 1024 // 1 MiB

// errBodyTooLarge is used as a sentinel so extractRequestedModel can
// distinguish a client payload that exceeded the cap from an I/O failure.
var errBodyTooLarge = errors.New("model_acl: request body exceeds cap")

// ModelACLMiddleware enforces SDKConfig.APIKeyPolicies for the routes it is
// installed on. The cfgFn closure is evaluated on every request so that hot
// config reloads take effect immediately.
func ModelACLMiddleware(cfgFn func() *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		cfg := cfgFn()
		if cfg == nil {
			c.Next()
			return
		}

		// Skip enforcement when no policies are configured AND the default
		// policy permits everything; the per-key check would be a no-op.
		if len(cfg.APIKeyPolicies) == 0 && !strings.EqualFold(strings.TrimSpace(cfg.APIKeyDefaultPolicy), config.APIKeyDefaultPolicyDenyAll) {
			c.Next()
			return
		}

		raw, exists := c.Get("apiKey")
		if !exists {
			// AuthMiddleware did not run or did not identify a key. We do not
			// enforce in that case to preserve the legacy "no auth provider =>
			// allow" behavior of AuthMiddleware.
			c.Next()
			return
		}
		apiKey, ok := raw.(string)
		if !ok || strings.TrimSpace(apiKey) == "" {
			c.Next()
			return
		}

		// Websocket upgrades cannot be inspected at this layer — the model
		// is selected later in frames consumed by ResponsesWebsocket, not in
		// the upgrade request itself. To prevent a restricted key from
		// escaping the ACL via the upgrade path, reject the upgrade when
		// the calling key has any per-model restriction. Unrestricted keys
		// (empty AllowedModels AND allow-all default) still pass through
		// so the legacy websocket flow keeps working for them.
		if isWebsocketUpgradeRequest(c.Request) && keyHasModelRestriction(cfg, apiKey) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": gin.H{
					"type":    "websocket_not_allowed_for_restricted_key",
					"message": "model-restricted api keys cannot use websocket upgrade routes; model selection happens in frames the ACL cannot inspect",
				},
			})
			return
		}

		model, found, err := extractRequestedModel(c)
		if err != nil {
			if errors.Is(err, errBodyTooLarge) {
				c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{
					"error": gin.H{
						"type":    "request_too_large",
						"message": "request body exceeds the model-ACL inspection cap",
					},
				})
				return
			}
			// Any other read error: fail closed rather than silently skipping
			// policy enforcement. Treat as a bad request.
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error": gin.H{
					"type":    "invalid_request_body",
					"message": "could not read request body for model ACL enforcement",
				},
			})
			return
		}
		if !found {
			// No model in this request shape (listing, ping, etc.) — allow.
			c.Next()
			return
		}

		if cfg.IsModelAllowedForKey(apiKey, model) {
			c.Next()
			return
		}

		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"error": gin.H{
				"type":    "model_not_allowed_for_key",
				"message": "this api key is not permitted to use the requested model",
				"model":   model,
			},
		})
	}
}

// extractRequestedModel returns the model identifier the current request is
// targeting, or ok=false when none can be determined for the route. An error
// is returned only when reading the request body fails (including when the
// body exceeds modelACLMaxBodyBytes — see errBodyTooLarge).
//
// Body inspection is gated on Content-Type so that large non-JSON uploads
// (e.g. image-edit multipart payloads) are not buffered just to look for a
// "model" field that JSON-shaped requests carry. JSON requests follow the
// existing peek+buffer path; multipart/form-data is parsed via the standard
// library, which extracts the small "model" form field while spooling large
// file parts to temporary disk; everything else is allowed through without
// inspection.
func extractRequestedModel(c *gin.Context) (model string, ok bool, err error) {
	if c == nil || c.Request == nil {
		return "", false, nil
	}

	// Gemini-style: /v1beta/models/<model>:<action>
	//
	// The <model> segment may itself contain a "/" when the deployment uses
	// force-model-prefix (e.g. /v1beta/models/teamA/gemini-3-pro:generateContent,
	// where the routed model identifier is literally "teamA/gemini-3-pro").
	// GeminiHandler forwards the whole segment-before-":" as the model, and
	// IsModelAllowedForKey tolerates a leading "<prefix>/" on matches. So the
	// ACL extractor mirrors that: everything between the prefix and ":" is the
	// model, including embedded slashes.
	if strings.HasPrefix(c.Request.URL.Path, "/v1beta/models/") {
		rest := strings.TrimPrefix(c.Request.URL.Path, "/v1beta/models/")
		if idx := strings.Index(rest, ":"); idx >= 0 {
			rest = rest[:idx]
		}
		rest = strings.TrimSpace(rest)
		if rest != "" {
			return rest, true, nil
		}
	}

	// Only POST/PUT/PATCH carry bodies we care about.
	method := c.Request.Method
	if method != http.MethodPost && method != http.MethodPut && method != http.MethodPatch {
		return "", false, nil
	}
	if c.Request.Body == nil {
		return "", false, nil
	}

	// Dispatch on Content-Type. Missing/unparseable Content-Type is treated
	// as JSON for backward compatibility — every model-routing client we ship
	// today either sets application/json explicitly or omits the header on
	// JSON bodies, and falling back to JSON inspection here is what the
	// middleware did before content-type gating.
	mediaType := parseRequestMediaType(c.Request.Header.Get("Content-Type"))
	switch {
	case mediaType == "" || mediaType == "application/json" || strings.HasSuffix(mediaType, "+json"):
		return extractModelFromJSONBody(c)
	case mediaType == "multipart/form-data":
		return extractModelFromMultipartBody(c)
	default:
		// Unknown body shape (text/plain, application/octet-stream, etc.).
		// We do not buffer or parse; the route handler enforces its own
		// contract. No model identified => allow through, consistent with
		// the existing "no extractable model" path used by listing and
		// websocket-upgrade routes.
		return "", false, nil
	}
}

// parseRequestMediaType returns the lowercased media type from a Content-Type
// header, dropping parameters (charset, boundary, etc.). Returns "" when the
// header is empty or unparseable.
func parseRequestMediaType(header string) string {
	if header == "" {
		return ""
	}
	mt, _, err := mime.ParseMediaType(header)
	if err != nil {
		return ""
	}
	return strings.ToLower(mt)
}

// extractModelFromJSONBody peeks the first modelACLPeekBytes for a "model"
// field. If found, the body is restored as peek+underlying for downstream
// handlers and we avoid buffering the rest. If not found, the remainder is
// buffered up to modelACLMaxBodyBytes; payloads larger than the cap return
// errBodyTooLarge without draining the connection.
func extractModelFromJSONBody(c *gin.Context) (string, bool, error) {
	// Short-circuit the too-large case cheaply when Content-Length is known.
	if c.Request.ContentLength > modelACLMaxBodyBytes {
		return "", false, errBodyTooLarge
	}

	peek := make([]byte, modelACLPeekBytes)
	peekN, peekErr := io.ReadFull(c.Request.Body, peek)
	peek = peek[:peekN]
	bodyFullyRead := peekErr == io.EOF || peekErr == io.ErrUnexpectedEOF
	if peekErr != nil && !bodyFullyRead {
		return "", false, peekErr
	}

	if bodyFullyRead {
		c.Request.Body = io.NopCloser(bytes.NewReader(peek))
		return extractModelFromBytes(peek)
	}

	// gjson tolerates truncated JSON: if "model" appeared before the peek
	// boundary, this returns it without buffering the rest.
	if model, ok, _ := extractModelFromBytes(peek); ok {
		c.Request.Body = io.NopCloser(io.MultiReader(bytes.NewReader(peek), c.Request.Body))
		return model, true, nil
	}

	remaining := modelACLMaxBodyBytes - int64(len(peek))
	if remaining <= 0 {
		return "", false, errBodyTooLarge
	}
	limited := io.LimitReader(c.Request.Body, remaining+1)
	rest, readErr := io.ReadAll(limited)
	if readErr != nil {
		return "", false, readErr
	}
	if int64(len(rest)) > remaining {
		// Do NOT drain the rest of the body. A chunked/streamed request
		// without a trustworthy Content-Length could hold the handler
		// goroutine indefinitely, turning the ACL check into a
		// request-slot exhaustion path. Returning here lets net/http
		// close the connection without us reading another byte.
		return "", false, errBodyTooLarge
	}

	bodyBytes := make([]byte, 0, len(peek)+len(rest))
	bodyBytes = append(bodyBytes, peek...)
	bodyBytes = append(bodyBytes, rest...)
	c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	return extractModelFromBytes(bodyBytes)
}

// extractModelFromMultipartBody parses a multipart/form-data body via the
// standard library and returns the value of the "model" form field, if any.
// File parts are spooled to temporary disk past modelACLMultipartMemoryCap so
// large image uploads do not balloon resident memory. ParseMultipartForm is
// idempotent, so downstream handlers calling c.MultipartForm() / c.PostForm()
// see the same parsed form without re-reading the body.
func extractModelFromMultipartBody(c *gin.Context) (string, bool, error) {
	if err := c.Request.ParseMultipartForm(modelACLMultipartMemoryCap); err != nil {
		// A malformed multipart body is the route's problem to surface in
		// its own 400; from the ACL's perspective there is no model to
		// gate on, so let it through.
		return "", false, nil
	}
	if c.Request.MultipartForm == nil {
		return "", false, nil
	}
	values := c.Request.MultipartForm.Value["model"]
	if len(values) == 0 {
		return "", false, nil
	}
	model := strings.TrimSpace(values[0])
	if model == "" {
		return "", false, nil
	}
	return model, true, nil
}

// extractModelFromBytes scans a (possibly truncated) JSON buffer for a
// top-level "model" string field and returns it. A missing or empty field
// yields ok=false; this is never an error condition.
func extractModelFromBytes(body []byte) (model string, ok bool, err error) {
	if len(body) == 0 {
		return "", false, nil
	}
	res := gjson.GetBytes(body, "model")
	if !res.Exists() || res.Type != gjson.String {
		return "", false, nil
	}
	model = strings.TrimSpace(res.String())
	if model == "" {
		return "", false, nil
	}
	return model, true, nil
}

// isWebsocketUpgradeRequest reports whether the request is attempting to
// upgrade to a websocket. Any route served by a ModelACLMiddleware-instrumented
// group that also carries "Upgrade: websocket" counts — we do not scope this to
// a specific path because future routes may join the upgrade set and we want
// the ACL guard to keep working without per-route updates.
func isWebsocketUpgradeRequest(req *http.Request) bool {
	if req == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(req.Header.Get("Upgrade")), "websocket")
}

// keyHasModelRestriction reports whether the given api key is subject to any
// per-model restriction under cfg. A key is "restricted" when either:
//
//   - it has an explicit APIKeyPolicy entry with a non-empty AllowedModels list
//     (so some models would be denied under the normal ACL), or
//   - the deployment's default policy is "deny-all" AND the key has no
//     matching policy entry (so every model is denied under the default).
//
// Unrestricted keys (no policy, allow-all default) return false and are
// allowed to take the legacy paths the middleware cannot inspect, such as
// websocket upgrades.
func keyHasModelRestriction(cfg *config.Config, key string) bool {
	key = strings.TrimSpace(key)
	if key == "" || cfg == nil {
		return false
	}
	for i := range cfg.APIKeyPolicies {
		if cfg.APIKeyPolicies[i].Key == key {
			return len(cfg.APIKeyPolicies[i].AllowedModels) > 0
		}
	}
	// No explicit policy for this key — fall back to the default.
	return strings.EqualFold(strings.TrimSpace(cfg.APIKeyDefaultPolicy), config.APIKeyDefaultPolicyDenyAll)
}
