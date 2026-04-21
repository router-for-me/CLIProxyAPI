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
	if req == nil || req.Body == nil || codexIsAPIKeyAuth(auth) || !codexRequestCompressionEnabled() {
		return nil
	}
	if encoding := strings.TrimSpace(req.Header.Get("Content-Encoding")); encoding != "" {
		return nil
	}
	if contentType := strings.ToLower(strings.TrimSpace(req.Header.Get("Content-Type"))); !strings.HasPrefix(contentType, "application/json") {
		return nil
	}

	body, err := io.ReadAll(req.Body)
	if err != nil {
		return err
	}
	if errClose := req.Body.Close(); errClose != nil {
		return errClose
	}

	var payload []byte
	if len(body) == 0 {
		payload = body
	} else {
		var compressed bytes.Buffer
		encoder, errEncoder := zstd.NewWriter(&compressed, zstd.WithEncoderLevel(zstd.SpeedFastest))
		if errEncoder != nil {
			req.Body = io.NopCloser(bytes.NewReader(body))
			req.ContentLength = int64(len(body))
			req.GetBody = func() (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(body)), nil
			}
			return errEncoder
		}
		if _, errWrite := encoder.Write(body); errWrite != nil {
			_ = encoder.Close()
			req.Body = io.NopCloser(bytes.NewReader(body))
			req.ContentLength = int64(len(body))
			req.GetBody = func() (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(body)), nil
			}
			return errWrite
		}
		if errClose := encoder.Close(); errClose != nil {
			req.Body = io.NopCloser(bytes.NewReader(body))
			req.ContentLength = int64(len(body))
			req.GetBody = func() (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(body)), nil
			}
			return errClose
		}
		payload = compressed.Bytes()
		req.Header.Set("Content-Encoding", "zstd")
	}

	req.Body = io.NopCloser(bytes.NewReader(payload))
	req.ContentLength = int64(len(payload))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(payload)), nil
	}
	return nil
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
