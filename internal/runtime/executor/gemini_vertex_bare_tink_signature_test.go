package executor

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	internalsignature "github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// testVertexBareTinkSignature mirrors the Vertex AI serve-path shape: a TINK
// prefix byte, a four-byte key id and filler ciphertext, with no protobuf
// wrapper. Synthetic only; no provider payload is embedded.
func testVertexBareTinkSignature() string {
	payload := []byte{0x01, 0x8f, 0x3d, 0x6b, 0x5f}
	for len(payload) < 405 {
		payload = append(payload, byte(0xA0+len(payload)%0x20))
	}
	return base64.StdEncoding.EncodeToString(payload)
}

func vertexBareTinkGeminiHistory(sig string) []byte {
	return []byte(`{"contents":[` +
		`{"role":"user","parts":[{"text":"list"}]},` +
		`{"role":"model","parts":[{"functionCall":{"name":"list_dir","args":{"path":"."}},"thoughtSignature":"` + sig + `"}]},` +
		`{"role":"user","parts":[{"functionResponse":{"name":"list_dir","response":{"files":[]}}}]}],` +
		`"tools":[{"functionDeclarations":[{"name":"list_dir","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}]}]}`)
}

const vertexBareTinkTestOKResponse = `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`

func TestGeminiExecutorReplacesVertexBareTinkSignature(t *testing.T) {
	sig := testVertexBareTinkSignature()
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		upstreamBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(vertexBareTinkTestOKResponse))
	}))
	defer server.Close()

	exec := NewGeminiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL}}
	req := cliproxyexecutor.Request{Model: "gemini-3.1-pro-preview", Payload: vertexBareTinkGeminiHistory(sig)}
	if _, err := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	contents := gjson.GetBytes(upstreamBody, "contents").Array()
	found := false
	for _, content := range contents {
		for _, part := range content.Get("parts").Array() {
			if !part.Get("functionCall").Exists() {
				continue
			}
			found = true
			if got := part.Get("thoughtSignature").String(); got != internalsignature.GeminiSkipThoughtSignatureValidator {
				t.Fatalf("functionCall signature = %q, want bypass sentinel; body=%s", got, upstreamBody)
			}
		}
	}
	if !found {
		t.Fatalf("functionCall missing from upstream body: %s", upstreamBody)
	}
}

func TestAntigravityExecutorReplacesVertexBareTinkSignature(t *testing.T) {
	sig := testVertexBareTinkSignature()
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		upstreamBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":` + vertexBareTinkTestOKResponse + `}`))
	}))
	defer server.Close()

	exec := NewAntigravityExecutor(&config.Config{RequestRetry: 1})
	auth := testAntigravityAuth(server.URL)
	auth.Metadata["project_id"] = "project-1"
	req := cliproxyexecutor.Request{Model: "gemini-3.6-flash-high", Payload: vertexBareTinkGeminiHistory(sig)}
	_, errExec := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatGemini,
		ResponseFormat:  sdktranslator.FormatGemini,
		OriginalRequest: req.Payload,
	})
	if len(upstreamBody) == 0 {
		t.Fatalf("upstream body was not captured; Execute error = %v", errExec)
	}
	if bytes.Contains(upstreamBody, []byte(sig)) {
		t.Fatalf("Vertex bare-Tink signature leaked to Antigravity upstream: %s", upstreamBody)
	}
	if !bytes.Contains(upstreamBody, []byte(internalsignature.GeminiSkipThoughtSignatureValidator)) {
		t.Fatalf("bypass sentinel missing from Antigravity upstream: %s", upstreamBody)
	}
}

func TestGeminiVertexSanitizePreservesBareTinkSignature(t *testing.T) {
	sig := testVertexBareTinkSignature()
	out := internalsignature.SanitizeGeminiRequestThoughtSignatures(vertexBareTinkGeminiHistory(sig), "contents")
	if got := gjson.GetBytes(out, "contents.1.parts.0.thoughtSignature").String(); got != sig {
		t.Fatalf("Vertex path signature = %q, want original bare-Tink value", got)
	}
}
