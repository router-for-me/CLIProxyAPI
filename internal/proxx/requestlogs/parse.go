// Package requestlogs indexes, searches and parses the per-request log files written by the
// request logger (internal/logging) when request-log is enabled, joins them with proxx usage
// records, enforces size-based retention and feeds the management request log viewer (ADR 0006).
package requestlogs

import (
	"bufio"
	"bytes"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// BodyLimit is the per-field cap for bodies returned by the detail endpoint (2 MB).
const BodyLimit = 2 << 20

// maxLine caps how much of a single line is kept while parsing (single-line JSON bodies).
const maxLine = 4 << 20

// indexBodyLimit caps the request/response body kept for model and usage extraction while indexing.
const indexBodyLimit = 4 << 20

type sectionKind int

// Section kinds in the order the request logger writes them.
const (
	secNone sectionKind = iota
	secInfo
	secHeaders
	secReqBody
	secWS
	secAPIWS
	secAPIReq
	secAPIErr
	secAPIResp
	secResp
)

var sectionNames = map[string]sectionKind{
	"REQUEST INFO":           secInfo,
	"HEADERS":                secHeaders,
	"REQUEST BODY":           secReqBody,
	"WEBSOCKET TIMELINE":     secWS,
	"API WEBSOCKET TIMELINE": secAPIWS,
	"API REQUEST":            secAPIReq,
	"API ERROR RESPONSE":     secAPIErr,
	"API RESPONSE":           secAPIResp,
	"RESPONSE":               secResp,
}

var sectionHeaderRe = regexp.MustCompile(`^=== ([A-Z ]+?)(?: (\d+))? ===$`)

// parseSectionHeader recognizes a known "=== NAME [n] ===" line.
func parseSectionHeader(line []byte) (sectionKind, int, bool) {
	if !bytes.HasPrefix(line, []byte("=== ")) || !bytes.HasSuffix(line, []byte(" ===")) {
		return secNone, 0, false
	}
	m := sectionHeaderRe.FindSubmatch(line)
	if m == nil {
		return secNone, 0, false
	}
	kind, ok := sectionNames[string(m[1])]
	if !ok {
		return secNone, 0, false
	}
	index := 0
	if len(m[2]) > 0 {
		index, _ = strconv.Atoi(string(m[2]))
	}
	return kind, index, true
}

// lineReader reads lines of any length, keeping at most max bytes of each.
type lineReader struct {
	r   *bufio.Reader
	max int
}

func (lr *lineReader) next() (line []byte, cut bool, err error) {
	var buf []byte
	read := false
	for {
		frag, errRead := lr.r.ReadSlice('\n')
		if len(frag) > 0 {
			read = true
		}
		room := lr.max - len(buf)
		switch {
		case len(frag) <= room:
			buf = append(buf, frag...)
		case room > 0:
			buf = append(buf, frag[:room]...)
			cut = true
		default:
			cut = true
		}
		if errRead == bufio.ErrBufferFull {
			continue
		}
		if errRead != nil && !read {
			return nil, false, errRead
		}
		break
	}
	buf = bytes.TrimSuffix(buf, []byte("\n"))
	buf = bytes.TrimSuffix(buf, []byte("\r"))
	return buf, cut, nil
}

// capBuf joins lines up to a byte limit and remembers whether anything was dropped.
type capBuf struct {
	limit     int
	b         []byte
	lines     int
	truncated bool
}

func (c *capBuf) write(p []byte) {
	room := c.limit - len(c.b)
	if len(p) <= room {
		c.b = append(c.b, p...)
		return
	}
	if room > 0 {
		c.b = append(c.b, p[:room]...)
	}
	c.truncated = true
}

func (c *capBuf) addLine(line []byte, cut bool) {
	if c.lines > 0 {
		c.write([]byte{'\n'})
	}
	c.write(line)
	if cut {
		c.truncated = true
	}
	c.lines++
}

func (c *capBuf) String() string {
	return strings.TrimRight(string(c.b), "\n")
}

// headerList keeps "Key: value" lines as a multi-map.
type headerList map[string][]string

func (h headerList) add(line string) {
	k, v, ok := strings.Cut(line, ":")
	if !ok || strings.TrimSpace(k) == "" {
		return
	}
	k = strings.TrimSpace(k)
	h[k] = append(h[k], strings.TrimSpace(v))
}

func (h headerList) get(key string) string {
	for k, v := range h {
		if strings.EqualFold(k, key) && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

func kv(line string) (string, string) {
	k, v, _ := strings.Cut(line, ":")
	return strings.TrimSpace(k), strings.TrimSpace(v)
}

// attemptRequest is one "=== API REQUEST n ===" section.
type attemptRequest struct {
	index     int
	timestamp string
	url       string
	method    string
	auth      string
	headers   headerList
	body      capBuf
	phase     int
}

func (a *attemptRequest) line(l []byte, cut bool) {
	s := string(l)
	switch a.phase {
	case 0:
		switch s {
		case "Headers:":
			a.phase = 1
			return
		case "Body:":
			a.phase = 3
			return
		}
		k, v := kv(s)
		switch k {
		case "Timestamp":
			a.timestamp = v
		case "Upstream URL":
			a.url = v
		case "HTTP Method":
			a.method = v
		case "Auth":
			a.auth = v
		}
	case 1:
		switch s {
		case "":
			a.phase = 2
		case "Body:":
			a.phase = 3
		default:
			a.headers.add(s)
		}
	case 2:
		if s == "Body:" {
			a.phase = 3
		}
	default:
		a.body.addLine(l, cut)
	}
}

// attemptResponse is one "=== API RESPONSE [n] ===" section.
type attemptResponse struct {
	index     int
	timestamp string
	status    int
	headers   headerList
	body      capBuf
	errors    []string
	phase     int
}

func (a *attemptResponse) line(l []byte, cut bool) {
	s := string(l)
	switch a.phase {
	case 0, 2:
		switch {
		case s == "":
			return
		case s == "Headers:" && a.phase == 0:
			a.phase = 1
			return
		case s == "Body:":
			a.phase = 3
			return
		case strings.HasPrefix(s, "Error: "):
			a.errors = append(a.errors, strings.TrimPrefix(s, "Error: "))
			return
		}
		k, v := kv(s)
		if a.phase == 0 {
			switch k {
			case "Timestamp":
				if a.timestamp == "" {
					a.timestamp = v
					return
				}
			case "Status":
				if n, err := strconv.Atoi(v); err == nil {
					a.status = n
					return
				}
			}
		}
		// Unstructured section (the unnumbered error-path API RESPONSE): the rest is body.
		a.phase = 3
		a.body.addLine(l, cut)
	case 1:
		if s == "" {
			a.phase = 2
			return
		}
		if s == "Body:" {
			a.phase = 3
			return
		}
		a.headers.add(s)
	default:
		a.body.addLine(l, cut)
	}
}

// apiError is one "=== API ERROR RESPONSE ===" section.
type apiError struct {
	status int
	text   capBuf
	seen   bool
}

func (a *apiError) line(l []byte, cut bool) {
	if !a.seen {
		a.seen = true
		if k, v := kv(string(l)); k == "HTTP Status" {
			a.status, _ = strconv.Atoi(v)
			return
		}
	}
	a.text.addLine(l, cut)
}

// parsedFile is the structured content of a request log file.
type parsedFile struct {
	valid            bool
	url              string
	method           string
	timestamp        string
	downstream       string
	headers          headerList
	reqBody          capBuf
	wsTimeline       capBuf
	apiWSTimeline    capBuf
	requests         []*attemptRequest
	responses        []*attemptResponse
	unnumbered       *attemptResponse
	apiErrors        []*apiError
	respStatus       int
	respHeaders      headerList
	respBody         capBuf
	respInBody       bool
	respSeen         bool
	onRespHeadersEnd func(h headerList)
	onRespLine       func(line []byte, cut bool)
}

type parseOptions struct {
	bodyLimit    int
	reqBodyLimit int
	onRespStart  func(h headerList)
	onRespLine   func(line []byte, cut bool)
}

// parseLog walks a request log file section by section. Section headers are recognized only
// at file start or after a blank line, only in the logger's write order, and never inside the
// final RESPONSE section, so payloads that contain header-like lines do not split the file.
func parseLog(r io.Reader, opts parseOptions) (*parsedFile, error) {
	p := &parsedFile{
		headers:          headerList{},
		respHeaders:      headerList{},
		reqBody:          capBuf{limit: opts.reqBodyLimit},
		wsTimeline:       capBuf{limit: opts.bodyLimit},
		apiWSTimeline:    capBuf{limit: opts.bodyLimit},
		respBody:         capBuf{limit: opts.bodyLimit},
		onRespHeadersEnd: opts.onRespStart,
		onRespLine:       opts.onRespLine,
	}
	lr := &lineReader{r: bufio.NewReaderSize(r, 64<<10), max: maxLine}
	current := secNone
	var curReq *attemptRequest
	var curResp *attemptResponse
	var curErr *apiError
	prevBlank := true
	first := true
	for {
		line, cut, err := lr.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return p, err
		}
		if first {
			first = false
			if string(line) != "=== REQUEST INFO ===" {
				return p, nil
			}
			p.valid = true
			current = secInfo
			prevBlank = false
			continue
		}
		if prevBlank && current != secResp && !cut {
			if kind, index, ok := parseSectionHeader(line); ok && kind >= current && kind != secInfo {
				current = kind
				prevBlank = false
				curReq, curResp, curErr = nil, nil, nil
				switch kind {
				case secAPIReq:
					curReq = &attemptRequest{index: index, headers: headerList{}, body: capBuf{limit: opts.bodyLimit}}
					p.requests = append(p.requests, curReq)
				case secAPIResp:
					curResp = &attemptResponse{index: index, headers: headerList{}, body: capBuf{limit: opts.bodyLimit}}
					if index == 0 {
						p.unnumbered = curResp
					} else {
						p.responses = append(p.responses, curResp)
					}
				case secAPIErr:
					curErr = &apiError{text: capBuf{limit: opts.bodyLimit}}
					p.apiErrors = append(p.apiErrors, curErr)
				case secResp:
					p.respSeen = true
				}
				continue
			}
		}
		prevBlank = len(line) == 0
		switch current {
		case secInfo:
			k, v := kv(string(line))
			switch k {
			case "URL":
				p.url = v
			case "Method":
				p.method = v
			case "Timestamp":
				p.timestamp = v
			case "Downstream Transport":
				p.downstream = v
			}
		case secHeaders:
			if len(line) > 0 {
				p.headers.add(string(line))
			}
		case secReqBody:
			p.reqBody.addLine(line, cut)
		case secWS:
			p.wsTimeline.addLine(line, cut)
		case secAPIWS:
			p.apiWSTimeline.addLine(line, cut)
		case secAPIReq:
			curReq.line(line, cut)
		case secAPIResp:
			curResp.line(line, cut)
		case secAPIErr:
			curErr.line(line, cut)
		case secResp:
			p.respLine(line, cut)
		}
	}
	if p.respSeen && !p.respInBody && p.onRespHeadersEnd != nil {
		p.onRespHeadersEnd(p.respHeaders)
	}
	return p, nil
}

func (p *parsedFile) respLine(line []byte, cut bool) {
	if p.respInBody {
		p.respBody.addLine(line, cut)
		if p.onRespLine != nil {
			p.onRespLine(line, cut)
		}
		return
	}
	s := string(line)
	if s == "" {
		p.respInBody = true
		if p.onRespHeadersEnd != nil {
			p.onRespHeadersEnd(p.respHeaders)
		}
		return
	}
	if k, v := kv(s); k == "Status" && p.respStatus == 0 && len(p.respHeaders) == 0 {
		if n, err := strconv.Atoi(v); err == nil {
			p.respStatus = n
			return
		}
	}
	p.respHeaders.add(s)
}

// attemptStatus returns the HTTP status of the last upstream attempt, if any.
func (p *parsedFile) lastAttemptStatus() int {
	for i := len(p.responses) - 1; i >= 0; i-- {
		if p.responses[i].status > 0 {
			return p.responses[i].status
		}
	}
	return 0
}

// attemptCount counts upstream attempts (API REQUEST sections, else numbered responses).
func (p *parsedFile) attemptCount() int {
	n := 0
	for _, req := range p.requests {
		if req.index > n {
			n = req.index
		}
	}
	if n < len(p.requests) {
		n = len(p.requests)
	}
	for _, resp := range p.responses {
		if resp.index > n {
			n = resp.index
		}
	}
	return n
}

func (p *parsedFile) lastAuth() string {
	for i := len(p.requests) - 1; i >= 0; i-- {
		if p.requests[i].auth != "" {
			return p.requests[i].auth
		}
	}
	return ""
}

// authProvider extracts provider=... from an "Auth:" line; it is the same provider key usage
// records carry (e.g. "openai-compatible-<name>" for openai-compatibility entries).
func authProvider(auth string) string {
	for _, part := range strings.FieldsFunc(auth, func(r rune) bool { return r == ',' || r == ' ' }) {
		if v, ok := strings.CutPrefix(part, "provider="); ok {
			return v
		}
	}
	return ""
}
