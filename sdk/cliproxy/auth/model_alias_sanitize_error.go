package auth

import (
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
)

type modelAliasSanitizedError struct {
	err       error
	sanitized string
}

func (e *modelAliasSanitizedError) Error() string { return e.sanitized }
func (e *modelAliasSanitizedError) Unwrap() error { return e.err }

func (e *modelAliasSanitizedError) StatusCode() int {
	type statusCoder interface{ StatusCode() int }
	if sc, ok := e.err.(statusCoder); ok && sc != nil {
		return sc.StatusCode()
	}
	return 0
}

func (e *modelAliasSanitizedError) Headers() http.Header {
	type headerProvider interface{ Headers() http.Header }
	if hp, ok := e.err.(headerProvider); ok && hp != nil {
		return hp.Headers()
	}
	return nil
}

func (e *modelAliasSanitizedError) RetryAfter() *time.Duration {
	type retryAfterProvider interface{ RetryAfter() *time.Duration }
	if rap, ok := e.err.(retryAfterProvider); ok && rap != nil {
		return rap.RetryAfter()
	}
	return nil
}

func sanitizeModelLeakMessage(message, requestedModel, upstreamModel string) string {
	message = redactURLs(message)

	requestedModel = strings.TrimSpace(requestedModel)
	upstreamModel = strings.TrimSpace(upstreamModel)
	if requestedModel == "" || upstreamModel == "" || requestedModel == upstreamModel {
		return message
	}

	message = strings.ReplaceAll(message, upstreamModel, requestedModel)

	upstreamBase := strings.TrimSpace(thinking.ParseSuffix(upstreamModel).ModelName)
	if upstreamBase != "" && upstreamBase != upstreamModel {
		message = strings.ReplaceAll(message, upstreamBase, requestedModel)
	}
	return message
}

func isURLTerminator(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', '"', '\'', ')', ']', '}', ',', ';', '<', '>':
		return true
	default:
		return false
	}
}

func consumeBackslashes(s string, i int) int {
	for i < len(s) && s[i] == '\\' {
		i++
	}
	return i
}

func urlBodyStart(s string, schemeStart, schemeLen int) (int, bool) {
	after := schemeStart + schemeLen
	if after >= len(s) {
		return 0, false
	}

	if after+1 < len(s) && s[after] == '/' && s[after+1] == '/' {
		return after + 2, true
	}

	k := after
	k = consumeBackslashes(s, k)
	if k >= len(s) || s[k] != '/' {
		return 0, false
	}
	k++
	k = consumeBackslashes(s, k)
	if k >= len(s) || s[k] != '/' {
		return 0, false
	}
	k++
	return k, true
}

func redactURLs(message string) string {
	if message == "" {
		return message
	}

	lower := strings.ToLower(message)
	if !strings.Contains(lower, "http:") && !strings.Contains(lower, "https:") {
		return message
	}

	var b strings.Builder
	b.Grow(len(message))

	i := 0
	for {
		next := -1
		schemeLen := 0

		if idx := strings.Index(lower[i:], "https:"); idx >= 0 {
			next = i + idx
			schemeLen = len("https:")
		}
		if idx := strings.Index(lower[i:], "http:"); idx >= 0 {
			abs := i + idx
			if next == -1 || abs < next {
				next = abs
				schemeLen = len("http:")
			}
		}

		if next == -1 {
			break
		}

		bodyStart, ok := urlBodyStart(message, next, schemeLen)
		if !ok {
			i = next + schemeLen
			continue
		}

		if next > i {
			b.WriteString(message[i:next])
		}

		j := bodyStart
		for j < len(message) && !isURLTerminator(message[j]) {
			j++
		}

		b.WriteString("<redacted_url>")
		i = j
	}
	if i < len(message) {
		b.WriteString(message[i:])
	}
	return b.String()
}

func sanitizeModelLeakError(err error, requestedModel, upstreamModel string) error {
	if err == nil {
		return nil
	}
	if _, ok := err.(*modelAliasSanitizedError); ok {
		return err
	}

	raw := err.Error()
	if raw == "" {
		return err
	}

	sanitized := sanitizeModelLeakMessage(raw, requestedModel, upstreamModel)
	if sanitized == raw {
		return err
	}
	return &modelAliasSanitizedError{err: err, sanitized: sanitized}
}
