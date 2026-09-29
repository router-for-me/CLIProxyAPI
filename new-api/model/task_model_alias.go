package model

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
)

// TaskAliasTarget is one mapping-derived alias after cross-channel aggregation.
// Declared is empty when the same plugin resolves the alias to more than one
// declared tail (display-only; pin is disabled).
type TaskAliasTarget struct {
	Alias     string
	Declared  string
	PluginKey string
}

type taskAliasView struct {
	generation uint64
	expiresAt  time.Time
	byFold     map[string]TaskAliasTarget
}

const taskAliasViewTTL = 60 * time.Second

var (
	taskAliasViewPtr   atomic.Pointer[taskAliasView]
	taskAliasRebuildMu sync.Mutex
)

// ResolveTaskModelAlias returns the mapping-derived alias target for name
// (exact or ASCII-folded). g is the caller's routing generation: a Number
// mismatch or TTL expiry rebuilds the view against that generation.
func ResolveTaskModelAlias(g *jsplugin.RoutingGeneration, name string) (TaskAliasTarget, bool) {
	if g == nil || name == "" {
		return TaskAliasTarget{}, false
	}
	view := loadFreshTaskAliasView(g)
	if view == nil {
		return TaskAliasTarget{}, false
	}
	target, ok := view.byFold[jsplugin.ASCIIFold(name)]
	return target, ok
}

func loadFreshTaskAliasView(g *jsplugin.RoutingGeneration) *taskAliasView {
	view := taskAliasViewPtr.Load()
	if taskAliasViewFresh(view, g.Number) {
		return view
	}
	taskAliasRebuildMu.Lock()
	defer taskAliasRebuildMu.Unlock()
	view = taskAliasViewPtr.Load()
	if taskAliasViewFresh(view, g.Number) {
		return view
	}
	rebuilt := buildTaskAliasView(g)
	taskAliasViewPtr.Store(rebuilt)
	return rebuilt
}

func taskAliasViewFresh(view *taskAliasView, generation uint64) bool {
	return view != nil && view.generation == generation && time.Now().Before(view.expiresAt)
}

func rebuildTaskAliasView() {
	taskAliasRebuildMu.Lock()
	defer taskAliasRebuildMu.Unlock()
	taskAliasViewPtr.Store(buildTaskAliasView(jsplugin.DefaultRegistry.Generation()))
}

type taskAliasDraft struct {
	spellings []string
	byPlugin  map[string]map[string]struct{}
}

func buildTaskAliasView(generation *jsplugin.RoutingGeneration) *taskAliasView {
	genNum := uint64(0)
	if generation != nil {
		genNum = generation.Number
	}
	view := &taskAliasView{
		generation: genNum,
		expiresAt:  time.Now().Add(taskAliasViewTTL),
		byFold:     make(map[string]TaskAliasTarget),
	}
	if DB == nil {
		return view
	}

	var channels []Channel
	err := DB.Select("id", "type", "models", "model_mapping").
		Where("status = ?", common.ChannelStatusEnabled).
		Find(&channels).Error
	if err != nil {
		common.SysError(fmt.Sprintf("rebuild task alias view: %s", err.Error()))
		return view
	}

	drafts := make(map[string]*taskAliasDraft)
	for i := range channels {
		channel := &channels[i]
		mappingJSON := channel.GetModelMapping()
		if mappingJSON == "" || mappingJSON == "{}" {
			continue
		}
		modelMap := make(map[string]string)
		if err := common.UnmarshalJsonStr(mappingJSON, &modelMap); err != nil {
			common.SysError(fmt.Sprintf("task alias view: channel %d model_mapping: %s", channel.Id, err.Error()))
			continue
		}
		inModels := make(map[string]struct{})
		for _, modelName := range channel.GetModels() {
			inModels[modelName] = struct{}{}
		}
		for alias, mapped := range modelMap {
			if mapped == "" {
				continue
			}
			if _, exposed := inModels[alias]; !exposed {
				continue
			}
			if _, declared := generation.CanonicalModel(alias); declared {
				continue
			}
			tail, cyclic := followChannelModelMapping(modelMap, alias)
			if cyclic {
				common.SysError(fmt.Sprintf("task alias mapping cycle dropped: channel=%d key=%q", channel.Id, alias))
				continue
			}
			declared, ok := generation.CanonicalModel(tail)
			if !ok {
				continue
			}
			plugin, ok := generation.GetByModel(declared)
			if !ok {
				continue
			}
			fold := jsplugin.ASCIIFold(alias)
			draft := drafts[fold]
			if draft == nil {
				draft = &taskAliasDraft{byPlugin: make(map[string]map[string]struct{})}
				drafts[fold] = draft
			}
			draft.spellings = append(draft.spellings, alias)
			declareds := draft.byPlugin[plugin.Meta.Key]
			if declareds == nil {
				declareds = make(map[string]struct{})
				draft.byPlugin[plugin.Meta.Key] = declareds
			}
			declareds[declared] = struct{}{}
		}
	}

	for _, draft := range drafts {
		alias := draft.spellings[0]
		for _, spelling := range draft.spellings[1:] {
			if spelling < alias {
				alias = spelling
			}
		}
		if len(draft.byPlugin) != 1 {
			pluginKeys := make([]string, 0, len(draft.byPlugin))
			for key := range draft.byPlugin {
				pluginKeys = append(pluginKeys, key)
			}
			common.SysLog(fmt.Sprintf("task model alias %q dropped: maps to multiple plugins %v", alias, pluginKeys))
			continue
		}
		var pluginKey, declared string
		for key, declareds := range draft.byPlugin {
			pluginKey = key
			if len(declareds) == 1 {
				for name := range declareds {
					declared = name
				}
			}
		}
		view.byFold[jsplugin.ASCIIFold(alias)] = TaskAliasTarget{
			Alias:     alias,
			Declared:  declared,
			PluginKey: pluginKey,
		}
	}
	return view
}

// followChannelModelMapping walks one channel's mapping the same way
// ModelMappedHelper does: visited-set cycle detection, self-map stops at
// the current hop, a non-self cycle is reported to the caller.
func followChannelModelMapping(modelMap map[string]string, start string) (string, bool) {
	current := start
	visited := map[string]bool{current: true}
	for {
		mapped, exists := modelMap[current]
		if !exists || mapped == "" {
			return current, false
		}
		if visited[mapped] {
			if mapped == current {
				return current, false
			}
			return "", true
		}
		visited[mapped] = true
		current = mapped
	}
}
