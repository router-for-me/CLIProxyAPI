package executor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	miniMaxM3ImageInlineMaxBytes  = 5 << 20
	miniMaxM3ImageInlineMaxImages = 4
)

var (
	fetchMiniMaxM3ImageURL = fetchMiniMaxM3ImageURLDefault

	miniMaxM3ImageHTTPClient = &http.Client{
		Transport: &http.Transport{
			Proxy:             nil,
			DialContext:       miniMaxM3ImageDialContext,
			ForceAttemptHTTP2: true,
		},
		CheckRedirect: miniMaxM3ImageCheckRedirect,
	}

	miniMaxM3BlockedImagePrefixes = []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("192.0.0.0/24"),
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("203.0.113.0/24"),
		netip.MustParsePrefix("240.0.0.0/4"),
		netip.MustParsePrefix("2001:db8::/32"),
	}
)

type miniMaxM3ImageInlineState struct {
	ctx       context.Context
	converted int
}

func inlineMiniMaxM3RemoteImageURLs(ctx context.Context, payload []byte, profile openAICompatProfile, model string) ([]byte, bool) {
	if !shouldInlineMiniMaxM3RemoteImageURLs(profile, model) || len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload, false
	}

	var root map[string]any
	if err := json.Unmarshal(payload, &root); err != nil {
		return payload, false
	}

	state := &miniMaxM3ImageInlineState{ctx: ctx}
	if !state.walk(root) {
		return payload, false
	}

	out, err := json.Marshal(root)
	if err != nil || !gjson.ValidBytes(out) {
		return payload, false
	}
	return out, true
}

func shouldInlineMiniMaxM3RemoteImageURLs(profile openAICompatProfile, model string) bool {
	if config.NormalizeOpenAICompatibilityKind(profile.Kind) != "minimax" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(model), "MiniMax-M3")
}

func (s *miniMaxM3ImageInlineState) walk(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		changed := s.inlineImagePart(typed)
		for _, child := range typed {
			if s.walk(child) {
				changed = true
			}
		}
		return changed
	case []any:
		changed := false
		for _, child := range typed {
			if s.walk(child) {
				changed = true
			}
		}
		return changed
	default:
		return false
	}
}

func (s *miniMaxM3ImageInlineState) inlineImagePart(part map[string]any) bool {
	partType := strings.ToLower(strings.TrimSpace(compatStringValue(part["type"])))
	switch partType {
	case "image_url":
		return s.inlineOpenAIImageURLPart(part, "image_url")
	case "input_image":
		if s.inlineOpenAIImageURLPart(part, "image_url") {
			return true
		}
		return s.inlineOpenAIImageURLPart(part, "url")
	default:
		return false
	}
}

func (s *miniMaxM3ImageInlineState) inlineOpenAIImageURLPart(part map[string]any, field string) bool {
	rawValue, ok := part[field]
	if !ok {
		return false
	}
	switch typed := rawValue.(type) {
	case string:
		if dataURL, okInline := s.inlineURL(typed); okInline {
			part[field] = dataURL
			return true
		}
	case map[string]any:
		urlValue := strings.TrimSpace(compatStringValue(typed["url"]))
		if dataURL, okInline := s.inlineURL(urlValue); okInline {
			typed["url"] = dataURL
			return true
		}
	}
	return false
}

func (s *miniMaxM3ImageInlineState) inlineURL(rawURL string) (string, bool) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" || s.converted >= miniMaxM3ImageInlineMaxImages {
		return "", false
	}
	if strings.HasPrefix(strings.ToLower(rawURL), "data:") {
		return "", false
	}
	mediaType, data, ok := fetchMiniMaxM3ImageURL(s.ctx, rawURL)
	if !ok {
		return "", false
	}
	s.converted++
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data), true
}

func fetchMiniMaxM3ImageURLDefault(ctx context.Context, rawURL string) (string, []byte, bool) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || !miniMaxM3ImageURLAllowed(parsed) {
		return "", nil, false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", nil, false
	}
	req.Header.Set("Accept", "image/png,image/jpeg,image/webp,image/gif")
	req.Header.Set("User-Agent", "cli-proxy-minimax-m3-image-fetch")

	resp, err := miniMaxM3ImageHTTPClient.Do(req)
	if err != nil {
		return "", nil, false
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("openai compat executor: close minimax m3 image fetch body error: %v", errClose)
		}
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", nil, false
	}
	if resp.ContentLength > miniMaxM3ImageInlineMaxBytes {
		return "", nil, false
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, miniMaxM3ImageInlineMaxBytes+1))
	if err != nil || len(data) == 0 || len(data) > miniMaxM3ImageInlineMaxBytes {
		return "", nil, false
	}
	mediaType := miniMaxM3AllowedImageMediaType(resp.Header.Get("Content-Type"), data)
	if mediaType == "" {
		return "", nil, false
	}
	return mediaType, data, true
}

func miniMaxM3ImageCheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 3 {
		return fmt.Errorf("too many redirects")
	}
	if req == nil || req.URL == nil || !miniMaxM3ImageURLAllowed(req.URL) {
		return fmt.Errorf("blocked image redirect")
	}
	return nil
}

func miniMaxM3ImageURLAllowed(parsed *url.URL) bool {
	if parsed == nil {
		return false
	}
	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	if scheme != "http" && scheme != "https" {
		return false
	}
	if parsed.User != nil {
		return false
	}
	host := strings.TrimSpace(parsed.Hostname())
	if host == "" {
		return false
	}
	lowerHost := strings.ToLower(strings.TrimSuffix(host, "."))
	if lowerHost == "localhost" || strings.HasSuffix(lowerHost, ".localhost") || strings.HasSuffix(lowerHost, ".local") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return miniMaxM3ImageIPAllowed(ip)
	}
	return true
}

func miniMaxM3ImageDialContext(ctx context.Context, network string, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("image host resolved to no addresses")
	}
	for _, candidate := range ips {
		if !miniMaxM3ImageIPAllowed(candidate.IP) {
			return nil, fmt.Errorf("blocked image host address")
		}
	}

	var dialer net.Dialer
	var lastErr error
	for _, candidate := range ips {
		conn, errDial := dialer.DialContext(ctx, network, net.JoinHostPort(candidate.IP.String(), port))
		if errDial == nil {
			return conn, nil
		}
		lastErr = errDial
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("image host dial failed")
}

func miniMaxM3ImageIPAllowed(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	if !addr.IsValid() ||
		addr.IsUnspecified() ||
		addr.IsLoopback() ||
		addr.IsPrivate() ||
		addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() ||
		addr.IsMulticast() {
		return false
	}
	for _, prefix := range miniMaxM3BlockedImagePrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

func miniMaxM3AllowedImageMediaType(contentType string, data []byte) string {
	if parsed, _, err := mime.ParseMediaType(strings.TrimSpace(contentType)); err == nil {
		if normalized := normalizeMiniMaxM3ImageMediaType(parsed); normalized != "" {
			return normalized
		}
	}
	return normalizeMiniMaxM3ImageMediaType(http.DetectContentType(data))
}

func normalizeMiniMaxM3ImageMediaType(mediaType string) string {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "image/jpeg", "image/jpg":
		return "image/jpeg"
	case "image/png":
		return "image/png"
	case "image/webp":
		return "image/webp"
	case "image/gif":
		return "image/gif"
	default:
		return ""
	}
}

func redactOpenAICompatImageDataURLsForLog(payload []byte) []byte {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload
	}
	var root any
	if err := json.Unmarshal(payload, &root); err != nil {
		return payload
	}
	if !redactImageDataURLs(root) {
		return payload
	}
	out, err := json.Marshal(root)
	if err != nil || !gjson.ValidBytes(out) {
		return payload
	}
	return out
}

func redactImageDataURLs(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		changed := false
		for key, child := range typed {
			if str, ok := child.(string); ok {
				if _, _, okData := util.ParseDataURL(str); okData {
					typed[key] = "<image data URL omitted>"
					changed = true
					continue
				}
			}
			if redactImageDataURLs(child) {
				changed = true
			}
		}
		return changed
	case []any:
		changed := false
		for _, child := range typed {
			if redactImageDataURLs(child) {
				changed = true
			}
		}
		return changed
	default:
		return false
	}
}
