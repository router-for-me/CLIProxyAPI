package executor

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	exec "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
)

// CopilotExecutor supplies Copilot credentials to the existing protocol executors.
type CopilotExecutor struct {
	cfg         *config.Config
	tokenSource func(context.Context, *coreauth.Auth, bool) (copilot.Token, error)
}

func NewCopilotExecutor(cfg *config.Config) *CopilotExecutor { return &CopilotExecutor{cfg: cfg} }
func (*CopilotExecutor) Identifier() string                  { return copilot.Provider }

func (e *CopilotExecutor) RequestToFormat(req exec.Request, opts exec.Options) translator.Format {
	authID, _ := opts.Metadata[exec.SelectedAuthMetadataKey].(string)
	return e.requestToFormatForClient(authID, req, opts)
}

func (*CopilotExecutor) requestToFormatForClient(authID string, req exec.Request, opts exec.Options) translator.Format {
	r := registry.GetGlobalRegistry()
	// The registry contains public aliases and prefixes, while req.Model has
	// already been resolved to the upstream name by the scheduler.
	requestedModel, _ := opts.Metadata[exec.RequestedModelMetadataKey].(string)
	model := r.GetModelForClient(authID, thinking.ParseSuffix(requestedModel).ModelName)
	if model == nil {
		model = r.GetModelForClient(authID, thinking.ParseSuffix(req.Model).ModelName)
	}
	if model != nil {
		switch model.UpstreamEndpoint {
		case "/responses":
			return translator.FormatOpenAIResponse
		case "/v1/messages":
			return translator.FormatClaude
		}
	}
	return translator.FormatOpenAI
}

func (e *CopilotExecutor) Models(ctx context.Context, auth *coreauth.Auth) ([]*registry.ModelInfo, error) {
	if auth == nil {
		return nil, fmt.Errorf("github-copilot: missing credential")
	}
	token, _ := auth.Metadata["access_token"].(string)
	return copilot.NewClient(e.cfg, auth.ProxyURL).Models(ctx, token)
}

func (e *CopilotExecutor) preparedAuth(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	if auth == nil {
		return nil, fmt.Errorf("github-copilot: missing credential")
	}
	token, err := e.copilotToken(ctx, auth, false)
	if err != nil {
		return nil, err
	}
	prepared := auth.Clone()
	if prepared.Attributes == nil {
		prepared.Attributes = make(map[string]string)
	}
	prepared.Attributes["base_url"] = token.BaseURL()
	prepared.Attributes["api_key"] = token.Token
	prepared.Attributes["auth_kind"] = "oauth"
	headers := make(http.Header)
	copilot.SetHeaders(headers)
	headers.Set("Authorization", "Bearer "+token.Token)
	headers.Set("X-Request-Id", uuid.NewString())
	headers.Set("Openai-Intent", "conversation-panel")
	for key, values := range headers {
		prepared.Attributes["header:"+key] = values[0]
	}
	return prepared, nil
}

func (e *CopilotExecutor) delegate(auth *coreauth.Auth, req exec.Request, opts exec.Options) coreauth.ProviderExecutor {
	format := e.requestToFormatForClient(auth.ID, req, opts)
	if format == translator.FormatClaude {
		return &ClaudeExecutor{cfg: e.cfg, provider: copilot.Provider, requestLogProvider: copilot.Provider}
	}
	return &OpenAICompatExecutor{provider: copilot.Provider, cfg: e.cfg, upstreamFormat: format}
}

func (e *CopilotExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req exec.Request, opts exec.Options) (exec.Response, error) {
	if opts.Alt == "responses/compact" || openAICompatImageEndpointPath(opts) != "" {
		return exec.Response{}, statusErr{code: http.StatusNotImplemented, msg: "github-copilot: endpoint is not supported"}
	}
	prepared, err := e.preparedAuth(ctx, auth)
	if err != nil {
		return exec.Response{}, err
	}
	response, err := e.delegate(auth, req, opts).Execute(ctx, prepared, req, opts)
	return response, helps.CopilotQuotaError(err)
}

func (e *CopilotExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req exec.Request, opts exec.Options) (*exec.StreamResult, error) {
	if opts.Alt == "responses/compact" || openAICompatImageEndpointPath(opts) != "" {
		return nil, statusErr{code: http.StatusNotImplemented, msg: "github-copilot: endpoint is not supported"}
	}
	prepared, err := e.preparedAuth(ctx, auth)
	if err != nil {
		return nil, err
	}
	response, err := e.delegate(auth, req, opts).ExecuteStream(ctx, prepared, req, opts)
	return response, helps.CopilotQuotaError(err)
}

func (e *CopilotExecutor) CountTokens(ctx context.Context, auth *coreauth.Auth, req exec.Request, opts exec.Options) (exec.Response, error) {
	return NewOpenAICompatExecutor(copilot.Provider, e.cfg).CountTokens(ctx, auth, req, opts)
}

func (e *CopilotExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	if auth == nil {
		return nil, fmt.Errorf("github-copilot: missing credential")
	}
	token, err := e.copilotToken(ctx, auth, true)
	if err != nil {
		return nil, err
	}
	githubToken, _ := auth.Metadata["access_token"].(string)
	if _, errQuota := copilot.NewClient(e.cfg, auth.ProxyURL).Quota(ctx, githubToken); errQuota != nil {
		log.WithError(errQuota).Debugf("github-copilot: quota refresh failed for auth %s", auth.ID)
	}
	updated := auth.Clone()
	updated.Metadata["expired"] = time.Unix(token.ExpiresAt, 0).UTC().Format(time.RFC3339)
	updated.LastRefreshedAt = time.Now().UTC()
	return updated, nil
}

func (e *CopilotExecutor) copilotToken(ctx context.Context, auth *coreauth.Auth, force bool) (copilot.Token, error) {
	if e.tokenSource != nil {
		return e.tokenSource(ctx, auth, force)
	}
	githubToken, _ := auth.Metadata["access_token"].(string)
	return copilot.NewClient(e.cfg, auth.ProxyURL).CopilotToken(ctx, githubToken, force)
}

func (e *CopilotExecutor) PrepareRequest(req *http.Request, auth *coreauth.Auth) error {
	if req == nil {
		return nil
	}
	prepared, err := e.preparedAuth(req.Context(), auth)
	if err != nil {
		return err
	}
	// Never forward the GitHub OAuth credential to a Copilot inference endpoint.
	req.Header.Set("Authorization", "Bearer "+prepared.Attributes["api_key"])
	copilot.SetHeaders(req.Header)
	return nil
}

func (e *CopilotExecutor) HttpRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("github-copilot: missing request")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	req = req.Clone(ctx)
	if req.URL == nil || req.URL.Scheme != "https" || req.URL.User != nil || !strings.HasSuffix(req.URL.Hostname(), ".githubcopilot.com") {
		return nil, fmt.Errorf("github-copilot: unsupported request host")
	}
	if err := e.PrepareRequest(req, auth); err != nil {
		return nil, err
	}
	return helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(req)
}
