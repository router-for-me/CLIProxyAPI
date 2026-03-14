package cluster

import (
	"errors"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

var (
	ErrClusterDisabled = errors.New("cluster mode is disabled")
	ErrPeerNotFound    = errors.New("cluster peer not found")
	ErrPeerDisabled    = errors.New("cluster peer is disabled")
)

// State is the management handshake payload exchanged between peers.
type State struct {
	NodeID       string `json:"node_id"`
	AdvertiseURL string `json:"advertise_url"`
	Version      string `json:"version"`
}

// PeerCatalog records the latest observed state for one peer node.
type PeerCatalog struct {
	ConfiguredID     string    `json:"configured_id"`
	NodeID           string    `json:"node_id,omitempty"`
	AdvertiseURL     string    `json:"advertise_url,omitempty"`
	Version          string    `json:"version,omitempty"`
	Active           bool      `json:"active"`
	DiscoveredModels []string  `json:"discovered_models,omitempty"`
	FilteredModels   []string  `json:"filtered_models,omitempty"`
	Warning          string    `json:"warning,omitempty"`
	Error            string    `json:"error,omitempty"`
	CheckedAt        time.Time `json:"checked_at,omitempty"`
}

// CatalogSnapshot is the in-memory view built by the peer reconciler.
type CatalogSnapshot struct {
	Enabled      bool                   `json:"enabled"`
	NodeID       string                 `json:"node_id,omitempty"`
	AdvertiseURL string                 `json:"advertise_url,omitempty"`
	UpdatedAt    time.Time              `json:"updated_at,omitempty"`
	Peers        map[string]PeerCatalog `json:"peers,omitempty"`
}

type configSnapshot struct {
	Enabled                 bool
	NodeID                  string
	AdvertiseURL            string
	PollInterval            time.Duration
	ForwardTimeout          time.Duration
	PreferLocal             bool
	RegisterNodePrefixAlias bool
	ProxyURL                string
	Nodes                   []internalconfig.ClusterNode
}

type peerRuntime struct {
	PreferredAPIKey string
}
