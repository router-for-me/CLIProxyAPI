package executor

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/klauspost/compress/zstd"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

const codexCompressionEnv = "CODEX_ENABLE_ZSTD_REQUEST_COMPRESSION"

func maybeEnableCodexRequestCompression(req *http.Request, auth *cliproxyauth.Auth) error {
	return maybeEnableCodexRequestCompressionWithBody(req, auth, nil)
}

func maybeEnableCodexRequestCompressionWithBody(req *http.Request, auth *cliproxyauth.Auth, body []byte) error {
	if req == nil || codexIsAPIKeyAuth(auth) || !codexRequestCompressionEnabled() {
		return nil
	}
	if encoding := strings.TrimSpace(req.Header.Get("Content-Encoding")); encoding != "" {
		return nil
	}
	if contentType := strings.ToLower(strings.TrimSpace(req.Header.Get("Content-Type"))); !strings.HasPrefix(contentType, "application/json") {
		return nil
	}

	payload := body
	if payload == nil {
		if req.Body == nil {
			return nil
		}
		readBody, err := io.ReadAll(req.Body)
		if err != nil {
			return err
		}
		if errClose := req.Body.Close(); errClose != nil {
			return errClose
		}
		payload = readBody
	}

	if len(payload) == 0 {
		codexResetRequestBody(req, payload)
		return nil
	}

	compressed, err := compressCodexRequestBody(payload)
	if err != nil {
		codexResetRequestBody(req, payload)
		return err
	}
	req.Header.Set("Content-Encoding", "zstd")
	codexResetRequestBody(req, compressed)
	return nil
}

func compressCodexRequestBody(body []byte) ([]byte, error) {
	var compressed bytes.Buffer
	encoder, err := zstd.NewWriter(&compressed, zstd.WithEncoderLevel(zstd.SpeedFastest))
	if err != nil {
		return nil, err
	}
	if _, errWrite := encoder.Write(body); errWrite != nil {
		_ = encoder.Close()
		return nil, errWrite
	}
	if errClose := encoder.Close(); errClose != nil {
		return nil, errClose
	}
	return compressed.Bytes(), nil
}

func codexRequestCompressionEnabled() bool {
	value := strings.TrimSpace(os.Getenv(codexCompressionEnv))
	switch strings.ToLower(value) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
