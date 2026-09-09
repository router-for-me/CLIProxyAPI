package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
)

// ModelPrefixAccessError reports a client permission error without upstream quota semantics.
func ModelPrefixAccessError() *interfaces.ErrorMessage {
	body, _ := json.Marshal(ErrorResponse{Error: ErrorDetail{
		Message: "model is not allowed for this API key; use an authorized prefix/model ID",
		Type:    "permission_error",
		Code:    "model_not_allowed",
	}})
	return &interfaces.ErrorMessage{
		StatusCode: http.StatusForbidden,
		Error:      errors.New(string(body)),
	}
}
