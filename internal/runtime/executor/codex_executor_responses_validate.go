package executor

import (
	"net/http"

	codexresponses "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/codex/openai/responses"
	"github.com/tidwall/sjson"
)

func validateCodexExecutorUpstreamBody(body []byte) error {
	verr := codexresponses.ValidateCodexResponsesInput(body)
	if verr == nil {
		return nil
	}
	payload := []byte("{\"error\":{}}")
	payload, _ = sjson.SetBytes(payload, "error.message", verr.Message)
	payload, _ = sjson.SetBytes(payload, "error.type", "invalid_request_error")
	payload, _ = sjson.SetBytes(payload, "error.code", "invalid_value")
	if verr.Param != "" {
		payload, _ = sjson.SetBytes(payload, "error.param", verr.Param)
	}
	return newCodexStatusErr(http.StatusBadRequest, payload)
}
