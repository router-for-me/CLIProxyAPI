package jimeng

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/logger"
	"github.com/gin-gonic/gin"
)

// SignRequestForJimeng 对即梦 API 请求进行签名，支持 http.Request 或 header+url+body 方式
//func SignRequestForJimeng(req *http.Request, accessKey, secretKey string) error {
//	var bodyBytes []byte
//	var err error
//
//	if req.Body != nil {
//		bodyBytes, err = io.ReadAll(req.Body)
//		if err != nil {
//			return fmt.Errorf("read request body failed: %w", err)
//		}
//		_ = req.Body.Close()
//		req.Body = io.NopCloser(bytes.NewBuffer(bodyBytes)) // rewind
//	} else {
//		bodyBytes = []byte{}
//	}
//
//	return signJimengHeaders(&req.Header, req.Method, req.URL, bodyBytes, accessKey, secretKey)
//}

const HexPayloadHashKey = "HexPayloadHash"

func SetPayloadHash(c *gin.Context, req any) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	logger.LogInfo(c, fmt.Sprintf("SetPayloadHash body: %s", body))
	payloadHash := sha256.Sum256(body)
	hexPayloadHash := hex.EncodeToString(payloadHash[:])
	c.Set(HexPayloadHashKey, hexPayloadHash)
	return nil
}
func getPayloadHash(c *gin.Context) string {
	return c.GetString(HexPayloadHashKey)
}

func Sign(c *gin.Context, req *http.Request, apiKey string) error {
	header := req.Header

	var bodyBytes []byte
	var err error

	if req.Body != nil {
		bodyBytes, err = io.ReadAll(req.Body)
		if err != nil {
			return err
		}
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewBuffer(bodyBytes)) // Rewind
	}

	payloadHash := sha256.Sum256(bodyBytes)
	hexPayloadHash := hex.EncodeToString(payloadHash[:])

	method := c.Request.Method
	u := req.URL
	keyParts := strings.Split(apiKey, "|")
	if len(keyParts) != 2 {
		return errors.New("invalid api key format for jimeng: expected 'ak|sk'")
	}
	accessKey := strings.TrimSpace(keyParts[0])
	secretKey := strings.TrimSpace(keyParts[1])
	t := time.Now().UTC()
	xDate := t.Format("20060102T150405Z")
	shortDate := t.Format("20060102")

	host := u.Host
	header.Set("Host", host)
	header.Set("X-Date", xDate)
	header.Set("X-Content-Sha256", hexPayloadHash)

	// Sort and encode query parameters to create canonical query string
	queryParams := u.Query()
	sortedKeys := make([]string, 0, len(queryParams))
	for k := range queryParams {
		sortedKeys = append(sortedKeys, k)
	}
	sort.Strings(sortedKeys)
	var queryParts []string
	for _, k := range sortedKeys {
		values := queryParams[k]
		sort.Strings(values)
		for _, v := range values {
			queryParts = append(queryParts, fmt.Sprintf("%s=%s", url.QueryEscape(k), url.QueryEscape(v)))
		}
	}
	canonicalQueryString := strings.Join(queryParts, "&")

	headersToSign := map[string]string{
		"host":             host,
		"x-date":           xDate,
		"x-content-sha256": hexPayloadHash,
	}
	if header.Get("Content-Type") == "" {
		header.Set("Content-Type", "application/json")
	}
	headersToSign["content-type"] = header.Get("Content-Type")

	var signedHeaderKeys []string
	for k := range headersToSign {
		signedHeaderKeys = append(signedHeaderKeys, k)
	}
	sort.Strings(signedHeaderKeys)

	var canonicalHeaders strings.Builder
	for _, k := range signedHeaderKeys {
		canonicalHeaders.WriteString(k)
		canonicalHeaders.WriteString(":")
		canonicalHeaders.WriteString(strings.TrimSpace(headersToSign[k]))
		canonicalHeaders.WriteString("\n")
	}
	signedHeaders := strings.Join(signedHeaderKeys, ";")

	canonicalRequest := fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n%s",
		method,
		u.Path,
		canonicalQueryString,
		canonicalHeaders.String(),
		signedHeaders,
		hexPayloadHash,
	)

	hashedCanonicalRequest := sha256.Sum256([]byte(canonicalRequest))
	hexHashedCanonicalRequest := hex.EncodeToString(hashedCanonicalRequest[:])

	region := "cn-north-1"
	serviceName := "cv"
	credentialScope := fmt.Sprintf("%s/%s/%s/request", shortDate, region, serviceName)
	stringToSign := fmt.Sprintf("HMAC-SHA256\n%s\n%s\n%s",
		xDate,
		credentialScope,
		hexHashedCanonicalRequest,
	)

	kDate := hmacSHA256([]byte(secretKey), []byte(shortDate))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(serviceName))
	kSigning := hmacSHA256(kService, []byte("request"))
	signature := hex.EncodeToString(hmacSHA256(kSigning, []byte(stringToSign)))

	authorization := fmt.Sprintf("HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		accessKey,
		credentialScope,
		signedHeaders,
		signature,
	)
	header.Set("Authorization", authorization)
	return nil
}

// hmacSHA256 计算 HMAC-SHA256
func hmacSHA256(key []byte, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}
