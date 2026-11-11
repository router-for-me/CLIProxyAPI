package helps

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

func isAntigravityVertexSearchRedirect(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return parsed.Scheme == "https" &&
		parsed.Host == "vertexaisearch.cloud.google.com" &&
		strings.HasPrefix(parsed.Path, "/grounding-api-redirect/")
}

func resolveAntigravityGroundingURL(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, rawURL string) string {
	if !isAntigravityVertexSearchRedirect(rawURL) {
		return rawURL
	}
	client := NewProxyAwareHTTPClient(ctx, cfg, auth, 0)
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	req, errReq := http.NewRequestWithContext(ctx, http.MethodHead, rawURL, nil)
	if errReq != nil {
		log.WithError(errReq).Debug("antigravity grounding url: create redirect request failed")
		return rawURL
	}
	resp, errDo := client.Do(req)
	if errDo != nil {
		log.WithError(errDo).Debug("antigravity grounding url: resolve redirect failed")
		return rawURL
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithError(errClose).Debug("antigravity grounding url: close redirect response failed")
		}
	}()

	if resp.StatusCode < http.StatusMultipleChoices || resp.StatusCode >= http.StatusBadRequest {
		return rawURL
	}
	location := strings.TrimSpace(resp.Header.Get("Location"))
	if location == "" {
		return rawURL
	}
	parsed, errParse := url.Parse(location)
	if errParse != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return rawURL
	}
	return location
}

// ResolveAntigravityGroundingURLs replaces Vertex Search redirect URLs in grounding chunks with their target URLs.
func ResolveAntigravityGroundingURLs(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, payload []byte) []byte {
	if len(payload) == 0 {
		return payload
	}
	doc := ojson.NewDoc(payload)
	if !ResolveAntigravityGroundingURLsDoc(ctx, cfg, auth, doc) {
		return payload
	}
	return doc.Bytes()
}

// ResolveAntigravityGroundingURLsDoc replaces Vertex Search redirect URLs in grounding chunks in place on doc.
func ResolveAntigravityGroundingURLsDoc(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, doc *ojson.Doc) bool {
	if doc == nil || doc.Len() == 0 {
		return false
	}

	basePath := "response.candidates.0.groundingMetadata.groundingChunks"
	chunks := doc.Get(basePath)
	if !chunks.IsArray() {
		basePath = "candidates.0.groundingMetadata.groundingChunks"
		chunks = doc.Get(basePath)
	}
	if !chunks.IsArray() {
		return false
	}

	modified := false
	resolved := map[string]string{}
	for i, chunk := range chunks.Array() {
		uri := strings.TrimSpace(chunk.Get("web.uri").String())
		if uri == "" {
			continue
		}
		resolvedURI, ok := resolved[uri]
		if !ok {
			resolvedURI = resolveAntigravityGroundingURL(ctx, cfg, auth, uri)
			resolved[uri] = resolvedURI
		}
		if resolvedURI == uri {
			continue
		}
		if errSet := doc.Set(fmt.Sprintf("%s.%d.web.uri", basePath, i), resolvedURI); errSet != nil {
			log.WithError(errSet).Debug("antigravity grounding url: set resolved url failed")
			continue
		}
		modified = true
	}
	return modified
}
