// Package middleware contains Gin middlewares that cut across HTTP routes.
// The policy middleware enforces per-API-key limits (RPM, hourly rate, model
// access, budget caps) after authentication has resolved the caller's
// principal.
package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/errormessages"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/policy"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// Context keys populated by the policy middleware so downstream handlers and
// the usage plugin sink can correlate requests with their policy decision.
const (
	// CtxPolicyChecked is set to true once the policy middleware has run, so
	// downstream middlewares can detect misconfigured route trees.
	CtxPolicyChecked = "policy.checked"
	// CtxPolicyKeyID carries the resolved key_id (when available) for log
	// correlation without exposing the plaintext key.
	CtxPolicyKeyID = "policy.key_id"
	// CtxPolicyModelRoutes carries the per-allowed-model upstream provider
	// routes resolved for the principal (a []store.ModelRoute). The request
	// handler reads it to confine provider selection to the pinned set. Absent
	// when no policy/service is active or no routes are configured.
	CtxPolicyModelRoutes = "policy.model_routes"
	// CtxPolicyAllowedModels carries the resolved allowed/blocked model lists
	// for the principal (a policy.ModelLists, post Model Group override). The
	// /v1/models (and /v1beta/models) handlers read it to filter the catalog
	// per API key. Absent when the service is inactive, no policy is attached,
	// or both lists are empty — handlers treat absence as "list everything".
	CtxPolicyAllowedModels = "policy.allowed_models"
)

// PolicyMiddleware returns a Gin middleware that delegates to the supplied
// PolicyService. When svc.Active() is false the middleware is a fast pass-
// through so file-only deployments pay no overhead.
//
// The middleware buffers the request body so it can extract the "model"
// field without consuming the stream: the buffered bytes are re-attached to
// c.Request.Body before the next handler runs, preserving the original body
// for the route handler.
func PolicyMiddleware(svc policy.PolicyService) gin.HandlerFunc {
	return func(c *gin.Context) {
		if svc == nil || !svc.Active() {
			c.Next()
			return
		}
		c.Set(CtxPolicyChecked, true)

		principal, _ := c.Get("userApiKey")
		principalStr, _ := principal.(string)
		if principalStr == "" {
			// No caller principal: defer to AuthMiddleware's existing verdict.
			// AuthMiddleware already rejected the request when the manager is
			// non-nil and no provider matched, so reaching here with an empty
			// principal means the manager is nil (legacy open mode) — we do
			// not enforce policy in that configuration.
			c.Next()
			return
		}

		model := extractModel(c)
		decision, err := svc.Check(c.Request.Context(), principalStr, model)
		if err != nil {
			log.WithError(err).
				WithField("route", c.Request.URL.Path).
				Warn("policy middleware: check failed; failing open")
			c.Next()
			return
		}
		if !decision.Allow {
			status := decision.StatusCode
			if status == 0 {
				status = http.StatusForbidden
			}
			// Route through the error messages registry so operator overrides
			// (e.g. custom 429 / 402 text) apply without a server restart.
			errormessages.Respond(c, status, decision.Reason)
			return
		}

		// IP allowlist/blocklist. Reuses the snapshot cache populated by Check
		// (no extra DB round-trip). Deny-first: an explicit block match always
		// rejects, even when the IP is also on the allowlist. When an allowlist
		// is configured, the client IP must match at least one entry; an empty
		// allowlist means "all IPs allowed" (subject to BlockedIPs). Mirror of
		// the management-token IP enforcement in mgmt_policy.go, kept here so
		// API-key policy is enforced on the same source-IP dimension.
		allowedIPs, blockedIPs := svc.ResolvedIPLists(c.Request.Context(), principalStr)
		if len(allowedIPs) > 0 || len(blockedIPs) > 0 {
			clientIP := c.ClientIP()
			if matched, pattern := policy.IPMatches(clientIP, blockedIPs); matched {
				errormessages.Respond(c, http.StatusForbidden,
					fmt.Sprintf("API key policy blocks this source IP (%s)", pattern))
				return
			}
			if len(allowedIPs) > 0 {
				if allowed, _ := policy.IPMatches(clientIP, allowedIPs); !allowed {
					errormessages.Respond(c, http.StatusForbidden,
						"API key policy does not allow this source IP")
					return
				}
			}
		}

		// Re-resolve the per-model upstream routes for the principal so the
		// request handler can confine provider selection. This reuses the
		// snapshot cache populated by Check (no extra DB round-trip). The
		// model-specific matching against the requested model is done later by
		// the handler; here we stash the full route table.
		if routes := svc.ResolvedRoutes(c.Request.Context(), principalStr); len(routes) > 0 {
			c.Set(CtxPolicyModelRoutes, routes)
		}

		// Stash the resolved allowed/blocked model lists so the /v1/models (and
		// /v1beta/models) handlers can filter the registry catalog per API key.
		// Reuses the snapshot cache populated by Check (no extra DB round-trip).
		if allowed, blocked := svc.ResolvedModelLists(c.Request.Context(), principalStr); len(allowed) > 0 || len(blocked) > 0 {
			c.Set(CtxPolicyAllowedModels, policy.ModelLists{Allowed: allowed, Blocked: blocked})
		}

		// Acquire an in-flight slot under the configured max_parallel_requests
		// cap. The slot is released in a defer so the counter does not leak,
		// including the upstream handler's panic / early-return paths. When
		// Acquire returns false we surface HTTP 429 immediately.
		acquired, acqErr := svc.AcquireParallel(c.Request.Context(), principalStr)
		if acqErr != nil {
			log.WithError(acqErr).
				WithField("route", c.Request.URL.Path).
				Warn("policy middleware: AcquireParallel failed; failing open")
			acquired = true // fail open so a transient error does not leak state
		} else if !acquired {
			errormessages.Respond(c, http.StatusTooManyRequests,
				"max concurrent requests exceeded for this principal")
			return
		}
		if acquired {
			defer func() {
				_ = svc.ReleaseParallel(context.Background(), principalStr)
			}()
		}
		c.Next()
	}
}

// RoutesForModel returns the pinned upstream providers for modelID from the gin
// context, where PolicyMiddleware stashed the principal's model_routes. Returns
// nil when no policy/service is active, no routes are configured, or no route
// matches modelID. A nil result means "use the registry default provider set".
// The match is case-insensitive on the model id (routes are keyed on the bare
// model id, without thinking suffix).
func RoutesForModel(c *gin.Context, modelID string) []string {
	if c == nil || modelID == "" {
		return nil
	}
	raw, ok := c.Get(CtxPolicyModelRoutes)
	if !ok || raw == nil {
		return nil
	}
	routes, ok := raw.([]store.ModelRoute)
	if !ok {
		return nil
	}
	target := strings.ToLower(strings.TrimSpace(modelID))
	for _, r := range routes {
		if strings.ToLower(strings.TrimSpace(r.Model)) == target {
			if len(r.Providers) == 0 {
				return nil
			}
			return r.Providers
		}
	}
	return nil
}

// ModelListsFor returns the resolved allowed/blocked model lists stashed by
// PolicyMiddleware for the current principal (post Model Group override).
// Returns nil, nil when no policy/service is active or both lists are empty,
// in which case the caller should list every model (no filtering).
func ModelListsFor(c *gin.Context) (allowed, blocked []string) {
	if c == nil {
		return nil, nil
	}
	raw, ok := c.Get(CtxPolicyAllowedModels)
	if !ok || raw == nil {
		return nil, nil
	}
	lists, ok := raw.(policy.ModelLists)
	if !ok {
		return nil, nil
	}
	return lists.Allowed, lists.Blocked
}

// extractModel reads the request body to find a "model" field. The body is
// buffered and re-attached so downstream handlers see the original bytes.
// Unparseable bodies default to an empty model (which the policy service
// treats as "defer").
func extractModel(c *gin.Context) string {
	if c.Request == nil || c.Request.Body == nil {
		return ""
	}
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		// Cannot read body without consuming it; leave model empty and
		// let the policy service defer. We restore the body best-effort.
		if body != nil {
			c.Request.Body = io.NopCloser(bytes.NewReader(body))
		}
		return ""
	}
	// Always restore the body so the handler can re-read it.
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	return parseModelField(body)
}

// parseModelField extracts the model identifier from a JSON request body.
// It tolerates top-level objects (OpenAI/Claude/Gemini) and the Anthropic
// envelope {"request": {...}} used by some Claude routes. Returns "" when no
// parseable "model" field is found — the policy service treats this as
// "defer (allow pending later re-check)".
func parseModelField(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	// Fast path: only attempt JSON parsing if the body looks like JSON.
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return ""
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &root); err != nil {
		return ""
	}
	// Anthropic envelope: look inside "request" first.
	if rawEnvelope, ok := root["request"]; ok {
		if model := modelFromRawMessage(rawEnvelope); model != "" {
			return model
		}
		// The envelope itself may be an object whose "model" field is
		// nested one level deeper than modelFromRawMessage checks.
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(rawEnvelope, &envelope); err == nil {
			if model := modelFromRawMessage(envelope["model"]); model != "" {
				return model
			}
		}
	}
	return modelFromRawMessage(root["model"])
}

// modelFromRawMessage decodes a JSON value as either a string (the common
// case) or an object/array (some providers accept {"model": {"name": ...}}).
// Returns the resolved string or "".
func modelFromRawMessage(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// Try simple string.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// Try object with name field.
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err == nil {
		if name, ok := obj["name"].(string); ok && name != "" {
			return name
		}
		if id, ok := obj["id"].(string); ok && id != "" {
			return id
		}
	}
	return ""
}
