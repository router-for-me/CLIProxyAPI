package websearch

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func logMCPCloseError(err error) {
	log.WithError(err).Debug("websearch: close MCP response body")
}

// maxMCPResponseBytes caps a single JSON-RPC response.
const maxMCPResponseBytes = 4 << 20

// MCP protocol constants used by every remote-MCP provider.
const (
	mcpProtocolVersion = "2025-03-26"
	// mcpClientName matches the clientInfo name the upstream Z.AI client
	// sends; some hosts gate protocol features on a recognized client.
	mcpClientName    = "omp-coding-agent"
	mcpClientVersion = "1.0.0"
)

// mcpSession is a live remote-MCP connection. Streamable HTTP servers
// require the full lifecycle — initialize, the initialized notification,
// then tool calls — with the server-issued session id threaded through
// every subsequent request.
type mcpSession struct {
	endpoint string
	headers  map[string]string
	client   Doer
	// sessionID is the Mcp-Session-Id handed out by `initialize` and
	// required on every later request.
	sessionID string
	// requestID increments per JSON-RPC call.
	requestID int
}

// newMCPSession starts a remote-MCP conversation. initialize failures are
// tolerated because some deployments accept a bare tools/call, but the
// session id is always used when the server supplies one.
func newMCPSession(ctx context.Context, cfg Config, label, endpoint, toolName string, headers map[string]string) *mcpSession {
	session := &mcpSession{
		endpoint: endpoint,
		headers:  headers,
		client:   doerFor(cfg),
	}
	initialize := []byte(`{"protocolVersion":"","capabilities":{},"clientInfo":{"name":"","version":""}}`)
	initialize, _ = sjson.SetBytes(initialize, "protocolVersion", mcpProtocolVersion)
	initialize, _ = sjson.SetBytes(initialize, "clientInfo.name", mcpClientName)
	initialize, _ = sjson.SetBytes(initialize, "clientInfo.version", mcpClientVersion)
	// A failed initialize must not abort the call: fall back to a bare
	// tools/call, which several servers accept.
	if _, errInit := session.post(ctx, label, "initialize", initialize, true); errInit == nil {
		_, _ = session.post(ctx, label, "notifications/initialized", []byte(`{}`), false)
	}
	return session
}

// post issues one JSON-RPC request, threading the session id.
func (s *mcpSession) post(ctx context.Context, label, method string, params []byte, expectResponse bool) ([]byte, error) {
	s.requestID++
	payload := []byte(`{"jsonrpc":"2.0","method":"","params":{}}`)
	payload, _ = sjson.SetBytes(payload, "method", method)
	payload, _ = sjson.SetRawBytes(payload, "params", params)
	if expectResponse {
		payload, _ = sjson.SetBytes(payload, "id", s.requestID)
	}
	headers := map[string]string{"Accept": "application/json, text/event-stream"}
	for key, value := range s.headers {
		headers[key] = value
	}
	if s.sessionID != "" {
		headers["Mcp-Session-Id"] = s.sessionID
	}
	httpReq, errRequest := newJSONRequest(ctx, http.MethodPost, s.endpoint, payload, headers)
	if errRequest != nil {
		return nil, &ProviderError{Provider: label, Message: errRequest.Error()}
	}
	resp, errDo := s.client.Do(httpReq)
	if errDo != nil {
		return nil, &ProviderError{Provider: label, Message: errDo.Error()}
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			logMCPCloseError(errClose)
		}
	}()
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, maxMCPResponseBytes+1))
	if errRead != nil {
		return nil, &ProviderError{Provider: label, Message: errRead.Error(), Status: resp.StatusCode}
	}
	if int64(len(body)) > maxMCPResponseBytes {
		return nil, &ProviderError{Provider: label, Message: "response too large", Status: resp.StatusCode}
	}
	if next := resp.Header.Get("Mcp-Session-Id"); next != "" {
		s.sessionID = next
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return body, &ProviderError{Provider: label, Message: summarizeErrorBody(body), Status: resp.StatusCode}
	}
	if !expectResponse {
		return nil, nil
	}
	return parseMCPSSEBody(body), nil
}

// callTool invokes one tool, retrying the caller's argument variants when
// the server rejects the argument shape. A `result.isError` reply counts as
// a shape failure, because that is how a strict tool reports bad arguments.
func (s *mcpSession) callTool(ctx context.Context, label, toolName string, argsVariants [][]byte) ([]byte, error) {
	var lastErr error
	for _, args := range argsVariants {
		call := []byte(`{"name":"","arguments":{}}`)
		call, _ = sjson.SetBytes(call, "name", toolName)
		call, _ = sjson.SetRawBytes(call, "arguments", args)
		body, errCall := s.post(ctx, label, "tools/call", call, true)
		if errCall != nil {
			lastErr = errCall
			if mcpStatusIsArgumentError(errCall) {
				continue
			}
			return nil, errCall
		}
		result, errTool := mcpToolResult(body, label)
		if errTool != nil {
			lastErr = errTool
			if mcpIsArgumentError(errTool) {
				continue
			}
			return nil, errTool
		}
		return result, nil
	}
	if lastErr == nil {
		lastErr = &ProviderError{Provider: label, Message: "no usable argument shape", Status: http.StatusBadRequest}
	}
	return nil, lastErr
}

// mcpToolResult extracts the tool result, turning a JSON-RPC error or a
// `result.isError` tool failure into a provider error so the caller can
// decide whether to retry the argument shape.
func mcpToolResult(body []byte, label string) ([]byte, error) {
	root := gjson.ParseBytes(body)
	// A remote MCP host may answer with a non-enveloped failure: Z.AI's
	// {code, msg, success:false} envelope, or a bare error object. Both must
	// surface as a provider error rather than as an empty result, or a
	// strict tool's rejection would read as "no results".
	if root.Get("jsonrpc").String() == "" && mcpDirectErrorMessage(root) != "" {
		status := http.StatusBadRequest
		if code := int(root.Get("code").Int()); code != 0 {
			status = mcpErrorCodeStatus(code)
		}
		return nil, &ProviderError{Provider: label, Message: mcpDirectErrorMessage(root), Status: status}
	}
	if rpcErr := root.Get("error"); rpcErr.Exists() {
		message := firstNonEmpty(rpcErr.Get("message").String(), "MCP error")
		return nil, &ProviderError{Provider: label, Message: message, Status: mcpErrorCodeStatus(int(rpcErr.Get("code").Int()))}
	}
	result := root.Get("result")
	if !result.Exists() {
		if message := mcpDirectErrorMessage(root); message != "" {
			return nil, &ProviderError{Provider: label, Message: message, Status: http.StatusBadRequest}
		}
		return nil, &ProviderError{Provider: label, Message: "empty tool result", Status: http.StatusBadRequest}
	}
	if result.Get("isError").Bool() {
		var text strings.Builder
		result.Get("content").ForEach(func(_, item gjson.Result) bool {
			if value := strings.TrimSpace(item.Get("text").String()); value != "" {
				if text.Len() > 0 {
					text.WriteString("\n")
				}
				text.WriteString(value)
			}
			return true
		})
		message := strings.TrimSpace(text.String())
		if message == "" {
			message = "MCP tool call failed"
		}
		return nil, &ProviderError{Provider: label, Message: message, Status: mcpErrorStatus(message)}
	}
	if content := result.Get("content"); content.Exists() {
		return []byte(content.Raw), nil
	}
	if structured := result.Get("structuredContent"); structured.Exists() {
		return []byte(structured.Raw), nil
	}
	// A tool may legitimately answer with an empty result; that is a valid
	// "no results" outcome, not a shape failure.
	return []byte(result.Raw), nil
}

// mcpDirectErrorMessage reads the non-enveloped error text a remote host
// may put at the top level of its reply.
func mcpDirectErrorMessage(root gjson.Result) string {
	return firstNonEmpty(
		root.Get("msg").String(),
		root.Get("message").String(),
		root.Get("error_message").String(),
	)
}

// mcpErrorCodeStatus maps a JSON-RPC error code to a provider status. The
// codes are negative protocol numbers, not HTTP statuses, so the magnitude
// is used: -32602 (invalid params) must read as a 400-class argument
// rejection, not as a status that no HTTP client would ever report.
func mcpErrorCodeStatus(code int) int {
	if code == 0 {
		return http.StatusBadRequest
	}
	if code < 0 {
		code = -code
	}
	if code < 100 || code > 599 {
		return http.StatusBadRequest
	}
	return code
}

// mcpErrorStatus extracts the numeric status a strict tool embeds in its
// error text, e.g. "MCP error -32602". The magnitude is the HTTP status.
var mcpErrorStatusPattern = regexp.MustCompile(`(?i)MCP error\s*(-?\d+)`)

func mcpErrorStatus(message string) int {
	match := mcpErrorStatusPattern.FindStringSubmatch(message)
	if match == nil {
		return http.StatusBadRequest
	}
	status, errStatus := strconv.Atoi(match[1])
	if errStatus != nil || status == 0 {
		return http.StatusBadRequest
	}
	if status < 0 {
		status = -status
	}
	return mcpErrorCodeStatus(status)
}

// mcpStatusIsArgumentError reports whether an HTTP failure looks like a
// rejected argument shape rather than a transport or auth problem.
func mcpStatusIsArgumentError(err error) bool {
	providerErr, ok := err.(*ProviderError)
	if !ok || providerErr == nil {
		return false
	}
	return providerErr.Status == http.StatusBadRequest || providerErr.Status == http.StatusUnprocessableEntity
}

// mcpIsArgumentError reports whether a tool-level error describes bad
// arguments, which is how a strict tool answers an unknown field name.
func mcpIsArgumentError(err error) bool {
	providerErr, ok := err.(*ProviderError)
	if !ok || providerErr == nil {
		return false
	}
	message := strings.ToLower(providerErr.Message)
	for _, marker := range []string{"invalid", "argument", "search_query", "query", "unknown field", "missing"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// parseMCPSSEBody unwraps a possibly SSE-framed JSON-RPC body and returns
// the final event payload, which is the authoritative reply.
func parseMCPSSEBody(body []byte) []byte {
	var last []byte
	for _, line := range bytes.Split(body, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if !bytes.HasPrefix(trimmed, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(trimmed[len("data:"):])
		if len(payload) > 0 {
			last = payload
		}
	}
	if last != nil {
		return last
	}
	return body
}

// mcpCall preserves the previous single-shot helper for providers that do
// not need a session: it opens a session and immediately calls the tool.
func mcpCall(ctx context.Context, cfg Config, label, endpoint, toolName string, headers map[string]string, argsVariants [][]byte) ([]byte, error) {
	session := newMCPSession(ctx, cfg, label, endpoint, toolName, headers)
	return session.callTool(ctx, label, toolName, argsVariants)
}

// mcpHeaders builds the standard header set for remote MCP endpoints.
func mcpHeaders(apiKey string) map[string]string {
	headers := map[string]string{
		"Content-Type": "application/json",
		"Accept":       "application/json, text/event-stream",
	}
	if apiKey != "" {
		headers["Authorization"] = "Bearer " + apiKey
	}
	return headers
}

// mcpArgumentBody builds one tools/call argument payload.
func mcpArgumentBody(fields map[string]any) []byte {
	encoded, errMarshal := json.Marshal(fields)
	if errMarshal != nil {
		return []byte(`{}`)
	}
	return encoded
}

// sseLines streams a text/event-stream body, calling fn for each data
// payload. It returns the first error from fn, or a transport error.
func sseLines(body io.Reader, fn func(data []byte) error) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), maxMCPResponseBytes)
	var event strings.Builder
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		switch {
		case line == "":
			if event.Len() == 0 {
				continue
			}
			payload := []byte(event.String())
			event.Reset()
			if errFn := fn(payload); errFn != nil {
				return errFn
			}
		case strings.HasPrefix(line, "data:"):
			if event.Len() > 0 {
				event.WriteString("\n")
			}
			event.WriteString(strings.TrimSpace(line[len("data:"):]))
		case strings.HasPrefix(line, "event:"), strings.HasPrefix(line, "id:"), strings.HasPrefix(line, "retry:"):
			// Frame metadata is not needed by the parsers.
		}
	}
	if errScan := scanner.Err(); errScan != nil {
		return errScan
	}
	if event.Len() > 0 {
		return fn([]byte(event.String()))
	}
	return nil
}

// mcpText flattens MCP text content blocks into a single string.
func mcpText(content []byte) string {
	var parts []string
	gjson.ParseBytes(content).ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() == "text" {
			if text := strings.TrimSpace(item.Get("text").String()); text != "" {
				parts = append(parts, text)
			}
		}
		return true
	})
	return strings.Join(parts, "\n")
}

// mcpTextSources parses JSON embedded in MCP text content. Remote MCP
// servers commonly answer with a JSON document inside a text block.
func mcpTextSources(content []byte, count int) []Source {
	text := mcpText(content)
	if text == "" {
		return nil
	}
	parsed := gjson.Parse(text)
	if !parsed.Exists() {
		return nil
	}
	return sourcesFromJSON(parsed, count)
}

// sourcesFromJSON pulls normalized sources out of a JSON document whose
// shape is not fixed. It walks result-shaped and list-shaped containers so
// one parser serves Exa, Z.AI, Parallel, Kimi, and Synthetic replies.
func sourcesFromJSON(root gjson.Result, count int) []Source {
	var sources []Source
	add := func(title, url, snippet, published string) {
		url = strings.TrimSpace(url)
		if url == "" {
			return
		}
		sources = append(sources, Source{
			Title:     strings.TrimSpace(title),
			URL:       url,
			Snippet:   strings.TrimSpace(snippet),
			Published: strings.TrimSpace(published),
		})
	}
	var visit func(node gjson.Result, depth int)
	visit = func(node gjson.Result, depth int) {
		if len(sources) >= count || depth > 5 {
			return
		}
		// Results often sit one object level down, e.g. a Firecrawl reply
		// nests its list under "data". Descend before giving up.
		if node.IsObject() {
			for _, key := range nodeKeys(node) {
				visit(node.Get(key), depth+1)
			}
			return
		}
		if !node.IsArray() {
			return
		}
		node.ForEach(func(_, item gjson.Result) bool {
			if len(sources) >= count {
				return false
			}
			// A snippet-bearing list is a list of sources itself, not a
			// container: Parallel returns `excerpts` per hit and the walker
			// would otherwise descend into the string array and find none.
			// Exa's `highlights` is the same shape.
			switch {
			case isJSONArray(item.Get("results")):
				visit(item.Get("results"), depth+1)
			case isJSONArray(item.Get("sources")):
				visit(item.Get("sources"), depth+1)
			case item.Get("url").Exists() || item.Get("link").Exists():
				add(
					firstNonEmpty(item.Get("title").String(), item.Get("name").String()),
					firstNonEmpty(item.Get("url").String(), item.Get("link").String()),
					firstNonEmpty(
						parallelExcerpts(item.Get("excerpts"), "\n\n"),
						item.Get("text").String(),
						item.Get("snippet").String(),
						item.Get("content").String(),
						item.Get("summary").String(),
						item.Get("description").String(),
						parallelExcerpts(item.Get("highlights"), " "),
					),
					firstNonEmpty(item.Get("publishedDate").String(), item.Get("published_date").String(), item.Get("publishedAt").String(), item.Get("published").String(), item.Get("publish_date").String(), item.Get("time").String(), item.Get("date").String()),
				)
			default:
				visit(item, depth+1)
			}
			return true
		})
	}
	for _, path := range []string{"result", "results", "sources", "search_result", "web", "data"} {
		node := root.Get(path)
		visit(node, 0)
		if len(sources) > 0 {
			return sources
		}
	}
	return sources
}

// parallelExcerpts joins a snippet-bearing list field into one string.
// The separator is the caller's: Parallel joins its `excerpts[]` with a
// blank line, while Exa joins `highlights[]` with a single space, so a
// single separator for both would misreport one of the two providers.
func parallelExcerpts(node gjson.Result, separator string) string {
	if !node.IsArray() {
		return ""
	}
	var parts []string
	node.ForEach(func(_, item gjson.Result) bool {
		if text := strings.TrimSpace(item.String()); text != "" {
			parts = append(parts, text)
		}
		return true
	})
	return strings.Join(parts, separator)
}

// nodeKeys lists the top-level keys of a JSON object.
func nodeKeys(node gjson.Result) []string {
	if !node.IsObject() {
		return nil
	}
	var keys []string
	node.ForEach(func(key, _ gjson.Result) bool {
		keys = append(keys, key.String())
		return true
	})
	return keys
}

func isJSONArray(node gjson.Result) bool {
	return node.IsArray() || (node.Type == gjson.JSON && len(node.Array()) > 0)
}
