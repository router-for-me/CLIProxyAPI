package pluginhost

import (
	"context"

	log "github.com/sirupsen/logrus"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/modelvisibility"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// RegisterModelVisibility installs the model-list filter of the highest
// priority plugin that declares one.
//
// Only one filter can be active: two plugins narrowing the same list would have
// to agree on an order, and the result would depend on load order rather than
// on anything an operator configured. Highest priority wins, ties broken by
// plugin id so the choice is stable across restarts.
func (h *Host) RegisterModelVisibility() {
	if h == nil {
		return
	}

	var (
		chosen   pluginapi.ModelVisibility
		chosenID string
		priority int
		found    bool
	)
	for _, record := range h.activeRecords() {
		provider := record.plugin.Capabilities.ModelVisibility
		if provider == nil || h.isPluginFused(record.id) {
			continue
		}
		if !found || record.priority > priority ||
			(record.priority == priority && record.id < chosenID) {
			chosen, chosenID, priority, found = provider, record.id, record.priority, true
		}
	}
	if !found {
		modelvisibility.Set(nil)
		return
	}

	pluginID := chosenID
	// Nói ra khi bộ lọc được cài. Bản vá đầu tiên gắn lời gọi này vào nhầm
	// hai đường GỠ plugin chứ không phải đường NẠP, và vì không có dòng nào
	// được in ra nên nó trông y hệt một bộ lọc đang chạy mà không lọc gì.
	log.Infof("pluginhost: model visibility filter installed by plugin %s", pluginID)
	modelvisibility.Set(func(ctx context.Context, req modelvisibility.Request) []string {
		resp, err := chosen.VisibleModels(ctx, pluginapi.ModelVisibilityRequest{
			Principal: req.Principal,
			Provider:  req.Provider,
			Metadata:  req.Metadata,
			Handler:   req.Handler,
			Models:    req.Models,
		})
		if err != nil {
			// A failing filter must not empty the catalogue: returning nil keeps
			// the full list, which is the same behaviour as having no plugin.
			log.Warnf("pluginhost: plugin %s model visibility failed: %v", pluginID, err)
			return nil
		}
		if !resp.Handled {
			return nil
		}
		if resp.Models == nil {
			// Handled with a nil list means "hide everything". Return an empty
			// non-nil slice so Apply does not read it as "no opinion".
			return []string{}
		}
		return resp.Models
	})
}
