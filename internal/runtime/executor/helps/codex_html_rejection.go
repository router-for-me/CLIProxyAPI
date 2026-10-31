package helps

import (
	"bytes"
	"net/http"
)

// IsCodexHTMLForbidden identifies an edge HTML page, not a structured API
// permission error. A transient network rejection must not disable OAuth for
// the ordinary thirty-minute forbidden-credential cooldown.
func IsCodexHTMLForbidden(status int, body []byte) bool {
	if status != http.StatusForbidden {
		return false
	}
	prefix := bytes.TrimSpace(bytes.TrimPrefix(body, []byte("\xef\xbb\xbf")))
	if len(prefix) > 64 {
		prefix = prefix[:64]
	}
	prefix = bytes.ToLower(prefix)
	for _, tag := range [][]byte{[]byte("<html"), []byte("<!doctype html")} {
		if bytes.HasPrefix(prefix, tag) && len(prefix) > len(tag) {
			switch prefix[len(tag)] {
			case '>', ' ', '\t', '\r', '\n':
				return true
			}
		}
	}
	return false
}
