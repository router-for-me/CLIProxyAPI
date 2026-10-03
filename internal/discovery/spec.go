package discovery

import (
	"fmt"
	"strings"
)

const (
	maxTXTRecordBytes = 255 // RFC 6763 §6.3 single string limit
	maxTXTBytes       = 400 // Conservative LAN UDP packet safety boundary
)

func sanitizeTXTValue(v string) string {
	var b strings.Builder
	for _, r := range v {
		if r >= 32 && r < 127 {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// TXTOptions contains input fields for generating safe DNS-SD TXT records.
type TXTOptions struct {
	Version             string
	Product             string
	Protocols           []string
	Features            []string
	APIPathOpenAI       string
	APIPathAnthropic    string
	APIPathGemini       string
	TLS                 bool
	AuthRequired        bool
	AuthMethods         []string
	InstanceID          string
	NodeRole            string
	AdvertiseManagement bool
}

// DefaultTXTOptions provides safe defaults for CPA TXT records.
func DefaultTXTOptions() TXTOptions {
	return TXTOptions{
		Version:          "1",
		Product:          ProductCPA,
		Protocols:        []string{"chat-completions", "responses", "messages", "generate-content", "interactions"},
		Features:         []string{"chat", "responses", "messages", "generate_content", "interactions"},
		APIPathOpenAI:    "/v1",
		APIPathAnthropic: "/v1",
		APIPathGemini:    "/v1beta",
		TLS:              false,
		AuthRequired:     true,
		AuthMethods:      []string{"api_key"},
		NodeRole:         "standalone",
	}
}

// BuildTXTRecords constructs key=value TXT records strictly within 400 bytes total
// and 255 bytes per record (RFC 6763 §6.3), prioritizing essential routing metadata.
func BuildTXTRecords(opts TXTOptions) []string {
	if opts.Version == "" {
		opts.Version = "1"
	}
	if opts.Product == "" {
		opts.Product = ProductCPA
	}
	if opts.APIPathOpenAI == "" {
		opts.APIPathOpenAI = "/v1"
	}
	if opts.APIPathAnthropic == "" {
		opts.APIPathAnthropic = "/v1"
	}
	if opts.APIPathGemini == "" {
		opts.APIPathGemini = "/v1beta"
	}

	type kv struct {
		key      string
		val      string
		priority int // 1: core, 2: standard, 3: optional
	}

	var candidates []kv
	addCandidate := func(key, val string, priority int) {
		val = sanitizeTXTValue(strings.TrimSpace(val))
		if val != "" {
			candidates = append(candidates, kv{key: key, val: val, priority: priority})
		}
	}

	// Priority 1: Core metadata
	addCandidate("version", opts.Version, 1)
	addCandidate("product", opts.Product, 1)
	addCandidate("instance_id", opts.InstanceID, 1)
	if opts.TLS {
		addCandidate("tls", "1", 1)
	} else {
		addCandidate("tls", "0", 1)
	}
	if opts.AuthRequired {
		addCandidate("auth_required", "true", 1)
	} else {
		addCandidate("auth_required", "false", 1)
	}
	addCandidate("api_openai", opts.APIPathOpenAI, 1)

	// Priority 2: Standard endpoints and protocols
	addCandidate("api_anthropic", opts.APIPathAnthropic, 2)
	addCandidate("api_gemini", opts.APIPathGemini, 2)
	if opts.AdvertiseManagement {
		addCandidate("management", "true", 2)
	} else {
		addCandidate("management", "false", 2)
	}
	if len(opts.AuthMethods) > 0 {
		addCandidate("auth_methods", strings.Join(opts.AuthMethods, ","), 2)
	}
	if len(opts.Protocols) > 0 {
		addCandidate("protocols", strings.Join(opts.Protocols, ","), 2)
	}

	// Priority 3: Optional discovery hints
	if opts.NodeRole != "" {
		addCandidate("node_role", opts.NodeRole, 3)
	}
	if len(opts.Features) > 0 {
		addCandidate("features", strings.Join(opts.Features, ","), 3)
	}

	// Assemble records respecting single-string (255) and total length (400) limits
	var records []string
	totalBytes := 0

	for _, item := range candidates {
		entry := fmt.Sprintf("%s=%s", item.key, item.val)
		if len(entry) > maxTXTRecordBytes {
			continue
		}
		entryCost := len(entry) + 1 // +1 for DNS TXT length prefix byte
		if totalBytes+entryCost > maxTXTBytes {
			continue
		}

		records = append(records, entry)
		totalBytes += entryCost
	}

	return records
}

// ParseTXTRecords parses a slice of "key=value" strings into a key-value map.
func ParseTXTRecords(txt []string) map[string]string {
	result := make(map[string]string, len(txt))
	for _, entry := range txt {
		parts := strings.SplitN(entry, "=", 2)
		key := strings.ToLower(strings.TrimSpace(parts[0]))
		if key == "" {
			continue
		}
		if len(parts) == 2 {
			result[key] = parts[1]
		} else if len(parts) == 1 {
			result[key] = ""
		}
	}
	return result
}
