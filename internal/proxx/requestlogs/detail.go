package requestlogs

import (
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/MoeForge/moego/regexp"

	"github.com/MoeForge/moego/ojson"
	log "github.com/sirupsen/logrus"
)

// fileMeta is what the index keeps per file (parsed once per name+size+mtime).
type fileMeta struct {
	valid          bool
	time           time.Time
	method         string
	path           string
	status         int
	stream         bool
	requestedModel string
	servedHeader   string
	requestID      string
	authProvider   string
	attempts       int
	tokens         tokenSet
}

var (
	bodyModelRe   = regexp.MustCompile(`"model"\s*:\s*"([^"]{1,256})"`)
	geminiModelRe = regexp.MustCompile(`/models/([^/:?]+)`)
	uuidSuffixRe  = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// traceRequestID extracts the full request id that ends the X-Cpa-Trace-Id header.
func traceRequestID(trace string) string {
	return strings.ToLower(uuidSuffixRe.FindString(strings.TrimSpace(trace)))
}

func isSSE(h headerList) bool {
	return strings.Contains(strings.ToLower(h.get("Content-Type")), "text/event-stream")
}

// requestModel extracts the client-requested model from the request body, else a Gemini path.
func requestModel(body capBuf, url string) (string, bool) {
	raw := body.String()
	model := ""
	stream := false
	if !body.truncated && ojson.Valid(raw) {
		root := ojson.Parse(raw)
		model = root.Get("model").String()
		stream = root.Get("stream").Bool()
	} else if m := bodyModelRe.FindStringSubmatch(raw); m != nil {
		model = m[1]
	}
	if model == "" {
		if m := geminiModelRe.FindStringSubmatch(url); m != nil {
			model = m[1]
		}
	}
	if strings.Contains(url, ":streamGenerateContent") || strings.Contains(url, "alt=sse") {
		stream = true
	}
	return model, stream
}

func parseTimestamp(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(s))
	if err != nil {
		return time.Time{}
	}
	return t
}

// readMeta parses the fields needed for listing and filtering.
func readMeta(path string) (fileMeta, error) {
	f, err := os.Open(path)
	if err != nil {
		return fileMeta{}, err
	}
	defer func() {
		if errClose := f.Close(); errClose != nil {
			log.WithError(errClose).Debug("proxx: close request log")
		}
	}()
	return readMetaReader(f)
}

func readMetaReader(r io.Reader) (fileMeta, error) {
	var meta fileMeta
	sse := false
	plain := capBuf{limit: indexBodyLimit}
	opts := parseOptions{
		bodyLimit:    0,
		reqBodyLimit: indexBodyLimit,
		onRespStart:  func(h headerList) { sse = isSSE(h) },
		onRespLine: func(line []byte, cut bool) {
			if !sse {
				plain.addLine(line, cut)
				return
			}
			if cut {
				return
			}
			s := string(line)
			if data, ok := strings.CutPrefix(s, "data:"); ok && strings.Contains(data, "sage") {
				meta.tokens.mergeUsage(data)
			}
		},
	}
	p, err := parseLog(r, opts)
	if err != nil || !p.valid {
		return meta, err
	}
	meta.valid = true
	meta.time = parseTimestamp(p.timestamp)
	meta.method = p.method
	meta.path = p.url
	meta.status = p.respStatus
	meta.requestedModel, meta.stream = requestModel(p.reqBody, p.url)
	if sse {
		meta.stream = true
	}
	meta.servedHeader = p.respHeaders.get("X-Proxx-Served-Model")
	meta.requestID = traceRequestID(p.respHeaders.get("X-Cpa-Trace-Id"))
	meta.authProvider = authProvider(p.lastAuth())
	meta.attempts = p.attemptCount()
	if !sse && !plain.truncated {
		meta.tokens.mergeUsage(plain.String())
	}
	// Index entries live for the whole process. Clone the strings so they do not keep the
	// parsed buffers (a request body can be megabytes) alive through substrings.
	meta.method = strings.Clone(meta.method)
	meta.path = strings.Clone(meta.path)
	meta.requestedModel = strings.Clone(meta.requestedModel)
	meta.servedHeader = strings.Clone(meta.servedHeader)
	meta.requestID = strings.Clone(meta.requestID)
	meta.authProvider = strings.Clone(meta.authProvider)
	return meta, nil
}

// Message is the client side of a request or the final client response.
type Message struct {
	URL           string              `json:"url"`
	Method        string              `json:"method"`
	Headers       map[string][]string `json:"headers"`
	Body          string              `json:"body"`
	BodyTruncated bool                `json:"body_truncated"`
}

// Attempt is one upstream attempt (API REQUEST n paired with API RESPONSE n).
type Attempt struct {
	Index             int                 `json:"index"`
	URL               string              `json:"url"`
	Method            string              `json:"method"`
	Auth              string              `json:"auth"`
	Headers           map[string][]string `json:"headers"`
	Body              string              `json:"body"`
	BodyTruncated     bool                `json:"body_truncated"`
	Status            *int                `json:"status"`
	ResponseHeaders   map[string][]string `json:"response_headers"`
	ResponseBody      string              `json:"response_body"`
	ResponseTruncated bool                `json:"response_truncated"`
	Error             string              `json:"error"`
	Timestamp         string              `json:"timestamp"`
}

// Response is the final response returned to the client.
type Response struct {
	Status        int                 `json:"status"`
	Headers       map[string][]string `json:"headers"`
	Errors        []string            `json:"errors,omitempty"`
	Body          string              `json:"body"`
	BodyTruncated bool                `json:"body_truncated"`
	SSEEvents     []SSEEvent          `json:"sse_events"`
	Assembled     *Assembled          `json:"assembled"`
}

// Detail is the full parsed view of one request log file.
type Detail struct {
	Row                  Row       `json:"row"`
	Client               Message   `json:"client"`
	Attempts             []Attempt `json:"attempts"`
	Response             Response  `json:"response"`
	WebsocketTimeline    string    `json:"websocket_timeline"`
	APIWebsocketTimeline string    `json:"api_websocket_timeline"`
	RawSizeBytes         int64     `json:"raw_size_bytes"`
}

func headerMap(h headerList) map[string][]string {
	out := make(map[string][]string, len(h))
	for k, v := range h {
		out[k] = v
	}
	return out
}

// readDetail parses the whole file with 2 MB per-field caps.
func readDetail(path string) (*Detail, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if errClose := f.Close(); errClose != nil {
			log.WithError(errClose).Debug("proxx: close request log")
		}
	}()
	var collector *sseCollector
	opts := parseOptions{
		bodyLimit:    BodyLimit,
		reqBodyLimit: BodyLimit,
		onRespStart: func(h headerList) {
			if isSSE(h) {
				collector = &sseCollector{}
			}
		},
		onRespLine: func(line []byte, cut bool) {
			if collector != nil && !cut {
				collector.line(string(line))
			}
		},
	}
	p, err := parseLog(f, opts)
	if err != nil {
		return nil, err
	}
	d := &Detail{
		Client: Message{
			URL:           p.url,
			Method:        p.method,
			Headers:       headerMap(p.headers),
			Body:          p.reqBody.String(),
			BodyTruncated: p.reqBody.truncated,
		},
		Attempts: buildAttempts(p),
		Response: Response{
			Status:        p.respStatus,
			Headers:       headerMap(p.respHeaders),
			Errors:        p.respErrors,
			Body:          p.respBody.String(),
			BodyTruncated: p.respBody.truncated,
		},
		WebsocketTimeline:    p.wsTimeline.String(),
		APIWebsocketTimeline: p.apiWSTimeline.String(),
	}
	if collector != nil {
		collector.flush()
		d.Response.SSEEvents = collector.events
		if d.Response.SSEEvents == nil {
			d.Response.SSEEvents = []SSEEvent{}
		}
		d.Response.Assembled = collector.asm.result()
	}
	return d, nil
}

// buildAttempts pairs API REQUEST n with API RESPONSE n. The unnumbered API RESPONSE written on
// the error path is used only when no numbered response exists; API ERROR RESPONSE entries fill
// attempts without a response, in order, and any remaining ones go to the last attempt.
func buildAttempts(p *parsedFile) []Attempt {
	byIndex := map[int]*Attempt{}
	var order []int
	get := func(index int) *Attempt {
		if a, ok := byIndex[index]; ok {
			return a
		}
		a := &Attempt{Index: index, Headers: map[string][]string{}, ResponseHeaders: map[string][]string{}}
		byIndex[index] = a
		order = append(order, index)
		return a
	}
	for i, req := range p.requests {
		index := req.index
		if index == 0 {
			index = i + 1
		}
		a := get(index)
		a.URL, a.Method, a.Auth, a.Timestamp = req.url, req.method, req.auth, req.timestamp
		a.Headers = headerMap(req.headers)
		a.Body, a.BodyTruncated = req.body.String(), req.body.truncated
	}
	responses := p.responses
	if len(responses) == 0 && p.unnumbered != nil {
		u := *p.unnumbered
		u.index = 1
		responses = []*attemptResponse{&u}
	}
	for _, resp := range responses {
		a := get(resp.index)
		if resp.status > 0 {
			status := resp.status
			a.Status = &status
		}
		a.ResponseHeaders = headerMap(resp.headers)
		a.ResponseBody, a.ResponseTruncated = resp.body.String(), resp.body.truncated
		a.Error = strings.Join(resp.errors, "\n")
		if a.Timestamp == "" {
			a.Timestamp = resp.timestamp
		}
	}
	sort.Ints(order)
	out := make([]Attempt, 0, len(order))
	for _, index := range order {
		out = append(out, *byIndex[index])
	}
	errs := p.apiErrors
	for i := range out {
		if len(errs) == 0 {
			break
		}
		if out[i].Status == nil && out[i].Error == "" {
			applyAPIError(&out[i], errs[0])
			errs = errs[1:]
		}
	}
	if len(errs) > 0 {
		if len(out) == 0 {
			out = append(out, Attempt{Index: 1, Headers: map[string][]string{}, ResponseHeaders: map[string][]string{}})
		}
		for _, e := range errs {
			applyAPIError(&out[len(out)-1], e)
		}
	}
	return out
}

func applyAPIError(a *Attempt, e *apiError) {
	if a.Status == nil && e.status > 0 {
		status := e.status
		a.Status = &status
	}
	text := e.text.String()
	if a.Error != "" && text != "" {
		a.Error += "\n"
	}
	a.Error += text
}
