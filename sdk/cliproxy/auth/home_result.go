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

// ReportHomeUnauthorized publishes a result-only zero-token usage record for an
// upstream 401 attempt that did not pass through an executor UsageReporter.
func (m *Manager) ReportHomeUnauthorized(ctx context.Context, auth *Auth, provider, model string) {
	m.ReportHomeUnauthorizedBody(ctx, auth, provider, model, "upstream unauthorized")
}

// ReportHomeUnauthorizedBody publishes the upstream 401 body with the usage record.
func (m *Manager) ReportHomeUnauthorizedBody(ctx context.Context, auth *Auth, provider, model, body string) {
	m.reportHomeUnauthorized(ctx, auth, provider, model, AccessTokenSHA256(auth), body)
}

func (m *Manager) reportHomeUnauthorized(ctx context.Context, auth *Auth, provider, model, accessTokenSHA256, body string) {
	if m == nil || auth == nil {
		return
	}
	authIndex := strings.TrimSpace(auth.Index)
	if authIndex == "" {
		authIndex = strings.TrimSpace(auth.EnsureIndex())
	}
	accessTokenSHA256 = strings.TrimSpace(accessTokenSHA256)
	if authIndex == "" && strings.TrimSpace(auth.ID) != "" {
		authIndex = strings.TrimSpace(auth.ID)
	}
	if accessTokenSHA256 == "" {
		accessTokenSHA256 = authIndex
	}
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
			StatusCode: http.StatusUnauthorized,
			Body:       body,
		},
	})
}
