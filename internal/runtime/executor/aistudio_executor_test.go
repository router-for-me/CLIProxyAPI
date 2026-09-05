package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/wsrelay"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestAIStudioTranslateRequestPreservesSummaryFromOriginalRequest(t *testing.T) {
	executor := NewAIStudioExecutor(&config.Config{}, "aistudio", nil)
	req := cliproxyexecutor.Request{
		Model:   "gemini-3.6-flash",
		Payload: []byte(`{"model":"gemini-3.6-flash","input":"hi"}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		OriginalRequest: []byte(`{"model":"gemini-3.6-flash","reasoning":{"summary":"auto"},"input":"hi"}`),
	}
	payload, _, err := executor.translateRequest(context.Background(), req, opts, false)
	if err != nil {
		t.Fatalf("translateRequest() error = %v", err)
	}
	if !gjson.GetBytes(payload, "generationConfig.thinkingConfig.includeThoughts").Bool() {
		t.Fatalf("original request summary intent was lost: %s", payload)
	}
}

func TestAIStudioTranslateRequestNormalizesThinkingLevel(t *testing.T) {
	executor := NewAIStudioExecutor(&config.Config{}, "aistudio", nil)
	req := cliproxyexecutor.Request{
		Model: "gemini-3.7-flash",
		Payload: []byte(`{
			"contents":[{"role":"user","parts":[{"text":"Human: Hi"}]}],
			"model":"gemini-3.7-flash",
			"generationConfig":{"thinkingConfig":{"thinkingLevel":"high","includeThoughts":true}}
		}`),
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini}

	payload, _, err := executor.translateRequest(context.Background(), req, opts, false)
	if err != nil {
		t.Fatalf("translateRequest() error = %v", err)
	}
	if got := gjson.GetBytes(payload, "generationConfig.thinkingConfig.thinkingLevel").String(); got != "HIGH" {
		t.Fatalf("thinkingLevel = %q, want HIGH; payload=%s", got, payload)
	}
	if !gjson.GetBytes(payload, "generationConfig.thinkingConfig.includeThoughts").Bool() {
		t.Fatalf("includeThoughts was lost: %s", payload)
	}
}

func TestAIStudioTranslateRequestNormalizesThinkingLevelAfterPayloadOverride(t *testing.T) {
	executor := NewAIStudioExecutor(&config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{
		Models: []config.PayloadModelRule{{Name: "gemini-3.7-flash", Protocol: "gemini"}},
		Params: map[string]any{"generationConfig.thinkingConfig.thinkingLevel": "medium"},
	}}}}, "aistudio", nil)
	req := cliproxyexecutor.Request{
		Model:   "gemini-3.7-flash",
		Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"model":"gemini-3.7-flash"}`),
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini}

	payload, _, err := executor.translateRequest(context.Background(), req, opts, false)
	if err != nil {
		t.Fatalf("translateRequest() error = %v", err)
	}
	if got := gjson.GetBytes(payload, "generationConfig.thinkingConfig.thinkingLevel").String(); got != "MEDIUM" {
		t.Fatalf("thinkingLevel = %q, want MEDIUM; payload=%s", got, payload)
	}
}

func TestNormalizeAIStudioThinkingLevel(t *testing.T) {
	tests := []struct {
		name      string
		levelJSON string
		want      string
		unchanged bool
	}{
		{name: "minimal", levelJSON: `"minimal"`, want: "MINIMAL"},
		{name: "low", levelJSON: `"low"`, want: "LOW"},
		{name: "medium", levelJSON: `"medium"`, want: "MEDIUM"},
		{name: "mixed case high", levelJSON: `"hIgH"`, want: "HIGH"},
		{name: "already uppercase", levelJSON: `"HIGH"`, want: "HIGH", unchanged: true},
		{name: "none", levelJSON: `"none"`, want: "none", unchanged: true},
		{name: "xhigh", levelJSON: `"xhigh"`, want: "xhigh", unchanged: true},
		{name: "max", levelJSON: `"max"`, want: "max", unchanged: true},
		{name: "custom", levelJSON: `"custom"`, want: "custom", unchanged: true},
		{name: "non-string", levelJSON: `1`, want: "1", unchanged: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := []byte(fmt.Sprintf(`{"generationConfig":{"thinkingConfig":{"thinkingLevel":%s}},"marker":true}`, test.levelJSON))
			output := normalizeAIStudioThinkingLevel(input)
			level := gjson.GetBytes(output, "generationConfig.thinkingConfig.thinkingLevel")
			if got := level.String(); got != test.want {
				t.Fatalf("thinkingLevel = %q, want %q; payload=%s", got, test.want, output)
			}
			if test.unchanged && string(output) != string(input) {
				t.Fatalf("payload changed from %s to %s", input, output)
			}
		})
	}
}

func TestAIStudioTranslateRequestPrependsLeadingUserForIssue4959ResponsesHistory(t *testing.T) {
	executor := NewAIStudioExecutor(&config.Config{}, "aistudio", nil)
	_, body, err := executor.translateRequest(context.Background(), cliproxyexecutor.Request{
		Model:   "gemini-3.7-flash-high",
		Payload: issue4959ResponsesModelFirstPayload(),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}, false)
	if err != nil {
		t.Fatalf("translateRequest() error = %v", err)
	}
	assertIssue4959LeadingUserContents(t, gjson.GetBytes(body.payload, "contents").Array())
}

func TestAIStudioExecutorWithoutRelaySessionDoesNotMarkUpstreamAttempt(t *testing.T) {
	const authID = "aistudio-not-connected"
	relay := wsrelay.NewManager(wsrelay.Options{})
	exec := NewAIStudioExecutor(&config.Config{}, "aistudio", relay)
	auth := &cliproxyauth.Auth{ID: authID, Provider: "aistudio"}
	req := cliproxyexecutor.Request{
		Model:   "gemini-3.1-pro-preview",
		Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`),
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini}

	tests := []struct {
		name string
		run  func(context.Context) error
	}{
		{
			name: "HTTP request",
			run: func(ctx context.Context) error {
				httpReq, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.com/generate", strings.NewReader(`{"contents":[]}`))
				if errRequest != nil {
					return errRequest
				}
				_, errRequest = exec.HttpRequest(ctx, auth, httpReq)
				return errRequest
			},
		},
		{
			name: "execute",
			run: func(ctx context.Context) error {
				_, errExecute := exec.Execute(ctx, auth, req, opts)
				return errExecute
			},
		},
		{
			name: "stream",
			run: func(ctx context.Context) error {
				_, errStream := exec.ExecuteStream(ctx, auth, req, opts)
				return errStream
			},
		},
		{
			name: "count tokens",
			run: func(ctx context.Context) error {
				_, errCount := exec.CountTokens(ctx, auth, req, opts)
				return errCount
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := cliproxyexecutor.WithUpstreamAttemptTracker(context.Background())
			errRun := test.run(ctx)
			if errRun == nil || !strings.Contains(errRun.Error(), "not connected") {
				t.Fatalf("request error = %v, want provider not connected", errRun)
			}
			if cliproxyexecutor.UpstreamAttempted(ctx) {
				t.Fatal("missing relay session was marked as an upstream attempt")
			}
		})
	}
}

func TestAIStudioExecutorExecuteStartsTTFTBeforeRelayWait(t *testing.T) {
	const authID = "aistudio-ttft-auth"
	delay := 40 * time.Millisecond
	connected := make(chan struct{})
	var connectedOnce sync.Once
	relay := wsrelay.NewManager(wsrelay.Options{
		ProviderFactory: func(*http.Request) (string, error) {
			return authID, nil
		},
		OnConnected: func(provider string) {
			if provider == authID {
				connectedOnce.Do(func() {
					close(connected)
				})
			}
		},
	})
	server := httptest.NewServer(relay.Handler())
	defer server.Close()
	defer func() {
		if errStop := relay.Stop(context.Background()); errStop != nil {
			t.Errorf("relay stop error = %v", errStop)
		}
	}()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + relay.Path()
	conn, _, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
	if errDial != nil {
		t.Fatalf("dial websocket: %v", errDial)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			t.Errorf("websocket close error = %v", errClose)
		}
	}()
	select {
	case <-connected:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for relay connection")
	}

	clientDone := make(chan error, 1)
	go func() {
		var msg wsrelay.Message
		if errReadJSON := conn.ReadJSON(&msg); errReadJSON != nil {
			clientDone <- fmt.Errorf("read relay request: %w", errReadJSON)
			return
		}
		if msg.Type != wsrelay.MessageTypeHTTPReq {
			clientDone <- fmt.Errorf("relay message type = %q, want %q", msg.Type, wsrelay.MessageTypeHTTPReq)
			return
		}
		time.Sleep(delay)
		response := wsrelay.Message{
			ID:   msg.ID,
			Type: wsrelay.MessageTypeHTTPResp,
			Payload: map[string]any{
				"status":  float64(http.StatusOK),
				"headers": map[string]any{"Content-Type": "application/json"},
				"body":    `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`,
			},
		}
		if errWriteJSON := conn.WriteJSON(response); errWriteJSON != nil {
			clientDone <- fmt.Errorf("write relay response: %w", errWriteJSON)
			return
		}
		clientDone <- nil
	}()

	plugin := &captureAIStudioUsagePlugin{records: make(chan usage.Record, 16)}
	usage.RegisterPlugin(plugin)
	exec := NewAIStudioExecutor(&config.Config{}, "aistudio", relay)
	ctx := cliproxyexecutor.WithUpstreamAttemptTracker(context.Background())
	_, errExecute := exec.Execute(ctx, &cliproxyauth.Auth{ID: authID, Provider: "aistudio"}, cliproxyexecutor.Request{
		Model:   "gemini-3.1-pro-preview",
		Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if !cliproxyexecutor.UpstreamAttempted(ctx) {
		t.Fatal("relay request did not mark an upstream attempt")
	}
	if errClient := <-clientDone; errClient != nil {
		t.Fatal(errClient)
	}

	record := waitForAIStudioUsageRecord(t, plugin.records, "gemini-3.1-pro-preview")
	if record.TTFT < delay {
		t.Fatalf("ttft = %v, want >= %v", record.TTFT, delay)
	}
}

type captureAIStudioUsagePlugin struct {
	records chan usage.Record
}

func (p *captureAIStudioUsagePlugin) HandleUsage(_ context.Context, record usage.Record) {
	if p == nil {
		return
	}
	select {
	case p.records <- record:
	default:
	}
}

func waitForAIStudioUsageRecord(t *testing.T, records <-chan usage.Record, model string) usage.Record {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case record := <-records:
			if record.Provider == "aistudio" && record.Model == model {
				return record
			}
		case <-timeout:
			t.Fatalf("timed out waiting for AI Studio usage record")
		}
	}
}
