// Package middleware contains Gin middlewares that cut across HTTP routes.
// The policy middleware enforces per-API-key limits (RPM, hourly rate, model
// access, budget caps) after authentication has resolved the caller's
// principal.
package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/errormessages"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/policy"
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
