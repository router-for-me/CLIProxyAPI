package cachex

import "strings"

// Namespace isolates keys between different cache use-cases. (e.g. "channel_affinity:v1").
type Namespace string

func (n Namespace) prefix() string {
	ns := strings.TrimSpace(string(n))
	ns = strings.TrimRight(ns, ":")
	if ns == "" {
		return ""
	}
	return ns + ":"
}

func (n Namespace) FullKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	p := n.prefix()
	if p == "" {
		return strings.TrimLeft(key, ":")
	}
	if strings.HasPrefix(key, p) {
		return key
	}
	return p + strings.TrimLeft(key, ":")
}

func (n Namespace) MatchPattern() string {
	p := n.prefix()
	if p == "" {
		return "*"
	}
	return p + "*"
}
