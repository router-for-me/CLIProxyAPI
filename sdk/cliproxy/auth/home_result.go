package auth

import (
	"context"
	"net/http"
	"strings"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

const homeResultExecutorType = "home-result"

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
	if m == nil || auth == nil {
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
	if failureBody == "" {
		failureBody = "upstream unauthorized"
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
	coreusage.PublishRecord(ctx, coreusage.Record{
		Provider:          provider,
		ExecutorType:      homeResultExecutorType,
		Model:             model,
		Alias:             alias,
		APIKey:            homeAPIKeyLabelFromContext(ctx),
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
			Body:       failureBody,
		},
	})
}

// homeAPIKeyLabelFromContext returns the configured display name of the matched
// client API key. It is attribution-only: the raw key is never used as a
// fallback, so unnamed keys stay unattributed on this path.
func homeAPIKeyLabelFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	ginCtx, ok := ctx.Value("gin").(interface{ Get(string) (any, bool) })
	if !ok || ginCtx == nil {
		return ""
	}
	rawLabel, ok := ginCtx.Get("userApiKeyLabel")
	if !ok {
		return ""
	}
	return contextStringValue(rawLabel)
}
