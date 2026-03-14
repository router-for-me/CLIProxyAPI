package cluster

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

const (
	ProviderPrefix        = "cluster/"
	RuntimeAuthIDPrefix   = "cluster:"
	HeaderHop             = "X-Cluster-Hop"
	HeaderForwardedBy     = "X-Cluster-Forwarded-By"
	AttributeRuntimeOnly  = "runtime_only"
	AttributePeerID       = "cluster_peer_id"
	AttributeAdvertiseURL = "cluster_advertise_url"
)

// CatalogUpdateHandler receives asynchronous catalog refresh notifications.
type CatalogUpdateHandler func(CatalogSnapshot)

// PeerBinding is the runtime-only binding used to register and execute remote peer models.
type PeerBinding struct {
	ConfiguredID            string
	NodeID                  string
	AuthID                  string
	Provider                string
	AdvertiseURL            string
	Models                  []string
	RegisterNodePrefixAlias bool
}

func ProviderKey(nodeID string) string {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return ""
	}
	return ProviderPrefix + nodeID
}

func RuntimeAuthID(nodeID string) string {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return ""
	}
	return RuntimeAuthIDPrefix + nodeID
}

func IsSyntheticProvider(provider string) bool {
	provider = strings.TrimSpace(strings.ToLower(provider))
	return strings.HasPrefix(provider, ProviderPrefix)
}

func PeerIDFromProvider(provider string) string {
	provider = strings.TrimSpace(provider)
	if !IsSyntheticProvider(provider) {
		return ""
	}
	return strings.TrimSpace(provider[len(ProviderPrefix):])
}

func BindingFromRuntime(runtime any) (*PeerBinding, bool) {
	switch typed := runtime.(type) {
	case *PeerBinding:
		if typed == nil {
			return nil, false
		}
		return typed, true
	case PeerBinding:
		clone := typed
		return &clone, true
	default:
		return nil, false
	}
}

func cloneBinding(binding PeerBinding) PeerBinding {
	clone := binding
	clone.Models = cloneStrings(binding.Models)
	return clone
}

func bindingsEqual(left, right PeerBinding) bool {
	if strings.TrimSpace(left.ConfiguredID) != strings.TrimSpace(right.ConfiguredID) {
		return false
	}
	if strings.TrimSpace(left.NodeID) != strings.TrimSpace(right.NodeID) {
		return false
	}
	if strings.TrimSpace(left.AuthID) != strings.TrimSpace(right.AuthID) {
		return false
	}
	if strings.TrimSpace(left.Provider) != strings.TrimSpace(right.Provider) {
		return false
	}
	if strings.TrimSpace(left.AdvertiseURL) != strings.TrimSpace(right.AdvertiseURL) {
		return false
	}
	if left.RegisterNodePrefixAlias != right.RegisterNodePrefixAlias {
		return false
	}
	if len(left.Models) != len(right.Models) {
		return false
	}
	for index := range left.Models {
		if strings.TrimSpace(left.Models[index]) != strings.TrimSpace(right.Models[index]) {
			return false
		}
	}
	return true
}

// ActiveBindings returns the current active peer bindings that should participate in routing.
func (s *Service) ActiveBindings() []PeerBinding {
	if s == nil {
		return nil
	}

	s.mu.RLock()
	cfg := s.cfg
	catalog := cloneCatalogSnapshot(s.catalog)
	s.mu.RUnlock()

	if !cfg.Enabled || len(cfg.Nodes) == 0 {
		return nil
	}

	out := make([]PeerBinding, 0, len(cfg.Nodes))
	for _, node := range cfg.Nodes {
		if !node.Enabled {
			continue
		}
		peer, ok := catalog.Peers[node.ID]
		if !ok || !peer.Active || len(peer.FilteredModels) == 0 {
			continue
		}
		out = append(out, PeerBinding{
			ConfiguredID:            node.ID,
			NodeID:                  strings.TrimSpace(peer.NodeID),
			AuthID:                  RuntimeAuthID(node.ID),
			Provider:                ProviderKey(node.ID),
			AdvertiseURL:            strings.TrimSpace(peer.AdvertiseURL),
			Models:                  cloneStrings(peer.FilteredModels),
			RegisterNodePrefixAlias: cfg.RegisterNodePrefixAlias,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ConfiguredID < out[j].ConfiguredID
	})
	return out
}

// BuildModelInfos converts a peer binding into registry model registrations.
func BuildModelInfos(binding PeerBinding) []*registry.ModelInfo {
	now := time.Now().Unix()
	out := make([]*registry.ModelInfo, 0, len(binding.Models)*2)
	seen := make(map[string]struct{}, len(binding.Models)*2)
	add := func(model *registry.ModelInfo) {
		if model == nil {
			return
		}
		modelID := strings.TrimSpace(model.ID)
		if modelID == "" {
			return
		}
		if _, exists := seen[modelID]; exists {
			return
		}
		seen[modelID] = struct{}{}
		out = append(out, model)
	}

	for _, modelID := range binding.Models {
		baseID := strings.TrimSpace(modelID)
		if baseID == "" {
			continue
		}
		base := buildModelInfo(baseID, binding.NodeID, now)
		add(base)
		if !binding.RegisterNodePrefixAlias {
			continue
		}
		alias := *base
		alias.ID = binding.NodeID + "/" + baseID
		alias.DisplayName = alias.ID
		alias.Name = rewriteModelInfoName(alias.Name, baseID, alias.ID)
		add(&alias)
	}
	return out
}

func buildModelInfo(modelID, nodeID string, created int64) *registry.ModelInfo {
	if static := registry.LookupStaticModelInfo(modelID); static != nil {
		clone := *static
		return &clone
	}
	return &registry.ModelInfo{
		ID:          modelID,
		Object:      "model",
		Created:     created,
		OwnedBy:     nodeID,
		Type:        "cluster",
		DisplayName: modelID,
	}
}

func rewriteModelInfoName(name, oldID, newID string) string {
	trimmed := strings.TrimSpace(name)
	oldID = strings.TrimSpace(oldID)
	newID = strings.TrimSpace(newID)
	if trimmed == "" || oldID == "" || newID == "" {
		return name
	}
	if strings.EqualFold(trimmed, oldID) {
		return newID
	}
	if strings.HasSuffix(trimmed, "/"+oldID) {
		return strings.TrimSuffix(trimmed, oldID) + newID
	}
	if trimmed == "models/"+oldID {
		return "models/" + newID
	}
	return name
}

func cloneHeader(src http.Header) http.Header {
	if len(src) == 0 {
		return nil
	}
	dst := make(http.Header, len(src))
	for key, values := range src {
		dst[key] = append([]string(nil), values...)
	}
	return dst
}
