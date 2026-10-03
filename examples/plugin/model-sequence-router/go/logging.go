package main

import (
	"encoding/json"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	log "github.com/sirupsen/logrus"
)

type pluginLogger func(level, message string, fields map[string]any, hostCallbackID string)

type hostLogRequest struct {
	HostCallbackID string         `json:"host_callback_id,omitempty"`
	Level          string         `json:"level,omitempty"`
	Message        string         `json:"message,omitempty"`
	Fields         map[string]any `json:"fields,omitempty"`
}

func (r *runtimeState) log(level, message string, fields map[string]any, hostCallbackID string) {
	if r == nil {
		return
	}
	if r.logger != nil {
		r.logger(level, message, fields, hostCallbackID)
	}
	r.diagnosticMu.RLock()
	if r.diagnostic != nil {
		r.diagnostic.write(level, message, fields, hostCallbackID)
	}
	r.diagnosticMu.RUnlock()
}

func (r *runtimeState) replaceDiagnosticSink(next *diagnosticSink) {
	if r == nil {
		if next != nil {
			next.close()
		}
		return
	}
	r.diagnosticMu.Lock()
	previous := r.diagnostic
	r.diagnostic = next
	if previous != nil {
		previous.close()
	}
	r.diagnosticMu.Unlock()
}

func hostPluginLogger(level, message string, fields map[string]any, hostCallbackID string) {
	payload, errMarshal := json.Marshal(hostLogRequest{
		HostCallbackID: hostCallbackID,
		Level:          level,
		Message:        message,
		Fields:         fields,
	})
	if errMarshal != nil {
		log.WithError(errMarshal).Warn("model-sequence-router: failed to encode host log")
		return
	}
	if errCall := callHost(pluginabi.MethodHostLog, payload); errCall != nil {
		entry := log.WithFields(log.Fields(fields))
		switch level {
		case "info":
			entry.Info(message)
		case "warn":
			entry.Warn(message)
		case "error":
			entry.Error(message)
		default:
			entry.Debug(message)
		}
	}
}
