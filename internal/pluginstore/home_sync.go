package pluginstore

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

const PluginSyncSchemaVersion = 1

type PluginSyncRequest struct {
	SchemaVersion     int               `json:"schema_version"`
	GOOS              string            `json:"goos"`
	GOARCH            string            `json:"goarch"`
	InstalledVersions map[string]string `json:"installed_versions,omitempty"`
}

func (r *PluginSyncRequest) Clear() {
	if r == nil {
		return
	}
	clear(r.InstalledVersions)
	r.InstalledVersions = nil
}

type PluginSyncItem struct {
	Manifest Manifest             `json:"manifest"`
	Auth     []ResolvedAuthConfig `json:"auth,omitempty"`
}

func (i *PluginSyncItem) Clear() {
	if i == nil {
		return
	}
	ClearResolvedAuthConfigs(i.Auth)
	i.Auth = nil
	i.Manifest = Manifest{}
}

type PluginSyncResponse struct {
	SchemaVersion int              `json:"schema_version"`
	ExpiresAt     time.Time        `json:"expires_at"`
	Items         []PluginSyncItem `json:"items"`
}

func (r *PluginSyncResponse) Validate(now time.Time) error {
	if r == nil {
		return fmt.Errorf("plugin sync response is nil")
	}
	if r.SchemaVersion != PluginSyncSchemaVersion {
		return fmt.Errorf("unsupported plugin sync schema_version %d", r.SchemaVersion)
	}
	if r.ExpiresAt.IsZero() {
		return fmt.Errorf("plugin sync response missing expires_at")
	}
	if !now.Before(r.ExpiresAt) {
		return fmt.Errorf("plugin sync response expired")
	}
	seen := make(map[string]struct{}, len(r.Items))
	for index := range r.Items {
		item := &r.Items[index]
		if errManifest := item.Manifest.Validate(); errManifest != nil {
			return fmt.Errorf("plugin sync item %d: %w", index, errManifest)
		}
		if errURLs := validatePluginSyncManifestURLs(item.Manifest); errURLs != nil {
			return fmt.Errorf("plugin sync item %d: %w", index, errURLs)
		}
		id := strings.TrimSpace(item.Manifest.ID)
		if _, exists := seen[id]; exists {
			return fmt.Errorf("plugin sync response contains duplicate plugin %q", id)
		}
		seen[id] = struct{}{}
		for authIndex := range item.Auth {
			if errAuth := ValidateResolvedAuthConfig(item.Auth[authIndex]); errAuth != nil {
				return fmt.Errorf("plugin sync item %d auth %d: %w", index, authIndex, errAuth)
			}
		}
	}
	return nil
}

func validatePluginSyncManifestURLs(manifest Manifest) error {
	if manifest.InstallType() != InstallTypeDirect {
		return nil
	}
	plan := NormalizeInstallPlan(manifest.Install)
	if len(plan.Artifacts) == 0 {
		return fmt.Errorf("direct plugin sync manifest requires pinned artifacts")
	}
	for index, artifact := range plan.Artifacts {
		parsed, errParse := url.Parse(strings.TrimSpace(artifact.URL))
		if errParse != nil || !strings.EqualFold(parsed.Scheme, "https") {
			return fmt.Errorf("direct plugin sync artifact %d must use https", index)
		}
	}
	return nil
}

func (r *PluginSyncResponse) Clear() {
	if r == nil {
		return
	}
	for index := range r.Items {
		r.Items[index].Clear()
	}
	r.Items = nil
	r.ExpiresAt = time.Time{}
	r.SchemaVersion = 0
}
