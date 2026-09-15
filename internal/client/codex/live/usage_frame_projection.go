package live

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strconv"
)

const usageProjectionBudget = 256 << 10
const usageProjectionDepth = 256

// projectUsageFrame validates the entire JSON frame while retaining only fields
// used by the usage observer. Skipped strings, numbers and containers are scanned
// byte by byte, so a large transcript or media item never becomes a JSON token
// allocation. Relevant metadata has a separate, explicit size limit.
func projectUsageFrame(reader io.Reader) ([]byte, error) {
	p := &usageProjectionParser{reader: bufio.NewReaderSize(reader, 16<<10), remaining: usageProjectionBudget}
	projected, err := p.object("", 0)
	if err != nil {
		return nil, err
	}
	if err = p.space(); err != nil {
		return nil, err
	}
	if _, err = p.peek(); err != io.EOF {
		if err == nil {
			err = errors.New("usage projection: trailing JSON data")
		}
		return nil, err
	}
	return projected, nil
}

type usageProjectionParser struct {
	reader    *bufio.Reader
	remaining int
	capturing bool
	capture   []byte
	readErr   error
}

func (p *usageProjectionParser) peek() (byte, error) {
	if p.readErr != nil {
		return 0, p.readErr
	}
	b, err := p.reader.Peek(1)
	if err != nil {
		p.readErr = err
		return 0, err
	}
	return b[0], nil
}

func (p *usageProjectionParser) take() (byte, error) {
	if p.readErr != nil {
		return 0, p.readErr
	}
	b, err := p.reader.ReadByte()
	if err != nil {
		p.readErr = err
		return 0, err
	}
	if p.capturing {
		if p.remaining == 0 {
			return 0, errors.New("usage projection: accounting metadata exceeds size limit")
		}
		p.remaining--
		p.capture = append(p.capture, b)
	}
	return b, nil
}

func (p *usageProjectionParser) expect(want byte) error {
	got, err := p.take()
	if err != nil {
		return err
	}
	if got != want {
		return errors.New("usage projection: invalid JSON delimiter")
	}
	return nil
}

func (p *usageProjectionParser) space() error {
	for {
		b, err := p.peek()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if b != ' ' && b != '\n' && b != '\r' && b != '\t' {
			return nil
		}
		if _, err = p.take(); err != nil {
			return err
		}
	}
}

// stringToken retains a bounded key/type token, or no bytes when limit is zero.
// An oversized skipped key cannot match the short accounting field names.
func (p *usageProjectionParser) stringToken(limit int) ([]byte, error) {
	if err := p.expect('"'); err != nil {
		return nil, err
	}
	var token []byte
	if limit > 0 {
		token = append(token, '"')
	}
	appendToken := func(b byte) {
		if limit > 0 {
			if len(token) == limit {
				limit, token = 0, nil
			} else {
				token = append(token, b)
			}
		}
	}
	for {
		b, err := p.take()
		if err != nil {
			return nil, err
		}
		appendToken(b)
		if b == '"' {
			return token, nil
		}
		if b < 0x20 {
			return nil, errors.New("usage projection: control byte in JSON string")
		}
		if b != '\\' {
			continue
		}
		b, err = p.take()
		if err != nil {
			return nil, err
		}
		appendToken(b)
		switch b {
		case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
		case 'u':
			for range 4 {
				b, err = p.take()
				if err != nil {
					return nil, err
				}
				appendToken(b)
				if !(b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F') {
					return nil, errors.New("usage projection: invalid JSON unicode escape")
				}
			}
		default:
			return nil, errors.New("usage projection: invalid JSON escape")
		}
	}
}

func (p *usageProjectionParser) number() error {
	b, _ := p.peek()
	if b == '-' {
		if _, err := p.take(); err != nil {
			return err
		}
		b, _ = p.peek()
	}
	if b == '0' {
		if _, err := p.take(); err != nil {
			return err
		}
	} else if b >= '1' && b <= '9' {
		if err := p.digits(); err != nil {
			return err
		}
	} else {
		return errors.New("usage projection: invalid JSON number")
	}
	b, _ = p.peek()
	if b == '.' {
		if _, err := p.take(); err != nil {
			return err
		}
		if err := p.digits(); err != nil {
			return err
		}
	}
	b, _ = p.peek()
	if b == 'e' || b == 'E' {
		if _, err := p.take(); err != nil {
			return err
		}
		b, _ = p.peek()
		if b == '+' || b == '-' {
			if _, err := p.take(); err != nil {
				return err
			}
		}
		return p.digits()
	}
	return nil
}

func (p *usageProjectionParser) digits() error {
	seen := false
	for {
		b, err := p.peek()
		if err != nil && err != io.EOF {
			return err
		}
		if err == io.EOF || b < '0' || b > '9' {
			if !seen {
				return errors.New("usage projection: missing JSON number digits")
			}
			return nil
		}
		if _, err = p.take(); err != nil {
			return err
		}
		seen = true
	}
}

func (p *usageProjectionParser) value(depth int) error {
	if depth > usageProjectionDepth {
		return errors.New("usage projection: JSON nesting exceeds limit")
	}
	if err := p.space(); err != nil {
		return err
	}
	b, err := p.peek()
	if err != nil {
		return err
	}
	switch b {
	case '"':
		_, err = p.stringToken(0)
		return err
	case '{':
		return p.members(func(string) error { return p.value(depth + 1) }, false)
	case '[':
		return p.elements(func() error { return p.value(depth + 1) })
	case 't', 'f', 'n':
		literal := map[byte]string{'t': "true", 'f': "false", 'n': "null"}[b]
		for i := range literal {
			if err := p.expect(literal[i]); err != nil {
				return err
			}
		}
		return nil
	default:
		return p.number()
	}
}

func (p *usageProjectionParser) members(visit func(string) error, keys bool) error {
	if err := p.expect('{'); err != nil {
		return err
	}
	return p.sequence('}', func() error {
		limit := 0
		if keys {
			limit = 256
		}
		raw, err := p.stringToken(limit)
		if err != nil {
			return err
		}
		var key string
		if len(raw) > 0 {
			if err = json.Unmarshal(raw, &key); err != nil {
				return err
			}
		}
		if err = p.space(); err != nil {
			return err
		}
		if err = p.expect(':'); err != nil {
			return err
		}
		if err = p.space(); err != nil {
			return err
		}
		return visit(key)
	})
}

func (p *usageProjectionParser) elements(visit func() error) error {
	if err := p.expect('['); err != nil {
		return err
	}
	return p.sequence(']', visit)
}

func (p *usageProjectionParser) sequence(end byte, visit func() error) error {
	if err := p.space(); err != nil {
		return err
	}
	if b, _ := p.peek(); b == end {
		return p.expect(end)
	}
	for {
		if err := visit(); err != nil {
			return err
		}
		if err := p.space(); err != nil {
			return err
		}
		b, err := p.take()
		if err != nil {
			return err
		}
		if b == end {
			return nil
		}
		if b != ',' {
			return errors.New("usage projection: invalid JSON separator")
		}
		if err = p.space(); err != nil {
			return err
		}
	}
}

func (p *usageProjectionParser) raw(depth int) (json.RawMessage, error) {
	p.capturing, p.capture = true, nil
	err := p.value(depth)
	raw := p.capture
	p.capturing, p.capture = false, nil
	return raw, err
}

func usageProjectionField(path, key string) (keep, object bool) {
	switch path {
	case "":
		switch key {
		case "type", "item_id", "content_index", "usage", "service_tier", "tool_usage":
			return true, false
		case "response", "session":
			return true, true
		}
	case "response":
		switch key {
		case "id", "model", "status", "service_tier", "usage", "tool_usage":
			return true, false
		}
	case "session":
		if key == "model" {
			return true, false
		}
		if key == "audio" || key == "input_audio_transcription" {
			return true, true
		}
	case "session.audio":
		return key == "input", true
	case "session.audio.input":
		return key == "transcription", true
	case "session.input_audio_transcription", "session.audio.input.transcription":
		return key == "model", false
	}
	return false, false
}

func (p *usageProjectionParser) object(path string, depth int) ([]byte, error) {
	if err := p.space(); err != nil {
		return nil, err
	}
	if b, _ := p.peek(); b != '{' {
		if path == "" {
			return nil, errors.New("usage projection: frame must be a JSON object")
		}
		return p.raw(depth)
	}
	fields := make(map[string]json.RawMessage)
	var counts usageProjectionTools
	err := p.members(func(key string) error {
		if key == "output" && (path == "response" || path == "") {
			counts = usageProjectionTools{}
			return p.tools(&counts, depth+1)
		}
		keep, nested := usageProjectionField(path, key)
		if !keep {
			return p.value(depth + 1)
		}
		var raw []byte
		var err error
		if nested {
			child := key
			if path != "" {
				child = path + "." + key
			}
			raw, err = p.object(child, depth+1)
		} else {
			raw, err = p.raw(depth + 1)
		}
		if err == nil {
			fields[key] = raw
		}
		return err
	}, true)
	if err != nil {
		return nil, err
	}
	if err = counts.apply(fields); err != nil {
		return nil, err
	}
	return json.Marshal(fields)
}

type usageProjectionTools struct {
	web, file int64
	unpriced  bool
}

func (p *usageProjectionParser) tools(counts *usageProjectionTools, depth int) error {
	if b, _ := p.peek(); b != '[' {
		return p.value(depth)
	}
	return p.elements(func() error {
		if b, _ := p.peek(); b != '{' {
			return p.value(depth + 1)
		}
		var kind string
		if err := p.members(func(key string) error {
			if key == "type" {
				kind = ""
			}
			if b, _ := p.peek(); key != "type" || b != '"' {
				return p.value(depth + 2)
			}
			raw, err := p.stringToken(256)
			if err == nil && len(raw) > 0 {
				err = json.Unmarshal(raw, &kind)
			}
			return err
		}, true); err != nil {
			return err
		}
		switch kind {
		case "web_search_call":
			counts.web++
		case "file_search_call":
			counts.file++
		case "code_interpreter_call", "shell_call":
			counts.unpriced = true
		}
		return nil
	})
}

func (c usageProjectionTools) apply(fields map[string]json.RawMessage) error {
	if c.web == 0 && c.file == 0 && !c.unpriced {
		return nil
	}
	// Never invent a usage object: without measured usage, retain billing-only
	// counts in tool_usage so consumers cannot mistake them for measured zero.
	key := "usage"
	var target map[string]json.RawMessage
	if json.Unmarshal(fields[key], &target) != nil || target == nil {
		key = "tool_usage"
		if len(fields[key]) > 0 && string(fields[key]) != "null" {
			if err := json.Unmarshal(fields[key], &target); err != nil {
				return errors.New("usage projection: tool_usage must be an object")
			}
		}
	}
	if target == nil {
		target = make(map[string]json.RawMessage)
	}
	for key, n := range map[string]int64{"web_search_calls": c.web, "file_search_calls": c.file} {
		if n == 0 {
			continue
		}
		// Compare without rewriting the provider's numeric representation.
		previous, _ := strconv.ParseFloat(string(target[key]), 64)
		if previous < float64(n) {
			target[key] = json.RawMessage(strconv.FormatInt(n, 10))
		}
	}
	if c.unpriced {
		target["unpriced_server_tools"] = json.RawMessage("true")
	}
	encoded, err := json.Marshal(target)
	fields[key] = encoded
	return err
}
