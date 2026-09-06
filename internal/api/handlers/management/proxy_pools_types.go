package management

import "github.com/router-for-me/CLIProxyAPI/v7/internal/store"

// proxyPoolReq is the create/update body for /v0/management/proxy-pools.
// Pointer booleans keep "field absent" distinct from "explicit false" so the
// update path only touches fields the dashboard actually sent.
type proxyPoolReq struct {
	Name        string `json:"name"`
	ProxyURL    string `json:"proxy_url"`
	NoProxy     string `json:"no_proxy,omitempty"`
	Type        string `json:"type,omitempty"`
	IsActive    *bool  `json:"is_active,omitempty"`
	StrictProxy *bool  `json:"strict_proxy,omitempty"`
}

// proxyPoolResponse wraps the store row with its computed bound_entry_count
// when the list requested usage counts (?include_usage=1).
type proxyPoolResponse struct {
	store.ProxyPool
	BoundEntryCount int64 `json:"bound_entry_count,omitempty"`
}

// allowedProxyPoolSchemes is the closed scheme set for standard pools; relay
// pools additionally require https (validated per type).
var allowedProxyPoolSchemes = map[string]bool{"http": true, "https": true, "socks5": true, "socks5h": true}
