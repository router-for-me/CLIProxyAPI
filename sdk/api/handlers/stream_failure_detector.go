package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

type streamFailureDetector struct {
	carry            []byte
	pendingEventType string
}

type streamPayloadFailure struct {
	statusCode int
	raw        string
	message    string
	code       string
}

func (e *streamPayloadFailure) Error() string {
	if e == nil {
		return ""
	}
	if strings.TrimSpace(e.raw) != "" {
		return strings.TrimSpace(e.raw)
	}
	if strings.TrimSpace(e.message) != "" {
		return strings.TrimSpace(e.message)
	}
	return http.StatusText(e.StatusCode())
}

func (e *streamPayloadFailure) StatusCode() int {
	if e == nil || e.statusCode <= 0 {
		return http.StatusBadGateway
	}
	return e.statusCode
}

func (d *streamFailureDetector) Observe(chunk []byte) error {
	if len(chunk) == 0 {
		return nil
	}

	buf := make([]byte, 0, len(d.carry)+len(chunk))
	buf = append(buf, d.carry...)
	buf = append(buf, chunk...)
	d.carry = nil

	lines := bytes.Split(buf, []byte("\n"))
	if len(lines) == 0 {
		return nil
	}

	endsWithNewline := len(buf) > 0 && buf[len(buf)-1] == '\n'
	limit := len(lines)
	if !endsWithNewline {
		limit--
	}

	for i := 0; i < limit; i++ {
		if err := d.observeLine(lines[i]); err != nil {
			return err
		}
	}

	if endsWithNewline {
		if len(lines) > 0 {
			d.pendingEventType = ""
		}
		return nil
	}

	tail := bytes.TrimRight(lines[len(lines)-1], "\r")
	if err := d.observeTail(tail); err != nil {
		return err
	}
	return nil
}

func (d *streamFailureDetector) observeLine(line []byte) error {
	trimmed := bytes.TrimSpace(bytes.TrimRight(line, "\r"))
	if len(trimmed) == 0 {
		d.pendingEventType = ""
		return nil
	}
	if bytes.HasPrefix(trimmed, []byte(":")) || bytes.HasPrefix(trimmed, []byte("id:")) || bytes.HasPrefix(trimmed, []byte("retry:")) {
		return nil
	}
	if bytes.HasPrefix(trimmed, []byte("event:")) {
		d.pendingEventType = strings.TrimSpace(string(bytes.TrimSpace(trimmed[len("event:"):])))
		return nil
	}
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		payload := bytes.TrimSpace(trimmed[len("data:"):])
		defer func() {
			if len(payload) > 0 {
				d.pendingEventType = ""
			}
		}()
		return detectStreamFailurePayload(payload, d.pendingEventType)
	}
	defer func() { d.pendingEventType = "" }()
	return detectStreamFailurePayload(trimmed, d.pendingEventType)
}

func (d *streamFailureDetector) observeTail(tail []byte) error {
	trimmed := bytes.TrimSpace(tail)
	if len(trimmed) == 0 {
		return nil
	}
	if bytes.HasPrefix(trimmed, []byte("event:")) {
		if err := d.observeLine(trimmed); err != nil {
			return err
		}
		return nil
	}
	if err := d.observeLine(trimmed); err != nil {
		return err
	}
	if shouldCarryStreamFailureTail(trimmed) {
		d.carry = bytes.Clone(tail)
	}
	return nil
}

func shouldCarryStreamFailureTail(line []byte) bool {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return false
	}
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		payload := bytes.TrimSpace(trimmed[len("data:"):])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) || json.Valid(payload) {
			return false
		}
		return true
	}
	if bytes.HasPrefix(trimmed, []byte("event:")) || bytes.HasPrefix(trimmed, []byte(":")) || bytes.HasPrefix(trimmed, []byte("id:")) || bytes.HasPrefix(trimmed, []byte("retry:")) {
		return false
	}
	return !json.Valid(trimmed)
}

func detectStreamFailurePayload(payload []byte, eventType string) error {
	payload = bytes.TrimSpace(payload)
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) || !json.Valid(payload) {
		return nil
	}

	root := gjson.ParseBytes(payload)
	normalizedEventType := strings.TrimSpace(eventType)
	typeValue := strings.TrimSpace(root.Get("type").String())
	errorNode := root.Get("error")
	responseErrorNode := root.Get("response.error")

	failed := normalizedEventType == "error" || typeValue == "error" || typeValue == "response.failed"
	if !failed && errorNode.Exists() && errorNode.Type != gjson.Null {
		failed = true
	}
	if !failed && responseErrorNode.Exists() && responseErrorNode.Type != gjson.Null {
		failed = true
	}
	if !failed {
		return nil
	}

	message := strings.TrimSpace(errorNode.Get("message").String())
	if message == "" {
		message = strings.TrimSpace(responseErrorNode.Get("message").String())
	}
	if message == "" {
		message = strings.TrimSpace(root.Get("message").String())
	}
	code := strings.TrimSpace(errorNode.Get("code").String())
	if code == "" {
		code = strings.TrimSpace(responseErrorNode.Get("code").String())
	}

	return &streamPayloadFailure{
		statusCode: http.StatusBadGateway,
		raw:        string(payload),
		message:    message,
		code:       code,
	}
}
