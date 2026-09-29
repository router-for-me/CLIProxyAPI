package controller

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaychannel "github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"golang.org/x/net/http/httpguts"
)

var errTaskMediaRequestRejected = errors.New("task media request rejected")

var taskMediaResponseHeaderTimeout = 60 * time.Second
var taskMediaDataURLMaxEncodedBytes = 64 << 20

type taskMediaProxyError struct {
	status  int
	code    string
	message string
	err     error
}

func (e *taskMediaProxyError) Error() string {
	if e.err == nil {
		return e.message
	}
	return e.message + ": " + e.err.Error()
}

func (e *taskMediaProxyError) Unwrap() error {
	return e.err
}

// videoProxyError returns a standardized OpenAI-style error response.
func videoProxyError(c *gin.Context, status int, errType, message string) {
	c.Header("Cache-Control", "private, no-store")
	c.JSON(status, gin.H{
		"error": gin.H{
			"message": message,
			"type":    errType,
		},
	})
}

func VideoProxy(c *gin.Context) {
	taskID := c.Param("task_id")
	if taskID == "" {
		videoProxyError(c, http.StatusBadRequest, "invalid_request_error", "task_id is required")
		return
	}

	task, exists, err := getTaskForArtifactRequest(c, taskID)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Failed to query task %s: %s", taskID, err.Error()))
		videoProxyError(c, http.StatusInternalServerError, "server_error", "Failed to query task")
		return
	}
	if !exists || task == nil {
		videoProxyError(c, http.StatusNotFound, "invalid_request_error", "Task not found")
		return
	}
	if task.Status != model.TaskStatusSuccess {
		videoProxyError(c, http.StatusBadRequest, "invalid_request_error",
			fmt.Sprintf("Task is not completed yet, current status: %s", task.Status))
		return
	}

	var descriptor *relaychannel.TaskContentRequest
	if taskHasPluginExecution(task) {
		artifacts, projectionErr := projectTaskArtifacts(task)
		if projectionErr == nil {
			for _, artifact := range artifacts {
				if artifact.Type != "video" {
					continue
				}
				adaptor, adaptorErr := initTaskArtifactAdaptor(task)
				if adaptorErr == nil {
					if provider, ok := adaptor.(relaychannel.TaskContentRequestProvider); ok {
						descriptor, adaptorErr = provider.BuildContentRequest(task, artifact.Key, relaychannel.TaskArtifactClientRequest{
							Method:  c.Request.Method,
							Headers: taskArtifactClientHeaders(c.Request.Header),
						})
					}
				}
				if adaptorErr != nil {
					logger.LogWarn(c.Request.Context(), fmt.Sprintf("Failed to resolve plugin video content for task %s", taskID))
					descriptor = nil
				}
				break
			}
		} else {
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("Failed to project plugin video for task %s", taskID))
		}
	}
	if descriptor == nil {
		resultURL := task.GetResultURL()
		if isTaskMediaFallbackLoop(resultURL, task.TaskID) {
			writeTaskMediaProxyError(c, &taskMediaProxyError{
				status: http.StatusGone, code: "artifact_gone",
				message: "Artifact content is no longer available",
			})
			return
		}
		descriptor = &relaychannel.TaskContentRequest{
			URL:            resultURL,
			Method:         c.Request.Method,
			Credentialless: true,
		}
	}
	if err := proxyTaskMedia(c, task, descriptor); err != nil {
		writeTaskMediaProxyError(c, err)
	}
}

func proxyTaskMedia(c *gin.Context, task *model.Task, descriptor *relaychannel.TaskContentRequest) error {
	if descriptor == nil {
		return &taskMediaProxyError{
			status: http.StatusInternalServerError, code: "artifact_plugin_error",
			message: "Artifact content plugin returned no request",
		}
	}
	rawURL := strings.TrimSpace(descriptor.URL)
	if rawURL == "" {
		return &taskMediaProxyError{
			status: http.StatusGone, code: "artifact_gone",
			message: "Artifact content is no longer available",
		}
	}
	if strings.HasPrefix(rawURL, "data:") {
		if len(rawURL) > taskMediaDataURLMaxEncodedBytes {
			return &taskMediaProxyError{
				status: http.StatusBadGateway, code: "artifact_request_rejected",
				message: "Artifact request was rejected", err: errTaskMediaRequestRejected,
			}
		}
		if err := writeVideoDataURL(c, rawURL); err != nil {
			return &taskMediaProxyError{
				status: http.StatusBadGateway, code: "artifact_upstream_error",
				message: "Failed to decode artifact content", err: err,
			}
		}
		return nil
	}
	if len(rawURL) > 64<<10 {
		return &taskMediaProxyError{
			status: http.StatusBadGateway, code: "artifact_request_rejected",
			message: "Artifact request was rejected", err: errTaskMediaRequestRejected,
		}
	}

	parsedURL, err := url.Parse(rawURL)
	if err != nil || parsedURL == nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") ||
		parsedURL.Host == "" || parsedURL.User != nil || parsedURL.Fragment != "" {
		return &taskMediaProxyError{
			status: http.StatusBadGateway, code: "artifact_request_rejected",
			message: "Artifact request was rejected", err: errTaskMediaRequestRejected,
		}
	}
	if isTaskMediaFallbackLoop(rawURL, task.TaskID) || isSelfTaskMediaURL(c, parsedURL) {
		return &taskMediaProxyError{
			status: http.StatusBadGateway, code: "artifact_request_rejected",
			message: "Artifact proxy loop was rejected", err: errTaskMediaRequestRejected,
		}
	}

	method := strings.ToUpper(strings.TrimSpace(descriptor.Method))
	if method == "" {
		method = c.Request.Method
	}
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost:
	default:
		return &taskMediaProxyError{
			status: http.StatusBadGateway, code: "artifact_request_rejected",
			message: "Artifact request method was rejected", err: errTaskMediaRequestRejected,
		}
	}
	if len(descriptor.Body) > 1<<20 {
		return &taskMediaProxyError{
			status: http.StatusBadGateway, code: "artifact_request_rejected",
			message: "Artifact request body was rejected", err: errTaskMediaRequestRejected,
		}
	}
	if descriptor.Credentialless &&
		(method != http.MethodGet && method != http.MethodHead ||
			descriptor.Body != nil || len(descriptor.Headers) != 0) {
		return &taskMediaProxyError{
			status: http.StatusBadGateway, code: "artifact_request_rejected",
			message: "Credentialless artifact request was rejected", err: errTaskMediaRequestRejected,
		}
	}

	channel, err := model.CacheGetChannel(task.ChannelId)
	if err != nil {
		return &taskMediaProxyError{
			status: http.StatusServiceUnavailable, code: "artifact_plugin_unavailable",
			message: "Artifact channel is unavailable", err: err,
		}
	}
	proxy := strings.TrimSpace(channel.GetSetting().Proxy)
	if err := validateTaskMediaURL(rawURL, proxy); err != nil {
		return &taskMediaProxyError{
			status: http.StatusBadGateway, code: "artifact_request_rejected",
			message: "Artifact request was rejected", err: err,
		}
	}

	client := service.GetSSRFProtectedHTTPClient()
	if proxy != "" {
		client, err = service.GetHttpClientWithProxy(proxy)
		if err != nil {
			return &taskMediaProxyError{
				status: http.StatusInternalServerError, code: "artifact_internal_error",
				message: "Failed to create artifact proxy client", err: err,
			}
		}
	}
	if client == nil {
		client = http.DefaultClient
	}

	req, err := http.NewRequestWithContext(c.Request.Context(), method, parsedURL.String(), bytes.NewReader(descriptor.Body))
	if err != nil {
		return &taskMediaProxyError{
			status: http.StatusInternalServerError, code: "artifact_internal_error",
			message: "Failed to create artifact request", err: err,
		}
	}
	if err := applyTaskMediaRequestHeaders(req.Header, descriptor.Headers); err != nil {
		return &taskMediaProxyError{
			status: http.StatusBadGateway, code: "artifact_request_rejected",
			message: "Artifact request headers were rejected", err: err,
		}
	}
	clientHeaders := taskArtifactClientHeaders(c.Request.Header)
	for name, value := range clientHeaders {
		req.Header.Set(name, value)
	}

	client = taskMediaRedirectClient(client, proxy, c, clientHeaders, descriptor.Credentialless)
	clientWithoutBodyTimeout := *client
	clientWithoutBodyTimeout.Timeout = 0
	resp, err := doTaskMediaRequest(&clientWithoutBodyTimeout, req, taskMediaResponseHeaderTimeout)
	if err != nil {
		if errors.Is(err, errTaskMediaRequestRejected) {
			return &taskMediaProxyError{
				status: http.StatusBadGateway, code: "artifact_request_rejected",
				message: "Artifact redirect was rejected", err: err,
			}
		}
		var netErr net.Error
		if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &netErr) && netErr.Timeout() {
			return &taskMediaProxyError{
				status: http.StatusGatewayTimeout, code: "artifact_upstream_timeout",
				message: "Artifact upstream request timed out", err: err,
			}
		}
		return &taskMediaProxyError{
			status: http.StatusBadGateway, code: "artifact_upstream_error",
			message: "Failed to fetch artifact content", err: err,
		}
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusPartialContent, http.StatusNotModified, http.StatusRequestedRangeNotSatisfiable:
		copyTaskMediaResponseHeaders(c.Writer.Header(), resp.Header)
		setTaskMediaResponseSecurityHeaders(c.Writer.Header())
		c.Status(resp.StatusCode)
		c.Writer.WriteHeaderNow()
		if c.Request.Method == http.MethodHead || resp.StatusCode == http.StatusNotModified {
			return nil
		}
		if _, err := io.Copy(c.Writer, resp.Body); err != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Failed to stream task media: %v", err))
		}
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return &taskMediaProxyError{
			status: http.StatusBadGateway, code: "artifact_upstream_auth_failed",
			message: "Artifact upstream authentication failed",
		}
	case http.StatusNotFound, http.StatusGone:
		return &taskMediaProxyError{
			status: http.StatusGone, code: "artifact_gone",
			message: "Artifact content is no longer available",
		}
	case http.StatusTooManyRequests:
		if retryAfter := strings.TrimSpace(resp.Header.Get("Retry-After")); retryAfter != "" &&
			len(retryAfter) <= 256 && !strings.ContainsAny(retryAfter, "\r\n") {
			c.Header("Retry-After", retryAfter)
		}
		return &taskMediaProxyError{
			status: http.StatusServiceUnavailable, code: "artifact_upstream_busy",
			message: "Artifact upstream is busy",
		}
	default:
		return &taskMediaProxyError{
			status: http.StatusBadGateway, code: "artifact_upstream_error",
			message: fmt.Sprintf("Artifact upstream returned status %d", resp.StatusCode),
		}
	}
}

type taskMediaHTTPResult struct {
	response *http.Response
	err      error
}

type taskMediaCancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
	once   sync.Once
}

func (b *taskMediaCancelBody) Close() error {
	b.once.Do(b.cancel)
	return b.ReadCloser.Close()
}

func doTaskMediaRequest(client *http.Client, request *http.Request, responseHeaderTimeout time.Duration) (*http.Response, error) {
	if client == nil {
		client = http.DefaultClient
	}
	requestContext, cancel := context.WithCancel(request.Context())
	request = request.Clone(requestContext)
	resultChannel := make(chan taskMediaHTTPResult, 1)
	go func() {
		response, err := client.Do(request)
		resultChannel <- taskMediaHTTPResult{response: response, err: err}
	}()

	timer := time.NewTimer(responseHeaderTimeout)
	defer timer.Stop()
	cleanupResult := func() {
		go func() {
			result := <-resultChannel
			if result.response != nil && result.response.Body != nil {
				_ = result.response.Body.Close()
			}
		}()
	}

	select {
	case result := <-resultChannel:
		if result.err != nil {
			cancel()
			if result.response != nil && result.response.Body != nil {
				_ = result.response.Body.Close()
			}
			return nil, result.err
		}
		if result.response == nil || result.response.Body == nil {
			cancel()
			return nil, errors.New("artifact upstream returned no response body")
		}
		result.response.Body = &taskMediaCancelBody{
			ReadCloser: result.response.Body,
			cancel:     cancel,
		}
		return result.response, nil
	case <-timer.C:
		cancel()
		cleanupResult()
		return nil, context.DeadlineExceeded
	case <-request.Context().Done():
		cancel()
		cleanupResult()
		return nil, request.Context().Err()
	}
}

func applyTaskMediaRequestHeaders(destination http.Header, headers map[string]string) error {
	if len(headers) > 64 {
		return errTaskMediaRequestRejected
	}
	for name, value := range headers {
		name = strings.TrimSpace(name)
		if !httpguts.ValidHeaderFieldName(name) || !httpguts.ValidHeaderFieldValue(value) || len(value) > 8192 {
			return errTaskMediaRequestRejected
		}
		switch strings.ToLower(name) {
		case "host", "content-length", "accept-encoding", "connection", "proxy-connection", "keep-alive",
			"proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
			return errTaskMediaRequestRejected
		}
		destination.Set(name, value)
	}
	return nil
}

func taskMediaRedirectClient(base *http.Client, proxy string, c *gin.Context, clientHeaders map[string]string, credentialless bool) *http.Client {
	cloned := *base
	cloned.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("%w: too many redirects", errTaskMediaRequestRejected)
		}
		if req.URL == nil || (req.URL.Scheme != "http" && req.URL.Scheme != "https") ||
			req.URL.Host == "" || req.URL.User != nil || req.URL.Fragment != "" {
			return fmt.Errorf("%w: invalid redirect URL", errTaskMediaRequestRejected)
		}
		if err := validateTaskMediaURL(req.URL.String(), proxy); err != nil {
			return fmt.Errorf("%w: %v", errTaskMediaRequestRejected, err)
		}
		if isSelfTaskMediaURL(c, req.URL) {
			return fmt.Errorf("%w: proxy loop", errTaskMediaRequestRejected)
		}
		if len(via) > 0 && !sameTaskMediaOrigin(via[len(via)-1].URL, req.URL) {
			if !credentialless {
				return fmt.Errorf("%w: credentialed cross-origin redirect", errTaskMediaRequestRejected)
			}
			for name := range req.Header {
				req.Header.Del(name)
			}
			req.Body = http.NoBody
			req.GetBody = nil
			req.ContentLength = 0
		}
		for name, value := range clientHeaders {
			req.Header.Set(name, value)
		}
		return nil
	}
	return &cloned
}

func validateTaskMediaURL(rawURL, proxy string) error {
	if proxy == "" {
		return service.ValidateSSRFProtectedFetchURL(rawURL)
	}
	fetchSetting := system_setting.GetFetchSetting()
	return common.ValidateURLWithFetchSetting(
		rawURL,
		fetchSetting.EnableSSRFProtection,
		fetchSetting.AllowPrivateIp,
		fetchSetting.DomainFilterMode,
		fetchSetting.IpFilterMode,
		fetchSetting.DomainList,
		fetchSetting.IpList,
		fetchSetting.AllowedPorts,
		fetchSetting.ApplyIPFilterForDomain,
	)
}

func sameTaskMediaOrigin(left, right *url.URL) bool {
	if left == nil || right == nil {
		return false
	}
	return strings.EqualFold(left.Scheme, right.Scheme) &&
		strings.EqualFold(normalizeTaskMediaHost(left.Scheme, left.Host), normalizeTaskMediaHost(right.Scheme, right.Host))
}

func normalizeTaskMediaHost(scheme, host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	hostname, port, err := net.SplitHostPort(host)
	if err != nil {
		return strings.TrimSuffix(host, ".")
	}
	hostname = strings.TrimSuffix(strings.ToLower(hostname), ".")
	if (strings.EqualFold(scheme, "http") && port == "80") || (strings.EqualFold(scheme, "https") && port == "443") {
		return hostname
	}
	return net.JoinHostPort(hostname, port)
}

func isSelfTaskMediaURL(c *gin.Context, target *url.URL) bool {
	if c == nil || target == nil || !isTaskMediaProxyPath(target.Path) {
		return false
	}
	targetHost := normalizeTaskMediaHost(target.Scheme, target.Host)
	if targetHost == "" {
		return true
	}
	scheme := strings.TrimSpace(strings.Split(c.Request.Header.Get("X-Forwarded-Proto"), ",")[0])
	if scheme == "" {
		scheme = "http"
		if c.Request.TLS != nil {
			scheme = "https"
		}
	}
	hosts := []string{c.Request.Host}
	if forwardedHost := strings.TrimSpace(strings.Split(c.Request.Header.Get("X-Forwarded-Host"), ",")[0]); forwardedHost != "" {
		hosts = append(hosts, forwardedHost)
	}
	for _, host := range hosts {
		if strings.EqualFold(targetHost, normalizeTaskMediaHost(scheme, host)) {
			return true
		}
	}
	return false
}

func isTaskMediaProxyPath(path string) bool {
	if strings.HasPrefix(path, "/v1/videos/") && strings.HasSuffix(path, "/content") {
		return true
	}
	return strings.HasPrefix(path, "/v1/tasks/") &&
		strings.Contains(path, "/artifacts/") &&
		strings.HasSuffix(path, "/content")
}

func isTaskMediaFallbackLoop(rawURL, taskID string) bool {
	parsedURL, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsedURL == nil {
		return false
	}
	path, err := url.PathUnescape(parsedURL.EscapedPath())
	if err != nil {
		path = parsedURL.Path
	}
	if path == "/v1/videos/"+taskID+"/content" {
		return true
	}
	artifactPrefix := "/v1/tasks/" + taskID + "/artifacts/"
	return strings.HasPrefix(path, artifactPrefix) && strings.HasSuffix(path, "/content")
}

func copyTaskMediaResponseHeaders(destination, source http.Header) {
	for _, name := range []string{
		"Content-Type",
		"Content-Length",
		"Content-Range",
		"Accept-Ranges",
		"ETag",
		"Last-Modified",
		"Content-Disposition",
	} {
		for _, value := range source.Values(name) {
			destination.Add(name, value)
		}
	}
}

func setTaskMediaResponseSecurityHeaders(header http.Header) {
	header.Set("Cache-Control", "private, no-store")
	header.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
}

func writeTaskMediaProxyError(c *gin.Context, err error) {
	if c.Writer.Written() {
		logger.LogError(c.Request.Context(), err.Error())
		return
	}
	var proxyErr *taskMediaProxyError
	if !errors.As(err, &proxyErr) {
		proxyErr = &taskMediaProxyError{
			status: http.StatusBadGateway, code: "artifact_upstream_error",
			message: "Failed to fetch artifact content", err: err,
		}
	}
	c.Header("Cache-Control", "private, no-store")
	writeTaskArtifactError(c, proxyErr.status, proxyErr.code, proxyErr.message)
}

func writeVideoDataURL(c *gin.Context, dataURL string) error {
	if len(dataURL) > taskMediaDataURLMaxEncodedBytes {
		return errTaskMediaRequestRejected
	}
	parts := strings.SplitN(dataURL, ",", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid data url")
	}

	header := parts[0]
	payload := parts[1]
	if !strings.HasPrefix(header, "data:") || !strings.Contains(header, ";base64") {
		return fmt.Errorf("unsupported data url")
	}

	mimeType := strings.TrimPrefix(header, "data:")
	mimeType = strings.TrimSuffix(mimeType, ";base64")
	if mimeType == "" {
		mimeType = "video/mp4"
	}
	if len(mimeType) > 255 || !httpguts.ValidHeaderFieldValue(mimeType) {
		return fmt.Errorf("invalid data url media type")
	}

	var encoding *base64.Encoding
	var contentLength int64
	for _, candidate := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding} {
		decodedLength, err := io.Copy(io.Discard, base64.NewDecoder(candidate, strings.NewReader(payload)))
		if err == nil {
			encoding = candidate
			contentLength = decodedLength
			break
		}
	}
	if encoding == nil {
		return fmt.Errorf("invalid base64 data")
	}

	c.Writer.Header().Set("Content-Type", mimeType)
	c.Writer.Header().Set("Content-Length", strconv.FormatInt(contentLength, 10))
	setTaskMediaResponseSecurityHeaders(c.Writer.Header())
	c.Writer.WriteHeader(http.StatusOK)
	if c.Request.Method == http.MethodHead {
		return nil
	}
	_, err := io.Copy(c.Writer, base64.NewDecoder(encoding, strings.NewReader(payload)))
	return err
}
