package helps

import (
	"context"
	"strings"
	"sync"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func TestExtractCodexResponseModel(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    string
	}{
		{
			name:    "sse response created",
			payload: `data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.6-luna"}}`,
			want:    "gpt-5.6-luna",
		},
		{
			name:    "sse response created without space after data prefix",
			payload: `data:{"type":"response.created","response":{"model":"gpt-6-astra"}}`,
			want:    "gpt-6-astra",
		},
		{
			name:    "raw json response completed",
			payload: `{"type":"response.completed","response":{"model":"gpt-5.6-luna","usage":{"total_tokens":12}}}`,
			want:    "gpt-5.6-luna",
		},
		{
			name:    "response in progress",
			payload: `{"type":"response.in_progress","response":{"model":"gpt-5.6-terra"}}`,
			want:    "gpt-5.6-terra",
		},
		{
			name:    "response incomplete",
			payload: `{"type":"response.incomplete","response":{"model":"gpt-5.6-sol"}}`,
			want:    "gpt-5.6-sol",
		},
		{
			name:    "response done",
			payload: `{"type":"response.done","response":{"model":"gpt-5.3-codex-spark"}}`,
			want:    "gpt-5.3-codex-spark",
		},
		{
			name:    "model value is trimmed",
			payload: `{"type":"response.created","response":{"model":"  gpt-6-astra  "}}`,
			want:    "gpt-6-astra",
		},
		{
			name:    "output text delta is ignored",
			payload: `data: {"type":"response.output_text.delta","delta":"hi","response":{"model":"gpt-5.6-luna"}}`,
			want:    "",
		},
		{
			name:    "output item done is ignored",
			payload: `{"type":"response.output_item.done","item":{"type":"message"}}`,
			want:    "",
		},
		{
			name:    "rate limits event is ignored",
			payload: `{"type":"codex.rate_limits","rate_limits":{"primary":{"used_percent":12}}}`,
			want:    "",
		},
		{
			name:    "terminal failure event is ignored",
			payload: `{"type":"error","error":{"code":"server_is_overloaded"}}`,
			want:    "",
		},
		{
			// /responses/compact answers with a compaction object that carries no
			// event type and no response.model, so that route reports nothing.
			name:    "responses compact object",
			payload: `{"id":"resp_1","object":"response.compaction","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`,
			want:    "",
		},
		{
			// The direct /images/generations and /images/edits endpoints answer in the
			// Images API shape, which never names the model that served the request.
			name:    "images api response",
			payload: `{"created":1745539200,"data":[{"b64_json":"aGk="}]}`,
			want:    "",
		},
		{
			// Direct image streams emit image_generation.* events only.
			name:    "image generation completed event",
			payload: `data: {"type":"image_generation.completed","b64_json":"aGk="}`,
			want:    "",
		},
		{
			name:    "non string model value",
			payload: `{"type":"response.created","response":{"model":123}}`,
			want:    "",
		},
		{
			name:    "object model value",
			payload: `{"type":"response.created","response":{"model":{"id":"gpt-5.6-luna"}}}`,
			want:    "",
		},
		{
			name:    "oversized model value",
			payload: `{"type":"response.created","response":{"model":"` + strings.Repeat("m", maxResponseModelLength+1) + `"}}`,
			want:    "",
		},
		{
			name:    "model value at the length limit",
			payload: `{"type":"response.created","response":{"model":"` + strings.Repeat("m", maxResponseModelLength) + `"}}`,
			want:    strings.Repeat("m", maxResponseModelLength),
		},
		{
			name:    "done marker",
			payload: "data: [DONE]",
			want:    "",
		},
		{
			name:    "sse event line",
			payload: "event: response.created",
			want:    "",
		},
		{
			name:    "empty payload",
			payload: "",
			want:    "",
		},
		{
			name:    "malformed json",
			payload: `{"type":"response.created","response":{"model":"gpt-5.6-luna"`,
			want:    "",
		},
		{
			name:    "response without model",
			payload: `{"type":"response.created","response":{"id":"resp_1"}}`,
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, _ := extractCodexResponseModelEvent([]byte(tt.payload)); got != tt.want {
				t.Fatalf("extractCodexResponseModelEvent(%q) = %q, want %q", tt.payload, got, tt.want)
			}
		})
	}
}

func TestIsModelSubstituted(t *testing.T) {
	tests := []struct {
		name      string
		requested string
		served    string
		want      bool
	}{
		{name: "silent substitution", requested: "gpt-6-astra", served: "gpt-5.6-luna", want: true},
		{name: "same model", requested: "gpt-6-astra", served: "gpt-6-astra", want: false},
		{name: "thinking suffix stripped from requested", requested: "gpt-6-astra(high)", served: "gpt-6-astra", want: false},
		{name: "thinking suffix stripped from served", requested: "gpt-6-astra", served: "gpt-6-astra(high)", want: false},
		{name: "thinking suffix on both sides", requested: "gpt-6-astra(high)", served: "gpt-6-astra(low)", want: false},
		{name: "thinking suffix with substitution", requested: "gpt-6-astra(high)", served: "gpt-5.6-luna", want: true},
		{name: "numeric thinking suffix", requested: "gpt-5.6-terra(16384)", served: "gpt-5.6-terra", want: false},
		{name: "case insensitive requested", requested: "GPT-6-Astra", served: "gpt-6-astra", want: false},
		{name: "case insensitive served", requested: "gpt-6-astra", served: "  GPT-6-ASTRA  ", want: false},
		{name: "served pins dashed date", requested: "gpt-5.6-terra", served: "gpt-5.6-terra-2026-05-13", want: false},
		{name: "served pins compact date", requested: "gpt-5.6-sol", served: "gpt-5.6-sol-20260513", want: false},
		{name: "requested pins dashed date", requested: "gpt-5.6-terra-2026-05-13", served: "gpt-5.6-terra", want: false},
		{name: "requested pins compact date", requested: "gpt-5.6-sol-20260513", served: "gpt-5.6-sol", want: false},
		{name: "different dates on both sides", requested: "gpt-5.6-terra-2026-05-13", served: "gpt-5.6-terra-2026-06-01", want: true},
		{name: "incomplete date suffix", requested: "gpt-5.6-sol", served: "gpt-5.6-sol-2026-5-13", want: true},
		{name: "non date suffix", requested: "gpt-5.5", served: "gpt-5.5-codex", want: true},
		{name: "date suffix with trailing tag", requested: "gpt-5.6-luna", served: "gpt-5.6-luna-2026-05-13-preview", want: true},
		{name: "spark unchanged", requested: "gpt-5.3-codex-spark", served: "gpt-5.3-codex-spark", want: false},
		{name: "review model unchanged", requested: "codex-auto-review", served: "codex-auto-review", want: false},
		{name: "review model substituted", requested: "codex-auto-review", served: "gpt-5.6-luna", want: true},
		{name: "empty served", requested: "gpt-6-astra", served: "", want: false},
		{name: "blank served", requested: "gpt-6-astra", served: "   ", want: false},
		{name: "empty requested", requested: "", served: "gpt-5.6-luna", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsModelSubstituted(tt.requested, tt.served); got != tt.want {
				t.Fatalf("IsModelSubstituted(%q, %q) = %v, want %v", tt.requested, tt.served, got, tt.want)
			}
		})
	}
}

// setupResponseModelLoggerHook captures warnings from the process-global logrus logger.
func setupResponseModelLoggerHook(t *testing.T) *logtest.Hook {
	t.Helper()
	logger := log.StandardLogger()
	oldLevel := logger.GetLevel()
	logger.SetLevel(log.WarnLevel)
	savedHooks := logger.ReplaceHooks(make(log.LevelHooks))
	hook := new(logtest.Hook)
	logger.AddHook(hook)
	t.Cleanup(func() {
		logger.SetLevel(oldLevel)
		logger.ReplaceHooks(savedHooks)
	})
	return hook
}

// codexUsageTestExecutor mirrors the production reporter construction path.
type codexUsageTestExecutor struct{}

func (codexUsageTestExecutor) Identifier() string { return "codex" }

func newCodexTestReporter(ctx context.Context, model string, auth *cliproxyauth.Auth) *UsageReporter {
	return NewExecutorUsageReporter(ctx, codexUsageTestExecutor{}, model, auth)
}

func substitutionWarnings(hook *logtest.Hook) []string {
	var messages []string
	for _, entry := range hook.AllEntries() {
		if entry.Level == log.WarnLevel && strings.Contains(entry.Message, "upstream served model") {
			messages = append(messages, entry.Message)
		}
	}
	return messages
}

const codexSubstitutedStream = `data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.6-luna"}}
data: {"type":"response.in_progress","response":{"id":"resp_1","model":"gpt-5.6-luna"}}
data: {"type":"response.output_text.delta","delta":"hello"}
data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.6-luna","usage":{"input_tokens":5,"output_tokens":7,"total_tokens":12}}}
data: [DONE]`

func newSubstitutionTestAuth() *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       "codex-auth-1",
		Index:    "auth-index-7",
		Provider: "codex",
		FileName: "/auths/codex-user@example.com.json",
	}
}

// Relies on the global logrus logger and warning throttle; do not add t.Parallel.
func TestUsageReporterRecordsSubstitutedCodexResponseModelAndWarnsOnce(t *testing.T) {
	hook := setupResponseModelLoggerHook(t)
	ctx := context.Background()
	reporter := newCodexTestReporter(ctx, "gpt-6-astra", newSubstitutionTestAuth())

	var detail usage.Detail
	for _, line := range strings.Split(codexSubstitutedStream, "\n") {
		reporter.ObserveResponseModel([]byte(line))
		if parsed, ok := ParseCodexUsage(JSONPayload([]byte(line))); ok {
			detail = parsed
		}
	}

	if got := reporter.ResponseModel(); got != "gpt-5.6-luna" {
		t.Fatalf("reporter response model = %q, want %q", got, "gpt-5.6-luna")
	}
	record := reporter.buildRecord(detail, false)
	if record.ResponseModel != "gpt-5.6-luna" {
		t.Fatalf("record response model = %q, want %q", record.ResponseModel, "gpt-5.6-luna")
	}
	if record.Model != "gpt-6-astra" {
		t.Fatalf("record model = %q, want %q", record.Model, "gpt-6-astra")
	}
	// Observing events must not log anything; the warning belongs to the attempt.
	if warnings := substitutionWarnings(hook); len(warnings) != 0 {
		t.Fatalf("expected no warning before the attempt published, got %#v", warnings)
	}

	reporter.Publish(ctx, detail)
	reporter.EnsurePublished(ctx)

	warnings := substitutionWarnings(hook)
	if len(warnings) != 1 {
		t.Fatalf("substitution warnings = %d, want 1: %#v", len(warnings), warnings)
	}
	want := `codex executor: upstream served model "gpt-5.6-luna" for requested model "gpt-6-astra" (auth_index=auth-index-7)`
	if warnings[0] != want {
		t.Fatalf("warning = %q, want %q", warnings[0], want)
	}
	// The credential file name carries the account e-mail and must stay out of logs.
	if strings.Contains(warnings[0], "example.com") || strings.Contains(warnings[0], "auth_file") {
		t.Fatalf("warning leaked credential details: %q", warnings[0])
	}
}

// Relies on the global logrus logger; do not add t.Parallel.
func TestUsageReporterDoesNotWarnWhenCodexResponseModelMatches(t *testing.T) {
	hook := setupResponseModelLoggerHook(t)
	ctx := context.Background()
	reporter := newCodexTestReporter(ctx, "gpt-6-astra(high)", nil)

	reporter.ObserveResponseModel([]byte(`data: {"type":"response.created","response":{"model":"gpt-6-astra-2026-05-13"}}`))
	reporter.Publish(ctx, usage.Detail{TotalTokens: 3})

	if got := reporter.ResponseModel(); got != "gpt-6-astra-2026-05-13" {
		t.Fatalf("reporter response model = %q, want %q", got, "gpt-6-astra-2026-05-13")
	}
	if warnings := substitutionWarnings(hook); len(warnings) != 0 {
		t.Fatalf("expected no substitution warning, got %#v", warnings)
	}
}

// Relies on the global logrus logger; do not add t.Parallel.
func TestUsageReporterIgnoresPayloadsWithoutResponseModel(t *testing.T) {
	hook := setupResponseModelLoggerHook(t)
	ctx := context.Background()
	reporter := newCodexTestReporter(ctx, "gpt-6-astra", nil)

	reporter.ObserveResponseModel([]byte(`data: {"type":"response.created","response":{"model":"gpt-5.6-luna"}}`))
	reporter.ObserveResponseModel([]byte(`data: {"type":"response.output_text.delta","delta":"hello"}`))
	reporter.Publish(ctx, usage.Detail{TotalTokens: 3})

	if got := reporter.ResponseModel(); got != "gpt-5.6-luna" {
		t.Fatalf("reporter response model = %q, want %q", got, "gpt-5.6-luna")
	}
	warnings := substitutionWarnings(hook)
	if len(warnings) != 1 {
		t.Fatalf("substitution warnings = %d, want 1: %#v", len(warnings), warnings)
	}
	// A reporter without a credential must still produce a usable label.
	if !strings.HasSuffix(warnings[0], "(auth_index=nil)") {
		t.Fatalf("warning = %q, want an auth_index=nil suffix", warnings[0])
	}
}

// Relies on the global logrus logger; do not add t.Parallel.
func TestUsageReporterWarnsOnceUnderConcurrentObservationsAndPublishes(t *testing.T) {
	hook := setupResponseModelLoggerHook(t)
	ctx := context.Background()
	reporter := newCodexTestReporter(ctx, "gpt-6-astra", newSubstitutionTestAuth())
	created := []byte(`data: {"type":"response.created","response":{"model":"gpt-5.6-luna"}}`)
	completed := []byte(`data: {"type":"response.completed","response":{"model":"gpt-5.6-luna","usage":{"total_tokens":4}}}`)

	const workers = 32
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := range workers {
		go func(worker int) {
			defer wg.Done()
			<-start
			if worker%2 == 0 {
				reporter.ObserveResponseModel(created)
			} else {
				reporter.ObserveResponseModel(completed)
			}
			reporter.EnsurePublished(ctx)
		}(i)
	}
	close(start)
	wg.Wait()

	if got := reporter.ResponseModel(); got != "gpt-5.6-luna" {
		t.Fatalf("reporter response model = %q, want %q", got, "gpt-5.6-luna")
	}
	if warnings := substitutionWarnings(hook); len(warnings) != 1 {
		t.Fatalf("substitution warnings = %d, want 1: %#v", len(warnings), warnings)
	}
}

// Relies on the global logrus logger; do not add t.Parallel.
func TestUsageReporterEmitsWarningOnRepeatedSubstitutionsWithoutThrottle(t *testing.T) {
	hook := setupResponseModelLoggerHook(t)
	ctx := context.Background()

	publishSubstitutedAttempt := func(authID, authIndex string) {
		auth := &cliproxyauth.Auth{ID: authID, Index: authIndex, Provider: "codex"}
		reporter := newCodexTestReporter(ctx, "gpt-6-astra", auth)
		reporter.ObserveResponseModel([]byte(`{"type":"response.completed","response":{"model":"gpt-5.6-luna"}}`))
		reporter.Publish(ctx, usage.Detail{TotalTokens: 3})
	}

	publishSubstitutedAttempt("codex-auth-1", "auth-index-7")
	publishSubstitutedAttempt("codex-auth-1", "auth-index-7")
	if warnings := substitutionWarnings(hook); len(warnings) != 2 {
		t.Fatalf("substitution warnings = %d, want 2 without throttle: %#v", len(warnings), warnings)
	}

	// A different credential is also recorded without throttle.
	publishSubstitutedAttempt("codex-auth-2", "auth-index-8")
	if warnings := substitutionWarnings(hook); len(warnings) != 3 {
		t.Fatalf("substitution warnings = %d, want 3: %#v", len(warnings), warnings)
	}
}

// Relies on the global logrus logger; do not add t.Parallel.
func TestUsageReporterEmitsWarningAcrossServedModelCaseWithoutThrottle(t *testing.T) {
	hook := setupResponseModelLoggerHook(t)
	ctx := context.Background()

	auth := &cliproxyauth.Auth{ID: "codex-auth-1", Index: "auth-index-7", Provider: "codex"}
	for _, served := range []string{"gpt-5.6-luna", "GPT-5.6-LUNA"} {
		reporter := newCodexTestReporter(ctx, "gpt-6-astra", auth)
		reporter.ObserveResponseModel([]byte(`{"type":"response.completed","response":{"model":"` + served + `"}}`))
		reporter.Publish(ctx, usage.Detail{TotalTokens: 3})
	}

	warnings := substitutionWarnings(hook)
	if len(warnings) != 2 {
		t.Fatalf("substitution warnings = %d, want 2 without throttle: %#v", len(warnings), warnings)
	}
}

func TestUsageReporterAdditionalModelRecordOmitsResponseModel(t *testing.T) {
	ctx := context.Background()
	reporter := newCodexTestReporter(ctx, "gpt-5.4-mini", nil)
	reporter.ObserveResponseModel([]byte(`data: {"type":"response.completed","response":{"model":"gpt-5.4-mini"}}`))

	mainRecord := reporter.buildRecord(usage.Detail{TotalTokens: 12}, false)
	if mainRecord.ResponseModel != "gpt-5.4-mini" {
		t.Fatalf("main record response model = %q, want %q", mainRecord.ResponseModel, "gpt-5.4-mini")
	}

	additionalRecord, ok := reporter.buildAdditionalModelRecord("gpt-image-1.5", usage.Detail{TotalTokens: 5})
	if !ok {
		t.Fatal("expected an additional model record")
	}
	if additionalRecord.Model != "gpt-image-1.5" {
		t.Fatalf("additional record model = %q, want %q", additionalRecord.Model, "gpt-image-1.5")
	}
	// The upstream response model describes the main text model, never the image
	// generation tool model, so consumers must not see a fake substitution here.
	if additionalRecord.ResponseModel != "" {
		t.Fatalf("additional record response model = %q, want empty", additionalRecord.ResponseModel)
	}
}
