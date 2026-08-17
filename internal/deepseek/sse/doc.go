// Package sse parses and normalises the Server-Sent Events stream emitted by
// DeepSeek's chat completion endpoint, including content-filter leak patching,
// citation link extraction and finish-state detection.
package sse
