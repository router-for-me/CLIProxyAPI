// Package discovery provides local network mDNS / DNS-SD service advertising
// and discovery capabilities for CLIProxyAPI (CPA).
package discovery

import (
	"context"
	"net"
)

const (
	// DefaultServiceType is the proposed common service type for AI Gateways.
	DefaultServiceType = "_ai-gateway._tcp"
	// DefaultDomain is the link-local domain used by mDNS.
	DefaultDomain = "local."
	// SubtypeChatCompletions identifies the Chat Completions API protocol.
	SubtypeChatCompletions = "_chat-completions"
	// SubtypeResponses identifies the Responses API protocol.
	SubtypeResponses = "_responses"
	// SubtypeMessages identifies the Anthropic Messages API protocol.
	SubtypeMessages = "_messages"
	// SubtypeGenerateContent identifies the Gemini GenerateContent API protocol.
	SubtypeGenerateContent = "_generate-content"
	// SubtypeInteractions identifies the Interactions API protocol.
	SubtypeInteractions = "_interactions"
	// SubtypeCompletions identifies the legacy text Completions API protocol.
	SubtypeCompletions = "_completions"
	// SubtypeResponsesWS identifies the persistent WebSocket Responses stream.
	SubtypeResponsesWS = "_responses-ws"
	// SubtypeRealtime identifies realtime WebSocket / WebRTC sessions.
	SubtypeRealtime = "_realtime"
	// SubtypeImages identifies image generation and editing.
	SubtypeImages = "_images"
	// SubtypeVideos identifies video generation, editing, and retrieval.
	SubtypeVideos = "_videos"
	// SubtypeAudio identifies speech synthesis.
	SubtypeAudio = "_audio"
	// SubtypeCountTokens identifies standalone token counting.
	SubtypeCountTokens = "_count-tokens"
	// SubtypeSearch identifies agentic web/code search.
	SubtypeSearch = "_search"
	// ProductCPA is the product identifier for CLIProxyAPI.
	ProductCPA = "cliproxyapi"
)

// ServiceSpec describes a service to be advertised on the local network.
type ServiceSpec struct {
	InstanceName  string
	ServiceType   string
	Domain        string
	Port          int
	Subtypes      []string
	TextRecords   []string
	Interfaces    []net.Interface
	AdvertisedIPs []string
}

// DiscoveredService describes a discovered AI gateway service on the LAN.
type DiscoveredService struct {
	InstanceName string   `json:"instance_name"`
	ServiceType  string   `json:"service_type"`
	Domain       string   `json:"domain"`
	Host         string   `json:"host"`
	Port         int      `json:"port"`
	IPv4         []net.IP `json:"ipv4"`
	IPv6         []net.IP `json:"ipv6"`
	// InterfaceIndex separates identical service names observed on different links.
	InterfaceIndex int `json:"interface_index,omitempty"`
	// IPv6Zones maps link-local address strings to their receiving interface names.
	IPv6Zones map[string]string `json:"ipv6_zones,omitempty"`
	// Path is the gateway mount path from the TXT path key; empty for legacy advertisers.
	Path         string   `json:"path,omitempty"`
	TLS          bool     `json:"tls"`
	Protocols    []string `json:"protocols,omitempty"`
	Product      string   `json:"product,omitempty"`
	InstanceID   string   `json:"instance_id,omitempty"`
	AuthRequired bool     `json:"auth_required"`
	AuthMethods  []string `json:"auth_methods,omitempty"`
	// Endpoints maps each advertised protocol token to its route on the gateway origin.
	Endpoints map[string]string `json:"endpoints,omitempty"`
	NodeRole  string            `json:"node_role,omitempty"`
	// Version is the discovery TXT schema version (txtvers, or legacy version).
	Version string            `json:"version,omitempty"`
	RawTXT  map[string]string `json:"raw_txt,omitempty"`
}

// Advertiser defines the server-side mDNS advertisement lifecycle.
type Advertiser interface {
	Start(ctx context.Context, spec ServiceSpec) error
	Stop() error
}

// Browser defines the client-side mDNS browsing interface.
type Browser interface {
	Browse(ctx context.Context, serviceType, domain string) ([]DiscoveredService, error)
	BrowseWithFallback(ctx context.Context) ([]DiscoveredService, error)
	BrowseWithFallbackServiceType(ctx context.Context, serviceType string) ([]DiscoveredService, error)
}
