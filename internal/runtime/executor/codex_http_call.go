package executor

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

// PrepareRequest injects Codex credentials into the outgoing HTTP request.
func (e *CodexExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	apiKey, _ := codexCreds(auth)
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest injects Codex credentials into the request and executes it.
func (e *CodexExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("codex executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	httpClient := helps.NewCodexFingerprintHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

type codexPreparedHTTPCall struct {
	url        string
	prepared   codexPreparedRequest
	requestLog helps.UpstreamRequestLog
}

func (e *CodexExecutor) prepareCodexHTTPCall(
	ctx context.Context,
	auth *cliproxyauth.Auth,
	from sdktranslator.Format,
	executionSessionID string,
	url string,
	req cliproxyexecutor.Request,
	body []byte,
	token string,
	stream bool,
) (codexPreparedHTTPCall, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	requestKind := codexFinalUpstreamRequestKindForURL(url)
	streamMode := codexStreamFieldTrue
	if requestKind == codexFinalUpstreamCompact {
		streamMode = codexStreamFieldDelete
	}
	body = normalizeCodexFinalUpstreamBody(body, baseModel, auth, codexFinalUpstreamBodyOptions{
		requestKind: requestKind,
		streamMode:  streamMode,
	})
	if requestKind != codexFinalUpstreamCompact {
		body = codexApplyHTTPClientMetadataWithSource(body, nil, codexGinHeadersFromContext(ctx), auth, e.cfg)
	}
	prepared, err := e.prepareCodexRequest(ctx, from, executionSessionID, url, req, body)
	if err != nil {
		return codexPreparedHTTPCall{}, err
	}
	applyCodexHeaders(prepared.httpReq, auth, token, stream, e.cfg)
	if requestKind == codexFinalUpstreamCompact {
		if installationID := codexResolvedInstallationID(prepared.httpReq.Header, codexGinHeadersFromContext(prepared.httpReq.Context()), auth, e.cfg); installationID != "" {
			prepared.httpReq.Header.Set(codexHeaderInstallationID, installationID)
		}
	}
	if err := maybeEnableCodexRequestCompressionWithBody(prepared.httpReq, auth, prepared.body); err != nil {
		return codexPreparedHTTPCall{}, fmt.Errorf("codex executor: request compression failed: %w", err)
	}
	return codexPreparedHTTPCall{
		url:      url,
		prepared: prepared,
		requestLog: codexUpstreamRequestLog(
			url,
			http.MethodPost,
			prepared.httpReq.Header,
			prepared.body,
			e.Identifier(),
			auth,
		),
	}, nil
}

func codexUpstreamRequestLog(
	url string,
	method string,
	headers http.Header,
	body []byte,
	provider string,
	auth *cliproxyauth.Auth,
) helps.UpstreamRequestLog {
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	return helps.UpstreamRequestLog{
		URL:       url,
		Method:    method,
		Headers:   headers,
		Body:      body,
		Provider:  provider,
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	}
}
