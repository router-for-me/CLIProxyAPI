package helps

import (
	"errors"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

// ResolveCursorRequestModel shares request preparation across streaming modes.
func ResolveCursorRequestModel(authID string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, fallback func(string) []*registry.ModelInfo) (string, error) {
	models := CursorRoutingModels(authID, nil)
	if models == nil {
		models = fallback(authID)
	}
	payload := req.Payload
	if len(opts.OriginalRequest) > 0 {
		payload = opts.OriginalRequest
	}
	model, err := ResolveCursorModel(req.Model, payload, opts.SourceFormat.String(), models)
	if err != nil {
		if errors.Is(err, ErrCursorVariantUnavailable) {
			return "", &cliproxyauth.Error{
				Code:       cliproxyauth.ErrorCodeModelVariantUnavailable,
				Message:    err.Error(),
				HTTPStatus: http.StatusBadRequest,
				Retryable:  true,
			}
		}
		return "", cliproxyauth.NewRequestScopedError(err.Error(), http.StatusBadRequest)
	}
	log.WithFields(log.Fields{"requested_model": req.Model, "upstream_model": model}).Debug("cursor: resolved model variant")
	return model, nil
}
