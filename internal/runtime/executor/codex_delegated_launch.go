package executor

import (
	"context"
	"errors"

	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	execution "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// ExecuteDelegatedLaunch uses only HTTP and the supplied immutable credential.
// Final business-payload model validation happens in buildCacheRequest after
// user payload rules. No websocket transport/session or selection retry is used.
func (e *CodexAutoExecutor) ExecuteDelegatedLaunch(ctx context.Context, a *auth.Auth, req execution.Request, opts execution.Options) (execution.Response, *execution.StreamResult, error) {
	if e == nil || e.httpExec == nil || codexUseSIWC(a) || opts.Alt != "" {
		return execution.Response{}, nil, errors.New("delegated launch unsupported")
	}
	ctx = execution.WithBoundModel(ctx, req.Model)
	if opts.Stream {
		stream, err := e.httpExec.ExecuteStream(ctx, a, req, opts)
		return execution.Response{}, stream, err
	}
	response, err := e.httpExec.Execute(ctx, a, req, opts)
	return response, nil, err
}
