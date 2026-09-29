package jsplugin

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	vertexcore "github.com/QuantumNous/new-api/relay/channel/vertex"
)

type cachedAuth struct {
	header, projectID string
	expiresAt         time.Time
}

var pluginAuthCache sync.Map
var acquireAccessToken = vertexcore.AcquireAccessToken

func resolveAuth(meta pluginruntime.AuthMeta, apiKey, proxy string) (map[string]any, error) {
	typeName := strings.TrimSpace(meta.Type)
	if typeName == "" || typeName == "none" || typeName == "api_key" {
		return map[string]any{"authHeader": apiKey}, nil
	}
	cacheKey := apiKey + "\x00" + proxy
	if value, ok := pluginAuthCache.Load(cacheKey); ok {
		entry := value.(cachedAuth)
		if time.Now().Before(entry.expiresAt) {
			return map[string]any{"authHeader": entry.header, "projectId": entry.projectID}, nil
		}
	}
	var credentials vertexcore.Credentials
	if err := common.Unmarshal([]byte(apiKey), &credentials); err != nil {
		return nil, fmt.Errorf("decode oauth2_jwt credentials: %w", err)
	}
	token, err := acquireAccessToken(credentials, proxy)
	if err != nil {
		return nil, err
	}
	entry := cachedAuth{header: "Bearer " + token, projectID: credentials.ProjectID, expiresAt: time.Now().Add(25 * time.Minute)}
	pluginAuthCache.Store(cacheKey, entry)
	return map[string]any{"authHeader": entry.header, "projectId": entry.projectID}, nil
}
