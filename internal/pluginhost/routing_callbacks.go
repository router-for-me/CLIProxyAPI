package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

// callHostRoutingResetCooldown clears quota and cooldown routing state for one credential, the
// same reset POST /v8/management/routing/cooldown/reset performs, while ensuring token files are left alone.
func (h *Host) callHostRoutingResetCooldown(ctx context.Context, request []byte) ([]byte, error) {
	var req pluginapi.HostRoutingResetCooldownRequest
	if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host routing reset cooldown request: %w", errUnmarshal)
	}
	authIndex := strings.TrimSpace(req.AuthIndex)
	auth, errGet := h.authByIndex(authIndex)
	if errGet != nil {
		return nil, errGet
	}
	manager := h.currentAuthManager()
	if manager == nil {
		return nil, fmt.Errorf("core auth manager unavailable")
	}
	resetCtx := coreauth.WithSkipPersist(ctx)
	updated, models, errReset := manager.ResetQuota(resetCtx, auth.ID)
	if errReset != nil {
		return nil, fmt.Errorf("reset cooldown: %w", errReset)
	}
	if updated == nil {
		return nil, fmt.Errorf("auth not found for auth_index %s", authIndex)
	}
	updated.EnsureIndex()
	log.WithFields(log.Fields{
		"plugin_id":  hostCallbackPluginIDFromContext(ctx),
		"auth_index": updated.Index,
		"models":     len(models),
	}).Info("pluginhost: plugin reset credential cooldown")
	return marshalRPCResult(pluginapi.HostRoutingResetCooldownResponse{
		AuthIndex: updated.Index,
		Models:    models,
	})
}
