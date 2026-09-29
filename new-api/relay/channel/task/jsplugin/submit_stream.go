package jsplugin

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/relay/helper"
)

// readSubmitEvents owns only SSE framing and bounded JSON state. The plugin
// interprets data, accumulates provider output and decides when it is complete.
// No response bytes are written to the client at this boundary.
func (a *TaskAdaptor) readSubmitEvents(parent context.Context, resp *http.Response, driverContext map[string]any) (any, error) {
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != "text/event-stream" {
		return nil, fmt.Errorf("expected a text/event-stream submit response")
	}
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	stopClose := context.AfterFunc(ctx, func() { _ = resp.Body.Close() })
	defer stopClose()
	idleTimeout := time.Duration(constant.StreamingTimeout) * time.Second
	if idleTimeout <= 0 {
		idleTimeout = 30 * time.Second
	}
	timer := time.AfterFunc(idleTimeout, func() { cancel(fmt.Errorf("task submit stream idle timeout")) })
	defer timer.Stop()

	scanner := helper.NewStreamScanner(resp.Body, maxTaskPluginPersistedJSONBytes+1)
	hook := "parseSubmitEvent"
	var accumulated *pluginruntime.JSONState
	if slices.Contains(a.plugin.Meta.RequiredCapabilities, pluginruntime.CapabilitySubmitSSEDelta) {
		hook = "parseSubmitEventDelta"
		accumulated = pluginruntime.NewJSONState(maxTaskPluginPersistedJSONBytes)
	}
	var state any
	var data []string
	var eventName, lastID string
	frameBytes := 0
	firstLine := true
	for scanner.Scan() {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		timer.Reset(idleTimeout)
		line := scanner.Text()
		if firstLine {
			line = strings.TrimPrefix(line, "\ufeff")
			firstLine = false
		}
		frameBytes += len(line) + 1
		if frameBytes > maxTaskPluginPersistedJSONBytes {
			return nil, fmt.Errorf("task submit SSE event exceeds size limit")
		}
		if line != "" {
			field, value, _ := strings.Cut(line, ":")
			value, _ = strings.CutPrefix(value, " ")
			switch field {
			case "data":
				data = append(data, value)
			case "event":
				eventName = value
			case "id":
				if !strings.ContainsRune(value, '\x00') {
					lastID = value
				}
			}
			continue
		}
		frameBytes = 0
		if len(data) == 0 {
			eventName = ""
			continue
		}
		if eventName == "" {
			eventName = "message"
		}
		event := map[string]any{"event": eventName, "id": lastID, "data": strings.Join(data, "\n")}
		value, err := a.plugin.Engine.Call(ctx, hook, driverContext, event, state)
		if err != nil {
			return nil, err
		}
		result, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s must return an object", hook)
		}
		nextState, hasState := result["state"]
		done, hasDone := result["done"].(bool)
		if !hasState || !hasDone {
			return nil, fmt.Errorf("%s must return state and a boolean done", hook)
		}
		encoded, err := common.Marshal(nextState)
		if err != nil {
			return nil, fmt.Errorf("invalid submit stream state: %w", err)
		}
		if len(encoded) > maxTaskPluginPersistedJSONBytes {
			return nil, fmt.Errorf("task submit stream state exceeds size limit")
		}
		if accumulated != nil {
			if len(result) != 3 {
				return nil, fmt.Errorf("parseSubmitEventDelta must return only changes, state and done")
			}
			// Control state is separate from the full result and is the only
			// snapshot passed back to JS on the next event.
			if len(encoded) > 64<<10 {
				return nil, fmt.Errorf("task submit stream control state exceeds size limit")
			}
			if err = accumulated.Apply(ctx, result["changes"]); err != nil {
				return nil, fmt.Errorf("invalid submit stream changes: %w", err)
			}
		}
		// Keep the exact encoded-byte limit above. Plain JSON state can be
		// isolated without parsing large strings again; codec-specific values
		// (for example exported typed arrays) retain the old normalization.
		var plainJSON bool
		state, plainJSON = cloneJSONValue(nextState, 0)
		if !plainJSON {
			if err = common.Unmarshal(encoded, &state); err != nil {
				return nil, err
			}
		}
		if done {
			if accumulated != nil {
				return accumulated.Value()
			}
			return state, nil
		}
		data = nil
		eventName = ""
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read task submit stream: %w", err)
	}
	return nil, fmt.Errorf("task submit stream ended before the plugin reported completion")
}
