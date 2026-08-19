package transport

import (
	"net/http"
	"strings"

	fhttp "github.com/bogdanfinn/fhttp"
)

// The fhttp HTTP/2 stack speaks its own fork of net/http types. Converting at
// this boundary keeps every caller in the codebase on the standard library's
// *http.Request / *http.Response.

// toFHTTPRequest converts a standard library request into the fhttp equivalent
// and stamps it with the Chrome header ordering.
//
// Header keys are stored lowercase on purpose. fhttp's HTTP/2 encoder looks up
// "content-length" and "accept-encoding" by exact lowercase key to decide
// whether to synthesise them; canonicalised keys such as "Content-Length"
// would miss those lookups and the header would be emitted twice.
func toFHTTPRequest(req *http.Request) *fhttp.Request {
	header := make(fhttp.Header, len(req.Header)+2)
	for k, vv := range req.Header {
		if strings.EqualFold(k, "Host") {
			// Carried as :authority.
			continue
		}
		header[strings.ToLower(k)] = append([]string(nil), vv...)
	}
	header[fhttp.HeaderOrderKey] = chromeHeaderOrder
	header[fhttp.PHeaderOrderKey] = chromePseudoHeaderOrder

	out := &fhttp.Request{
		Method:        req.Method,
		URL:           req.URL,
		Proto:         "HTTP/2.0",
		ProtoMajor:    2,
		ProtoMinor:    0,
		Header:        header,
		Body:          req.Body,
		GetBody:       req.GetBody,
		ContentLength: req.ContentLength,
		Host:          req.Host,
	}
	return out.WithContext(req.Context())
}

// fromFHTTPResponse converts an fhttp response back to the standard library
// type, leaving Body untouched so streaming (SSE) responses stay streaming.
func fromFHTTPResponse(resp *fhttp.Response, req *http.Request) *http.Response {
	header := make(http.Header, len(resp.Header))
	for k, vv := range resp.Header {
		header[http.CanonicalHeaderKey(k)] = append([]string(nil), vv...)
	}
	return &http.Response{
		Status:        resp.Status,
		StatusCode:    resp.StatusCode,
		Proto:         resp.Proto,
		ProtoMajor:    resp.ProtoMajor,
		ProtoMinor:    resp.ProtoMinor,
		Header:        header,
		Body:          resp.Body,
		ContentLength: resp.ContentLength,
		Uncompressed:  resp.Uncompressed,
		// resp.TLS is deliberately dropped: fhttp types it against its own
		// utls fork, and nothing in this codebase reads Response.TLS.
		Request: req,
	}
}
