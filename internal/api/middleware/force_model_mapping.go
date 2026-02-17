// Package middleware provides HTTP middleware components for the CLI Proxy API server.
// This file contains the ForceModelMapping middleware that applies ampcode model mappings
// to /v1 routes when force-model-mappings is enabled.
package middleware

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/api/modules/amp"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ForceModelMapping creates a Gin middleware that applies ampcode model mappings
// to incoming requests when force-model-mappings is enabled in the config.
// This allows clients like Claude Desktop to get model fallback (e.g. claude-opus-4-6-thinking -> gemini-3-pro-preview)
// and avoid 429 errors when the requested model is not available.
func ForceModelMapping(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		if cfg == nil || !cfg.AmpCode.ForceModelMappings {
			c.Next()
			return
		}

		// Only apply to POST requests that might contain a model field
		if c.Request.Method != http.MethodPost {
			c.Next()
			return
		}

		// Read the request body
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			log.Debugf("force-model-mapping: failed to read request body: %v", err)
			c.Next()
			return
		}
		c.Request.Body.Close()

		// Check if body contains a model field
		modelVal := gjson.GetBytes(body, "model")
		if !modelVal.Exists() || modelVal.String() == "" {
			// Restore body and continue
			c.Request.Body = io.NopCloser(bytes.NewReader(body))
			c.Next()
			return
		}

		requestedModel := modelVal.String()

		// Create a model mapper from config
		mapper := amp.NewModelMapper(cfg.AmpCode.ModelMappings)

		// Try to map the model
		mappedModel := mapper.MapModel(requestedModel)
		if mappedModel != "" && mappedModel != requestedModel {
			log.Debugf("force-model-mapping: mapping %s -> %s", requestedModel, mappedModel)

			// Update the request body with the mapped model
			newBody, err := sjson.SetBytes(body, "model", mappedModel)
			if err != nil {
				log.Warnf("force-model-mapping: failed to update model in request body: %v", err)
				c.Request.Body = io.NopCloser(bytes.NewReader(body))
				c.Next()
				return
			}

			// Store original model in context for potential use by handlers
			c.Set("original_model", requestedModel)

			// Restore body with mapped model
			c.Request.Body = io.NopCloser(bytes.NewReader(newBody))
			c.Request.ContentLength = int64(len(newBody))
		} else {
			// Restore original body
			c.Request.Body = io.NopCloser(bytes.NewReader(body))
		}

		c.Next()
	}
}

// shouldApplyModelMapping checks if the path should have model mapping applied.
// It applies to chat completions and responses endpoints.
func shouldApplyModelMapping(path string) bool {
	path = strings.ToLower(path)
	return strings.Contains(path, "/chat/completions") ||
		strings.Contains(path, "/completions") ||
		strings.Contains(path, "/responses")
}
