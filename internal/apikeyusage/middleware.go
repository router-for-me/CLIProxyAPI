package apikeyusage

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// Middleware enforces named API key policies after the normal access middleware.
func Middleware(service *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		if service == nil || !service.Enabled() || !isAccountedRequest(c.Request) {
			c.Next()
			return
		}
		rawKey, exists := c.Get("userApiKey")
		if !exists {
			c.Next()
			return
		}
		apiKey := strings.TrimSpace(stringValue(rawKey))
		if apiKey == "" {
			c.Next()
			return
		}
		model, err := requestedModel(c)
		if err != nil {
			// Let the endpoint own request validation. Accounting must not change
			// the response for malformed or unreadable request bodies.
			c.Next()
			return
		}
		decision, err := service.Reserve(c.Request.Context(), apiKey, model, time.Now())
		if err != nil {
			log.WithError(err).Error("api-key usage reservation failed")
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
				"error": gin.H{"message": "API key usage accounting is temporarily unavailable.", "type": "server_error", "code": "usage_accounting_unavailable"},
			})
			return
		}
		if !decision.Allowed {
			if !decision.ResetAt.IsZero() {
				seconds := int64(time.Until(decision.ResetAt).Seconds())
				if seconds < 1 {
					seconds = 1
				}
				c.Header("Retry-After", strconv.FormatInt(seconds, 10))
				c.Header("X-RateLimit-Reset", decision.ResetAt.UTC().Format(time.RFC3339))
			}
			status := http.StatusTooManyRequests
			if decision.Code == "model_not_allowed" || decision.Code == "api_key_disabled" {
				status = http.StatusForbidden
			}
			c.AbortWithStatusJSON(status, gin.H{
				"error": gin.H{
					"message": decision.Message,
					"type":    "api_key_policy_error",
					"code":    decision.Code,
					"period":  decision.Period,
					"limit":   decision.Limit,
					"used":    decision.Used,
				},
			})
			return
		}
		if decision.RemainingRequests >= 0 {
			c.Header("X-RateLimit-Remaining-Requests", strconv.FormatInt(decision.RemainingRequests, 10))
		}
		if decision.RemainingTokens >= 0 {
			c.Header("X-RateLimit-Remaining-Tokens", strconv.FormatInt(decision.RemainingTokens, 10))
		}

		c.Next()
		completeContext := context.WithoutCancel(c.Request.Context())
		if err = service.Complete(completeContext, decision.Reservation, c.Writer.Status()); err != nil {
			log.WithError(err).Warn("api-key usage request completion failed")
		}
	}
}

func isAccountedRequest(request *http.Request) bool {
	if request == nil || request.URL == nil {
		return false
	}
	if request.Method != http.MethodPost {
		return false
	}
	path := request.URL.Path
	if path == "/v1/messages/count_tokens" {
		return false
	}
	return strings.HasPrefix(path, "/v1/") ||
		strings.HasPrefix(path, "/v1beta/") ||
		strings.HasPrefix(path, "/openai/v1/") ||
		strings.HasPrefix(path, "/backend-api/codex/")
}

func requestedModel(c *gin.Context) (string, error) {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return "", nil
	}
	if model := strings.TrimSpace(c.Query("model")); model != "" {
		return model, nil
	}
	path := c.Request.URL.Path
	if marker := strings.Index(path, "/models/"); marker >= 0 {
		model := path[marker+len("/models/"):]
		if separator := strings.IndexByte(model, ':'); separator >= 0 {
			model = model[:separator]
		}
		if slash := strings.IndexByte(model, '/'); slash >= 0 {
			model = model[:slash]
		}
		if model = strings.TrimSpace(model); model != "" {
			return model, nil
		}
	}
	if c.Request.Body == nil {
		return "", nil
	}
	contentType := strings.TrimSpace(c.GetHeader("Content-Type"))
	if contentType != "" {
		mediaType, _, err := mime.ParseMediaType(contentType)
		if err == nil && mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json") {
			return "", nil
		}
	}
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return "", err
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	if len(bytes.TrimSpace(body)) == 0 {
		return "", nil
	}
	var payload struct {
		Model string `json:"model"`
	}
	if err = json.Unmarshal(body, &payload); err != nil {
		return "", err
	}
	return strings.TrimSpace(payload.Model), nil
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	case interface{ String() string }:
		return typed.String()
	default:
		return ""
	}
}
