// Package signaturetest provides synthetic envelopes for signature regression tests.
package signaturetest

import (
	"encoding/base64"

	"google.golang.org/protobuf/encoding/protowire"
)

// AntigravityCAQS builds a double-base64 Google Claude thinking envelope.
// All opaque fields are generated zeros, not captured or replayable state.
func AntigravityCAQS() string {
	appendVarint := func(dst []byte, field protowire.Number, value uint64) []byte {
		return protowire.AppendVarint(protowire.AppendTag(dst, field, protowire.VarintType), value)
	}
	appendBytes := func(dst []byte, field protowire.Number, value []byte) []byte {
		return protowire.AppendBytes(protowire.AppendTag(dst, field, protowire.BytesType), value)
	}
	var channel []byte
	channel = appendVarint(channel, 1, 18)
	channel = appendVarint(channel, 2, 2)
	channel = appendVarint(channel, 3, 2)
	channel = appendVarint(channel, 7, 1)
	channel = appendBytes(channel, 8, []byte("thinking"))
	container := appendBytes(nil, 1, channel)
	container = appendBytes(container, 2, make([]byte, 12))
	container = appendBytes(container, 3, make([]byte, 12))
	container = appendBytes(container, 4, make([]byte, 48))
	container = appendBytes(container, 5, make([]byte, 1020))
	payload := appendVarint(nil, 1, 4)
	payload = appendBytes(payload, 2, container)
	payload = appendVarint(payload, 3, 1)
	inner := base64.StdEncoding.EncodeToString(payload)
	return base64.StdEncoding.EncodeToString([]byte(inner))
}
