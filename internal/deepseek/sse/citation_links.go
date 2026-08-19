package sse

import (
	"strconv"
	"strings"
)

type citationLinkCollector struct {
	ordered     []string
	explicitRaw map[int]string
	hasZeroIdx  bool
}

func newCitationLinkCollector() *citationLinkCollector {
	return &citationLinkCollector{
		explicitRaw: map[int]string{},
	}
}

func (c *citationLinkCollector) ingestChunk(chunk map[string]any) {
	if c == nil || len(chunk) == 0 {
		return
	}
	c.walkValue(chunk)
}

func (c *citationLinkCollector) build() map[int]string {
	out := make(map[int]string, len(c.explicitRaw)+len(c.ordered))
	for idx, u := range c.buildNormalizedExplicit() {
		out[idx] = u
	}
	for i, u := range c.ordered {
		idx := i + 1
		if _, exists := out[idx]; !exists {
			out[idx] = u
		}
	}
	return out
}

func (c *citationLinkCollector) buildNormalizedExplicit() map[int]string {
	out := make(map[int]string, len(c.explicitRaw))

	// Default behavior keeps positive indices as-is (one-based payloads).
	for idx, u := range c.explicitRaw {
		if idx <= 0 || strings.TrimSpace(u) == "" {
			continue
		}
		out[idx] = u
	}

	if !c.hasZeroIdx {
		return out
	}

	// If zero index appears, upstream may be using zero-based indices.
	// Add shifted candidates and resolve conflicts using ordered appearance,
	// which matches visible citation marker order in response text.
	for rawIdx, u := range c.explicitRaw {
		if rawIdx < 0 || strings.TrimSpace(u) == "" {
			continue
		}
		normalized := rawIdx + 1
		existing, exists := out[normalized]
		if !exists {
			out[normalized] = u
			continue
		}
		if c.preferURLForIndex(normalized, existing, u) == u {
			out[normalized] = u
		}
	}

	return out
}

func (c *citationLinkCollector) preferURLForIndex(idx int, current, candidate string) string {
	if idx <= 0 || idx > len(c.ordered) {
		return current
	}
	expected := c.ordered[idx-1]
	switch {
	case strings.TrimSpace(expected) == "":
		return current
	case candidate == expected && current != expected:
		return candidate
	default:
		return current
	}
}

func (c *citationLinkCollector) walkValue(v any) {
	switch x := v.(type) {
	case []any:
		for _, item := range x {
			c.walkValue(item)
		}
	case map[string]any:
		c.captureURLAndIndex(x)
		for _, vv := range x {
			c.walkValue(vv)
		}
	}
}

func (c *citationLinkCollector) captureURLAndIndex(m map[string]any) {
	url := strings.TrimSpace(asString(m["url"]))
	if !isWebURL(url) {
		return
	}
	c.addOrdered(url)

	idx, hasIdx := citationIndexFromAny(m["cite_index"])
	if !hasIdx {
		return
	}
	if idx < 0 {
		return
	}
	if idx == 0 {
		c.hasZeroIdx = true
	}
	if existing, ok := c.explicitRaw[idx]; ok && strings.TrimSpace(existing) != "" {
		return
	}
	c.explicitRaw[idx] = url
}

func (c *citationLinkCollector) addOrdered(url string) {
	c.ordered = append(c.ordered, url)
}

func citationIndexFromAny(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int32:
		return int(x), true
	case int64:
		return int(x), true
	case float32:
		return int(x), true
	case float64:
		return int(x), true
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return 0, false
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

func isWebURL(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	return strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://")
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}
