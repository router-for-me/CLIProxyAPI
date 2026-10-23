package management

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/zai"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const zaiCallbackProvider = "zai"

var newZaiOAuthService = func(cfg *config.Config) *zai.Service {
	client := util.SetProxy(&cfg.SDKConfig, &http.Client{Timeout: 30 * time.Second})
	return zai.NewService(client)
}

// RequestZaiToken starts the ZCode server-mediated OAuth flow and returns the
// authorize URL for the browser. Completion is detected by polling z.ai, so no
// redirect reaches this server and the user never pastes a callback.
func (h *Handler) RequestZaiToken(c *gin.Context) {
	state, errState := misc.GenerateRandomState()
	if errState != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state parameter"})
		return
	}
	authSvc := newZaiOAuthService(h.cfg)
	flow, errFlow := authSvc.InitFlow(c.Request.Context())
	if errFlow != nil || flow == nil || strings.TrimSpace(flow.AuthorizeURL) == "" {
		log.WithError(errFlow).Error("failed to start Z.ai OAuth flow")
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to start Z.ai OAuth flow"})
		return
	}
	RegisterOAuthSession(state, zaiCallbackProvider)
	ctx := PopulateAuthContext(context.Background(), c)
	authDir := h.cfg.AuthDir
	go h.completeZaiOAuth(ctx, authDir, state, authSvc, flow)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "url": flow.AuthorizeURL, "state": state})
}

func (h *Handler) completeZaiOAuth(ctx context.Context, authDir, state string, authSvc *zai.Service, flow *zai.FlowInfo) {
	// Bounded by maxPollDuration inside WaitForFlow; the outer deadline only
	// guarantees the goroutine cannot outlive the flow.
	ctx, cancel := context.WithTimeout(ctx, 12*time.Minute)
	defer cancel()
	go watchOAuthSessionCancel(ctx, cancel, state, zaiCallbackProvider)

	bundle, errWait := authSvc.WaitForFlow(ctx, flow)
	if errWait != nil {
		if IsOAuthSessionPending(state, zaiCallbackProvider) {
			SetOAuthSessionError(state, errWait.Error())
		}
		return
	}
	if !IsOAuthSessionPending(state, zaiCallbackProvider) {
		return
	}
	businessToken, expiresIn, errBusiness := authSvc.ExchangeBusinessToken(ctx, bundle.OAuthToken)
	if errBusiness != nil || strings.TrimSpace(businessToken) == "" {
		if IsOAuthSessionPending(state, zaiCallbackProvider) {
			reason := "unknown error"
			if errBusiness != nil {
				reason = errBusiness.Error()
			}
			log.WithError(errBusiness).Error("Z.ai business token exchange failed")
			SetOAuthSessionError(state, "Failed to exchange Z.ai authorization for business token: "+reason)
		}
		return
	}
	bundle.BusinessToken = businessToken
	bundle.ExpiresIn = expiresIn
	// The business JWT is not an inference credential. Probe the Start Plan
	// (free tier) first: some accounts can still mint an API key the coding
	// plan never funds, which would send inference to a dead endpoint. The
	// minted coding-plan key is only used when no active Start Plan exists.
	bundle.InferenceKey = businessToken
	bundle.InferenceBaseURL = zai.DefaultBaseURL
	deviceMid := uuid.NewString()
	if status, errStart := authSvc.FetchStartPlanStatus(ctx, bundle.ZCodeToken, deviceMid); errStart == nil && status != nil && status.Active {
		// Start Plan accounts use the ZCode JWT at the zcode-plan Anthropic
		// gateway, mirroring the official client.
		bundle.InferenceKey = bundle.ZCodeToken
		bundle.InferenceBaseURL = zai.StartPlanAnthropicBaseURL
		bundle.PlanKind = "start-plan"
		bundle.Models = status.Models
		bundle.DeviceMid = deviceMid
		log.Info("Z.ai Start Plan detected; using zcode-plan Anthropic endpoint")
	} else if errStart != nil {
		log.WithError(errStart).Debug("Z.ai Start Plan probe failed; falling back to coding plan")
	}
	if bundle.PlanKind == "" {
		if minted, errMint := authSvc.MintAPIKey(ctx, businessToken); errMint == nil && strings.TrimSpace(minted) != "" {
			bundle.InferenceKey = minted
			bundle.InferenceBaseURL = zai.MintAPIBaseURL
		} else {
			log.WithError(errMint).Warn("Z.ai coding-plan API key mint failed; falling back to business token")
		}
	}
	if bundle.User.ID == "" && bundle.User.Email == "" && bundle.User.Name == "" {
		if profile, errProfile := authSvc.FetchUserInfo(ctx, bundle.OAuthToken); errProfile == nil {
			bundle.User = profile
		}
	}
	if !IsOAuthSessionPending(state, zaiCallbackProvider) {
		return
	}
	record := zaiAuthRecord(bundle)
	if errGuard := guardOAuthSessionPendingForSave(state, zaiCallbackProvider); errGuard != nil {
		return
	}
	if _, errSave := h.saveTokenRecord(ctx, record); errSave != nil {
		SetOAuthSessionError(state, "Failed to save Z.ai authentication tokens")
		return
	}
	CompleteOAuthSession(state)
	log.Info("Z.ai authentication successful")
}

func zaiAuthRecord(bundle *zai.TokenBundle) *coreauth.Auth {
	identity := firstZaiIdentity(bundle.User)
	fileName := "zai-" + safeZaiFilePart(identity) + ".json"
	baseURL := strings.TrimSpace(bundle.InferenceBaseURL)
	if baseURL == "" {
		baseURL = zai.DefaultBaseURL
	}
	metadata := map[string]any{
		"type":               zaiCallbackProvider,
		"auth_kind":          coreauth.AuthKindOAuth,
		"access_token":       bundle.InferenceKey,
		"api_key":            bundle.InferenceKey,
		"business_token":     bundle.BusinessToken,
		"oauth_access_token": bundle.OAuthToken,
		"zcode_jwt_token":    bundle.ZCodeToken,
		"base_url":           baseURL,
		"headers": map[string]string{
			"x-api-key":     bundle.InferenceKey,
			"Authorization": "Bearer " + bundle.InferenceKey,
		},
	}
	if bundle.ExpiresIn > 0 {
		metadata["expires_at"] = time.Now().Add(time.Duration(bundle.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
	}
	if bundle.PlanKind != "" {
		metadata["plan_kind"] = bundle.PlanKind
	}
	if len(bundle.Models) > 0 {
		metadata["start_plan_models"] = strings.Join(bundle.Models, ",")
	}
	// The zcode-plan gateway speaks the Anthropic messages protocol only; the
	// executor picks the upstream format from this flag.
	startPlan := bundle.PlanKind == "start-plan"
	if startPlan {
		metadata["upstream_protocol"] = "anthropic"
		metadata["device_mid"] = bundle.DeviceMid
		// Mirror the official client identity; the zcode-plan gateway and the
		// balance endpoints reject calls without these (X-Device-Mid makes
		// the difference between HTTP 200 and 400 code 3001 on billing).
		if headers, ok := metadata["headers"].(map[string]string); ok {
			for name, value := range zai.ZCodeSourceHeaders(bundle.DeviceMid) {
				headers[name] = value
			}
		}
	}
	if bundle.User.ID != "" {
		metadata["user_id"] = bundle.User.ID
	}
	if bundle.User.Email != "" {
		metadata["email"] = bundle.User.Email
	}
	if bundle.User.Name != "" {
		metadata["name"] = bundle.User.Name
	}
	if bundle.User.Avatar != "" {
		metadata["avatar"] = bundle.User.Avatar
	}
	attrs := map[string]string{
		coreauth.AttributeAuthKind: coreauth.AuthKindOAuth,
		coreauth.AttributeAPIKey:   bundle.InferenceKey,
		"base_url":                 baseURL,
		"header:x-api-key":         bundle.InferenceKey,
	}
	if startPlan {
		attrs["upstream_protocol"] = "anthropic"
		for name, value := range zai.ZCodeSourceHeaders(bundle.DeviceMid) {
			attrs["header:"+name] = value
		}
	}
	return &coreauth.Auth{
		ID:         fileName,
		Provider:   zaiCallbackProvider,
		FileName:   fileName,
		Label:      identity,
		Attributes: attrs,
		Metadata:   metadata,
	}
}

func firstZaiIdentity(user zai.UserInfo) string {
	for _, value := range []string{user.Email, user.Name, user.ID} {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return "account"
}

func safeZaiFilePart(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
		if b.Len() >= 48 {
			break
		}
	}
	part := strings.Trim(b.String(), "-._")
	if part == "" {
		return "account"
	}
	return part
}
