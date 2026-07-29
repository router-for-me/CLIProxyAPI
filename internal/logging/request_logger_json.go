package logging

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	maxJSONStreamingResponseBytes = 8 << 20
	maxJSONFileBackedSectionBytes = 8 << 20
)

type jsonLogPayload struct {
	Version                       string              `json:"version"`
	URL                           string              `json:"url"`
	Method                        string              `json:"method"`
	DownstreamTransport           string              `json:"downstream_transport,omitempty"`
	UpstreamTransport             string              `json:"upstream_transport,omitempty"`
	Timestamp                     string              `json:"timestamp"`
	Headers                       map[string][]string `json:"headers,omitempty"`
	RequestBody                   json.RawMessage     `json:"request_body,omitempty"`
	RequestBodyRaw                string              `json:"request_body_raw,omitempty"`
	RequestBodyEncoding           string              `json:"request_body_encoding,omitempty"`
	RequestBodyTruncated          bool                `json:"request_body_truncated,omitempty"`
	Response                      *jsonLogResponse    `json:"response,omitempty"`
	APIRequest                    json.RawMessage     `json:"api_request,omitempty"`
	APIRequestRaw                 string              `json:"api_request_raw,omitempty"`
	APIRequestEncoding            string              `json:"api_request_encoding,omitempty"`
	APIRequestTruncated           bool                `json:"api_request_truncated,omitempty"`
	APIResponse                   json.RawMessage     `json:"api_response,omitempty"`
	APIResponseRaw                string              `json:"api_response_raw,omitempty"`
	APIResponseEncoding           string              `json:"api_response_encoding,omitempty"`
	APIResponseTruncated          bool                `json:"api_response_truncated,omitempty"`
	APIResponseErrors             []jsonLogError      `json:"api_response_errors,omitempty"`
	APIResponseTimestamp          string              `json:"api_response_timestamp,omitempty"`
	APIWebsocketTimelineRaw       string              `json:"api_websocket_timeline_raw,omitempty"`
	APIWebsocketTimelineEncoding  string              `json:"api_websocket_timeline_encoding,omitempty"`
	APIWebsocketTimelineTruncated bool                `json:"api_websocket_timeline_truncated,omitempty"`
	WebsocketTimelineRaw          string              `json:"websocket_timeline_raw,omitempty"`
	WebsocketTimelineEncoding     string              `json:"websocket_timeline_encoding,omitempty"`
	WebsocketTimelineTruncated    bool                `json:"websocket_timeline_truncated,omitempty"`
}

type jsonLogError struct {
	StatusCode int                 `json:"status_code"`
	Error      string              `json:"error,omitempty"`
	Addon      map[string][]string `json:"addon,omitempty"`
}

type jsonLogResponse struct {
	Status             int                 `json:"status"`
	Headers            map[string][]string `json:"headers,omitempty"`
	Body               json.RawMessage     `json:"body,omitempty"`
	BodyRaw            string              `json:"body_raw,omitempty"`
	BodyEncoding       string              `json:"body_encoding,omitempty"`
	BodyTruncated      bool                `json:"body_truncated,omitempty"`
	DecompressionError string              `json:"decompression_error,omitempty"`
}

func maskHeaders(headers map[string][]string) map[string][]string {
	if len(headers) == 0 {
		return nil
	}
	masked := make(map[string][]string, len(headers))
	for key, values := range headers {
		maskedValues := make([]string, len(values))
		for i, value := range values {
			maskedValues[i] = util.MaskSensitiveHeaderValue(key, value)
		}
		masked[key] = maskedValues
	}
	return masked
}

func (l *FileRequestLogger) writeJSONLog(
	w io.Writer,
	url, method string,
	requestHeaders map[string][]string,
	requestBody []byte,
	requestBodyPath string,
	requestBodyTruncated bool,
	statusCode int,
	responseHeaders map[string][]string,
	response []byte,
	responsePath string,
	responseBodyTruncated bool,
	decompressErr error,
	apiRequest []byte,
	apiRequestSource *FileBodySource,
	apiResponse []byte,
	apiResponseSource *FileBodySource,
	apiResponseErrors []*interfaces.ErrorMessage,
	websocketTimeline []byte,
	websocketTimelineSource *FileBodySource,
	apiWebsocketTimeline []byte,
	apiWebsocketTimelineSource *FileBodySource,
	requestTimestamp time.Time,
	apiResponseTimestamp time.Time,
	downstreamTransport string,
	upstreamTransport string,
) error {
	if requestTimestamp.IsZero() {
		requestTimestamp = time.Now()
	}

	var requestBytes []byte
	if requestBodyPath != "" {
		var truncated bool
		var errRead error
		requestBytes, truncated, errRead = readFileLimited(requestBodyPath, maxJSONFileBackedSectionBytes)
		if errRead != nil {
			return fmt.Errorf("read JSON request body: %w", errRead)
		}
		requestBodyTruncated = requestBodyTruncated || truncated
	} else {
		requestBytes = requestBody
		if len(requestBytes) > maxJSONFileBackedSectionBytes {
			requestBytes = requestBytes[:maxJSONFileBackedSectionBytes]
			requestBodyTruncated = true
		}
	}

	entry := jsonLogPayload{
		Version:              buildinfo.Version,
		URL:                  url,
		Method:               method,
		DownstreamTransport:  downstreamTransport,
		UpstreamTransport:    upstreamTransport,
		Timestamp:            requestTimestamp.Format(time.RFC3339Nano),
		Headers:              maskHeaders(requestHeaders),
		RequestBodyTruncated: requestBodyTruncated,
	}
	setJSONPayload(requestBytes, &entry.RequestBody, &entry.RequestBodyRaw, &entry.RequestBodyEncoding)
	if !apiResponseTimestamp.IsZero() {
		entry.APIResponseTimestamp = apiResponseTimestamp.Format(time.RFC3339Nano)
	}

	apiRequestBytes, truncated, errMerge := mergeJSONSectionLimited(apiRequestSource, apiRequest, maxJSONFileBackedSectionBytes)
	if errMerge != nil {
		return fmt.Errorf("read JSON API request: %w", errMerge)
	}
	entry.APIRequestTruncated = truncated
	setJSONPayload(apiRequestBytes, &entry.APIRequest, &entry.APIRequestRaw, &entry.APIRequestEncoding)

	apiResponseBytes, truncated, errMerge := mergeJSONSectionLimited(apiResponseSource, apiResponse, maxJSONFileBackedSectionBytes)
	if errMerge != nil {
		return fmt.Errorf("read JSON API response: %w", errMerge)
	}
	entry.APIResponseTruncated = truncated
	setJSONPayload(apiResponseBytes, &entry.APIResponse, &entry.APIResponseRaw, &entry.APIResponseEncoding)

	for _, apiErr := range apiResponseErrors {
		if apiErr == nil {
			continue
		}
		logErr := jsonLogError{StatusCode: apiErr.StatusCode, Addon: maskHeaders(apiErr.Addon)}
		if apiErr.Error != nil {
			logErr.Error = apiErr.Error.Error()
		}
		entry.APIResponseErrors = append(entry.APIResponseErrors, logErr)
	}

	apiTimeline, truncated, errMerge := mergeJSONSectionLimited(apiWebsocketTimelineSource, apiWebsocketTimeline, maxJSONFileBackedSectionBytes)
	if errMerge != nil {
		return fmt.Errorf("read JSON API websocket timeline: %w", errMerge)
	}
	setJSONRawPayload(apiTimeline, &entry.APIWebsocketTimelineRaw, &entry.APIWebsocketTimelineEncoding)
	entry.APIWebsocketTimelineTruncated = truncated

	downstreamTimeline, truncated, errMerge := mergeJSONSectionLimited(websocketTimelineSource, websocketTimeline, maxJSONFileBackedSectionBytes)
	if errMerge != nil {
		return fmt.Errorf("read JSON websocket timeline: %w", errMerge)
	}
	setJSONRawPayload(downstreamTimeline, &entry.WebsocketTimelineRaw, &entry.WebsocketTimelineEncoding)
	entry.WebsocketTimelineTruncated = truncated

	responseEntry := &jsonLogResponse{Status: statusCode, Headers: maskHeaders(responseHeaders)}
	if decompressErr != nil {
		responseEntry.DecompressionError = decompressErr.Error()
	}
	if responsePath != "" {
		var truncated bool
		var errRead error
		response, truncated, errRead = readFileLimited(responsePath, maxJSONStreamingResponseBytes)
		if errRead != nil {
			return fmt.Errorf("read JSON response body: %w", errRead)
		}
		responseBodyTruncated = responseBodyTruncated || truncated
	} else if len(response) > maxJSONStreamingResponseBytes {
		response = response[:maxJSONStreamingResponseBytes]
		responseBodyTruncated = true
	}
	responseEntry.BodyTruncated = responseBodyTruncated
	setJSONPayload(response, &responseEntry.Body, &responseEntry.BodyRaw, &responseEntry.BodyEncoding)
	entry.Response = responseEntry

	data, errMarshal := json.Marshal(&entry)
	if errMarshal != nil {
		return errMarshal
	}
	data = append(data, '\n')
	_, errWrite := w.Write(data)
	return errWrite
}

func setJSONPayload(payload []byte, structured *json.RawMessage, raw, encoding *string) {
	if len(payload) == 0 {
		return
	}
	if !utf8.Valid(payload) {
		*raw = base64.StdEncoding.EncodeToString(payload)
		*encoding = "base64"
		return
	}
	if json.Valid(payload) {
		*structured = json.RawMessage(payload)
		return
	}
	*raw = string(payload)
}

func setJSONRawPayload(payload []byte, raw, encoding *string) {
	if len(payload) == 0 {
		return
	}
	if !utf8.Valid(payload) {
		*raw = base64.StdEncoding.EncodeToString(payload)
		*encoding = "base64"
		return
	}
	*raw = string(payload)
}

func readFileLimited(path string, limit int) ([]byte, bool, error) {
	file, errOpen := os.Open(path)
	if errOpen != nil {
		return nil, false, errOpen
	}
	defer func() {
		if errClose := file.Close(); errClose != nil {
			log.WithError(errClose).Warn("failed to close JSON log source file")
		}
	}()

	payload, errRead := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if errRead != nil {
		return nil, false, errRead
	}
	if len(payload) > limit {
		return payload[:limit], true, nil
	}
	return payload, false, nil
}

var errJSONLogSectionLimit = errors.New("JSON log section size limit reached")

type limitedJSONLogWriter struct {
	buffer bytes.Buffer
	limit  int
}

func (w *limitedJSONLogWriter) Write(payload []byte) (int, error) {
	remaining := w.limit - w.buffer.Len()
	if remaining <= 0 {
		return 0, errJSONLogSectionLimit
	}
	if len(payload) > remaining {
		_, _ = w.buffer.Write(payload[:remaining])
		return remaining, errJSONLogSectionLimit
	}
	return w.buffer.Write(payload)
}

func mergeJSONSectionLimited(source *FileBodySource, inline []byte, limit int) ([]byte, bool, error) {
	writer := &limitedJSONLogWriter{limit: limit}
	truncated := source != nil && source.Truncated()
	if source != nil && source.HasPayload() {
		if errWrite := source.WriteTo(writer); errWrite != nil {
			if errors.Is(errWrite, errJSONLogSectionLimit) {
				return writer.buffer.Bytes(), true, nil
			}
			return nil, false, errWrite
		}
	}
	if len(inline) > 0 {
		if _, errWrite := writer.Write(inline); errWrite != nil {
			if errors.Is(errWrite, errJSONLogSectionLimit) {
				return writer.buffer.Bytes(), true, nil
			}
			return nil, false, errWrite
		}
	}
	return writer.buffer.Bytes(), truncated, nil
}
