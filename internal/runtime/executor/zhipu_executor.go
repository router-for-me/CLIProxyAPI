package executor

import (
    "bufio"
    "bytes"
    "context"
    "fmt"
    "io"
    "net/http"
    "os"
    "strings"

    "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
    cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
    cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
    sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
    log "github.com/sirupsen/logrus"
)

// ZhipuExecutor is a stateless executor for Zhipu GLM using an
// OpenAI-compatible chat completions interface.
type ZhipuExecutor struct {
    cfg *config.Config
}

// NewZhipuExecutor creates a new ZhipuExecutor instance.
func NewZhipuExecutor(cfg *config.Config) *ZhipuExecutor { return &ZhipuExecutor{cfg: cfg} }

// Identifier implements cliproxyauth.ProviderExecutor.
func (e *ZhipuExecutor) Identifier() string { return "zhipu" }

func (e *ZhipuExecutor) PrepareRequest(_ *http.Request, _ *cliproxyauth.Auth) error { return nil }

func (e *ZhipuExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
    token, _ := zhipuCreds(auth)
    reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
    from := opts.SourceFormat
    to := sdktranslator.FromString("openai")
    body := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), false)
    bridgeURL := strings.TrimSpace(os.Getenv("CLAUDE_AGENT_SDK_URL"))
    if bridgeURL == "" {
        var err error
        bridgeURL, err = ensureClaudePythonBridge(ctx, e.cfg, auth)
        if err != nil {
            return cliproxyexecutor.Response{}, err
        }
    }
    url := strings.TrimSuffix(bridgeURL, "/") + "/v1/chat/completions"
    httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
    if err != nil { return cliproxyexecutor.Response{}, err }
    applyZhipuHeaders(httpReq, token, false)

    var authID, authLabel, authType, authValue string
    if auth != nil { authID = auth.ID; authLabel = auth.Label; authType, authValue = auth.AccountInfo() }
    recordAPIRequest(ctx, e.cfg, upstreamRequestLog{ URL: url, Method: http.MethodPost, Headers: httpReq.Header.Clone(), Body: body, Provider: e.Identifier(), AuthID: authID, AuthLabel: authLabel, AuthType: authType, AuthValue: authValue })

    httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
    resp, err := httpClient.Do(httpReq)
    if err != nil { recordAPIResponseError(ctx, e.cfg, err); return cliproxyexecutor.Response{}, err }
    defer func() { _ = resp.Body.Close() }()
    recordAPIResponseMetadata(ctx, e.cfg, resp.StatusCode, resp.Header.Clone())
    if resp.StatusCode < 200 || resp.StatusCode >= 300 {
        b, _ := io.ReadAll(resp.Body)
        appendAPIResponseChunk(ctx, e.cfg, b)
        log.Debugf("request error, error status: %d, error body: %s", resp.StatusCode, string(b))
        return cliproxyexecutor.Response{}, statusErr{code: resp.StatusCode, msg: string(b)}
    }
    data, err := io.ReadAll(resp.Body)
    if err != nil { recordAPIResponseError(ctx, e.cfg, err); return cliproxyexecutor.Response{}, err }
    appendAPIResponseChunk(ctx, e.cfg, data)
    reporter.publish(ctx, parseOpenAIUsage(data))
    var param any
    out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), body, data, &param)
    return cliproxyexecutor.Response{Payload: []byte(out)}, nil
}

func (e *ZhipuExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
    token, _ := zhipuCreds(auth)
    reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
    from := opts.SourceFormat
    to := sdktranslator.FromString("openai")
    body := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), true)
    bridgeURL := strings.TrimSpace(os.Getenv("CLAUDE_AGENT_SDK_URL"))
    if bridgeURL == "" {
        var err error
        bridgeURL, err = ensureClaudePythonBridge(ctx, e.cfg, auth)
        if err != nil {
            return nil, err
        }
    }
    url := strings.TrimSuffix(bridgeURL, "/") + "/v1/chat/completions"
    httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
    if err != nil { return nil, err }
    applyZhipuHeaders(httpReq, token, true)

    var authID, authLabel, authType, authValue string
    if auth != nil { authID = auth.ID; authLabel = auth.Label; authType, authValue = auth.AccountInfo() }
    recordAPIRequest(ctx, e.cfg, upstreamRequestLog{ URL: url, Method: http.MethodPost, Headers: httpReq.Header.Clone(), Body: body, Provider: e.Identifier(), AuthID: authID, AuthLabel: authLabel, AuthType: authType, AuthValue: authValue })

    httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
    resp, err := httpClient.Do(httpReq)
    if err != nil { recordAPIResponseError(ctx, e.cfg, err); return nil, err }
    recordAPIResponseMetadata(ctx, e.cfg, resp.StatusCode, resp.Header.Clone())
    if resp.StatusCode < 200 || resp.StatusCode >= 300 {
        defer func() { _ = resp.Body.Close() }()
        b, _ := io.ReadAll(resp.Body)
        appendAPIResponseChunk(ctx, e.cfg, b)
        log.Debugf("request error, error status: %d, error body: %s", resp.StatusCode, string(b))
        return nil, statusErr{code: resp.StatusCode, msg: string(b)}
    }
    out := make(chan cliproxyexecutor.StreamChunk)
    go func() {
        defer close(out)
        defer func() { _ = resp.Body.Close() }()
        scanner := bufio.NewScanner(resp.Body)
        buf := make([]byte, 20_971_520)
        scanner.Buffer(buf, 20_971_520)
        var param any
        for scanner.Scan() {
            line := scanner.Bytes()
            appendAPIResponseChunk(ctx, e.cfg, line)
            if detail, ok := parseOpenAIStreamUsage(line); ok { reporter.publish(ctx, detail) }
            chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), body, bytes.Clone(line), &param)
            for i := range chunks { out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunks[i])} }
        }
        if err = scanner.Err(); err != nil {
            recordAPIResponseError(ctx, e.cfg, err)
            out <- cliproxyexecutor.StreamChunk{Err: err}
        }
    }()
    return out, nil
}

func (e *ZhipuExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
    return cliproxyexecutor.Response{Payload: []byte{}}, fmt.Errorf("not implemented")
}

func (e *ZhipuExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
    log.Debugf("zhipu executor: refresh called")
    _ = ctx
    return auth, nil
}

func applyZhipuHeaders(r *http.Request, token string, stream bool) {
    r.Header.Set("Content-Type", "application/json")
    if strings.TrimSpace(token) != "" {
        r.Header.Set("Authorization", "Bearer "+token)
    }
    r.Header.Set("User-Agent", "cli-proxy-zhipu")
    if stream {
        r.Header.Set("Accept", "text/event-stream")
        return
    }
    r.Header.Set("Accept", "application/json")
}

func zhipuCreds(a *cliproxyauth.Auth) (token, baseURL string) {
    if a == nil { return "", "" }
    if a.Attributes != nil {
        if v := a.Attributes["api_key"]; v != "" { token = v }
        if v := a.Attributes["base_url"]; v != "" { baseURL = v }
    }
    return
}
