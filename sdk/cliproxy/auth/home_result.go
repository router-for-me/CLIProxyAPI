package auth

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

const homeResultExecutorType = "home-result"

type homeAuthPreparationModelKey struct{}

// ReportHomeAuthPreparationFailure publishes an upstream 401 or 429 using the
// credential snapshot used for that call. Report from the preparer, not from each
// request waiting on a shared preparation.
func ReportHomeAuthPreparationFailure(ctx context.Context, auth *Auth, status int, body []byte) {
	if ctx == nil || (status != http.StatusUnauthorized && status != http.StatusTooManyRequests) {
		return
	}
	model, _ := ctx.Value(homeAuthPreparationModelKey{}).(string)
	if model == "" || auth == nil {
		return
	}
	publishHomeCredentialFailure(ctx, auth, auth.Provider, model, AccessTokenSHA256(auth), status, string(body))
}

// ReportHomeUnauthorized publishes a result-only zero-token usage record for an
// upstream 401 attempt that did not pass through an executor UsageReporter.
func (m *Manager) ReportHomeUnauthorized(ctx context.Context, auth *Auth, provider, model string, upstreamBody ...[]byte) {
	failureBody := ""
	if len(upstreamBody) > 0 {
		failureBody = string(upstreamBody[0])
	}
	m.reportHomeUnauthorized(ctx, auth, provider, model, AccessTokenSHA256(auth), failureBody)
}

func (m *Manager) reportHomeUnauthorized(ctx context.Context, auth *Auth, provider, model, accessTokenSHA256, failureBody string) {
	if m == nil {
		return
	}
	if failureBody == "" {
		failureBody = "upstream unauthorized"
	}
	publishHomeCredentialFailure(ctx, auth, provider, model, accessTokenSHA256, http.StatusUnauthorized, failureBody)
}

func publishHomeCredentialFailure(ctx context.Context, auth *Auth, provider, model, accessTokenSHA256 string, status int, failureBody string) {
	if auth == nil {
		return
	}
	authIndex := strings.TrimSpace(auth.Index)
	if authIndex == "" {
		authIndex = strings.TrimSpace(auth.EnsureIndex())
	}
	accessTokenSHA256 = strings.TrimSpace(accessTokenSHA256)
	if authIndex == "" || accessTokenSHA256 == "" {
		return
	}
	provider = strings.TrimSpace(provider)
	if provider == "" {
		provider = strings.TrimSpace(auth.Provider)
	}
	model = strings.TrimSpace(model)
	alias := strings.TrimSpace(coreusage.RequestedModelAliasFromContext(ctx))
	if alias == "" {
		alias = model
	}
	accessProvider := coreusage.AccessProviderFromContext(ctx)
	isNativeKey := false
	if nativeExplicit, ok := coreusage.IsNativeKeyFromContext(ctx); ok {
		isNativeKey = nativeExplicit
	} else if accessProvider != "" {
		isNativeKey = coreusage.IsNativeAccessProvider(accessProvider)
	}
	clientMeta := logging.GetClientRequestMetadata(ctx)
	coreusage.PublishRecord(ctx, coreusage.Record{
		Provider:          provider,
		ExecutorType:      homeResultExecutorType,
		Model:             model,
		Alias:             alias,
		APIKey:            coreusage.APIKeyFromContext(ctx),
		IsNativeKey:       isNativeKey,
		AccessProvider:    accessProvider,
		SessionID:         clientMeta.SessionID,
		ParentSessionID:   clientMeta.ParentSessionID,
		AuthID:            auth.ID,
		AuthIndex:         authIndex,
		AccessTokenSHA256: accessTokenSHA256,
		AuthType:          auth.AuthKind(),
		Source:            auth.AuthSourceKind(),
		ReasoningEffort:   coreusage.ReasoningEffortFromContext(ctx),
		ServiceTier:       coreusage.ServiceTierFromContext(ctx),
		Generate:          coreusage.GenerateFlag(false),
		RequestedAt:       time.Now(),
		Failed:            true,
		Fail: coreusage.Failure{
			StatusCode: status,
			Body:       failureBody,
		},
	})
}
