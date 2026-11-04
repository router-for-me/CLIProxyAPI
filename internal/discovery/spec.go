package discovery

import (
	"fmt"
	"strings"
)

const (
	maxTXTRecordBytes = 255 // RFC 6763 §6.1 single character-string limit
	maxTXTBytes       = 400 // draft-su-ai-gateway-discovery-00 TXT budget

	txtSchemaVersion = "1"
	defaultMountPath = "/"
)

// TXT keys defined by draft-su-ai-gateway-discovery-00.
// Every key is at most nine characters long (RFC 6763 §6.4).
const (
	txtKeyVersion   = "txtvers"
	txtKeyPath      = "path"
	txtKeyTLS       = "tls"
	txtKeyAuth      = "auth"
	txtKeyProtocols = "protocols"
	txtKeyAuthType  = "auth_type"
	txtKeyProduct   = "product"
	txtKeyID        = "id"
	txtKeyRole      = "role"
)

// Legacy TXT keys advertised by CPA releases before the draft schema.
// Browsers still accept them so older gateways remain discoverable.
const (
	legacyKeyVersion     = "version"
	legacyKeyAuth        = "auth_required"
	legacyKeyAuthMethods = "auth_methods"
	legacyKeyInstanceID  = "instance_id"
	legacyKeyNodeRole    = "node_role"
	legacyKeyOpenAI      = "api_openai"
	legacyKeyAnthropic   = "api_anthropic"
	legacyKeyGemini      = "api_gemini"
)

// protocolRoute maps a protocol token to its standard route below the gateway mount path.
type protocolRoute struct {
	version     string // Versioned prefix below path, e.g. /v1 or /v1beta
	suffix      string // Operation route below the versioned prefix
	legacyField string // Legacy api_* key that carried the versioned base path
}

var protocolRoutes = map[string]protocolRoute{
	"chat-completions": {version: "/v1", suffix: "/chat/completions", legacyField: legacyKeyOpenAI},
	"completions":      {version: "/v1", suffix: "/completions", legacyField: legacyKeyOpenAI},
	"responses":        {version: "/v1", suffix: "/responses", legacyField: legacyKeyOpenAI},
	"responses-ws":     {version: "/v1", suffix: "/responses", legacyField: legacyKeyOpenAI},
	"realtime":         {version: "/v1", suffix: "/realtime", legacyField: legacyKeyOpenAI},
	"images":           {version: "/v1", suffix: "/images/generations", legacyField: legacyKeyOpenAI},
	"videos":           {version: "/v1", suffix: "/videos", legacyField: legacyKeyOpenAI},
	"audio":            {version: "/v1", suffix: "/audio/speech", legacyField: legacyKeyOpenAI},
	"search":           {version: "/v1", suffix: "/alpha/search", legacyField: legacyKeyOpenAI},
	"messages":         {version: "/v1", suffix: "/messages", legacyField: legacyKeyAnthropic},
	"count-tokens":     {version: "/v1", suffix: "/messages/count_tokens", legacyField: legacyKeyAnthropic},
	"generate-content": {version: "/v1beta", suffix: "/models/{model}:generateContent", legacyField: legacyKeyGemini},
	"interactions":     {version: "/v1beta", suffix: "/interactions", legacyField: legacyKeyGemini},
}

// printableTXTASCII checks bytes without Unicode folding or whitespace normalization.
func printableTXTASCII(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] < 0x20 || value[i] > 0x7e {
			return false
		}
	}
	return true
}

// TXTOptions contains input fields for generating DNS-SD TXT records.
type TXTOptions struct {
	Path         string
	Product      string
	Protocols    []string
	TLS          bool
	AuthRequired bool
	AuthMethods  []string
	InstanceID   string
	NodeRole     string
}

// DefaultTXTOptions provides safe defaults for CPA TXT records.
func DefaultTXTOptions() TXTOptions {
	return TXTOptions{
		Path:         defaultMountPath,
		Product:      ProductCPA,
		Protocols:    []string{"chat-completions", "responses", "messages", "generate-content", "interactions"},
		TLS:          false,
		AuthRequired: true,
		AuthMethods:  []string{"api_key"},
		NodeRole:     "standalone",
	}
}

// BuildTXTRecords constructs the draft-su-ai-gateway-discovery-00 TXT record.
// Required attributes are always emitted first; optional attributes are skipped
// when they would exceed the 255-byte string or 400-byte record budget.
func BuildTXTRecords(opts TXTOptions) ([]string, error) {
	path := opts.Path
	if path == "" {
		path = defaultMountPath
	}
	if validateMountPath(path) == "" {
		return nil, fmt.Errorf("discovery: invalid TXT path %q", path)
	}

	records := []string{
		txtKeyVersion + "=" + txtSchemaVersion,
		txtKeyPath + "=" + path,
		txtKeyTLS + "=" + txtFlag(opts.TLS),
		txtKeyAuth + "=" + txtFlag(opts.AuthRequired),
	}
	totalBytes := 0
	for _, record := range records {
		if len(record) > maxTXTRecordBytes {
			return nil, fmt.Errorf("discovery: TXT record %q exceeds %d bytes", record, maxTXTRecordBytes)
		}
		totalBytes += len(record) + 1 // +1 for the DNS character-string length prefix
	}
	if totalBytes > maxTXTBytes {
		return nil, fmt.Errorf("discovery: required TXT records exceed %d bytes", maxTXTBytes)
	}

	for _, tokens := range [][]string{opts.Protocols, opts.AuthMethods} {
		for _, token := range tokens {
			if !validTXTToken(token) {
				return nil, fmt.Errorf("discovery: invalid TXT token %q", token)
			}
		}
	}
	for _, value := range []string{opts.Product, opts.InstanceID, opts.NodeRole} {
		if !printableTXTASCII(value) {
			return nil, fmt.Errorf("discovery: optional TXT value must be printable ASCII")
		}
	}

	optional := []struct {
		key   string
		value string
	}{
		{txtKeyProtocols, joinTXTTokens(opts.Protocols)},
		{txtKeyAuthType, joinTXTTokens(opts.AuthMethods)},
		{txtKeyProduct, opts.Product},
		{txtKeyID, opts.InstanceID},
		{txtKeyRole, opts.NodeRole},
	}
	for _, item := range optional {
		if item.value == "" {
			continue
		}
		record := item.key + "=" + item.value
		cost := len(record) + 1
		if len(record) > maxTXTRecordBytes || totalBytes+cost > maxTXTBytes {
			continue
		}
		records = append(records, record)
		totalBytes += cost
	}
	return records, nil
}

func txtFlag(enabled bool) string {
	if enabled {
		return "1"
	}
	return "0"
}

// parseTXTFlag accepts the draft boolean encoding ("0" or "1") only.
func parseTXTFlag(value string) (bool, bool) {
	switch value {
	case "1":
		return true, true
	case "0":
		return false, true
	default:
		return false, false
	}
}

func joinTXTTokens(values []string) string {
	tokens := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		token := value
		if !validTXTToken(token) {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		tokens = append(tokens, token)
	}
	return strings.Join(tokens, ",")
}

func validTXTToken(token string) bool {
	if token == "" {
		return false
	}
	for i := 0; i < len(token); i++ {
		c := token[i]
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// ParseTXTRecords parses "key=value" strings into a map with lowercase keys.
// Per RFC 6763 §6.4, only the first occurrence of a key is used.
func ParseTXTRecords(txt []string) map[string]string {
	result := make(map[string]string, len(txt))
	for _, entry := range txt {
		key, value, _ := strings.Cut(entry, "=")
		if key == "" || !printableTXTASCII(key) {
			continue
		}
		key = strings.ToLower(key)
		if _, exists := result[key]; exists {
			continue
		}
		result[key] = value
	}
	return result
}

// validateMountPath returns p when it is a safe absolute path for the TXT path attribute.
// It rejects authorities, queries, fragments, escapes, traversal segments, and trailing slashes.
func validateMountPath(p string) string {
	if p == defaultMountPath {
		return p
	}
	if len(p) < 2 || p[0] != '/' || p[len(p)-1] == '/' {
		return ""
	}
	for i := 0; i < len(p); i++ {
		c := p[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || strings.ContainsRune("/-._~!$&'()*+,;=:@", rune(c))) {
			return ""
		}
	}
	for _, segment := range strings.Split(p[1:], "/") {
		if segment == "" || segment == "." || segment == ".." {
			return ""
		}
	}
	return p
}

// joinURLPath appends route below base without producing a double slash.
func joinURLPath(base, route string) string {
	if base == defaultMountPath {
		return route
	}
	return base + route
}

// applyTXTMetadata fills svc from parsed TXT attributes and reports whether the
// record is usable. Draft records (txtvers) are validated strictly; legacy CPA
// records (version + api_*) remain accepted for mixed-version networks.
func applyTXTMetadata(svc *DiscoveredService, parsed map[string]string) bool {
	// Clear schema-specific metadata if the destination was previously populated.
	svc.RawTXT = parsed
	svc.Product = parsed[txtKeyProduct]
	svc.InstanceID, svc.NodeRole, svc.Path = "", "", ""
	svc.Protocols, svc.AuthMethods = nil, nil
	svc.AuthRequired, svc.TLS = false, false
	svc.Endpoints = make(map[string]string)
	if version, draft := parsed[txtKeyVersion]; draft {
		svc.Version = version
		return applyDraftTXTMetadata(svc, parsed)
	}
	return applyLegacyTXTMetadata(svc, parsed)
}

func applyDraftTXTMetadata(svc *DiscoveredService, parsed map[string]string) bool {
	for _, key := range []string{txtKeyVersion, txtKeyPath, txtKeyTLS, txtKeyAuth,
		txtKeyProtocols, txtKeyAuthType, txtKeyProduct, txtKeyID, txtKeyRole} {
		if value, present := parsed[key]; present && (value == "" || !printableTXTASCII(value)) {
			return false
		}
	}
	tls, okTLS := parseTXTFlag(parsed[txtKeyTLS])
	auth, okAuth := parseTXTFlag(parsed[txtKeyAuth])
	path := validateMountPath(parsed[txtKeyPath])
	if svc.Version != txtSchemaVersion || !okTLS || !okAuth || path == "" {
		return false
	}
	for _, key := range []string{txtKeyProtocols, txtKeyAuthType} {
		if value, present := parsed[key]; present {
			for _, token := range strings.Split(value, ",") {
				if !validTXTToken(token) {
					return false
				}
			}
		}
	}
	svc.TLS, svc.AuthRequired, svc.Path = tls, auth, path
	svc.InstanceID, svc.NodeRole = parsed[txtKeyID], parsed[txtKeyRole]
	if value, present := parsed[txtKeyProtocols]; present {
		for _, token := range draftTXTList(value) {
			if _, known := protocolRoutes[token]; known {
				svc.Protocols = append(svc.Protocols, token)
			}
		}
	}
	if value, present := parsed[txtKeyAuthType]; present {
		for _, token := range draftTXTList(value) {
			if token == "api_key" || token == "oauth" {
				svc.AuthMethods = append(svc.AuthMethods, token)
			}
		}
	}
	for _, protocol := range svc.Protocols {
		if route, known := protocolRoutes[protocol]; known {
			svc.Endpoints[protocol] = joinURLPath(path, route.version+route.suffix)
		}
	}
	return true
}

// draftTXTList bounds retained hints without changing token spelling or syntax.
func draftTXTList(value string) []string {
	var result []string
	seen := make(map[string]bool)
	for _, token := range strings.Split(value, ",") {
		if !seen[token] {
			result = append(result, token)
			seen[token] = true
			if len(result) == maxDiscoveredMetadataItems {
				break
			}
		}
	}
	return result
}

func applyLegacyTXTMetadata(svc *DiscoveredService, parsed map[string]string) bool {
	svc.Version = parsed[legacyKeyVersion]
	tls, okTLS := parseTXTFlag(parsed[txtKeyTLS])
	if svc.Version != txtSchemaVersion || !okTLS {
		return false
	}
	svc.TLS = tls
	switch parsed[legacyKeyAuth] {
	case "true":
		svc.AuthRequired = true
	case "false":
		svc.AuthRequired = false
	default:
		return false
	}
	svc.InstanceID, svc.NodeRole = parsed[legacyKeyInstanceID], parsed[legacyKeyNodeRole]
	svc.Protocols = parseTXTList(parsed[txtKeyProtocols])
	svc.AuthMethods = parseTXTList(parsed[legacyKeyAuthMethods])
	if _, present := parsed[txtKeyProtocols]; !present {
		// A base path does not imply support for any particular operation.
		for _, key := range []string{legacyKeyOpenAI, legacyKeyAnthropic, legacyKeyGemini} {
			if base := validateMountPath(parsed[key]); base != "" {
				svc.Endpoints[key] = base
			}
		}
	}
	for _, protocol := range svc.Protocols {
		route, known := protocolRoutes[protocol]
		if !known {
			continue
		}
		if base := validateMountPath(parsed[route.legacyField]); base != "" {
			svc.Endpoints[protocol] = joinURLPath(base, route.suffix)
		}
	}
	return len(svc.Endpoints) > 0
}

// validateDraftTXTRecords enforces wire-size limits before parsing loses duplicate
// strings and their length prefixes. Call this for draft entries before conversion.
func validateDraftTXTRecords(records []string) bool {
	total := 0
	for _, record := range records {
		if len(record) > maxTXTRecordBytes {
			return false
		}
		total += len(record) + 1
		if total > maxTXTBytes {
			return false
		}
	}
	return true
}

// ProtocolFromSubtype converts a DNS-SD subtype label (e.g. "_responses") to its protocol token.
func ProtocolFromSubtype(subtype string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(subtype), "_"))
}
