package codearts

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

func SignRequest(req *http.Request, body []byte, ak, sk, securityToken string) {
	now := time.Now().UTC()
	timeStr := now.Format("20060102T150405Z")

	req.Header.Set("X-Sdk-Date", timeStr)
	req.Header.Set("host", req.URL.Host)
	if securityToken != "" {
		req.Header.Set("X-Security-Token", securityToken)
	}

	signedHeaderKeys := extractSignedHeaders(req.Header)

	canonicalURI := buildCanonicalURI(req.URL.Path)
	canonicalQuery := buildCanonicalQueryString(req.URL.Query())
	canonicalHdrs := buildCanonicalHeaders(req, signedHeaderKeys)
	signedHeadersStr := strings.Join(signedHeaderKeys, ";")

	bodyHash := sha256Hex(body)

	canonicalReq := fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n%s",
		req.Method, canonicalURI, canonicalQuery,
		canonicalHdrs, signedHeadersStr, bodyHash)

	stringToSign := fmt.Sprintf("SDK-HMAC-SHA256\n%s\n%s",
		timeStr, sha256Hex([]byte(canonicalReq)))

	signature := hmacSHA256Hex([]byte(sk), []byte(stringToSign))

	authHeader := fmt.Sprintf("SDK-HMAC-SHA256 Access=%s, SignedHeaders=%s, Signature=%s",
		ak, signedHeadersStr, signature)
	req.Header.Set("Authorization", authHeader)
}

func extractSignedHeaders(headers http.Header) []string {
	var sh []string
	for key := range headers {
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "content-type") || strings.Contains(lower, "_") {
			continue
		}
		sh = append(sh, lower)
	}
	sort.Strings(sh)
	return sh
}

func buildCanonicalURI(rawPath string) string {
	parts := strings.Split(rawPath, "/")
	var encoded []string
	for _, p := range parts {
		encoded = append(encoded, sdkEscape(p))
	}
	path := strings.Join(encoded, "/")
	if len(path) == 0 || path[len(path)-1] != '/' {
		path = path + "/"
	}
	return path
}

func buildCanonicalQueryString(query url.Values) string {
	if len(query) == 0 {
		return ""
	}
	keys := make([]string, 0, len(query))
	for k := range query {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vals := query[k]
		sort.Strings(vals)
		for _, v := range vals {
			parts = append(parts, sdkEscape(k)+"="+sdkEscape(v))
		}
	}
	return strings.Join(parts, "&")
}

func buildCanonicalHeaders(req *http.Request, signedHeaderKeys []string) string {
	headerMap := make(map[string][]string)
	for k, v := range req.Header {
		lower := strings.ToLower(k)
		if _, ok := headerMap[lower]; !ok {
			headerMap[lower] = make([]string, 0)
		}
		headerMap[lower] = append(headerMap[lower], v...)
	}

	var lines []string
	for _, key := range signedHeaderKeys {
		values := headerMap[key]
		if key == "host" {
			values = []string{req.URL.Host}
		}
		sort.Strings(values)
		for _, v := range values {
			lines = append(lines, key+":"+strings.TrimSpace(v))
		}
	}
	return fmt.Sprintf("%s\n", strings.Join(lines, "\n"))
}

func sdkEscape(s string) string {
	hexCount := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if shouldEscape(c) {
			hexCount++
		}
	}
	if hexCount == 0 {
		return s
	}
	t := make([]byte, len(s)+2*hexCount)
	j := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if shouldEscape(c) {
			t[j] = '%'
			t[j+1] = "0123456789ABCDEF"[c>>4]
			t[j+2] = "0123456789ABCDEF"[c&15]
			j += 3
		} else {
			t[j] = s[i]
			j++
		}
	}
	return string(t)
}

func shouldEscape(c byte) bool {
	return !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-' || c == '~' || c == '.')
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func hmacSHA256Hex(key, data []byte) string {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}
