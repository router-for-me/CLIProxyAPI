package executor

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	neturl "net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

const (
	codexConversationDefaultAcceptLanguage = "en-US,en;q=0.9"
	codexConversationDefaultLanguage       = "en-US"
	codexConversationBrowserUserAgent      = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.7103.113 Safari/537.36"
	codexConversationSecCHUA               = `"Chromium";v="136", "Google Chrome";v="136", "Not.A/Brand";v="99"`
	codexConversationSentinelScriptURL     = "https://sentinel.openai.com/sentinel/20260124ceb8/sdk.js"
	codexConversationSentinelErrorPrefix   = "wQ8Lk5FbGpA2NcR9dShT6gYjU7VxZ4D"
	codexConversationSentinelMaxAttempts   = 500000
)

type codexConversationChatRequirementsResponse struct {
	Token       string `json:"token"`
	ProofOfWork struct {
		Required   bool   `json:"required"`
		Seed       string `json:"seed"`
		Difficulty string `json:"difficulty"`
	} `json:"proofofwork"`
}

type codexConversationSentinelGenerator struct {
	deviceID         string
	userAgent        string
	requirementsSeed string
	sid              string
	random           *rand.Rand
}

func ensureCodexConversationSession(ctx context.Context, client *http.Client, auth *cliproxyauth.Auth, req *http.Request, bearerToken string) error {
	if req == nil {
		return nil
	}

	deviceID := strings.TrimSpace(req.Header.Get("Oai-Device-Id"))
	if deviceID == "" {
		deviceID = codexConversationDeviceID(auth)
	}
	if deviceID != "" {
		req.Header.Set("Oai-Device-Id", deviceID)
	}
	misc.EnsureHeader(req.Header, nil, "Accept-Language", codexConversationDefaultAcceptLanguage)
	misc.EnsureHeader(req.Header, nil, "Oai-Language", codexConversationDefaultLanguage)
	misc.EnsureHeader(req.Header, nil, "Sec-Fetch-Dest", "empty")
	misc.EnsureHeader(req.Header, nil, "Sec-Fetch-Mode", "cors")
	misc.EnsureHeader(req.Header, nil, "Sec-Fetch-Site", "same-origin")
	misc.EnsureHeader(req.Header, nil, "Sec-Ch-Ua", codexConversationSecCHUA)
	misc.EnsureHeader(req.Header, nil, "Sec-Ch-Ua-Mobile", "?0")
	misc.EnsureHeader(req.Header, nil, "Sec-Ch-Ua-Platform", `"Windows"`)
	misc.EnsureHeader(req.Header, nil, "Sec-Ch-Ua-Arch", `"x86"`)
	misc.EnsureHeader(req.Header, nil, "Sec-Ch-Ua-Bitness", `"64"`)
	misc.EnsureHeader(req.Header, nil, "Sec-Ch-Ua-Full-Version", `"136.0.7103.113"`)
	misc.EnsureHeader(req.Header, nil, "Sec-Ch-Ua-Platform-Version", `"15.0.0"`)

	builtCookieHeader := buildCodexConversationCookieHeader(auth, deviceID, "")
	cookieHeader := mergeCodexConversationCookies(req.Header.Get("Cookie"), builtCookieHeader)
	if cookieHeader != "" {
		req.Header.Set("Cookie", cookieHeader)
	}

	if strings.TrimSpace(req.Header.Get("Openai-Sentinel-Chat-Requirements-Token")) != "" &&
		strings.TrimSpace(req.Header.Get("Openai-Sentinel-Proof-Token")) != "" {
		return nil
	}

	if client == nil || strings.TrimSpace(bearerToken) == "" || deviceID == "" {
		return nil
	}

	requirementsToken, proofToken, oaiSC, err := codexConversationFetchChatRequirements(
		ctx,
		client,
		req.URL,
		bearerToken,
		deviceID,
		req.Header.Get("User-Agent"),
		cookieHeader,
	)
	if err != nil {
		return err
	}

	if requirementsToken != "" {
		req.Header.Set("Openai-Sentinel-Chat-Requirements-Token", requirementsToken)
	}
	if proofToken != "" {
		req.Header.Set("Openai-Sentinel-Proof-Token", proofToken)
	}
	if oaiSC != "" {
		updatedCookieHeader := mergeCodexConversationCookies(cookieHeader, "oai-sc="+oaiSC)
		if updatedCookieHeader != "" {
			req.Header.Set("Cookie", updatedCookieHeader)
		}
	}

	return nil
}

func codexConversationFetchChatRequirements(ctx context.Context, client *http.Client, targetURL *neturl.URL, bearerToken, deviceID, userAgent, cookieHeader string) (requirementsToken, proofToken, oaiSC string, err error) {
	if client == nil {
		return "", "", "", nil
	}

	origin := codexConversationRequestOrigin(targetURL)
	endpoints := []string{
		origin + "/backend-api/sentinel/chat-requirements",
		origin + "/backend-api/sentinel/chat-requirements/prepare",
	}

	generator := newCodexConversationSentinelGenerator(deviceID, userAgent)
	reqBody, marshalErr := json.Marshal(map[string]any{
		"p": generator.generateRequirementsToken(),
	})
	if marshalErr != nil {
		return "", "", "", marshalErr
	}

	var lastErr error
	for i := range endpoints {
		endpoint := endpoints[i]
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
		if reqErr != nil {
			lastErr = reqErr
			continue
		}
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(bearerToken))
		req.Header.Set("Accept", "*/*")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", codexConversationBrowserizeUserAgent(userAgent))
		req.Header.Set("Oai-Device-Id", deviceID)
		req.Header.Set("Oai-Language", codexConversationDefaultLanguage)
		req.Header.Set("Origin", origin)
		req.Header.Set("Referer", origin+"/")
		req.Header.Set("Sec-Fetch-Dest", "empty")
		req.Header.Set("Sec-Fetch-Mode", "cors")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.Header.Set("Sec-Ch-Ua", codexConversationSecCHUA)
		req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
		req.Header.Set("Sec-Ch-Ua-Platform", `"Windows"`)
		if strings.TrimSpace(cookieHeader) != "" {
			req.Header.Set("Cookie", cookieHeader)
		}

		resp, doErr := client.Do(req)
		if doErr != nil {
			lastErr = doErr
			continue
		}

		body, readErr := ioReadAllAndClose(resp)
		if readErr != nil {
			lastErr = readErr
			continue
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			lastErr = newCodexStatusErr(resp.StatusCode, body)
			continue
		}

		var parsed codexConversationChatRequirementsResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			lastErr = err
			continue
		}
		token := strings.TrimSpace(parsed.Token)
		if token == "" {
			lastErr = fmt.Errorf("codex conversation bridge: chat-requirements response missing token")
			continue
		}

		proof := generator.generateRequirementsToken()
		if parsed.ProofOfWork.Required && strings.TrimSpace(parsed.ProofOfWork.Seed) != "" {
			proof = generator.generateToken(parsed.ProofOfWork.Seed, parsed.ProofOfWork.Difficulty)
		}

		return token, proof, extractCookieValue(resp, "oai-sc"), nil
	}

	if lastErr != nil {
		return "", "", "", lastErr
	}
	return "", "", "", fmt.Errorf("codex conversation bridge: failed to fetch chat-requirements")
}

func ioReadAllAndClose(resp *http.Response) ([]byte, error) {
	if resp == nil || resp.Body == nil {
		return nil, nil
	}
	defer func() { _ = resp.Body.Close() }()
	return io.ReadAll(resp.Body)
}

func extractCookieValue(resp *http.Response, name string) string {
	if resp == nil {
		return ""
	}
	for _, cookie := range resp.Cookies() {
		if strings.EqualFold(cookie.Name, name) && strings.TrimSpace(cookie.Value) != "" {
			return strings.TrimSpace(cookie.Value)
		}
	}
	for _, setCookie := range resp.Header.Values("Set-Cookie") {
		if value := extractCookieValueFromHeader(setCookie, name); value != "" {
			return value
		}
	}
	return ""
}

func extractCookieValueFromHeader(setCookie, name string) string {
	for _, segment := range strings.Split(setCookie, ";") {
		trimmed := strings.TrimSpace(segment)
		if trimmed == "" {
			continue
		}
		key, value, ok := strings.Cut(trimmed, "=")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(key), name) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func buildCodexConversationCookieHeader(auth *cliproxyauth.Auth, deviceID, oaiSC string) string {
	var parts []string
	if auth != nil {
		if raw := metaStringValue(auth.Metadata, "cookie"); raw != "" {
			parts = append(parts, raw)
		}
		if raw := metaStringValue(auth.Metadata, "cookies"); raw != "" {
			parts = append(parts, raw)
		}
		if sessionToken := metaStringValue(auth.Metadata, "session_token"); sessionToken != "" {
			parts = append(parts, "__Secure-next-auth.session-token="+sessionToken)
		}
		if storedOaiSC := metaStringValue(auth.Metadata, "oai_sc"); storedOaiSC != "" {
			parts = append(parts, "oai-sc="+storedOaiSC)
		}
		if auth.Attributes != nil {
			if raw := strings.TrimSpace(auth.Attributes["cookie"]); raw != "" {
				parts = append(parts, raw)
			}
			if raw := strings.TrimSpace(auth.Attributes["cookies"]); raw != "" {
				parts = append(parts, raw)
			}
			if sessionToken := strings.TrimSpace(auth.Attributes["session_token"]); sessionToken != "" {
				parts = append(parts, "__Secure-next-auth.session-token="+sessionToken)
			}
			if storedOaiSC := strings.TrimSpace(auth.Attributes["oai_sc"]); storedOaiSC != "" {
				parts = append(parts, "oai-sc="+storedOaiSC)
			}
		}
	}
	if deviceID != "" {
		parts = append(parts, "oai-did="+deviceID)
	}
	if oaiSC != "" {
		parts = append(parts, "oai-sc="+oaiSC)
	}
	return mergeCodexConversationCookies(parts...)
}

func mergeCodexConversationCookies(parts ...string) string {
	order := make([]string, 0, 8)
	values := make(map[string]string)
	for i := range parts {
		raw := strings.TrimSpace(parts[i])
		if raw == "" {
			continue
		}
		for _, segment := range strings.Split(raw, ";") {
			pair := strings.TrimSpace(segment)
			if pair == "" {
				continue
			}
			key, value, ok := strings.Cut(pair, "=")
			if !ok {
				continue
			}
			name := strings.TrimSpace(key)
			if name == "" {
				continue
			}
			if _, exists := values[name]; !exists {
				order = append(order, name)
			}
			values[name] = strings.TrimSpace(value)
		}
	}
	if len(order) == 0 {
		return ""
	}
	merged := make([]string, 0, len(order))
	for i := range order {
		name := order[i]
		merged = append(merged, name+"="+values[name])
	}
	return strings.Join(merged, "; ")
}

func codexConversationDeviceID(auth *cliproxyauth.Auth) string {
	if auth != nil {
		if value := metaStringValue(auth.Metadata, "oai_device_id"); value != "" {
			return value
		}
		if value := metaStringValue(auth.Metadata, "device_id"); value != "" {
			return value
		}
		if auth.Attributes != nil {
			if value := strings.TrimSpace(auth.Attributes["oai_device_id"]); value != "" {
				return value
			}
			if value := strings.TrimSpace(auth.Attributes["device_id"]); value != "" {
				return value
			}
		}
	}
	return uuid.NewString()
}

func codexConversationRequestOrigin(targetURL *neturl.URL) string {
	if targetURL != nil && strings.TrimSpace(targetURL.Scheme) != "" && strings.TrimSpace(targetURL.Host) != "" {
		return targetURL.Scheme + "://" + targetURL.Host
	}
	return "https://chatgpt.com"
}

func codexConversationBrowserizeUserAgent(userAgent string) string {
	trimmed := strings.TrimSpace(userAgent)
	if trimmed == "" {
		return codexConversationBrowserUserAgent
	}
	lower := strings.ToLower(trimmed)
	if strings.Contains(lower, "mozilla/") || strings.Contains(lower, "chrome/") {
		return trimmed
	}
	return codexConversationBrowserUserAgent
}

func newCodexConversationSentinelGenerator(deviceID, userAgent string) *codexConversationSentinelGenerator {
	seed := time.Now().UnixNano()
	return &codexConversationSentinelGenerator{
		deviceID:         strings.TrimSpace(deviceID),
		userAgent:        codexConversationBrowserizeUserAgent(userAgent),
		requirementsSeed: fmt.Sprintf("%.16f", rand.New(rand.NewSource(seed)).Float64()),
		sid:              uuid.NewString(),
		random:           rand.New(rand.NewSource(seed + 1)),
	}
}

func (g *codexConversationSentinelGenerator) generateToken(seed, difficulty string) string {
	if g == nil {
		return ""
	}
	normalizedSeed := strings.TrimSpace(seed)
	if normalizedSeed == "" {
		normalizedSeed = g.requirementsSeed
	}
	normalizedDifficulty := strings.TrimSpace(difficulty)
	if normalizedDifficulty == "" {
		normalizedDifficulty = "0"
	}

	start := time.Now()
	config := g.getConfig()
	for nonce := 0; nonce < codexConversationSentinelMaxAttempts; nonce++ {
		if result := g.runCheck(start, normalizedSeed, normalizedDifficulty, config, nonce); result != "" {
			return "gAAAAAB" + result
		}
	}
	return "gAAAAAB" + codexConversationSentinelErrorPrefix + g.base64Encode("None")
}

func (g *codexConversationSentinelGenerator) generateRequirementsToken() string {
	if g == nil {
		return ""
	}
	config := g.getConfig()
	config[3] = 1
	config[9] = int(math.Round(g.randomRange(5, 50)))
	return "gAAAAAC" + g.base64Encode(config)
}

func (g *codexConversationSentinelGenerator) runCheck(start time.Time, seed, difficulty string, config []any, nonce int) string {
	config[3] = nonce
	config[9] = int(math.Round(float64(time.Since(start).Milliseconds())))
	data := g.base64Encode(config)
	hashHex := codexConversationSentinelHash(seed + data)
	prefixLen := len(difficulty)
	if prefixLen <= len(hashHex) && hashHex[:prefixLen] <= difficulty {
		return data + "~S"
	}
	return ""
}

func (g *codexConversationSentinelGenerator) getConfig() []any {
	if g == nil {
		return nil
	}
	nowUTC := time.Now().UTC()
	perfNow := g.randomRange(1000, 50000)
	timeOrigin := float64(time.Now().UnixMilli()) - perfNow
	navProps := []string{
		"vendorSub", "productSub", "vendor", "maxTouchPoints",
		"scheduling", "userActivation", "doNotTrack", "geolocation",
		"connection", "plugins", "mimeTypes", "pdfViewerEnabled",
		"webkitTemporaryStorage", "webkitPersistentStorage",
		"hardwareConcurrency", "cookieEnabled", "credentials",
		"mediaDevices", "permissions", "locks", "ink",
	}
	docKeys := []string{"location", "implementation", "URL", "documentURI", "compatMode"}
	winKeys := []string{"Object", "Function", "Array", "Number", "parseFloat", "undefined"}
	hardwareConcurrency := []int{4, 8, 12, 16}

	return []any{
		"1920x1080",
		nowUTC.Format("Mon Jan 02 2006 15:04:05 GMT+0000 (Coordinated Universal Time)"),
		4294705152,
		g.random.Float64(),
		g.userAgent,
		codexConversationSentinelScriptURL,
		nil,
		nil,
		"en-US",
		"en-US,en",
		g.random.Float64(),
		navProps[g.random.Intn(len(navProps))] + "\u2212undefined",
		docKeys[g.random.Intn(len(docKeys))],
		winKeys[g.random.Intn(len(winKeys))],
		perfNow,
		g.sid,
		"",
		hardwareConcurrency[g.random.Intn(len(hardwareConcurrency))],
		timeOrigin,
	}
}

func (g *codexConversationSentinelGenerator) randomRange(min, max float64) float64 {
	return min + g.random.Float64()*(max-min)
}

func (g *codexConversationSentinelGenerator) base64Encode(data any) string {
	raw, _ := json.Marshal(data)
	return base64.StdEncoding.EncodeToString(raw)
}

func codexConversationSentinelHash(text string) string {
	var h uint32 = 2166136261
	for _, r := range text {
		h ^= uint32(r)
		h *= 16777619
	}
	h ^= h >> 16
	h *= 2246822507
	h ^= h >> 13
	h *= 3266489909
	h ^= h >> 16
	return fmt.Sprintf("%08x", h)
}
