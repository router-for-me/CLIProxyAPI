// Package main demonstrates how to create a custom AI provider executor
// and integrate it with the CLI Proxy API server. This example shows how to:
// - Create a custom executor that implements the Executor interface
// - Register custom translators for request/response transformation
// - Integrate the custom provider with the SDK server
// - Register custom models in the model registry
//
// This example uses a simple echo service (httpbin.org) as the upstream API
// for demonstration purposes. In a real implementation, you would replace
// this with your actual AI service provider.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	clipexec "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/logging"
	sdktr "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

const (
	// providerKey is the identifier for our custom provider.
	providerKey = "myprov"

	// fOpenAI represents the OpenAI chat format.
	fOpenAI = sdktr.Format("openai.chat")

	// fMyProv represents our custom provider's chat format.
	fMyProv = sdktr.Format("myprov.chat")
)

// init registers trivial translators for demonstration purposes.
// In a real implementation, you would implement proper request/response
// transformation logic between OpenAI format and your provider's format.
func init() {
	sdktr.Register(fOpenAI, fMyProv,
		func(model string, raw []byte, stream bool) []byte { return raw },
		sdktr.ResponseTransform{
			Stream: func(ctx context.Context, model string, originalReq, translatedReq, raw []byte, param *any) []string {
				return []string{string(raw)}
			},
			NonStream: func(ctx context.Context, model string, originalReq, translatedReq, raw []byte, param *any) string {
				return string(raw)
			},
		},
	)
}

// MyExecutor is a minimal provider implementation for demonstration purposes.
// It implements the Executor interface to handle requests to a custom AI provider.
type MyExecutor struct{}

// Identifier returns the unique identifier for this executor.
func (MyExecutor) Identifier() string { return providerKey }

// PrepareRequest optionally injects credentials to raw HTTP requests.
// This method is called before each request to allow the executor to modify
// the HTTP request with authentication headers or other necessary modifications.
//
// Parameters:
//   - req: The HTTP request to prepare
//   - a: The authentication information
//
// Returns:
//   - error: An error if request preparation fails
func (MyExecutor) PrepareRequest(req *http.Request, a *coreauth.Auth) error {
	if req == nil || a == nil {
		return nil
	}
	if a.Attributes != nil {
		if ak := strings.TrimSpace(a.Attributes["api_key"]); ak != "" {
			req.Header.Set("Authorization", "Bearer "+ak)
		}
	}
	return nil
}

func buildHTTPClient(a *coreauth.Auth) *http.Client {
	if a == nil || strings.TrimSpace(a.ProxyURL) == "" {
		return http.DefaultClient
	}
	u, err := url.Parse(a.ProxyURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return http.DefaultClient
	}
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(u)}}
}

func upstreamEndpoint(a *coreauth.Auth) string {
	if a != nil && a.Attributes != nil {
		if ep := strings.TrimSpace(a.Attributes["endpoint"]); ep != "" {
			return ep
		}
	}
	// Demo echo endpoint; replace with your upstream.
	return "https://httpbin.org/post"
}

func (MyExecutor) Execute(ctx context.Context, a *coreauth.Auth, req clipexec.Request, opts clipexec.Options) (clipexec.Response, error) {
	client := buildHTTPClient(a)
	endpoint := upstreamEndpoint(a)

	httpReq, errNew := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(req.Payload))
	if errNew != nil {
		return clipexec.Response{}, errNew
	}
	httpReq.Header.Set("Content-Type", "application/json")

	// Inject credentials via PrepareRequest hook.
	if errPrep := (MyExecutor{}).PrepareRequest(httpReq, a); errPrep != nil {
		return clipexec.Response{}, errPrep
	}

	resp, errDo := client.Do(httpReq)
	if errDo != nil {
		return clipexec.Response{}, errDo
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			fmt.Fprintf(os.Stderr, "close response body error: %v\n", errClose)
		}
	}()
	body, _ := io.ReadAll(resp.Body)
	return clipexec.Response{Payload: body}, nil
}

func (MyExecutor) HttpRequest(ctx context.Context, a *coreauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("myprov executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if errPrep := (MyExecutor{}).PrepareRequest(httpReq, a); errPrep != nil {
		return nil, errPrep
	}
	client := buildHTTPClient(a)
	return client.Do(httpReq)
}

func (MyExecutor) CountTokens(context.Context, *coreauth.Auth, clipexec.Request, clipexec.Options) (clipexec.Response, error) {
	return clipexec.Response{}, errors.New("count tokens not implemented")
}

func (MyExecutor) ExecuteStream(ctx context.Context, a *coreauth.Auth, req clipexec.Request, opts clipexec.Options) (*clipexec.StreamResult, error) {
	ch := make(chan clipexec.StreamChunk, 1)
	go func() {
		defer close(ch)
		ch <- clipexec.StreamChunk{Payload: []byte("data: {\"ok\":true}\n\n")}
	}()
	return &clipexec.StreamResult{Chunks: ch}, nil
}

func (MyExecutor) Refresh(ctx context.Context, a *coreauth.Auth) (*coreauth.Auth, error) {
	return a, nil
}

func main() {
	cfg, err := config.LoadConfig("config.yaml")
	if err != nil {
		panic(err)
	}

	tokenStore := sdkAuth.GetTokenStore()
	if dirSetter, ok := tokenStore.(interface{ SetBaseDir(string) }); ok {
		dirSetter.SetBaseDir(cfg.AuthDir)
	}
	core := coreauth.NewManager(tokenStore, nil, nil)
	core.RegisterExecutor(MyExecutor{})

	hooks := cliproxy.Hooks{
		OnAfterStart: func(s *cliproxy.Service) {
			// Register demo models for the custom provider so they appear in /v1/models.
			models := []*cliproxy.ModelInfo{{ID: "myprov-pro-1", Object: "model", Type: providerKey, DisplayName: "MyProv Pro 1"}}
			for _, a := range core.List() {
				if strings.EqualFold(a.Provider, providerKey) {
					cliproxy.GlobalModelRegistry().RegisterClient(a.ID, providerKey, models)
				}
			}
		},
	}

	svc, err := cliproxy.NewBuilder().
		WithConfig(cfg).
		WithConfigPath("config.yaml").
		WithCoreAuthManager(core).
		WithServerOptions(
			// Optional: add a simple middleware + custom request logger
			api.WithMiddleware(func(c *gin.Context) { c.Header("X-Example", "custom-provider"); c.Next() }),
			api.WithRequestLoggerFactory(func(cfg *config.Config, cfgPath string) logging.RequestLogger {
				return logging.NewFileRequestLoggerWithOptions(true, "logs", filepath.Dir(cfgPath), cfg.ErrorLogsMaxFiles)
			}),
		).
		WithHooks(hooks).
		Build()
	if err != nil {
		panic(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if errRun := svc.Run(ctx); errRun != nil && !errors.Is(errRun, context.Canceled) {
		panic(errRun)
	}
	_ = os.Stderr // keep os import used (demo only)
	_ = time.Second
}
