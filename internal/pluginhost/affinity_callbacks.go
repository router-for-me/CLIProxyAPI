package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func (h *Host) callHostAffinityLookup(ctx context.Context, request []byte) ([]byte, error) {
	_ = ctx
	var req pluginapi.HostAffinityLookupRequest
	if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host affinity lookup request: %w", errUnmarshal)
	}
	req.Provider = strings.TrimSpace(req.Provider)
	req.Model = strings.TrimSpace(req.Model)
	req.SessionID = strings.TrimSpace(req.SessionID)
	if req.Provider == "" {
		return nil, fmt.Errorf("provider is required")
	}
	if req.Model == "" {
		return nil, fmt.Errorf("model is required")
	}
	if req.SessionID == "" {
		return nil, fmt.Errorf("session_id is required")
	}

	now := time.Now().UTC()
	manager := h.currentAuthManager()
	if manager == nil {
		return marshalRPCResult(pluginapi.HostAffinityLookupResponse{
			Status:     pluginapi.HostAffinityStatusUnsupported,
			ObservedAt: now,
		})
	}

	auth, status := manager.LookupSessionAffinity(req.Provider, req.Model, req.SessionID)
	if status != pluginapi.HostAffinityStatusBound || auth == nil {
		return marshalRPCResult(pluginapi.HostAffinityLookupResponse{
			Status:     status,
			ObservedAt: now,
		})
	}

	auth.EnsureIndex()
	disabled := auth.Disabled || auth.Status == coreauth.StatusDisabled
	unavailable := auth.Unavailable || auth.Status == coreauth.StatusError

	return marshalRPCResult(pluginapi.HostAffinityLookupResponse{
		Status:      pluginapi.HostAffinityStatusBound,
		AuthIndex:   auth.Index,
		ObservedAt:  now,
		Disabled:    disabled,
		Unavailable: unavailable,
	})
}
