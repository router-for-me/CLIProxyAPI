// Package qoder implements Qoder device OAuth and the COSY wire protocol.
package qoder

import (
	"fmt"
	"net/url"
	"strings"
)

const (
	RegionGlobal = "global"
	RegionCN     = "cn"

	ProviderGlobal = "qoder"
	ProviderCN     = "qoder-cn"

	// ClientVersion tracks the official Qoder CLI protocol baseline.
	ClientVersion  = "1.1.85"
	deviceClientID = "e883ade2-e6e3-4d6d-adf7-f92ceff5fdcb"
)

// Endpoints contains the independent web, OpenAPI, and inference surfaces for one region.
type Endpoints struct {
	WebBase     string
	OpenAPIBase string
	InferBase   string
}

var regionEndpoints = map[string]Endpoints{
	RegionGlobal: {
		WebBase:     "https://qoder.com",
		OpenAPIBase: "https://openapi.qoder.sh",
		InferBase:   "https://api2.qoder.sh",
	},
	RegionCN: {
		WebBase:     "https://qoder.cn",
		OpenAPIBase: "https://openapi.qoder.com.cn",
		InferBase:   "https://gateway.qoder.com.cn",
	},
}

// NormalizeRegion canonicalizes supported region aliases. International is the default.
func NormalizeRegion(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case RegionCN, "china", "cn-cn", ProviderCN, "qoder.cn", "qoder.com.cn":
		return RegionCN
	default:
		return RegionGlobal
	}
}

// ProviderForRegion returns the distinct provider key used for scheduling and model ownership.
func ProviderForRegion(region string) string {
	if NormalizeRegion(region) == RegionCN {
		return ProviderCN
	}
	return ProviderGlobal
}

// RegionFromAuth resolves the stored region without relying on a concrete Storage type.
func RegionFromAuth(provider string, metadata map[string]any) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case ProviderCN:
		return RegionCN
	case ProviderGlobal:
		return RegionGlobal
	}
	if metadata != nil {
		if region := String(metadata, "region"); region != "" {
			return NormalizeRegion(region)
		}
	}
	return RegionGlobal
}

// ResolveEndpoints returns validated endpoints for a region.
func ResolveEndpoints(region string) (Endpoints, error) {
	endpoints, ok := regionEndpoints[NormalizeRegion(region)]
	if !ok {
		return Endpoints{}, fmt.Errorf("qoder: unsupported region %q", region)
	}
	for _, raw := range []string{endpoints.WebBase, endpoints.OpenAPIBase, endpoints.InferBase} {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
			return Endpoints{}, fmt.Errorf("qoder: invalid endpoint %q", raw)
		}
	}
	return endpoints, nil
}

func (e Endpoints) LoginURL() string {
	return e.WebBase + "/device/selectAccounts"
}

func (e Endpoints) DevicePollURL() string {
	return e.OpenAPIBase + "/api/v1/deviceToken/poll"
}

func (e Endpoints) DeviceRefreshURL() string {
	return e.OpenAPIBase + "/api/v1/deviceToken/refresh"
}

func (e Endpoints) UserInfoURL() string {
	return e.OpenAPIBase + "/api/v1/userinfo"
}

func (e Endpoints) ModelListURL() string {
	return e.InferBase + "/algo/api/v2/model/list?Encode=1"
}

func (e Endpoints) ChatURL() string {
	return e.InferBase + "/algo/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1"
}
