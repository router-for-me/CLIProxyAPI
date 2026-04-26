package management

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

type apiCallRequest struct {
	AuthIndexSnake  *string           `json:"auth_index"`
	AuthIndexCamel  *string           `json:"authIndex"`
	AuthIndexPascal *string           `json:"AuthIndex"`
	Method          string            `json:"method"`
	URL             string            `json:"url"`
	Header          map[string]string `json:"header"`
	Data            string            `json:"data"`
}

type apiCallResponse struct {
	StatusCode int                 `json:"status_code"`
	Header     map[string][]string `json:"header"`
	Body       string              `json:"body"`
	Quota      *QuotaSnapshots     `json:"quota,omitempty"`
}

// APICall makes a generic HTTP request on behalf of the management API caller.
// It is protected by the management middleware.
//
// Endpoint:
//
//	POST /v0/management/api-call
//
// Authentication:
//
//	Same as other management APIs (requires a management key and remote-management rules).
//	You can provide the key via:
//	- Authorization: Bearer <key>
//	- X-Management-Key: <key>
//
// Request JSON (supports both application/json and application/cbor):
//   - auth_index / authIndex / AuthIndex (optional):
//     The credential "auth_index" from GET /v0/management/auth-files (or other endpoints returning it).
//     If omitted or not found, credential-specific proxy/token substitution is skipped.
//   - method (required): HTTP method, e.g. GET, POST, PUT, PATCH, DELETE.
//   - url (required): Absolute URL including scheme and host, e.g. "https://api.example.com/v1/ping".
//   - header (optional): Request headers map.
//     Supports magic variable "$TOKEN$" which is replaced using the selected credential:
//     1) metadata.access_token
//     2) attributes.api_key
//     3) metadata.token / metadata.id_token / metadata.cookie
//     Example: {"Authorization":"Bearer $TOKEN$"}.
//     Note: if you need to override the HTTP Host header, set header["Host"].
//   - data (optional): Raw request body as string (useful for POST/PUT/PATCH).
//
// Proxy selection (highest priority first):
//  1. Selected credential proxy_url
//  2. Global config proxy-url
//  3. Direct connect (environment proxies are not used)
//
// Response (returned with HTTP 200 when the APICall itself succeeds):
//
//	Format matches request Content-Type (application/json or application/cbor)
//	- status_code: Upstream HTTP status code.
//	- header: Upstream response headers.
//	- body: Upstream response body as string.
//	- quota (optional): For GitHub Copilot enterprise accounts, contains quota_snapshots
//	  with details for chat, completions, and premium_interactions.
//
// Example:
//
//	curl -sS -X POST "http://127.0.0.1:8317/v0/management/api-call" \
//	  -H "Authorization: Bearer <MANAGEMENT_KEY>" \
//	  -H "Content-Type: application/json" \
//	  -d '{"auth_index":"<AUTH_INDEX>","method":"GET","url":"https://api.example.com/v1/ping","header":{"Authorization":"Bearer $TOKEN$"}}'
//
//	curl -sS -X POST "http://127.0.0.1:8317/v0/management/api-call" \
//	  -H "Authorization: Bearer 831227" \
//	  -H "Content-Type: application/json" \
//	  -d '{"auth_index":"<AUTH_INDEX>","method":"POST","url":"https://api.example.com/v1/fetchAvailableModels","header":{"Authorization":"Bearer $TOKEN$","Content-Type":"application/json","User-Agent":"cliproxyapi"},"data":"{}"}'
func (h *Handler) APICall(c *gin.Context) {
	// Detect content type
	contentType := strings.ToLower(strings.TrimSpace(c.GetHeader("Content-Type")))
	isCBOR := strings.Contains(contentType, "application/cbor")

	var body apiCallRequest

	// Parse request body based on content type
	if isCBOR {
		rawBody, errRead := io.ReadAll(c.Request.Body)
		if errRead != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read request body"})
			return
		}
		if errUnmarshal := cbor.Unmarshal(rawBody, &body); errUnmarshal != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid cbor body"})
			return
		}
	} else {
		if errBindJSON := c.ShouldBindJSON(&body); errBindJSON != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
			return
		}
	}

	method := strings.ToUpper(strings.TrimSpace(body.Method))
	if method == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing method"})
		return
	}

	urlStr := strings.TrimSpace(body.URL)
	if urlStr == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing url"})
		return
	}
	safeURL, parsedURL, errSanitizeURL := sanitizeAPICallURL(urlStr)
	if errSanitizeURL != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errSanitizeURL.Error()})
		return
	}
	if errResolve := validateResolvedHostIPs(parsedURL.Hostname()); errResolve != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errResolve.Error()})
		return
	}

	authIndex := firstNonEmptyString(body.AuthIndexSnake, body.AuthIndexCamel, body.AuthIndexPascal)
	auth := h.authByIndex(authIndex)

	reqHeaders := body.Header
	if reqHeaders == nil {
		reqHeaders = map[string]string{}
	}

	var hostOverride string
	var token string
	var tokenResolved bool
	var tokenErr error
	for key, value := range reqHeaders {
		if !strings.Contains(value, "$TOKEN$") {
			continue
		}
		if !tokenResolved {
			token, tokenErr = h.resolveTokenForAuth(c.Request.Context(), auth)
			tokenResolved = true
		}
		if auth != nil && token == "" {
			if tokenErr != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "auth token refresh failed"})
				return
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": "auth token not found"})
			return
		}
		if token == "" {
			continue
		}
		reqHeaders[key] = strings.ReplaceAll(value, "$TOKEN$", token)
	}

	// When caller indicates CBOR in request headers, convert JSON string payload to CBOR bytes.
	useCBORPayload := headerContainsValue(reqHeaders, "Content-Type", "application/cbor")

	var requestBody io.Reader
	if body.Data != "" {
		if useCBORPayload {
			cborPayload, errEncode := encodeJSONStringToCBOR(body.Data)
			if errEncode != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json data for cbor content-type"})
				return
			}
			requestBody = bytes.NewReader(cborPayload)
		} else {
			requestBody = strings.NewReader(body.Data)
		}
	}

	req, errNewRequest := http.NewRequestWithContext(c.Request.Context(), method, safeURL, requestBody)
	if errNewRequest != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to build request"})
		return
	}

	for key, value := range reqHeaders {
		if strings.EqualFold(key, "host") {
			hostOverride = strings.TrimSpace(value)
			continue
		}
		req.Header.Set(key, value)
	}
	if hostOverride != "" {
		if !isAllowedHostOverride(parsedURL, hostOverride) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid host override"})
			return
		}
		req.Host = hostOverride
	}

	httpClient := h.apiCallHTTPClient(auth)
	resp, errDo := httpClient.Do(req)
	if errDo != nil {
		log.WithError(errDo).Debug("management APICall request failed")
		c.JSON(http.StatusBadGateway, gin.H{"error": "request failed"})
		return
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
	}()

	respBody, errReadAll := io.ReadAll(resp.Body)
	if errReadAll != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to read response"})
		return
	}

	// For CBOR upstream responses, decode into plain text or JSON string before returning.
	responseBodyText := string(respBody)
	if headerContainsValue(reqHeaders, "Accept", "application/cbor") || strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "application/cbor") {
		if decodedBody, errDecode := decodeCBORBodyToTextOrJSON(respBody); errDecode == nil {
			responseBodyText = decodedBody
		}
	}

	response := apiCallResponse{
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
		Body:       responseBodyText,
	}

	// If this is a GitHub Copilot token endpoint response, try to enrich with quota information
	if resp.StatusCode == http.StatusOK &&
		strings.Contains(safeURL, "copilot_internal") &&
		strings.Contains(safeURL, "/token") {
		response = h.enrichCopilotTokenResponse(c.Request.Context(), response, auth, urlStr)
	}

	// Return response in the same format as the request
	if isCBOR {
		cborData, errMarshal := cbor.Marshal(response)
		if errMarshal != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to encode cbor response"})
			return
		}
		c.Data(http.StatusOK, "application/cbor", cborData)
	} else {
		c.JSON(http.StatusOK, response)
	}
}

const defaultAPICallTimeout = 60 * time.Second
