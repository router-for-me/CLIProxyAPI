package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/homeplugins"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"golang.org/x/net/context"
)

type homeLocalExecutorHost interface {
	HomeLocalExecutorFence() func() bool
}

type homeLocalExecution struct {
	homeplugins.LocalExecutorGrant
	principal string
	access    string

	// Keep the authorized request so local dispatch does not intercept it again.
	request   coreexecutor.Request
	options   coreexecutor.Options
	lifecycle *requestLifecycleTracker
}

func (h *BaseAPIHandler) isHomeLocalExecutorModel(model string) bool {
	return h.AuthManager != nil && h.AuthManager.HomeEnabled() &&
		strings.HasPrefix(strings.TrimSpace(model), homeplugins.ACPModelPrefix)
}

func (h *BaseAPIHandler) authorizeHomeLocalExecution(ctx context.Context, model string, opts coreexecutor.Options, skipRouterPluginID string) (*homeLocalExecution, *interfaces.ErrorMessage) {
	if !h.isHomeLocalExecutorModel(model) {
		return nil, nil
	}
	parsed := thinking.ParseSuffix(strings.TrimSpace(model))
	var current func() bool
	if host, ok := h.pluginExecutorHost().(homeLocalExecutorHost); ok {
		current = host.HomeLocalExecutorFence()
	}
	decision, homeCurrent, errAuthorize := h.AuthManager.AuthorizeHomeLocalExecutor(
		ctx, parsed.ModelName, opts.Headers, opts.Query,
	)
	if errAuthorize != nil {
		return nil, &interfaces.ErrorMessage{StatusCode: http.StatusServiceUnavailable, Error: errAuthorize}
	}
	if decision.Error != nil {
		var status int
		switch decision.Error.Type {
		case "error":
			if decision.Error.Message == "unsupported type" {
				return nil, nil
			}
		case "no_credentials", "invalid_credential":
			status = http.StatusUnauthorized
		case "model_forbidden":
			status = http.StatusForbidden
		case "model_not_found":
			status = http.StatusNotFound
		case "user_credits_insufficient":
			status = http.StatusPaymentRequired
		case "user_period_limit_exceeded":
			status = http.StatusTooManyRequests
		case "local_executor_unavailable":
			status = http.StatusServiceUnavailable
		}
		if status == 0 {
			status = http.StatusBadGateway
		}
		return nil, &interfaces.ErrorMessage{StatusCode: status, Error: errors.New(decision.Error.Message)}
	}
	if decision.Kind == "credential" {
		return nil, nil
	}
	if decision.Kind != home.LocalExecutorDispatchKind || decision.Plugin == "" || decision.Version == "" || decision.ConfigDigest == "" ||
		parsed.ModelName != homeplugins.ACPModelPrefix+decision.Model || decision.Model == "" ||
		strings.TrimSpace(decision.Principal) == "" {
		return nil, &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: errors.New("invalid Home local plugin authorization")}
	}
	if decision.Plugin == strings.TrimSpace(skipRouterPluginID) {
		return nil, &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error: &coreauth.Error{
				Code:       "local_executor_excluded",
				Message:    "Home selected a local plugin executor excluded from this request",
				HTTPStatus: http.StatusBadRequest,
			},
		}
	}
	if current == nil || !current() {
		return nil, &interfaces.ErrorMessage{StatusCode: http.StatusServiceUnavailable, Error: errors.New("local plugin executor is unavailable")}
	}
	return &homeLocalExecution{
		LocalExecutorGrant: homeplugins.LocalExecutorGrant{
			Plugin: decision.Plugin, Model: decision.Model, Version: decision.Version, ConfigDigest: decision.ConfigDigest,
			Valid: func() bool { return homeCurrent() && current() },
		},
		principal: decision.Principal, access: decision.Access,
	}, nil
}

func (grant *homeLocalExecution) prepare(ctx context.Context, req *coreexecutor.Request, requestedModel string) context.Context {
	req.Model = grant.Model
	if parsed := thinking.ParseSuffix(strings.TrimSpace(requestedModel)); parsed.HasSuffix {
		req.Model += "(" + parsed.RawSuffix + ")"
	}
	return homeplugins.WithLocalExecutorGrant(ctx, grant.LocalExecutorGrant)
}

func (grant *homeLocalExecution) usageContext(ctx context.Context, requestedModel string) context.Context {
	ctx = coreusage.WithCapturedIdentity(ctx, grant.principal, grant.access, coreusage.IsNativeAccessProvider(grant.access))
	return coreusage.WithRequestedModelAlias(ctx, strings.TrimSpace(requestedModel))
}
