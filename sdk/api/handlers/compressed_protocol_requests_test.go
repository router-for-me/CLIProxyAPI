package handlers_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/gemini"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httpcompression"
)

func TestCompressedProtocolRequestsEnforceDecodedLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	claudeHandler := claude.NewClaudeCodeAPIHandler(base)
	geminiHandler := gemini.NewGeminiAPIHandler(base)
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set(handlers.MaxRequestBodyBytesContextKey, int64(256)) })
	router.POST("/v1/messages", claudeHandler.ClaudeMessages)
	router.POST("/v1/messages/count_tokens", claudeHandler.ClaudeCountTokens)
	router.POST("/v1beta/models/*action", geminiHandler.GeminiHandler)
	router.POST("/v1beta/interactions", geminiHandler.Interactions)
	server := httptest.NewServer(router)
	defer server.Close()
	payload := []byte(`{"model":"compression-limit","input":"` + strings.Repeat("a", 4096) + `"}`)
	for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens", "/v1beta/models/compression-limit:generateContent", "/v1beta/models/compression-limit:countTokens", "/v1beta/interactions"} {
		for _, encoding := range []string{"gzip", "br", "zstd"} {
			t.Run(path+"/"+encoding, func(t *testing.T) {
				var compressed bytes.Buffer
				encoder, err := httpcompression.Encode(&compressed, encoding)
				if err != nil {
					t.Fatal(err)
				}
				if _, errWrite := encoder.Write(payload); errWrite != nil {
					t.Fatal(errWrite)
				}
				if errClose := encoder.Close(); errClose != nil {
					t.Fatal(errClose)
				}
				if compressed.Len() >= 256 {
					t.Fatal("fixture must fit wire limit")
				}
				request, err := http.NewRequest(http.MethodPost, server.URL+path, &compressed)
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Content-Encoding", encoding)
				response, err := server.Client().Do(request)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				if response.StatusCode != http.StatusRequestEntityTooLarge {
					body, _ := io.ReadAll(response.Body)
					t.Fatalf("status=%d body=%s; decoded limit must fail before provider execution", response.StatusCode, body)
				}
			})
		}
	}
}

func TestRepeatedRequestContentEncoding(t *testing.T) {
	const payload = `{"model":"stacked-codecs","input":"preserve this input"}`
	var compressed bytes.Buffer
	outer, err := httpcompression.Encode(&compressed, "br")
	if err != nil {
		t.Fatal(err)
	}
	inner, err := httpcompression.Encode(outer, "gzip")
	if err != nil {
		t.Fatal(err)
	}
	if _, errWrite := io.WriteString(inner, payload); errWrite != nil {
		t.Fatal(errWrite)
	}
	if errClose := inner.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	if errClose := outer.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	router := gin.New()
	router.POST("/", func(c *gin.Context) {
		body, errRead := handlers.ReadRequestBody(c)
		if errRead != nil {
			c.String(400, "%v", errRead)
			return
		}
		c.Data(200, "application/json", body)
	})
	server := httptest.NewServer(router)
	defer server.Close()
	request, err := http.NewRequest(http.MethodPost, server.URL, &compressed)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Add("Content-Encoding", "gzip")
	request.Header.Add("Content-Encoding", "br")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	got, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK || string(got) != payload {
		t.Fatalf("status=%d body=%q error=%v", response.StatusCode, got, err)
	}
}
