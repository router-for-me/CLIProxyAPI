// Package groups holds the proxx model-group configuration (proxx.model-groups).
//
// A model group is a client-visible virtual model (for example "auto") backed by an ordered
// list of real models that may be served by different providers. This package only stores the
// active groups and publishes them to the model registry; execution and failover live in
// sdk/cliproxy/auth/model_groups_proxx.go. See docs/proxx/decisions/0003-model-groups.md.
package groups

import (
	"strings"
	"sync"
	"sync/atomic"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	log "github.com/sirupsen/logrus"
)

const (
	// Provider is the synthetic provider key returned for group models. The auth manager
	// recognizes it and fans the request out to the group's members.
	Provider = "proxx-group"
	// RegistryClientID is the model-registry client that lists visible groups in /v1/models.
	RegistryClientID = "proxx-model-groups"

	// StrategyOrdered tries members in configured order on every request.
	StrategyOrdered = "ordered"
	// StrategyRoundRobin rotates the first member per request, then fails over in order.
	StrategyRoundRobin = "round-robin"
)

// Group is a normalized model group.
type Group struct {
	Name     string
	Strategy string
	Models   []string
	Hidden   bool
}

type snapshot struct {
	list   []Group
	byName map[string]int
}

var (
	current  atomic.Pointer[snapshot]
	rrMu     sync.Mutex
	rrCursor = map[string]uint64{}
	applyMu  sync.Mutex
)

// Normalize trims and validates configured groups. Groups without a name or members are
// dropped, duplicate names keep the first definition, and members that are themselves group
// names are dropped (groups do not nest).
func Normalize(raw []config.ProxxModelGroup) []Group {
	names := make(map[string]struct{}, len(raw))
	for _, g := range raw {
		if name := strings.TrimSpace(g.Name); name != "" {
			names[name] = struct{}{}
		}
	}
	out := make([]Group, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, g := range raw {
		name := strings.TrimSpace(g.Name)
		if name == "" {
			log.Warn("proxx: ignoring model group without a name")
			continue
		}
		if _, dup := seen[name]; dup {
			log.Warnf("proxx: ignoring duplicate model group %q", name)
			continue
		}
		strategy := strings.ToLower(strings.TrimSpace(g.Strategy))
		switch strategy {
		case "", StrategyOrdered:
			strategy = StrategyOrdered
		case StrategyRoundRobin, "round_robin", "roundrobin":
			strategy = StrategyRoundRobin
		default:
			log.Warnf("proxx: model group %q has unknown strategy %q, using %q", name, g.Strategy, StrategyOrdered)
			strategy = StrategyOrdered
		}
		members := make([]string, 0, len(g.Models))
		memberSeen := make(map[string]struct{}, len(g.Models))
		for _, model := range g.Models {
			model = strings.TrimSpace(model)
			if model == "" {
				continue
			}
			if _, isGroup := names[thinking.ParseSuffix(model).ModelName]; isGroup {
				log.Warnf("proxx: model group %q member %q is a group; nested groups are not supported", name, model)
				continue
			}
			if _, dup := memberSeen[model]; dup {
				continue
			}
			memberSeen[model] = struct{}{}
			members = append(members, model)
		}
		if len(members) == 0 {
			log.Warnf("proxx: ignoring model group %q without members", name)
			continue
		}
		seen[name] = struct{}{}
		out = append(out, Group{Name: name, Strategy: strategy, Models: members, Hidden: g.Hidden})
	}
	return out
}

// Apply replaces the active groups from cfg and republishes visible groups to the model
// registry. It is called on startup and on every config reload. Groups are disabled in Home
// mode, where the control plane owns routing.
func Apply(cfg *config.Config) {
	var list []Group
	if cfg != nil && !cfg.Home.Enabled {
		list = Normalize(cfg.Proxx.ModelGroups)
	}
	set(list)
}

func set(list []Group) {
	applyMu.Lock()
	defer applyMu.Unlock()
	snap := &snapshot{list: list, byName: make(map[string]int, len(list))}
	for i, g := range list {
		snap.byName[g.Name] = i
	}
	current.Store(snap)
	publish(list)
}

// publish registers visible groups as models of a synthetic registry client so they appear in
// model listings (/v1/models, /v1beta/models, Claude model lists) without touching list handlers.
func publish(list []Group) {
	reg := registry.GetGlobalRegistry()
	models := make([]*registry.ModelInfo, 0, len(list))
	for _, g := range list {
		if g.Hidden {
			continue
		}
		for _, provider := range reg.GetModelProviders(g.Name) {
			if provider != Provider {
				log.Warnf("proxx: model group %q shadows a real model with the same name", g.Name)
				break
			}
		}
		models = append(models, &registry.ModelInfo{
			ID:          g.Name,
			Object:      "model",
			OwnedBy:     "proxx",
			Type:        "proxx-group",
			DisplayName: g.Name,
			Description: "proxx model group: " + strings.Join(g.Models, ", "),
			// Created stays 0 so upstream's "auto" resolution (newest model) never picks a group.
		})
	}
	if len(models) == 0 {
		reg.UnregisterClient(RegistryClientID)
		return
	}
	reg.RegisterClient(RegistryClientID, Provider, models)
}

// List returns a copy of the active groups.
func List() []Group {
	snap := current.Load()
	if snap == nil {
		return nil
	}
	out := make([]Group, len(snap.list))
	for i, g := range snap.list {
		g.Models = append([]string(nil), g.Models...)
		out[i] = g
	}
	return out
}

// Lookup resolves a requested model name to a group. A thinking suffix on the request
// (for example "auto(high)") is returned separately so it can be applied to each member.
func Lookup(model string) (Group, string, bool) {
	snap := current.Load()
	if snap == nil || len(snap.list) == 0 {
		return Group{}, "", false
	}
	model = strings.TrimSpace(model)
	if idx, ok := snap.byName[model]; ok {
		return snap.list[idx], "", true
	}
	parsed := thinking.ParseSuffix(model)
	if parsed.HasSuffix {
		if idx, ok := snap.byName[strings.TrimSpace(parsed.ModelName)]; ok {
			return snap.list[idx], parsed.RawSuffix, true
		}
	}
	return Group{}, "", false
}

// IsGroup reports whether model names a configured group.
func IsGroup(model string) bool {
	_, _, ok := Lookup(model)
	return ok
}

// RequestProviders is the handler hook: it returns the synthetic provider for group models so
// the request bypasses registry provider lookup and upstream's "auto" resolution.
func RequestProviders(model string) ([]string, bool) {
	if !IsGroup(model) {
		return nil, false
	}
	return []string{Provider}, true
}

// Members returns the member model names to try for one request, in try order, with the
// request's thinking suffix applied. Round-robin groups rotate the starting member.
func Members(g Group, suffix string) []string {
	n := len(g.Models)
	if n == 0 {
		return nil
	}
	start := 0
	if g.Strategy == StrategyRoundRobin {
		rrMu.Lock()
		start = int(rrCursor[g.Name] % uint64(n))
		rrCursor[g.Name]++
		rrMu.Unlock()
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		model := g.Models[(start+i)%n]
		if suffix != "" && !thinking.ParseSuffix(model).HasSuffix {
			model = model + "(" + suffix + ")"
		}
		out = append(out, model)
	}
	return out
}
