package common

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"strings"
)

type digestAnchor struct{}

func modulePath() string {
	return reflect.TypeFor[digestAnchor]().PkgPath()
}

var digestSeed = func() (s [sha256.Size]byte) {
	return sha256.Sum256([]byte(modulePath()))
}()

// ETagFor returns a weak ETag derived from the namespace and content.
func ETagFor(namespace, content string) string {
	buf := make([]byte, 0, sha256.Size+1+len(namespace)+1+len(content))
	buf = append(buf, digestSeed[:]...)
	buf = append(buf, 0)
	buf = append(buf, namespace...)
	buf = append(buf, 0)
	buf = append(buf, content...)
	digest := sha256.Sum256(buf)
	return `W/"` + hex.EncodeToString(digest[:]) + `"`
}

// ETagMatches reports whether an If-None-Match header matches etag under weak
// comparison (RFC 9110 §13.1.2): the W/ prefix is ignored on both sides, and
// "*" matches everything.
func ETagMatches(ifNoneMatch, etag string) bool {
	ifNoneMatch = strings.TrimSpace(ifNoneMatch)
	if ifNoneMatch == "" {
		return false
	}
	if ifNoneMatch == "*" {
		return true
	}
	etag = strings.TrimPrefix(etag, "W/")
	for candidate := range strings.SplitSeq(ifNoneMatch, ",") {
		if strings.TrimPrefix(strings.TrimSpace(candidate), "W/") == etag {
			return true
		}
	}
	return false
}
