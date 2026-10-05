package helps

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"io"
	"net/http"
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

type accountingTestSink struct{ events chan usage.AccountingEvent }

func (s *accountingTestSink) HandleAccountingEvent(event usage.AccountingEvent) {
	select {
	case s.events <- event:
	default:
	}
}

func TestAccountingAttemptOutcomes(t *testing.T) {
	previous := redisqueue.UsageStatisticsEnabled()
	redisqueue.SetUsageStatisticsEnabled(false)
	t.Cleanup(func() { redisqueue.SetUsageStatisticsEnabled(previous) })
	sink := &accountingTestSink{events: make(chan usage.AccountingEvent, 16)}
	usage.RegisterAccountingSink(t.Name(), sink)
	ctx := usage.WithTraceID(context.Background(), "parent")
	ctx = usage.WithRequestedModelAlias(ctx, "client-alias")
	seen := make(map[string]bool)
	for _, provider := range []string{"openai", "codex", "xai"} {
		for _, account := range []string{"first", "failover"} {
			reporter := NewUsageReporter(ctx, provider, "executed", &cliproxyauth.Auth{ID: account, Provider: provider})
			reporter.SetStream(provider != "openai")
			payload := []byte(`{"response":{"model":"served","usage":{"input_tokens":10,"output_tokens":6,"total_tokens":16,"output_tokens_details":{"reasoning_tokens":5},"input_tokens_details":{"cached_tokens":4}}}}`)
			if provider == "openai" {
				payload = []byte(`{"model":"served","usage":{"prompt_tokens":10,"completion_tokens":6,"total_tokens":16}}`)
			}
			reporter.ObserveResponseModel(payload)
			reporter.PublishFailure(ctx, errors.New("private-failure-body"))
			reporter.EnsurePublished(ctx)
			reporter.Publish(ctx, usage.Detail{TotalTokens: 999})
			select {
			case event := <-sink.events:
				if event.ExecutionID != reporter.RequestID() || seen[event.ExecutionID] || event.TraceID != "parent" || event.RequestedAlias != "client-alias" || event.Status != "failed" || event.Tokens == nil || event.Tokens.Breakdown.TotalTokens != 16 {
					t.Fatalf("event = %+v", event)
				}
				seen[event.ExecutionID] = true
			case <-time.After(5 * time.Second):
				t.Fatal("missing event")
			}
		}
	}
	// A marker proves all earlier publications have passed through the dispatcher.
	marker := NewUsageReporter(ctx, "openai", "marker", nil)
	marker.EnsurePublished(ctx)
	select {
	case event := <-sink.events:
		if event.ExecutionID != marker.RequestID() {
			t.Fatalf("duplicate event = %+v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("missing marker")
	}
}

func TestAccountingProviderEvidenceAndZero(t *testing.T) {
	for _, parse := range []func([]byte) usage.Detail{ParseOpenAIUsage, ParseClaudeUsage} {
		detail := parse([]byte(`{"usage":{"input_tokens":0,"output_tokens":0,"prompt":"private-prompt","total_tokens":0}}`))
		if !detail.UsagePresent || len(detail.TokenEvidence.Fields) != 3 {
			t.Fatalf("detail = %+v", detail)
		}
	}
	var buffer StreamUsageBuffer
	buffer.Observe(ParseOpenAIUsage([]byte(`{"usage":{"input_tokens":4,"output_tokens":2}}`)), true)
	buffer.Observe(ParseOpenAIUsage([]byte(`{"service_tier":"default","usage":{"input_tokens":0,"output_tokens":0}}`)), true)
	detail, _ := buffer.Detail()
	if !detail.UsagePresent || detail.InputTokens != 0 || detail.OutputTokens != 0 {
		t.Fatalf("explicit zero replaced: %+v", detail)
	}
}

type accountingRetryExecutor struct {
	failure  error
	accounts []string
	ids      []string
}

func (*accountingRetryExecutor) Identifier() string { return "codex" }
func (e *accountingRetryExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	reporter := NewExecutorUsageReporter(ctx, e, req.Model, auth)
	e.accounts = append(e.accounts, auth.ID)
	e.ids = append(e.ids, reporter.RequestID())
	if len(e.ids) == 1 {
		reporter.PublishFailure(ctx, e.failure)
		return cliproxyexecutor.Response{}, e.failure
	}
	reporter.Publish(ctx, ParseOpenAIUsage([]byte(`{"usage":{"input_tokens":0,"output_tokens":0}}`)))
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}
func (*accountingRetryExecutor) ExecuteStream(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, errors.New("unused")
}
func (*accountingRetryExecutor) CountTokens(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("unused")
}
func (*accountingRetryExecutor) Refresh(_ context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return auth, nil
}
func (*accountingRetryExecutor) HttpRequest(context.Context, *cliproxyauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("unused")
}

func TestAccountingConductorRetryAndAccountFailover(t *testing.T) {
	for _, failover := range []bool{false, true} {
		t.Run(fmt.Sprintf("failover_%t", failover), func(t *testing.T) {
			manager := cliproxyauth.NewManager(nil, nil, nil)
			manager.SetRetryConfig(1, 0, 0)
			failure := error(io.ErrUnexpectedEOF)
			if failover {
				failure = usageResponseBodyError{status: 401, body: []byte(`{"error":"private-failure"}`)}
			}
			executor := &accountingRetryExecutor{failure: failure}
			manager.RegisterExecutor(executor)
			model := "accounting-" + uuid.NewString()
			accountCount := 1
			if failover {
				accountCount = 2
			}
			for i := 0; i < accountCount; i++ {
				id := uuid.NewString()
				registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
				if _, err := manager.Register(context.Background(), &cliproxyauth.Auth{ID: id, Provider: "codex"}); err != nil {
					t.Fatal(err)
				}
			}
			sink := &accountingTestSink{events: make(chan usage.AccountingEvent, 4)}
			usage.RegisterAccountingSink("conductor", sink)
			trace := uuid.NewString()
			ctx := usage.WithTraceID(context.Background(), trace)
			if _, err := manager.Execute(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); err != nil {
				t.Fatal(err)
			}
			if len(executor.ids) != 2 || executor.ids[0] == executor.ids[1] {
				t.Fatalf("execution IDs = %v", executor.ids)
			}
			if (executor.accounts[0] != executor.accounts[1]) != failover {
				t.Fatalf("accounts = %v", executor.accounts)
			}
			for i := 0; i < 2; i++ {
				select {
				case event := <-sink.events:
					if event.ExecutionID != executor.ids[i] || event.TraceID != trace {
						t.Fatalf("event = %+v", event)
					}
					if i == 0 && (event.Status != "failed" || event.Tokens != nil) {
						t.Fatalf("failure = %+v", event)
					}
					if i == 1 && (event.Status != "succeeded" || event.Tokens == nil || event.Tokens.Total != 0) {
						t.Fatalf("success = %+v", event)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("missing accounting event")
				}
			}
		})
	}
}

func TestAccountingInterruptedAttemptRetainsUsage(t *testing.T) {
	sink := &accountingTestSink{events: make(chan usage.AccountingEvent, 1)}
	usage.RegisterAccountingSink(t.Name(), sink)
	ctx, cancel := context.WithCancel(context.Background())
	reporter := NewUsageReporter(ctx, "codex", "gpt-5.5", nil)
	reporter.ObserveResponseModel([]byte(`{"type":"response.in_progress","response":{"usage":{"input_tokens":10,"output_tokens":2}}}`))
	cancel()
	reporter.PublishFailure(ctx, ctx.Err())
	select {
	case event := <-sink.events:
		if event.Status != "interrupted" || event.Tokens == nil || event.Tokens.Breakdown.TotalTokens != 12 {
			t.Fatalf("event = %+v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("missing interrupted event")
	}
}

func TestAccountingClaudeUsageMergePreservesEvidenceAndZero(t *testing.T) {
	reporter := NewUsageReporter(context.Background(), "claude", "claude", nil)
	reporter.ObserveResponseModel([]byte(`{"type":"message_start","message":{"usage":{"input_tokens":10,"cache_read_input_tokens":4,"output_tokens":1}}}`))
	reporter.ObserveResponseModel([]byte(`{"type":"message_delta","usage":{"output_tokens":0}}`))
	detail := reporter.observed
	if !detail.UsagePresent || detail.InputTokens != 10 || detail.OutputTokens != 0 || detail.TokenBreakdown.TotalTokens != 14 || detail.TokenEvidence.Fields["output_tokens"] != 0 {
		t.Fatalf("detail = %+v", detail)
	}
}
