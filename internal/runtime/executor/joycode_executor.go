package executor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/joycode"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	joycodeChatURL = "https://joycode-api.jd.com/api/saas/openai/v1/chat/completions"
)

type JoyCodeExecutor struct {
	cfg *config.Config
}

func NewJoyCodeExecutor(cfg *config.Config) *JoyCodeExecutor {
	return &JoyCodeExecutor{cfg: cfg}
}

func (e *JoyCodeExecutor) Identifier() string { return "joycode" }

func (e *JoyCodeExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if auth == nil || auth.Metadata == nil {
		return fmt.Errorf("joycode: missing auth metadata")
	}

	ptKey, _ := auth.Metadata["ptKey"].(string)
	if ptKey == "" {
		return fmt.Errorf("joycode: missing ptKey credential")
	}

	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Header.Set("ptKey", ptKey)
	req.Header.Set("loginType", "")
	req.Header.Set("User-Agent", joycode.JoyCodeUA)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-ms-client-request-id", generateJoyCodeRequestID())

	return nil
}

func (e *JoyCodeExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	client := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 5*time.Minute)

	if err := e.PrepareRequest(req, auth); err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("joycode: request failed: %w", err)
	}
	return resp, nil
}

func (e *JoyCodeExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	parsed := thinking.ParseSuffix(req.Model)
	baseModel := parsed.ModelName

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	payload := buildJoyCodePayload(req.Payload, baseModel, auth)

	httpReq, err := http.NewRequestWithContext(ctx, "POST", joycodeChatURL, bytes.NewReader(payload))
	if err != nil {
		return resp, err
	}

	httpResp, err := e.HttpRequest(ctx, auth, httpReq)
	if err != nil {
		return resp, err
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != 200 {
		body, _ := io.ReadAll(httpResp.Body)
		return resp, statusErr{
			code: httpResp.StatusCode,
			msg:  fmt.Sprintf("joycode: API returned %d: %s", httpResp.StatusCode, string(body)),
		}
	}

	body, _ := io.ReadAll(httpResp.Body)

	from := sdktranslator.FromString("openai")
	to := sdktranslator.FromString("joycode")

	var param any
	translated := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, req.Payload, body, &param)

	promptTokens := gjson.GetBytes(body, "usage.prompt_tokens").Int()
	completionTokens := gjson.GetBytes(body, "usage.completion_tokens").Int()

	reporter.Publish(ctx, usage.Detail{
		InputTokens:  promptTokens,
		OutputTokens: completionTokens,
	})

	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:      joycodeChatURL,
		Method:   "POST",
		Provider: "joycode",
		AuthID:   auth.ID,
	})

	return cliproxyexecutor.Response{Payload: translated}, nil
}

func (e *JoyCodeExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	parsed := thinking.ParseSuffix(req.Model)
	baseModel := parsed.ModelName

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	payload := buildJoyCodePayload(req.Payload, baseModel, auth)

	httpReq, err := http.NewRequestWithContext(ctx, "POST", joycodeChatURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}

	httpResp, err := e.HttpRequest(ctx, auth, httpReq)
	if err != nil {
		return nil, err
	}

	if httpResp.StatusCode != 200 {
		body, _ := io.ReadAll(httpResp.Body)
		httpResp.Body.Close()
		return nil, statusErr{
			code: httpResp.StatusCode,
			msg:  fmt.Sprintf("joycode: API returned %d: %s", httpResp.StatusCode, string(body)),
		}
	}

	chunks := make(chan cliproxyexecutor.StreamChunk, 64)

	go func() {
		defer close(chunks)
		defer httpResp.Body.Close()

		from := sdktranslator.FromString("openai")
		to := sdktranslator.FromString("joycode")
		var streamParam any
		var totalPromptTokens, totalCompletionTokens int64

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}

			var data string
			if strings.HasPrefix(line, "data: ") {
				data = strings.TrimPrefix(line, "data: ")
			} else if strings.HasPrefix(line, "data:") {
				data = strings.TrimPrefix(line, "data:")
			} else {
				continue
			}

			if data == "[DONE]" {
				break
			}

			if pt := gjson.Get(data, "usage.prompt_tokens").Int(); pt > 0 {
				totalPromptTokens = pt
			}
			if ct := gjson.Get(data, "usage.completion_tokens").Int(); ct > 0 {
				totalCompletionTokens = ct
			}

			translatedChunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, req.Payload, []byte(data), &streamParam)
			for _, tc := range translatedChunks {
				if len(tc) > 0 {
					chunks <- cliproxyexecutor.StreamChunk{Payload: tc}
				}
			}
		}

		if err := scanner.Err(); err != nil {
			log.Warnf("joycode: stream scanner error: %v", err)
			chunks <- cliproxyexecutor.StreamChunk{Err: err}
		}

		reporter.Publish(ctx, usage.Detail{
			InputTokens:  totalPromptTokens,
			OutputTokens: totalCompletionTokens,
		})

		helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
			URL:      joycodeChatURL,
			Method:   "POST",
			Provider: "joycode",
			AuthID:   auth.ID,
		})
	}()

	return &cliproxyexecutor.StreamResult{
		Headers: httpResp.Header,
		Chunks:  chunks,
	}, nil
}

func (e *JoyCodeExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, fmt.Errorf("joycode: token counting not supported")
}

func (e *JoyCodeExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return auth, nil
}

func buildJoyCodePayload(openaiPayload []byte, modelName string, auth *cliproxyauth.Auth) []byte {
	var payload map[string]interface{}
	if err := json.Unmarshal(openaiPayload, &payload); err != nil {
		log.Warnf("joycode: failed to parse payload, passing through: %v", err)
		return openaiPayload
	}

	payload["model"] = modelName
	payload["stream_options"] = map[string]interface{}{"include_usage": true}

	if _, ok := payload["thinking"]; !ok {
		payload["thinking"] = map[string]interface{}{"type": "disabled"}
	}

	tenant := ""
	userId := ""
	if auth != nil && auth.Metadata != nil {
		if t, ok := auth.Metadata["tenant"].(string); ok {
			tenant = t
		}
		if u, ok := auth.Metadata["userId"].(string); ok {
			userId = u
		}
	}
	payload["tenant"] = tenant
	payload["userId"] = userId
	payload["client"] = "JoyCode"
	payload["clientVersion"] = "2.4.8"
	payload["language"] = "text"
	payload["scene"] = "chat"
	payload["source"] = "joyCoderFe"

	result, err := json.Marshal(payload)
	if err != nil {
		log.Errorf("joycode: failed to marshal payload: %v", err)
		return openaiPayload
	}
	result = util.CleanupOrphanedRequiredInTools(result)
	return result
}

func generateJoyCodeRequestID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%032d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
