package pluginhost

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

// HandleServerTool offers a selected-auth request to the highest-priority server-tool plugin.
func (h *Host) HandleServerTool(ctx context.Context, req pluginapi.ServerToolRequest) (pluginapi.ServerToolResponse, bool, error) {
	record := h.serverToolRecord()
	if record == nil {
		return pluginapi.ServerToolResponse{}, false, nil
	}
	resp, handled, errHandle := h.callServerTool(ctx, *record, req)
	if errHandle != nil || !handled {
		return resp, handled, errHandle
	}
	if !resp.Handled {
		return pluginapi.ServerToolResponse{}, false, nil
	}
	return resp, true, nil
}

// HandleServerToolStream offers a selected-auth stream request to the highest-priority server-tool plugin.
func (h *Host) HandleServerToolStream(ctx context.Context, req pluginapi.ServerToolRequest) (pluginapi.ServerToolStreamResponse, bool, error) {
	record := h.serverToolRecord()
	if record == nil {
		return pluginapi.ServerToolStreamResponse{}, false, nil
	}
	resp, handled, errHandle := h.callServerToolStream(ctx, *record, req)
	if errHandle != nil || !handled {
		return resp, handled, errHandle
	}
	if !resp.Handled {
		return pluginapi.ServerToolStreamResponse{}, false, nil
	}
	return resp, true, nil
}

func (h *Host) HasServerToolHandler() bool {
	return h.serverToolRecord() != nil
}

func (h *Host) serverToolRecord() *capabilityRecord {
	if h == nil {
		return nil
	}
	for _, record := range h.Snapshot().records {
		if h.isPluginFused(record.id) || record.plugin.Capabilities.ServerToolHandler == nil {
			continue
		}
		copyRecord := record
		return &copyRecord
	}
	return nil
}

func (h *Host) callServerTool(ctx context.Context, record capabilityRecord, req pluginapi.ServerToolRequest) (resp pluginapi.ServerToolResponse, handled bool, err error) {
	handler := record.plugin.Capabilities.ServerToolHandler
	if h == nil || handler == nil || h.isPluginFused(record.id) {
		return pluginapi.ServerToolResponse{}, false, nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(record.id, "ServerToolHandler.HandleServerTool", recovered)
			resp = pluginapi.ServerToolResponse{}
			handled = false
			err = nil
		}
	}()

	req.Plugin = record.meta
	resp, errHandle := handler.HandleServerTool(ctx, req)
	if errHandle != nil {
		log.WithField("plugin_id", record.id).WithError(errHandle).Warn("pluginhost: server tool handler rejected request")
		return pluginapi.ServerToolResponse{}, true, errHandle
	}
	return resp, true, nil
}

func (h *Host) callServerToolStream(ctx context.Context, record capabilityRecord, req pluginapi.ServerToolRequest) (resp pluginapi.ServerToolStreamResponse, handled bool, err error) {
	handler := record.plugin.Capabilities.ServerToolHandler
	if h == nil || handler == nil || h.isPluginFused(record.id) {
		return pluginapi.ServerToolStreamResponse{}, false, nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(record.id, "ServerToolHandler.HandleServerToolStream", recovered)
			resp = pluginapi.ServerToolStreamResponse{}
			handled = false
			err = nil
		}
	}()

	req.Plugin = record.meta
	resp, errHandle := handler.HandleServerToolStream(ctx, req)
	if errHandle != nil {
		log.WithField("plugin_id", record.id).WithError(errHandle).Warn("pluginhost: server tool stream handler rejected request")
		return pluginapi.ServerToolStreamResponse{}, true, errHandle
	}
	return resp, true, nil
}
