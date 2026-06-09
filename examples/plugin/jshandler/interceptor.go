package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/dop251/goja"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

type jsHandlerPlugin struct {
	cfg        jsHandlerConfig
	configYAML []byte
	pluginDir  string
}

type processedHeaders struct {
	headers      http.Header
	clearHeaders []string
}

var _ pluginapi.RequestInterceptor = (*jsHandlerPlugin)(nil)
var _ pluginapi.ResponseInterceptor = (*jsHandlerPlugin)(nil)
var _ pluginapi.StreamChunkInterceptor = (*jsHandlerPlugin)(nil)

func (p *jsHandlerPlugin) Identifier() string {
	return jsHandlerProvider
}

func (p *jsHandlerPlugin) allScriptPaths() []string {
	paths := builtinScriptPaths(p.pluginDir)
	configuredPaths, errPaths := p.cfg.resolvedScriptPaths(p.pluginDir)
	if errPaths != nil {
		log.Warnf("failed to resolve JS handler script paths: %v", errPaths)
		return paths
	}
	paths = append(paths, configuredPaths...)
	return paths
}

func (p *jsHandlerPlugin) InterceptRequest(ctx context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
	resp := pluginapi.RequestInterceptResponse{}
	scriptPaths := p.allScriptPaths()
	if len(scriptPaths) == 0 {
		return resp, nil
	}

	body := string(req.Body)
	headers := cloneHeader(req.Headers)
	var clearHeaders []string

	for _, scriptPath := range scriptPaths {
		scriptPath = strings.TrimSpace(scriptPath)
		if scriptPath == "" {
			continue
		}
		processed, cleared, errJS := p.applyJSBeforeRequest(scriptPath, []byte(body), req.Model, req.SourceFormat, headers)
		if errJS != nil {
			log.Warnf("failed to execute JS request interceptor [%s]: %v", scriptPath, errJS)
			continue
		}
		body = string(processed)
		clearHeaders = append(clearHeaders, cleared...)
	}

	if len(body) > 0 {
		resp.Body = []byte(body)
	}
	resp.Headers = headers
	resp.ClearHeaders = dedupeStrings(clearHeaders)
	return resp, nil
}

func (p *jsHandlerPlugin) InterceptResponse(ctx context.Context, req pluginapi.ResponseInterceptRequest) (pluginapi.ResponseInterceptResponse, error) {
	resp := pluginapi.ResponseInterceptResponse{}
	scriptPaths := p.allScriptPaths()
	if len(scriptPaths) == 0 {
		return resp, nil
	}

	bodyStr := string(req.Body)
	reqHeadersMap := headerToAnyMap(req.RequestHeaders)
	respHeaders := cloneHeader(req.ResponseHeaders)
	var clearHeaders []string

	for _, scriptPath := range scriptPaths {
		scriptPath = strings.TrimSpace(scriptPath)
		if scriptPath == "" {
			continue
		}
		processedBody, processedHeaders, bodyModified, errJS := p.applyJSAfterResponse(
			scriptPath, req.Model, req.SourceFormat,
			reqHeadersMap, req.RequestBody,
			bodyStr, nil, respHeaders, false, nil,
		)
		if errJS != nil {
			log.Warnf("failed to execute JS response interceptor [%s]: %v", scriptPath, errJS)
			continue
		}
		if bodyModified {
			bodyStr = processedBody
		}
		if processedHeaders != nil {
			respHeaders = processedHeaders.headers
			clearHeaders = append(clearHeaders, processedHeaders.clearHeaders...)
		}
	}

	if len(bodyStr) > 0 {
		resp.Body = []byte(bodyStr)
	}
	resp.Headers = respHeaders
	resp.ClearHeaders = dedupeStrings(clearHeaders)
	return resp, nil
}

func (p *jsHandlerPlugin) InterceptStreamChunk(ctx context.Context, req pluginapi.StreamChunkInterceptRequest) (pluginapi.StreamChunkInterceptResponse, error) {
	resp := pluginapi.StreamChunkInterceptResponse{}
	scriptPaths := p.allScriptPaths()
	if len(scriptPaths) == 0 {
		return resp, nil
	}

	reqHeadersMap := headerToAnyMap(req.RequestHeaders)
	respHeaders := cloneHeader(req.ResponseHeaders)
	var clearHeaders []string
	historyStrings := make([]string, 0, len(req.HistoryChunks))
	for _, hc := range req.HistoryChunks {
		historyStrings = append(historyStrings, string(hc))
	}

	isHeaderInit := req.ChunkIndex == pluginapi.StreamChunkHeaderInitIndex
	chunkStr := ""
	if !isHeaderInit && len(req.Body) > 0 {
		chunkStr = string(req.Body)
	}

	var chunkPtr *string
	chunkModified := false
	if !isHeaderInit {
		chunkPtr = &chunkStr
	}

	for _, scriptPath := range scriptPaths {
		scriptPath = strings.TrimSpace(scriptPath)
		if scriptPath == "" {
			continue
		}
		processedBody, processedHeaders, chunkChanged, errJS := p.applyJSAfterResponse(
			scriptPath, req.Model, req.SourceFormat,
			reqHeadersMap, req.RequestBody,
			"", chunkPtr, respHeaders, !isHeaderInit, historyStrings,
		)
		if errJS != nil {
			log.Warnf("failed to execute JS stream chunk interceptor [%s]: %v", scriptPath, errJS)
			continue
		}
		if processedHeaders != nil {
			respHeaders = processedHeaders.headers
			clearHeaders = append(clearHeaders, processedHeaders.clearHeaders...)
		}
		if chunkPtr != nil && chunkChanged {
			*chunkPtr = processedBody
			chunkModified = true
		}
	}

	resp.Headers = respHeaders
	resp.ClearHeaders = dedupeStrings(clearHeaders)
	if chunkPtr != nil && *chunkPtr != "" {
		resp.Body = []byte(*chunkPtr)
	} else if isHeaderInit {
		// header-only init, no body to return
	} else if chunkModified || len(req.Body) == 0 {
		resp.DropChunk = true
	}
	return resp, nil
}

func (p *jsHandlerPlugin) applyJSBeforeRequest(scriptPath string, payloadBytes []byte, model, protocol string, headers http.Header) ([]byte, []string, error) {
	program, err := getJSProgram(scriptPath)
	if err != nil {
		return nil, nil, err
	}

	engine := newJSEngine()
	if errRun := engine.runProgram(program, p.cfg.Timeout); errRun != nil {
		return nil, nil, errRun
	}

	headersMap := headerToAnyMap(headers)

	jsCtx := map[string]any{
		"id":       generateRequestID(),
		"body":     string(payloadBytes),
		"headers":  headersMap,
		"url":      "",
		"model":    model,
		"protocol": protocol,
	}

	jsVal, errCall := engine.callFunction("on_before_request", p.cfg.Timeout, jsCtx)
	if errCall != nil {
		if errors.Is(errCall, ErrFunctionNotFound) {
			return payloadBytes, nil, nil
		}
		return nil, nil, fmt.Errorf("on_before_request failed for %s: %w", scriptPath, errCall)
	}

	if jsVal == nil || goja.IsUndefined(jsVal) || goja.IsNull(jsVal) {
		return payloadBytes, nil, nil
	}

	exported := jsVal.Export()
	if exported == nil {
		return payloadBytes, nil, nil
	}

	var clearHeaders []string
	if objMap, ok := exported.(map[string]any); ok {
		if headersVal, exists := objMap["headers"]; exists {
			clearHeaders = append(clearHeaders, updateHeaderFromAny(headers, headersVal)...)
		}
		if bodyVal, exists := objMap["body"]; exists {
			if bodyStr, okStr := bodyVal.(string); okStr {
				return []byte(bodyStr), clearHeaders, nil
			}
		}
	}

	if bodyStr, ok := exported.(string); ok {
		return []byte(bodyStr), clearHeaders, nil
	}

	return payloadBytes, clearHeaders, nil
}

func (p *jsHandlerPlugin) applyJSAfterResponse(
	scriptPath, model, protocol string,
	reqHeadersMap map[string]any, reqBody []byte,
	bodyStr string, chunkStr *string,
	respHeaders http.Header, isStream bool, historyChunks []string,
) (string, *processedHeaders, bool, error) {
	program, err := getJSProgram(scriptPath)
	if err != nil {
		return bodyStr, nil, false, err
	}

	engine := newJSEngine()
	if errRun := engine.runProgram(program, p.cfg.Timeout); errRun != nil {
		return bodyStr, nil, false, errRun
	}

	var bodyVal any = bodyStr
	if isStream {
		bodyVal = nil
	}

	reqCtx := engine.vm.NewObject()
	if errSet := reqCtx.Set("body", string(reqBody)); errSet != nil {
		return bodyStr, nil, false, errSet
	}
	if errSet := reqCtx.Set("headers", reqHeadersMap); errSet != nil {
		return bodyStr, nil, false, errSet
	}
	if errSet := reqCtx.Set("url", ""); errSet != nil {
		return bodyStr, nil, false, errSet
	}

	jsCtx := engine.vm.NewObject()
	if errSet := jsCtx.Set("id", generateRequestID()); errSet != nil {
		return bodyStr, nil, false, errSet
	}
	if errSet := jsCtx.Set("body", bodyVal); errSet != nil {
		return bodyStr, nil, false, errSet
	}
	if errSet := jsCtx.Set("req", reqCtx); errSet != nil {
		return bodyStr, nil, false, errSet
	}
	if errSet := jsCtx.Set("protocol", protocol); errSet != nil {
		return bodyStr, nil, false, errSet
	}
	if errSet := jsCtx.Set("headers", headerToAnyMap(respHeaders)); errSet != nil {
		return bodyStr, nil, false, errSet
	}
	if isStream {
		if chunkStr != nil {
			if errSet := jsCtx.Set("chunk", *chunkStr); errSet != nil {
				return bodyStr, nil, false, errSet
			}
		} else {
			if errSet := jsCtx.Set("chunk", ""); errSet != nil {
				return bodyStr, nil, false, errSet
			}
		}
		historyChunksValue, errHistory := engine.frozenStringArray(historyChunks)
		if errHistory != nil {
			return bodyStr, nil, false, fmt.Errorf("failed to freeze history_chunks: %w", errHistory)
		}
		if errDefine := jsCtx.DefineDataProperty("history_chunks", historyChunksValue, goja.FLAG_FALSE, goja.FLAG_FALSE, goja.FLAG_TRUE); errDefine != nil {
			return bodyStr, nil, false, fmt.Errorf("failed to define history_chunks: %w", errDefine)
		}
	} else {
		if errSet := jsCtx.Set("chunk", nil); errSet != nil {
			return bodyStr, nil, false, errSet
		}
		if errSet := jsCtx.Set("history_chunks", nil); errSet != nil {
			return bodyStr, nil, false, errSet
		}
	}

	hookName := "on_after_nonstream_response"
	if isStream {
		hookName = "on_after_stream_response"
	}
	jsVal, errCall := engine.callFunction(hookName, p.cfg.Timeout, jsCtx)
	if errCall != nil {
		if errors.Is(errCall, ErrFunctionNotFound) {
			return bodyStr, nil, false, nil
		}
		return bodyStr, nil, false, fmt.Errorf("%s failed for %s: %w", hookName, scriptPath, errCall)
	}

	if jsVal == nil || goja.IsUndefined(jsVal) || goja.IsNull(jsVal) {
		return bodyStr, nil, false, nil
	}

	exported := jsVal.Export()
	if exported == nil {
		return bodyStr, nil, false, nil
	}

	var headersResult *processedHeaders
	if objMap, ok := exported.(map[string]any); ok {
		if headersVal, exists := objMap["headers"]; exists {
			cleared := updateHeaderFromAny(respHeaders, headersVal)
			headersResult = &processedHeaders{headers: respHeaders, clearHeaders: cleared}
		}
		if !isStream {
			if bodyVal, exists := objMap["body"]; exists {
				if bStr, okStr := bodyVal.(string); okStr {
					return bStr, headersResult, true, nil
				}
			}
		} else {
			if chunkVal, exists := objMap["chunk"]; exists {
				if cStr, okStr := chunkVal.(string); okStr {
					return cStr, headersResult, true, nil
				}
			}
		}
	}

	if strVal, ok := exported.(string); ok {
		return strVal, headersResult, true, nil
	}

	return bodyStr, headersResult, false, nil
}

func headerToAnyMap(h http.Header) map[string]any {
	m := make(map[string]any)
	if h == nil {
		return m
	}
	for k, v := range h {
		switch len(v) {
		case 0:
			continue
		case 1:
			m[k] = v[0]
		default:
			m[k] = append([]string(nil), v...)
		}
	}
	return m
}

func updateHeaderFromAny(h http.Header, val interface{}) []string {
	var clearHeaders []string
	if h == nil || val == nil {
		return clearHeaders
	}
	rv := reflect.ValueOf(val)
	if rv.Kind() != reflect.Map {
		return clearHeaders
	}
	for _, key := range rv.MapKeys() {
		kStr := key.String()
		vVal := rv.MapIndex(key).Interface()
		if vVal == nil {
			h.Del(kStr)
			clearHeaders = append(clearHeaders, kStr)
		} else if valStr, ok := vVal.(string); ok {
			h.Set(kStr, valStr)
		} else {
			values, okValues := stringSliceFromAny(vVal)
			if !okValues {
				h.Set(kStr, fmt.Sprintf("%v", vVal))
				continue
			}
			if len(values) == 0 {
				h.Del(kStr)
				clearHeaders = append(clearHeaders, kStr)
			} else {
				h[http.CanonicalHeaderKey(kStr)] = values
			}
		}
	}
	return clearHeaders
}

func cloneHeader(h http.Header) http.Header {
	cloned := make(http.Header, len(h))
	for key, values := range h {
		cloned[key] = append([]string(nil), values...)
	}
	return cloned
}

func stringSliceFromAny(val any) ([]string, bool) {
	switch typed := val.(type) {
	case []string:
		return append([]string(nil), typed...), true
	case []any:
		values := make([]string, 0, len(typed))
		for _, item := range typed {
			itemStr, okItem := item.(string)
			if !okItem {
				return nil, false
			}
			values = append(values, itemStr)
		}
		return values, true
	}

	rv := reflect.ValueOf(val)
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return nil, false
	}
	values := make([]string, 0, rv.Len())
	for i := 0; i < rv.Len(); i++ {
		item, okItem := rv.Index(i).Interface().(string)
		if !okItem {
			return nil, false
		}
		values = append(values, item)
	}
	return values, true
}

func dedupeStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	deduped := make([]string, 0, len(values))
	for _, value := range values {
		canonical := http.CanonicalHeaderKey(value)
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		deduped = append(deduped, canonical)
	}
	return deduped
}

func generateRequestID() string {
	return fmt.Sprintf("%s-%x", time.Now().Format("20060102150405"), time.Now().UnixNano()&0xffffffff)
}
