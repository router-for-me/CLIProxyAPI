package management

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// Platform API base URLs are package vars so tests point them at stub servers.
var (
	vercelAPIBase     = "https://api.vercel.com"
	cloudflareAPIBase = "https://api.cloudflare.com/client/v4"
	denoAPIBase       = "https://api.deno.com/v2"
)

// relayDeployTimeout bounds each platform API call. This is deployment
// orchestration (credential/setup phase), not request-serving traffic, so an
// explicit client timeout is allowed per the repo timeout policy.
const relayDeployTimeout = 60 * time.Second

// relayDeployPollMaxBounds bounds the deploy status polling loops.
const (
	vercelPollMaxMs  = 120000
	vercelPollStep   = 3 * time.Second
	denoPollMaxTries = 30
	denoPollStep     = 2 * time.Second
)

// relayDeployReq is the body of POST /proxy-pools/relay-deploy. Platform
// credentials are one-shot from the request body and never persisted.
type relayDeployReq struct {
	Platform    string `json:"platform"`
	ProjectName string `json:"project_name,omitempty"`
	// Vercel: bearer token.
	VercelToken string `json:"vercel_token,omitempty"`
	// Cloudflare: account id + API token.
	CFAccountID string `json:"cf_account_id,omitempty"`
	CFToken     string `json:"cf_api_token,omitempty"`
	// Deno: API token + org domain (e.g. "myorg.deno.net").
	DenoToken string `json:"deno_token,omitempty"`
	DenoOrg   string `json:"deno_org_domain,omitempty"`
}

// relayWorkerSource is the relay worker source deployed to every platform.
// The x-relay-target/x-relay-path contract is shared by all three runtimes;
// only the registration shape differs. The \\/$/ escapes are raw-string safe.
const relayWorkerSource = `export default {
  async fetch(request, env, ctx) {
    const target = request.headers.get("x-relay-target");
    const relayPath = request.headers.get("x-relay-path") || "/";

    if (!target) {
      return new Response(JSON.stringify({ error: "Missing x-relay-target header" }), {
        status: 400,
        headers: { "content-type": "application/json" },
      });
    }

    const targetUrl = target.replace(/\/$/, "") + relayPath;
    const newRequestInit = {
      method: request.method,
      headers: new Headers(request.headers),
    };

    if (request.method !== "GET" && request.method !== "HEAD") {
      newRequestInit.body = request.body;
      newRequestInit.duplex = "half";
    }

    newRequestInit.headers.delete("x-relay-target");
    newRequestInit.headers.delete("x-relay-path");
    newRequestInit.headers.delete("host");

    try {
      const response = await fetch(targetUrl, newRequestInit);
      return new Response(response.body, {
        status: response.status,
        headers: response.headers,
      });
    } catch (error) {
      return new Response(JSON.stringify({ error: error.message }), {
        status: 502,
        headers: { "content-type": "application/json" },
      });
    }
  },
};
`

// platformHTTPClient returns the client used for platform API calls.
func platformHTTPClient() *http.Client {
	return &http.Client{Timeout: relayDeployTimeout}
}

// platformError is an upstream platform API failure that must surface with
// its status code and message verbatim.
type platformError struct {
	status  int
	message string
}

func (e *platformError) Error() string { return e.message }

// readPlatformError decodes the best-effort error message from a platform
// response body (Cloudflare errors[].message, Vercel error.message, Deno text).
func readPlatformError(resp *http.Response) string {
	raw, errRead := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if errRead != nil {
		return fmt.Sprintf("platform API returned status %d", resp.StatusCode)
	}
	var envelope struct {
		Error  any `json:"error"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if errUnmarshal := json.Unmarshal(raw, &envelope); errUnmarshal == nil {
		if len(envelope.Errors) > 0 && envelope.Errors[0].Message != "" {
			return envelope.Errors[0].Message
		}
		switch e := envelope.Error.(type) {
		case string:
			if e != "" {
				return e
			}
		case map[string]any:
			if m, ok := e["message"].(string); ok && m != "" {
				return m
			}
		}
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return fmt.Sprintf("platform API returned status %d", resp.StatusCode)
	}
	return text
}

// doPlatformJSON performs a JSON request against a platform API and decodes
// the response envelope into out. Non-2xx becomes a *platformError.
func doPlatformJSON(ctx context.Context, method, url, token string, body io.Reader, contentType string, out any) error {
	req, errReq := http.NewRequestWithContext(ctx, method, url, body)
	if errReq != nil {
		return errReq
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, errDo := platformHTTPClient().Do(req)
	if errDo != nil {
		return errDo
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &platformError{status: resp.StatusCode, message: readPlatformError(resp)}
	}
	if out == nil {
		return nil
	}
	if errDecode := json.NewDecoder(resp.Body).Decode(out); errDecode != nil {
		return fmt.Errorf("decode platform response: %w", errDecode)
	}
	return nil
}

// DeployRelayProxyPool handles POST /v0/management/proxy-pools/relay-deploy:
// deploy a relay worker to the requested platform (one-shot credentials,
// never stored), then create the pool row and re-render upstream artifacts.
func (h *Handler) DeployRelayProxyPool(c *gin.Context) {
	srcs, ok := h.proxyPoolsStore(c)
	if !ok {
		return
	}
	var body relayDeployReq
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type": "invalid_request", "message": err.Error(),
		}})
		return
	}
	if errValidate := validateRelayDeployReq(&body); errValidate != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type": "invalid_request", "message": errValidate.Error(),
		}})
		return
	}

	ctx := c.Request.Context()
	var deployURL string
	var errDeploy error
	switch body.Platform {
	case "vercel":
		deployURL, errDeploy = deployVercelRelay(ctx, &body)
	case "cloudflare":
		deployURL, errDeploy = deployCloudflareRelay(ctx, &body)
	case "deno":
		deployURL, errDeploy = deployDenoRelay(ctx, &body)
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type": "invalid_request", "message": "platform must be one of vercel, cloudflare, deno",
		}})
		return
	}
	if errDeploy != nil {
		var perr *platformError
		if errors_As(errDeploy, &perr) {
			c.JSON(perr.status, gin.H{"error": gin.H{
				"type": "upstream_platform", "message": perr.message,
			}})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{
			"type": "deploy_failed", "message": errDeploy.Error(),
		}})
		return
	}

	pool, errCreate := srcs.Create(ctx, store.ProxyPool{
		Name:        body.ProjectName,
		ProxyURL:    deployURL,
		Type:        body.Platform,
		IsActive:    true,
		StrictProxy: true,
		TestStatus:  "unknown",
	})
	if errCreate != nil {
		h.proxyPoolErrorResponse(c, errCreate)
		return
	}
	h.applyUpstreamProviders(ctx)
	c.JSON(http.StatusCreated, gin.H{"pool": pool, "deploy_url": deployURL})
}

// errors_As is a tiny indirection so the platformError unwrapping above reads
// without importing the errors package twice under different shapes.
func errors_As(err error, target **platformError) bool {
	for err != nil {
		if perr, ok := err.(*platformError); ok {
			*target = perr
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}

// validateRelayDeployReq checks the platform + per-platform credentials.
func validateRelayDeployReq(body *relayDeployReq) error {
	switch body.Platform {
	case "vercel":
		if strings.TrimSpace(body.VercelToken) == "" {
			return fmt.Errorf("vercel_token is required")
		}
	case "cloudflare":
		if strings.TrimSpace(body.CFAccountID) == "" {
			return fmt.Errorf("cf_account_id is required")
		}
		if strings.TrimSpace(body.CFToken) == "" {
			return fmt.Errorf("cf_api_token is required")
		}
	case "deno":
		if strings.TrimSpace(body.DenoToken) == "" {
			return fmt.Errorf("deno_token is required")
		}
		if strings.TrimSpace(body.DenoOrg) == "" {
			return fmt.Errorf("deno_org_domain is required")
		}
	default:
		return fmt.Errorf("platform must be one of vercel, cloudflare, deno")
	}
	body.ProjectName = strings.TrimSpace(body.ProjectName)
	if body.ProjectName == "" {
		body.ProjectName = fmt.Sprintf("relay-%d", time.Now().Unix())
	}
	return nil
}

// deployVercelRelay ports 9router's vercel-deploy flow: create deployment
// with the embedded relay function, disable deployment protection, poll to
// READY, return the deployment URL.
func deployVercelRelay(ctx context.Context, body *relayDeployReq) (string, error) {
	files := []map[string]string{
		{"file": "api/relay.js", "data": relayWorkerVercelSource},
		{"file": "package.json", "data": fmt.Sprintf(`{"name":%q,"version":"1.0.0"}`, body.ProjectName)},
		{"file": "vercel.json", "data": `{"rewrites": [{"source": "/(.*)", "destination": "/api/relay"}]}`},
	}
	payload, errMarshal := json.Marshal(map[string]any{
		"name":            body.ProjectName,
		"files":           files,
		"projectSettings": map[string]any{"framework": nil},
		"target":          "production",
	})
	if errMarshal != nil {
		return "", errMarshal
	}
	var created struct {
		ID        string `json:"id"`
		ProjectID string `json:"projectId"`
	}
	if err := doPlatformJSON(ctx, http.MethodPost, vercelAPIBase+"/v13/deployments",
		body.VercelToken, bytes.NewReader(payload), "application/json", &created); err != nil {
		return "", err
	}

	// Best-effort: disable Vercel Authentication so the relay is reachable.
	protection, errMarshal := json.Marshal(map[string]any{"ssoProtection": nil})
	if errMarshal == nil {
		if err := doPlatformJSON(ctx, http.MethodPatch,
			vercelAPIBase+"/v9/projects/"+projectIDOrName(created.ProjectID, body.ProjectName),
			body.VercelToken, bytes.NewReader(protection), "application/json", nil); err != nil {
			log.Debugf("vercel: disable deployment protection failed (continuing): %v", err)
		}
	}

	// Poll until READY.
	deadline := time.Now().Add(time.Duration(vercelPollMaxMs) * time.Millisecond)
	for {
		var status struct {
			ReadyState string `json:"readyState"`
			URL        string `json:"url"`
		}
		if err := doPlatformJSON(ctx, http.MethodGet,
			vercelAPIBase+"/v13/deployments/"+created.ID, body.VercelToken, nil, "", &status); err != nil {
			return "", err
		}
		switch status.ReadyState {
		case "READY":
			if status.URL == "" {
				return "", fmt.Errorf("deployment READY but no URL returned")
			}
			return "https://" + status.URL, nil
		case "ERROR", "CANCELED":
			return "", fmt.Errorf("deployment failed: %s", status.ReadyState)
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("deployment timed out")
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(vercelPollStep):
		}
	}
}

func projectIDOrName(projectID, name string) string {
	if projectID != "" {
		return projectID
	}
	return name
}

// deployCloudflareRelay ports 9router's cloudflare-deploy flow: upload the
// worker script (multipart), enable the workers.dev subdomain, read the
// account subdomain, return https://<name>.<subdomain>.workers.dev.
func deployCloudflareRelay(ctx context.Context, body *relayDeployReq) (string, error) {
	form := &bytes.Buffer{}
	writer := multipart.NewWriter(form)
	part, errPart := writer.CreateFormFile("index.js", "index.js")
	if errPart != nil {
		return "", errPart
	}
	if _, err := part.Write([]byte(relayWorkerSource)); err != nil {
		return "", err
	}
	metadata, errMarshal := json.Marshal(map[string]any{
		"main_module":        "index.js",
		"compatibility_date": "2024-03-20",
		"observability":      map[string]any{"enabled": true},
	})
	if errMarshal != nil {
		return "", errMarshal
	}
	metaPart, errMeta := writer.CreateFormFile("metadata", "metadata.json")
	if errMeta != nil {
		return "", errMeta
	}
	if _, err := metaPart.Write(metadata); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}

	scriptURL := fmt.Sprintf("%s/accounts/%s/workers/scripts/%s", cloudflareAPIBase, body.CFAccountID, body.ProjectName)
	if err := doPlatformJSON(ctx, http.MethodPut, scriptURL, body.CFToken, form, writer.FormDataContentType(), nil); err != nil {
		return "", err
	}

	// Best-effort: enable the workers.dev subdomain for the script.
	enableBody := strings.NewReader(`{"enabled":true}`)
	if err := doPlatformJSON(ctx, http.MethodPost, scriptURL+"/subdomain", body.CFToken, enableBody, "application/json", nil); err != nil {
		log.Debugf("cloudflare: enable subdomain failed (continuing): %v", err)
	}

	var subdomain struct {
		Result struct {
			Subdomain string `json:"subdomain"`
		} `json:"result"`
	}
	if err := doPlatformJSON(ctx, http.MethodGet,
		fmt.Sprintf("%s/accounts/%s/workers/subdomain", cloudflareAPIBase, body.CFAccountID),
		body.CFToken, nil, "", &subdomain); err != nil {
		return "", err
	}
	if subdomain.Result.Subdomain == "" {
		return "", fmt.Errorf("worker deployed but the workers.dev subdomain is unavailable; set it up in the Cloudflare dashboard")
	}
	return fmt.Sprintf("https://%s.%s.workers.dev", body.ProjectName, subdomain.Result.Subdomain), nil
}

// deployDenoRelay ports 9router's deno-deploy flow: create app, deploy the
// relay as main.ts, poll the revision to succeeded (delete the app on
// failure), return https://<name>.<org-slug>.deno.net.
func deployDenoRelay(ctx context.Context, body *relayDeployReq) (string, error) {
	orgSlug := strings.SplitN(body.DenoOrg, ".", 2)[0]

	var app struct {
		ID string `json:"id"`
	}
	createPayload, errMarshal := json.Marshal(map[string]any{
		"slug":   body.ProjectName,
		"labels": map[string]string{"custom.kind": "nixllm-relay"},
		"config": map[string]any{
			"install": "deno install",
			"runtime": map[string]any{"type": "dynamic", "entrypoint": "main.ts"},
		},
	})
	if errMarshal != nil {
		return "", errMarshal
	}
	if err := doPlatformJSON(ctx, http.MethodPost, denoAPIBase+"/apps", body.DenoToken,
		bytes.NewReader(createPayload), "application/json", &app); err != nil {
		return "", err
	}

	deleteApp := func() {
		req, errReq := http.NewRequestWithContext(ctx, http.MethodDelete, denoAPIBase+"/apps/"+app.ID, nil)
		if errReq != nil {
			return
		}
		req.Header.Set("Authorization", "Bearer "+body.DenoToken)
		resp, errDo := platformHTTPClient().Do(req)
		if errDo == nil {
			_ = resp.Body.Close()
		}
	}

	deployPayload, errMarshal := json.Marshal(map[string]any{
		"assets": map[string]any{
			"main.ts": map[string]string{
				"kind":     "file",
				"content":  relayWorkerDenoSource,
				"encoding": "utf-8",
			},
		},
	})
	if errMarshal != nil {
		deleteApp()
		return "", errMarshal
	}
	var revision struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := doPlatformJSON(ctx, http.MethodPost, denoAPIBase+"/apps/"+app.ID+"/deploy",
		body.DenoToken, bytes.NewReader(deployPayload), "application/json", &revision); err != nil {
		deleteApp()
		return "", err
	}

	for tries := 0; tries < denoPollMaxTries; tries++ {
		switch revision.Status {
		case "succeeded":
			return fmt.Sprintf("https://%s.%s.deno.net", body.ProjectName, orgSlug), nil
		case "queued", "building":
			// keep polling
		default:
			deleteApp()
			return "", fmt.Errorf("deploy failed with status: %s", revision.Status)
		}
		select {
		case <-ctx.Done():
			deleteApp()
			return "", ctx.Err()
		case <-time.After(denoPollStep):
		}
		var status struct {
			Status string `json:"status"`
		}
		if err := doPlatformJSON(ctx, http.MethodGet, denoAPIBase+"/revisions/"+revision.ID,
			body.DenoToken, nil, "", &status); err != nil {
			deleteApp()
			return "", err
		}
		revision.Status = status.Status
	}
	deleteApp()
	return "", fmt.Errorf("deploy timed out")
}
