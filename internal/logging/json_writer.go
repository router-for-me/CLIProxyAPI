// Package logging provides request logging functionality for the CLI Proxy API server.
package logging

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
)

// Package-level compiled regexes for performance
var (
	requestIndexRegex         = regexp.MustCompile(`REQUEST\s+(\d+)`)
	fileNameInvalidCharsRegex = regexp.MustCompile(`[<>:"|?*\s]`)
	fileNameMultipleHyphens   = regexp.MustCompile(`-+`)
)

// WriteJSONLog writes a complete request/response cycle as a JSON file.
func WriteJSONLog(
	filePath string,
	url, method string,
	requestHeaders map[string][]string,
	body []byte,
	statusCode int,
	responseHeaders map[string][]string,
	response, apiRequest, apiResponse []byte,
	apiResponseErrors []*interfaces.ErrorMessage,
	requestTimestamp, apiResponseTimestamp time.Time,
) error {
	builder := NewRequestLogBuilder(buildinfo.Version)

	// Set request
	headerMap := make(map[string]string)
	for k, v := range requestHeaders {
		if len(v) > 0 {
			headerMap[k] = util.MaskSensitiveHeaderValue(k, v[0])
		}
	}
	var bodyJSON json.RawMessage
	if json.Valid(body) {
		bodyJSON = body
	}
	builder.SetRequest(url, method, headerMap, bodyJSON)

	// Set models
	clientModel := ExtractModelFromBody(body)
	builder.SetModels(clientModel, "")

	// Set response
	respHeaderMap := make(map[string]string)
	for k, v := range responseHeaders {
		if len(v) > 0 {
			respHeaderMap[k] = v[0]
		}
	}
	builder.SetResponse(statusCode, respHeaderMap)

	// Parse API request
	if len(apiRequest) > 0 {
		attempt := parseAPIRequestLogData(apiRequest)
		builder.AddUpstreamAttempt(attempt)
	}

	// Parse SSE response
	if len(apiResponse) > 0 || len(response) > 0 {
		var sseData []byte
		if len(apiResponse) > 0 {
			sseData = apiResponse
		} else {
			sseData = response
		}
		content, tokenUsage, modelVersion := ParseRawSSE(sseData)

		if len(builder.log.Upstream.Attempts) > 0 {
			lastAttempt := builder.log.Upstream.Attempts[len(builder.log.Upstream.Attempts)-1]
			if lastAttempt.Response == nil {
				lastAttempt.Response = &UpstreamResponse{
					Status: statusCode,
				}
			}
			lastAttempt.Response.Content = content
			lastAttempt.Response.TokenUsage = tokenUsage
			if !apiResponseTimestamp.IsZero() {
				lastAttempt.Response.Timestamp = apiResponseTimestamp.Format(time.RFC3339Nano)
			}
		}

		builder.log.Summary.UpstreamModel = modelVersion
		builder.log.Summary.Tokens = tokenUsage
	}

	// Add error responses
	for _, errResp := range apiResponseErrors {
		if errResp != nil {
			attempt := &UpstreamAttempt{
				Index:     len(builder.log.Upstream.Attempts) + 1,
				Timestamp: time.Now().Format(time.RFC3339Nano),
				Response: &UpstreamResponse{
					Status: errResp.StatusCode,
				},
			}
			if errResp.Error != nil {
				attempt.Error = errResp.Error.Error()
			}
			builder.AddUpstreamAttempt(attempt)
		}
	}

	// Set timestamps
	if !requestTimestamp.IsZero() {
		builder.startTime = requestTimestamp
	}
	if !apiResponseTimestamp.IsZero() {
		builder.SetTTFB(apiResponseTimestamp)
	}

	// Extract protocol translation
	upstreamBody := extractBodyFromAPIRequestData(apiRequest)
	if len(upstreamBody) > 0 && len(body) > 0 {
		transforms := ExtractProtocolTransformations(body, upstreamBody)
		if len(transforms) > 0 {
			builder.SetProtocolTranslation("Claude Messages API", "Vertex v1internal", transforms)
		}
	}

	// Finalize
	builder.Finalize()
	jsonData, err := builder.ToJSON()
	if err != nil {
		return fmt.Errorf("failed to serialize log: %w", err)
	}

	return os.WriteFile(filePath, jsonData, 0644)
}

// parseAPIRequestLogData parses formatted API request log into structured data.
func parseAPIRequestLogData(data []byte) *UpstreamAttempt {
	attempt := &UpstreamAttempt{
		Index:     1,
		Timestamp: time.Now().Format(time.RFC3339Nano),
	}

	text := string(data)
	lines := strings.Split(text, "\n")

	var inBody bool
	var bodyBuilder strings.Builder
	headers := make(map[string]string)
	var authParts []string

	for _, line := range lines {
		trimmedLine := strings.TrimSpace(line)

		if strings.HasPrefix(trimmedLine, "=== API REQUEST") {
			if idx := extractRequestIndex(trimmedLine); idx > 0 {
				attempt.Index = idx
			}
			continue
		}

		switch {
		case strings.HasPrefix(trimmedLine, "Timestamp:"):
			attempt.Timestamp = strings.TrimSpace(strings.TrimPrefix(trimmedLine, "Timestamp:"))
		case strings.HasPrefix(trimmedLine, "Upstream URL:"):
			attempt.URL = strings.TrimSpace(strings.TrimPrefix(trimmedLine, "Upstream URL:"))
		case strings.HasPrefix(trimmedLine, "HTTP Method:"):
			attempt.Method = strings.TrimSpace(strings.TrimPrefix(trimmedLine, "HTTP Method:"))
		case strings.HasPrefix(trimmedLine, "Auth:"):
			authParts = append(authParts, strings.TrimSpace(strings.TrimPrefix(trimmedLine, "Auth:")))
		case trimmedLine == "Headers:":
			inBody = false
		case trimmedLine == "Body:":
			inBody = true
		case inBody && trimmedLine != "" && trimmedLine != "<empty>":
			// Preserve newlines for multi-line body
			if bodyBuilder.Len() > 0 {
				bodyBuilder.WriteString("\n")
			}
			bodyBuilder.WriteString(line) // Use original line to preserve indentation
		case strings.Contains(trimmedLine, ":") && !inBody:
			parts := strings.SplitN(trimmedLine, ":", 2)
			if len(parts) == 2 {
				headers[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
			}
		}
	}

	if len(headers) > 0 {
		attempt.Headers = headers
	}

	if bodyStr := strings.TrimSpace(bodyBuilder.String()); bodyStr != "" {
		if json.Valid([]byte(bodyStr)) {
			attempt.Body = json.RawMessage(bodyStr)
		}
	}

	if len(authParts) > 0 {
		attempt.Auth = parseAuthInfoData(strings.Join(authParts, ", "))
	}

	return attempt
}

func extractRequestIndex(line string) int {
	matches := requestIndexRegex.FindStringSubmatch(line)
	if len(matches) >= 2 {
		if idx, err := strconv.Atoi(matches[1]); err == nil {
			return idx
		}
	}
	return 0
}

// statusText returns HTTP status text using standard library.
func statusText(code int) string {
	return http.StatusText(code)
}

func parseAuthInfoData(authStr string) *AuthInfo {
	auth := &AuthInfo{}
	parts := strings.Split(authStr, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		switch {
		case strings.HasPrefix(part, "provider="):
			auth.Provider = strings.TrimPrefix(part, "provider=")
		case strings.HasPrefix(part, "auth_id="):
			auth.AuthID = strings.TrimPrefix(part, "auth_id=")
		case strings.HasPrefix(part, "label="):
			auth.Label = strings.TrimPrefix(part, "label=")
		case strings.HasPrefix(part, "type="):
			auth.Type = strings.TrimPrefix(part, "type=")
		}
	}
	return auth
}

func extractBodyFromAPIRequestData(data []byte) []byte {
	text := string(data)
	idx := strings.Index(text, "Body:\n")
	if idx == -1 {
		return nil
	}
	body := text[idx+6:]
	if endIdx := strings.Index(body, "\n\n==="); endIdx != -1 {
		body = body[:endIdx]
	}
	body = strings.TrimSpace(body)
	if body == "<empty>" {
		return nil
	}
	return []byte(body)
}

// GenerateJSONFilename creates a .json filename for logs.
func GenerateJSONFilename(url string, requestID ...string) string {
	path := url
	if strings.Contains(url, "?") {
		path = strings.Split(url, "?")[0]
	}

	if strings.HasPrefix(path, "/") {
		path = path[1:]
	}

	sanitized := strings.ReplaceAll(path, "/", "-")
	sanitized = strings.ReplaceAll(sanitized, ":", "-")

	reg := regexp.MustCompile(`[<>:"|?*\s]`)
	sanitized = reg.ReplaceAllString(sanitized, "-")

	reg = regexp.MustCompile(`-+`)
	sanitized = reg.ReplaceAllString(sanitized, "-")

	sanitized = strings.Trim(sanitized, "-")

	// Map path to protocol name for cleaner filenames
	sanitized = mapPathToProtocol(sanitized)

	if sanitized == "" {
		sanitized = "root"
	}

	timestamp := time.Now().Format("2006-01-02T150405")

	var idPart string
	if len(requestID) > 0 && requestID[0] != "" {
		idPart = requestID[0]
	} else {
		id := requestLogID.Add(1)
		idPart = fmt.Sprintf("%d", id)
	}

	return fmt.Sprintf("%s-%s-%s.json", timestamp, sanitized, idPart)
}

// mapPathToProtocol maps API paths to protocol names for cleaner filenames.
// e.g., "v1-chat-completions" -> "openai", "v1-messages" -> "anthropic"
func mapPathToProtocol(path string) string {
	// OpenAI protocol endpoints
	if strings.Contains(path, "chat-completions") || strings.Contains(path, "responses") {
		return "openai"
	}

	// Anthropic/Claude protocol endpoints
	if strings.Contains(path, "messages") && !strings.Contains(path, "beta") {
		return "anthropic"
	}

	// Gemini protocol endpoints
	if strings.Contains(path, "v1beta") || strings.Contains(path, "generateContent") ||
		strings.Contains(path, "streamGenerateContent") {
		return "gemini"
	}

	// Keep original for unrecognized paths
	return path
}
