package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	qoderauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/qoder"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
)

// QoderExecutor executes Qoder subscription credentials through the signed COSY gateway.
type QoderExecutor struct {
	cfg *config.Config
}

func NewQoderExecutor(cfg *config.Config) *QoderExecutor {
	return &QoderExecutor{cfg: cfg}
}

func (e *QoderExecutor) Identifier() string { return qoderauth.ProviderGlobal }

func (e *QoderExecutor) RequestToFormat(_ cliproxyexecutor.Request, _ cliproxyexecutor.Options) sdktranslator.Format {
	return sdktranslator.FormatOpenAI
}

// PrepareRequest signs a direct Qoder request without changing its business payload.
func (e *QoderExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return fmt.Errorf("qoder executor: request is nil")
	}
	storage, err := qoderauth.CredentialFromAuth(auth)
	if err != nil {
		return err
	}
	var body []byte
	if req.Body != nil {
		body, err = io.ReadAll(req.Body)
		if err != nil {
			return fmt.Errorf("qoder executor: read request body: %w", err)
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	if errSign := qoderauth.SignRequest(req, body, storage); errSign != nil {
		return errSign
	}
	util.ApplyCustomHeadersFromAttrs(req, auth.Attributes)
	return nil
}

func (e *QoderExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("qoder executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	return helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(httpReq)
}

// Refresh rotates the official device token and returns an isolated auth snapshot.
func (e *QoderExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	if auth == nil {
		return nil, fmt.Errorf("qoder executor: auth is nil")
	}
	storage, err := qoderauth.CredentialFromAuth(auth)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(storage.RefreshToken) == "" {
		return auth, nil
	}
	client := qoderauth.NewClient(e.cfg, storage.Region)
	client.SetHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 30*time.Second))
	refreshed, err := client.Refresh(ctx, storage)
	if err != nil {
		return nil, err
	}
	updated := auth.Clone()
	if updated.Metadata == nil {
		updated.Metadata = make(map[string]any)
	}
	refreshed.ApplyMetadata(updated.Metadata)
	if _, ok := updated.Storage.(*qoderauth.TokenStorage); ok {
		updated.Storage = refreshed
	}
	if updated.Attributes == nil {
		updated.Attributes = make(map[string]string)
	}
	updated.Attributes["region"] = refreshed.Region
	updated.LastRefreshedAt = time.Now()
	updated.NextRefreshAfter = time.Time{}
	return updated, nil
}

// ShouldPrepareRequestAuth refreshes only credentials inside the refresh window.
func (e *QoderExecutor) ShouldPrepareRequestAuth(auth *cliproxyauth.Auth) bool {
	storage, err := qoderauth.CredentialFromAuth(auth)
	if err != nil || storage.RefreshToken == "" {
		return false
	}
	expiry, ok := storage.Expiry()
	return ok && time.Until(expiry) <= qoderRefreshWindow
}

// PrepareRequestAuth rotates an expiring token through the shared manager path.
func (e *QoderExecutor) PrepareRequestAuth(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if !e.ShouldPrepareRequestAuth(auth) {
		return auth, nil
	}
	return e.Refresh(ctx, auth)
}

var qoderRefreshWindow = 30 * time.Minute

func (e *QoderExecutor) CountTokens(ctx context.Context, _ *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	body := sdktranslator.TranslateRequest(opts.SourceFormat, sdktranslator.FormatOpenAI, req.Model, req.Payload, false)
	encoder, err := helps.TokenizerForModel(req.Model)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	count, err := helps.CountOpenAIChatTokens(encoder, body)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	payload := sdktranslator.TranslateTokenCount(ctx, sdktranslator.FormatOpenAI, cliproxyexecutor.ResponseFormatOrSource(opts), count, helps.BuildOpenAIUsageJSON(count))
	return cliproxyexecutor.Response{Payload: payload}, nil
}

func closeQoderResponse(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	if err := resp.Body.Close(); err != nil {
		log.Debugf("qoder executor: close response: %v", err)
	}
}
