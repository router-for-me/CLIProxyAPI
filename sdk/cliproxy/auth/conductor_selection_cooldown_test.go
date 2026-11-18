package auth

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestBuiltInSelectorCooldownErrorPreservesRouteModel(t *testing.T) {
	t.Parallel()

	const routeModel = "client-opus(high)"
	next := time.Now().Add(time.Hour)
	auth := &Auth{
		ID:             "cooling-auth",
		Unavailable:    true,
		NextRetryAfter: next,
		Quota: QuotaState{
			Exceeded:      true,
			NextRecoverAt: next,
		},
		ModelStates: map[string]*ModelState{
			"other-model": {Status: StatusActive},
		},
	}

	selectors := map[string]Selector{
		"round-robin":          &RoundRobinSelector{},
		"weighted-round-robin": &WeightedRoundRobinSelector{},
		"fill-first":           &FillFirstSelector{},
	}
	for name, selector := range selectors {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, errPick := selector.Pick(
				context.Background(),
				"mixed",
				selectionArgForSelector(selector, routeModel),
				cliproxyexecutor.Options{},
				[]*Auth{auth},
			)
			if errPick == nil {
				t.Fatal("Pick() error = nil, want model cooldown")
			}

			errPick = restoreModelCooldownErrorModel(errPick, routeModel)
			var cooldownErr *modelCooldownError
			if !errors.As(errPick, &cooldownErr) {
				t.Fatalf("Pick() error = %T, want *modelCooldownError", errPick)
			}
			if cooldownErr.model != routeModel {
				t.Fatalf("cooldown model = %q, want %q", cooldownErr.model, routeModel)
			}
		})
	}
}

func TestAvailableAuthsForRouteModel_AttachesUpstreamErrorWhenCandidatesCooling(t *testing.T) {
	t.Parallel()

	const routeModel = "gpt-5.6-sol"
	next := time.Now().Add(time.Minute)
	upstreamErrorMsg := `{"type":"error","code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later.","sequence_number":0}`
	auth := &Auth{
		ID:             "codex-auth-1",
		Provider:       "codex",
		Unavailable:    true,
		NextRetryAfter: next,
		Status:         StatusError,
		StatusMessage:  upstreamErrorMsg,
		Quota: QuotaState{
			Exceeded:      true,
			NextRecoverAt: next,
		},
		LastError: &Error{
			Code:       "auth_unavailable",
			Message:    upstreamErrorMsg,
			HTTPStatus: 503,
		},
		ModelStates: map[string]*ModelState{
			routeModel: {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: next,
				StatusMessage:  upstreamErrorMsg,
				Quota: QuotaState{
					Exceeded:      true,
					NextRecoverAt: next,
				},
				LastError: &Error{
					Code:       "auth_unavailable",
					Message:    upstreamErrorMsg,
					HTTPStatus: 503,
				},
			},
		},
	}

	manager := NewManager(nil, nil, nil)
	_, err := manager.availableAuthsForRouteModel([]*Auth{auth}, "codex", routeModel, time.Now())
	if err == nil {
		t.Fatal("expected error when all auths are cooling")
	}

	var cooldownErr *modelCooldownError
	if !errors.As(err, &cooldownErr) || cooldownErr == nil {
		t.Fatalf("expected *modelCooldownError, got %T (%v)", err, err)
	}
	cause := errors.Unwrap(err)
	if cause == nil {
		t.Fatal("expected unwrap cause from cooldown error")
	}
	if !strings.Contains(cause.Error(), "server_is_overloaded") {
		t.Fatalf("expected cause to contain server_is_overloaded, got %q", cause.Error())
	}
	if !strings.Contains(cooldownErr.Error(), "server_is_overloaded") {
		t.Fatalf("expected cooldown error string to contain server_is_overloaded, got %q", cooldownErr.Error())
	}
}

func TestAvailableAuthsForRouteModel_PicksLatestCandidateError(t *testing.T) {
	t.Parallel()

	const routeModel = "gpt-5.6-sol"
	now := time.Now()
	olderErr := "older error: quota exceeded"
	newerErr := "newer error: server is overloaded"

	auth1 := &Auth{
		ID:            "codex-auth-1",
		Provider:      "codex",
		Unavailable:   true,
		Status:        StatusError,
		StatusMessage: olderErr,
		UpdatedAt:     now.Add(-10 * time.Minute),
		ModelStates: map[string]*ModelState{
			routeModel: {
				Status:        StatusError,
				Unavailable:   true,
				StatusMessage: olderErr,
				UpdatedAt:     now.Add(-10 * time.Minute),
			},
		},
	}
	auth2 := &Auth{
		ID:            "codex-auth-2",
		Provider:      "codex",
		Unavailable:   true,
		Status:        StatusError,
		StatusMessage: newerErr,
		UpdatedAt:     now.Add(-10 * time.Second),
		ModelStates: map[string]*ModelState{
			routeModel: {
				Status:        StatusError,
				Unavailable:   true,
				StatusMessage: newerErr,
				UpdatedAt:     now.Add(-10 * time.Second),
			},
		},
	}

	manager := NewManager(nil, nil, nil)
	_, err := manager.availableAuthsForRouteModel([]*Auth{auth1, auth2}, "codex", routeModel, now)
	if err == nil {
		t.Fatal("expected error when all auths are unavailable")
	}

	cause := errors.Unwrap(err)
	if cause == nil {
		t.Fatal("expected unwrap cause from auth_unavailable error")
	}
	if !strings.Contains(cause.Error(), "newer error") {
		t.Fatalf("expected latest error (newer error), got %q", cause.Error())
	}
}

func TestAvailableAuthsForRouteModel_ModelErrorPrioritizedOverGlobalAuthError(t *testing.T) {
	t.Parallel()

	const routeModel = "gpt-5.6-sol"
	now := time.Now()
	modelErr := "model error: model rate limited"
	unrelatedGlobalErr := "global error: unrelated failure"

	auth := &Auth{
		ID:            "codex-auth-1",
		Provider:      "codex",
		Unavailable:   true,
		Status:        StatusError,
		StatusMessage: unrelatedGlobalErr,
		UpdatedAt:     now,
		ModelStates: map[string]*ModelState{
			routeModel: {
				Status:        StatusError,
				Unavailable:   true,
				StatusMessage: modelErr,
				UpdatedAt:     now.Add(-10 * time.Minute),
			},
		},
	}

	manager := NewManager(nil, nil, nil)
	_, err := manager.availableAuthsForRouteModel([]*Auth{auth}, "codex", routeModel, now)
	if err == nil {
		t.Fatal("expected error when auth is unavailable")
	}

	cause := errors.Unwrap(err)
	if cause == nil {
		t.Fatal("expected unwrap cause from auth_unavailable error")
	}
	if !strings.Contains(cause.Error(), "model rate limited") {
		t.Fatalf("expected model error to be prioritized over global auth error, got %q", cause.Error())
	}
}

func TestAvailableAuthsForRouteModel_FallbackToGlobalAuthErrorWhenNoModelState(t *testing.T) {
	t.Parallel()

	const routeModel = "gpt-5.6-sol"
	now := time.Now()
	globalErr := "global error: credential exhausted"

	auth := &Auth{
		ID:            "codex-auth-1",
		Provider:      "codex",
		Unavailable:   true,
		Status:        StatusError,
		StatusMessage: globalErr,
		UpdatedAt:     now,
	}

	manager := NewManager(nil, nil, nil)
	_, err := manager.availableAuthsForRouteModel([]*Auth{auth}, "codex", routeModel, now)
	if err == nil {
		t.Fatal("expected error when auth is unavailable")
	}

	cause := errors.Unwrap(err)
	if cause == nil {
		t.Fatal("expected unwrap cause from auth_unavailable error")
	}
	if !strings.Contains(cause.Error(), "credential exhausted") {
		t.Fatalf("expected fallback to global auth error, got %q", cause.Error())
	}
}

func TestAvailableAuthsForRouteModel_CrossCandidateModelErrorPrioritizedOverNewerAuthError(t *testing.T) {
	t.Parallel()

	const routeModel = "gpt-5.6-sol"
	now := time.Now()
	candidateAModelErr := "model error on candidate A: quota exceeded"
	candidateBGlobalErr := "global auth error on candidate B: connection reset"

	authA := &Auth{
		ID:            "codex-auth-A",
		Provider:      "codex",
		Unavailable:   true,
		Status:        StatusError,
		StatusMessage: "older global status A",
		UpdatedAt:     now.Add(-10 * time.Minute),
		ModelStates: map[string]*ModelState{
			routeModel: {
				Status:        StatusError,
				Unavailable:   true,
				StatusMessage: candidateAModelErr,
				UpdatedAt:     now.Add(-10 * time.Minute),
			},
		},
	}

	authB := &Auth{
		ID:            "codex-auth-B",
		Provider:      "codex",
		Unavailable:   true,
		Status:        StatusError,
		StatusMessage: candidateBGlobalErr,
		UpdatedAt:     now.Add(-10 * time.Second), // Newer timestamp, but only auth-level
	}

	manager := NewManager(nil, nil, nil)
	_, err := manager.availableAuthsForRouteModel([]*Auth{authA, authB}, "codex", routeModel, now)
	if err == nil {
		t.Fatal("expected error when all auths are unavailable")
	}

	cause := errors.Unwrap(err)
	if cause == nil {
		t.Fatal("expected unwrap cause from auth_unavailable error")
	}
	if !strings.Contains(cause.Error(), "quota exceeded") {
		t.Fatalf("expected candidate A model-scoped error to be strictly prioritized over candidate B auth-level error, got %q", cause.Error())
	}
}

func TestSafeResponseHeaders_IncludesModelCooldownRetryAfter(t *testing.T) {
	t.Parallel()

	err := newModelCooldownError("gpt-5.6-sol", "codex", 15*time.Second)
	headers := SafeResponseHeaders(err)
	if headers == nil {
		t.Fatal("SafeResponseHeaders(modelCooldownError) = nil, want http.Header")
	}
	if got := headers.Get("Retry-After"); got != "15" {
		t.Fatalf("Retry-After = %q, want 15", got)
	}
}

func TestMixedUnavailableErrorLocked_GlobalModelErrorPriorityAcrossShards(t *testing.T) {
	t.Parallel()

	const routeModel = "gpt-5.6-sol"
	now := time.Now()
	codexModelErr := "codex model error: rate limited"
	geminiAuthErr := "gemini global error: network timeout"

	registry.GetGlobalRegistry().RegisterClient("auth-codex", "codex", []*registry.ModelInfo{{ID: routeModel}})
	registry.GetGlobalRegistry().RegisterClient("auth-gemini", "gemini", []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient("auth-codex")
		registry.GetGlobalRegistry().UnregisterClient("auth-gemini")
	})

	manager := NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &Auth{
		ID:            "auth-codex",
		Provider:      "codex",
		Unavailable:   true,
		Status:        StatusError,
		StatusMessage: "older codex global status",
		UpdatedAt:     now.Add(-10 * time.Minute),
		ModelStates: map[string]*ModelState{
			routeModel: {
				Status:        StatusError,
				Unavailable:   true,
				StatusMessage: codexModelErr,
				UpdatedAt:     now.Add(-10 * time.Minute),
			},
		},
	}); err != nil {
		t.Fatalf("manager.Register(codex): %v", err)
	}

	if _, err := manager.Register(context.Background(), &Auth{
		ID:            "auth-gemini",
		Provider:      "gemini",
		Unavailable:   true,
		Status:        StatusError,
		StatusMessage: geminiAuthErr,
		UpdatedAt:     now.Add(-10 * time.Second), // Newer timestamp, but only auth-level on gemini
	}); err != nil {
		t.Fatalf("manager.Register(gemini): %v", err)
	}

	manager.syncScheduler()
	manager.scheduler.mu.Lock()
	err := manager.scheduler.mixedUnavailableErrorLocked([]string{"codex", "gemini"}, routeModel, nil)
	manager.scheduler.mu.Unlock()
	if err == nil {
		t.Fatal("expected error when all auths in mixed scheduler are unavailable")
	}

	cause := errors.Unwrap(err)
	if cause == nil {
		t.Fatal("expected unwrap cause from mixed unavailable error")
	}
	if !strings.Contains(cause.Error(), "rate limited") {
		t.Fatalf("expected codex model-level error to be prioritized across shards over gemini auth-level error, got %q", cause.Error())
	}
}

type nonComparableSliceError []string

func (e nonComparableSliceError) Error() string { return strings.Join(e, ", ") }

func TestErrorWithCause_IsDoesNotPanicOnNonComparableError(t *testing.T) {
	t.Parallel()

	cause := nonComparableSliceError{"err1", "err2"}
	wrapped := WithCause(&Error{Code: "auth_unavailable", Message: "no auth available"}, cause)
	target := nonComparableSliceError{"err1", "err2"}
	// Must not panic on non-comparable error target
	_ = errors.Is(wrapped, target)
}

func TestExtractUpstreamErrorSummary_AuthPackageDirectSanitization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		input        string
		wantMask     string
		wantExact    string
		forbiddenRaw string
	}{
		{
			name:         "authorization header with bearer redacted",
			input:        "authorization: Bearer abcdef+TOPSECRET==",
			wantMask:     "Authorization: [REDACTED]",
			forbiddenRaw: "TOPSECRET",
		},
		{
			name:         "authorization header with basic redacted",
			input:        "authorization: Basic my-secret-basic-auth",
			wantMask:     "Authorization: [REDACTED]",
			forbiddenRaw: "my-secret-basic-auth",
		},
		{
			name:         "authorization header with custom scheme redacted",
			input:        "authorization: ApiKey SUPERSECRET",
			wantMask:     "Authorization: [REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "authorization header with comma separated parameters redacted",
			input:        "Authorization: ApiKey first,SECONDSECRET",
			wantMask:     "Authorization: [REDACTED]",
			forbiddenRaw: "SECONDSECRET",
		},
		{
			name:         "authorization header with digest parameters redacted",
			input:        `Authorization: Digest username="Mufasa", realm="myrealm", nonce="NONCE", uri="/dir/index.html", response="SIG"`,
			wantMask:     "Authorization: [REDACTED]",
			forbiddenRaw: "NONCE",
		},
		{
			name:         "double quoted multi word password redacted",
			input:        `password="correct horse battery staple"`,
			wantMask:     `[REDACTED]`,
			forbiddenRaw: "correct horse battery staple",
		},
		{
			name:         "double quoted comma containing api key redacted",
			input:        `api_key="secret,value"`,
			wantMask:     `[REDACTED]`,
			forbiddenRaw: "secret,value",
		},
		{
			name:         "sk key redacted",
			input:        "invalid key sk-live-secret-key-123456",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "live-secret-key",
		},
		{
			name:         "user path redacted",
			input:        "open /Users/alice/configs/auth.json: permission denied",
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "alice",
		},
		{
			name:         "json message containing invalid token secret redacted",
			input:        `{"code":"oops","message":"invalid token SUPERSECRET"}`,
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "single quoted key kv redacted",
			input:        `'api_key'=SUPERSECRET`,
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "authorization equals bearer redacted",
			input:        `authorization=Bearer SUPERSECRET`,
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "unstructured invalid api key redacted",
			input:        "invalid API key SUPERSECRET",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "unstructured invalid access token redacted",
			input:        "invalid access token SUPERSECRET",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "unstructured socks5 proxy auth redacted",
			input:        "proxyconnect tcp: socks5://alice:PASSSECRET@proxy.internal:1080",
			wantMask:     "[REDACTED_AUTH]",
			forbiddenRaw: "PASSSECRET",
		},
		{
			name:         "unstructured signature url query redacted",
			input:        "request failed: https://example.com?sig=SUPERSECRET",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "json message containing invalid token secret with escaped quotes redacted",
			input:        `{"code":"oops","message":"invalid token \"SUPERSECRET\""}`,
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "json message containing password with escaped quotes redacted",
			input:        `{"code":"oops","message":"password=\"abc\\\"SUPERSECRET\""}`,
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "natural language api key redacted",
			input:        "upstream rejected API key: SUPERSECRET",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "natural language password is redacted",
			input:        "password is SUPERSECRET",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "mnt unix path redacted",
			input:        "open /mnt/secrets/alice: permission denied",
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "alice",
		},
		{
			name:         "windows path with spaces redacted",
			input:        `open C:\Users\Alice Smith\secret.txt: permission denied`,
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "Alice Smith",
		},
		{
			name:         "cookie header redacted",
			input:        "Cookie: sessionid=COOKIESECRET",
			wantMask:     "Cookie: [REDACTED]",
			forbiddenRaw: "COOKIESECRET",
		},
		{
			name:         "set-cookie header redacted",
			input:        "Set-Cookie: session=SETCOOKIESECRET",
			wantMask:     "Cookie: [REDACTED]",
			forbiddenRaw: "SETCOOKIESECRET",
		},
		{
			name:         "private key kv redacted",
			input:        "private_key=PRIVATEKEYSECRET",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "PRIVATEKEYSECRET",
		},
		{
			name:         "uri userinfo redacted",
			input:        "https://user:PASSSECRET@example.com/api",
			wantMask:     "https://[REDACTED_AUTH]@",
			forbiddenRaw: "PASSSECRET",
		},
		{
			name:         "workspace unix path redacted",
			input:        "/workspace/tenants/alice/oauth-cache",
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "alice",
		},
		{
			name:         "opt unix path redacted",
			input:        `open /opt/cli-proxy/auth/alice.json: failed`,
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "alice",
		},
		{
			name:         "windows path redacted",
			input:        `open C:\Users\alice\secret.txt: failed`,
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "alice",
		},
		{
			name:         "kv secret redacted",
			input:        "failed with api_key=secret-value-123 and token: my-secret-token",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "secret-value-123",
		},
		{
			name:         "incorrect api key provided redacted",
			input:        "Incorrect API key provided: SUPERSECRET",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "plural credentials redacted",
			input:        "credentials: SUPERSECRET",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "aws secret access key redacted",
			input:        "AWS_SECRET_ACCESS_KEY=SUPERSECRET",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "protocol relative uri userinfo redacted",
			input:        "//alice:PASSSECRET@example.com/api",
			wantMask:     "[REDACTED_AUTH]",
			forbiddenRaw: "PASSSECRET",
		},
		{
			name:         "run secrets unix path redacted",
			input:        "open /run/secrets/alice: permission denied",
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "alice",
		},
		{
			name:         "custom tenant unix path redacted",
			input:        "open /custom/tenant/alice: permission denied",
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "alice",
		},
		{
			name:         "windows unc path redacted",
			input:        `open \\server\share\alice\secret.txt: permission denied`,
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "alice",
		},
		{
			name:         "service key kv redacted",
			input:        "SERVICE_KEY=SUPERSECRET",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "openai key kv redacted",
			input:        "OPENAI_KEY=SUPERSECRET",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "x-key query param redacted",
			input:        "https://example.com?x-key=SUPERSECRET",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "SUPERSECRET",
		},
		{
			name:         "unix path with spaces redacted",
			input:        "open /custom/tenant/Alice Smith/secret.txt: denied",
			wantMask:     "[REDACTED_PATH]",
			wantExact:    "open [REDACTED_PATH]: denied",
			forbiddenRaw: "Smith",
		},
		{
			name:         "unix path with unicode redacted",
			input:        "open /custom/租户/alice: denied",
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "alice",
		},
		{
			name:         "single segment unix path redacted",
			input:        "open /alice: permission denied",
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "alice",
		},
		{
			name:         "stat single segment unicode path redacted",
			input:        "stat /客户: no such file or directory",
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "客户",
		},
		{
			name:         "quoted path with colon and secret redacted",
			input:        `open "/tmp/customer:TOPSECRET/creds": denied`,
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "TOPSECRET",
		},
		{
			name:         "unquoted multi-word password redacted",
			input:        "password = correct horse battery staple",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "battery staple",
		},
		{
			name:         "unquoted multi-word credentials redacted",
			input:        "credentials: alice secret",
			wantMask:     "[REDACTED]",
			forbiddenRaw: "alice secret",
		},
		{
			name:         "quoted path containing password assignment redacted",
			input:        `open "/Users/alice/password=foo/bar": denied`,
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "alice",
		},
		{
			name:         "unquoted path with colon and secret redacted",
			input:        "open /tmp/customer:TOPSECRET/creds: denied",
			wantMask:     "[REDACTED_PATH]",
			forbiddenRaw: "TOPSECRET",
		},
		{
			name:         "unquoted path with colon in leaf component redacted",
			input:        "open /tmp/customer:TOPSECRET: denied",
			wantMask:     "[REDACTED_PATH]",
			wantExact:    "open [REDACTED_PATH]: denied",
			forbiddenRaw: "TOPSECRET",
		},
		{
			name:         "multiple unquoted unix paths redacted",
			input:        "rename /Users/alice/source /Users/bob/private-data: denied",
			wantMask:     "[REDACTED_PATH]",
			wantExact:    "rename [REDACTED_PATH] [REDACTED_PATH]: denied",
			forbiddenRaw: "bob",
		},
		{
			name:         "non-terminal space path in multiple unix paths redacted",
			input:        "rename /Users/Alice Smith/source /Users/bob/private-data: denied",
			wantMask:     "[REDACTED_PATH]",
			wantExact:    "rename [REDACTED_PATH] [REDACTED_PATH]: denied",
			forbiddenRaw: "Alice Smith",
		},
		{
			name:         "leaf filename with space redacted",
			input:        "open /tmp/Alice Smith.txt: denied",
			wantMask:     "[REDACTED_PATH]",
			wantExact:    "open [REDACTED_PATH]: denied",
			forbiddenRaw: "Alice Smith",
		},
		{
			name:         "multiple paths with leaf filename spaces redacted",
			input:        "rename /tmp/Alice Smith /tmp/Bob Jones: denied",
			wantMask:     "[REDACTED_PATH]",
			wantExact:    "rename [REDACTED_PATH] [REDACTED_PATH]: denied",
			forbiddenRaw: "Alice Smith",
		},
		{
			name:         "unquoted path with colon space in leaf component redacted",
			input:        "open /tmp/customer: TOPSECRET: denied",
			wantMask:     "[REDACTED_PATH]",
			wantExact:    "open [REDACTED_PATH]: denied",
			forbiddenRaw: "TOPSECRET",
		},
		{
			name:         "parenthesized unquoted path redacted and parens preserved",
			input:        "open (/tmp/customer): denied",
			wantMask:     "[REDACTED_PATH]",
			wantExact:    "open ([REDACTED_PATH]): denied",
			forbiddenRaw: "customer",
		},
		{
			name:         "braced unquoted path redacted and braces preserved",
			input:        "open {/tmp/customer}: denied",
			wantMask:     "[REDACTED_PATH]",
			wantExact:    "open {[REDACTED_PATH]}: denied",
			forbiddenRaw: "customer",
		},
		{
			name:         "nested error text preserved after path",
			input:        "open /tmp/config: permission denied: retry later",
			wantMask:     "[REDACTED_PATH]",
			wantExact:    "open [REDACTED_PATH]: permission denied: retry later",
			forbiddenRaw: "config",
		},
		{
			name:         "nested known error text preserved after path",
			input:        "open /tmp/config: permission denied: access denied",
			wantMask:     "[REDACTED_PATH]",
			wantExact:    "open [REDACTED_PATH]: permission denied: access denied",
			forbiddenRaw: "config",
		},
		{
			name:         "uppercase connector between paths preserved",
			input:        "copy /tmp/a   TO\t/tmp/b: denied",
			wantMask:     "[REDACTED_PATH]",
			wantExact:    "copy [REDACTED_PATH]   TO\t[REDACTED_PATH]: denied",
			forbiddenRaw: "",
		},
		{
			name:         "long connector string bounded to 256 runes",
			input:        strings.Repeat("a", 300) + " to /tmp/x: denied",
			wantMask:     "...",
			forbiddenRaw: "x",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractUpstreamErrorSummary(tc.input)
			if !strings.Contains(got, tc.wantMask) {
				t.Fatalf("ExtractUpstreamErrorSummary(%q) = %q, want containing %q", tc.input, got, tc.wantMask)
			}
			if tc.wantExact != "" && got != tc.wantExact {
				t.Fatalf("ExtractUpstreamErrorSummary(%q) = %q, want exact %q", tc.input, got, tc.wantExact)
			}
			if tc.forbiddenRaw != "" && strings.Contains(got, tc.forbiddenRaw) {
				t.Fatalf("ExtractUpstreamErrorSummary(%q) leaked sensitive value %q in output: %q", tc.input, tc.forbiddenRaw, got)
			}
			if len([]rune(got)) > 256 {
				t.Fatalf("ExtractUpstreamErrorSummary length = %d runes, want <= 256", len([]rune(got)))
			}
		})
	}
}

func TestErrorWithCause_ErrorDirectFormat(t *testing.T) {
	t.Parallel()

	cause := errors.New(`{"type":"error","code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later.","sequence_number":0}`)
	wrapped := WithCause(&Error{Code: "auth_unavailable", Message: "no auth available"}, cause)

	got := wrapped.Error()
	if !strings.Contains(got, "auth_unavailable: no auth available") {
		t.Fatalf("Error() = %q, want containing base error", got)
	}
	if !strings.Contains(got, "server_is_overloaded") || !strings.Contains(got, "Our servers are currently overloaded. Please try again later.") {
		t.Fatalf("Error() = %q, want containing upstream error summary", got)
	}
}

func TestModelCooldownError_ClaudeUnifiedRateLimitHeaders(t *testing.T) {
	t.Parallel()

	t.Run("preserves observed 7d claim under 5h remaining across affinity and scheduler paths", func(t *testing.T) {
		for _, useAffinity := range []bool{true, false} {
			name := "scheduler"
			if useAffinity {
				name = "session_affinity"
			}
			t.Run(name, func(t *testing.T) {
				var selector Selector
				if useAffinity {
					selector = NewSessionAffinitySelector(&RoundRobinSelector{})
				}
				manager := NewManager(nil, selector, nil)
				manager.RegisterExecutor(&mockCustomErrorExecutor{identifier: "claude"})

				const model = "issue-7417-claude-7d-short-"
				modelID := model + name
				authID := "issue-7417-auth-" + name
				registry.GetGlobalRegistry().RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: modelID}})
				t.Cleanup(func() {
					registry.GetGlobalRegistry().UnregisterClient(authID)
				})

				if _, errRegister := manager.Register(context.Background(), &Auth{
					ID:       authID,
					Provider: "claude",
					Status:   StatusActive,
				}); errRegister != nil {
					t.Fatalf("register auth: %v", errRegister)
				}

				retryDelay := 2 * time.Hour
				upstreamReset := time.Now().Add(retryDelay - 15*time.Second).Unix()
				markCtx := internallogging.WithResponseHeadersHolder(context.Background())
				internallogging.SetResponseHeaders(markCtx, http.Header{
					"Anthropic-Ratelimit-Unified-Status":               {"rejected"},
					"Anthropic-Ratelimit-Unified-7d-Status":            {"rejected"},
					"Anthropic-Ratelimit-Unified-7d-Reset":             {strconv.FormatInt(upstreamReset, 10)},
					"Anthropic-Ratelimit-Unified-5h-Status":            {"allowed"},
					"Anthropic-Ratelimit-Unified-Representative-Claim": {"seven_day"},
					"Anthropic-Ratelimit-Unified-Overage-Status":       {"rejected"},
					"Anthropic-Ratelimit-Unified-Reset":                {strconv.FormatInt(upstreamReset, 10)},
				})
				manager.MarkResult(markCtx, Result{
					AuthID:          authID,
					Provider:        "claude",
					Model:           modelID,
					Success:         false,
					CredentialScope: true,
					RetryAfter:      &retryDelay,
					Error:           &Error{Code: ErrorCodeCredentialQuota, Message: "rate_limit_error", HTTPStatus: http.StatusTooManyRequests},
				})

				_, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: modelID}, cliproxyexecutor.Options{})
				if errExecute == nil {
					t.Fatal("expected cooldown error, got nil")
				}

				var cooldownErr *modelCooldownError
				if !errors.As(errExecute, &cooldownErr) {
					t.Fatalf("Execute() error = %T (%v), want *modelCooldownError", errExecute, errExecute)
				}

				before := time.Now().Unix()
				headers := cooldownErr.Headers()
				after := time.Now().Unix()
				retrySeconds, errSeconds := strconv.ParseInt(headers.Get("Retry-After"), 10, 64)
				if errSeconds != nil || retrySeconds <= 0 {
					t.Fatalf("Retry-After = %q, want positive seconds", headers.Get("Retry-After"))
				}
				if got := headers.Get("Anthropic-Ratelimit-Unified-Status"); got != "rejected" {
					t.Fatalf("Anthropic-Ratelimit-Unified-Status = %q, want rejected", got)
				}
				if got := headers.Get("Anthropic-Ratelimit-Unified-Representative-Claim"); got != "seven_day" {
					t.Fatalf("Anthropic-Ratelimit-Unified-Representative-Claim = %q, want seven_day", got)
				}
				if got := headers.Get("Anthropic-Ratelimit-Unified-7d-Status"); got != "rejected" {
					t.Fatalf("Anthropic-Ratelimit-Unified-7d-Status = %q, want rejected", got)
				}
				if got := headers.Get("Anthropic-Ratelimit-Unified-5h-Status"); got != "allowed" {
					t.Fatalf("Anthropic-Ratelimit-Unified-5h-Status = %q, want allowed", got)
				}
				if got := headers.Get("Anthropic-Ratelimit-Unified-Overage-Status"); got != "rejected" {
					t.Fatalf("Anthropic-Ratelimit-Unified-Overage-Status = %q, want rejected", got)
				}

				unifiedReset, errReset := strconv.ParseInt(headers.Get("Anthropic-Ratelimit-Unified-Reset"), 10, 64)
				if errReset != nil {
					t.Fatalf("Anthropic-Ratelimit-Unified-Reset = %q, want unix epoch", headers.Get("Anthropic-Ratelimit-Unified-Reset"))
				}
				if unifiedReset < before+retrySeconds || unifiedReset > after+retrySeconds {
					t.Fatalf("Anthropic-Ratelimit-Unified-Reset = %d, want in [%d, %d] aligned with Retry-After %d", unifiedReset, before+retrySeconds, after+retrySeconds, retrySeconds)
				}
				if got := headers.Get("Anthropic-Ratelimit-Unified-7d-Reset"); got != strconv.FormatInt(unifiedReset, 10) {
					t.Fatalf("Anthropic-Ratelimit-Unified-7d-Reset = %q, want %d", got, unifiedReset)
				}

				safeHeaders := SafeResponseHeaders(errExecute)
				if got := safeHeaders.Get("Anthropic-Ratelimit-Unified-Status"); got != "" {
					t.Fatalf("SafeResponseHeaders leaked unified header %q", got)
				}
			})
		}
	})

	t.Run("infers claim from duration when signals are absent", func(t *testing.T) {
		errFiveHour := newModelCooldownError("claude-haiku-4-5-20251001", "claude", 2*time.Hour)
		h5 := errFiveHour.Headers()
		if got := h5.Get("Anthropic-Ratelimit-Unified-Representative-Claim"); got != "five_hour" {
			t.Fatalf("5h fallback claim = %q, want five_hour", got)
		}
		if got := h5.Get("Anthropic-Ratelimit-Unified-5h-Status"); got != "rejected" {
			t.Fatalf("5h fallback status = %q, want rejected", got)
		}
		if got := h5.Get("Anthropic-Ratelimit-Unified-Overage-Status"); got != "rejected" {
			t.Fatalf("5h fallback overage status = %q, want rejected", got)
		}

		errSevenDay := newModelCooldownError("claude-haiku-4-5-20251001", "claude", 6*time.Hour)
		h7 := errSevenDay.Headers()
		if got := h7.Get("Anthropic-Ratelimit-Unified-Representative-Claim"); got != "seven_day" {
			t.Fatalf("7d fallback claim = %q, want seven_day", got)
		}
		if got := h7.Get("Anthropic-Ratelimit-Unified-7d-Status"); got != "rejected" {
			t.Fatalf("7d fallback status = %q, want rejected", got)
		}
	})

	t.Run("preserves allowed shared windows on overage claim and aligns model-family window resets", func(t *testing.T) {
		overageErr := newModelCooldownErrorForCandidates("claude-sonnet-4-5-20250929", "claude", 30*time.Second, nil, true, map[string]string{
			"Anthropic-Ratelimit-Unified-Status":               "rejected",
			"Anthropic-Ratelimit-Unified-Representative-Claim": "overage",
			"Anthropic-Ratelimit-Unified-5h-Status":            "allowed",
			"Anthropic-Ratelimit-Unified-7d-Status":            "allowed",
			"Anthropic-Ratelimit-Unified-Overage-Status":       "rejected",
		})
		overageHeaders := overageErr.Headers()
		if got := overageHeaders.Get("Anthropic-Ratelimit-Unified-5h-Status"); got != "allowed" {
			t.Fatalf("overage cooldown overwrote 5h status = %q, want allowed", got)
		}
		if got := overageHeaders.Get("Anthropic-Ratelimit-Unified-7d-Status"); got != "allowed" {
			t.Fatalf("overage cooldown overwrote 7d status = %q, want allowed", got)
		}
		if got := overageHeaders.Get("Anthropic-Ratelimit-Unified-Representative-Claim"); got != "overage" {
			t.Fatalf("overage cooldown claim = %q, want overage", got)
		}

		sonnetErr := newModelCooldownErrorForCandidates("claude-sonnet-4-5-20250929", "claude", 2*time.Hour, nil, true, map[string]string{
			"Anthropic-Ratelimit-Unified-Status":               "rejected",
			"Anthropic-Ratelimit-Unified-Representative-Claim": "seven_day_sonnet",
			"Anthropic-Ratelimit-Unified-5h-Status":            "allowed",
			"Anthropic-Ratelimit-Unified-7d-Status":            "allowed",
			"Anthropic-Ratelimit-Unified-7d_sonnet-Status":     "rejected",
			"Anthropic-Ratelimit-Unified-7d_sonnet-Reset":      "1700000000",
			"Anthropic-Ratelimit-Unified-Overage-Status":       "rejected",
		})
		sonnetHeaders := sonnetErr.Headers()
		if got := sonnetHeaders.Get("Anthropic-Ratelimit-Unified-5h-Status"); got != "allowed" {
			t.Fatalf("7d_sonnet cooldown overwrote 5h status = %q, want allowed", got)
		}
		if got := sonnetHeaders.Get("Anthropic-Ratelimit-Unified-7d_sonnet-Reset"); got != sonnetHeaders.Get("Anthropic-Ratelimit-Unified-Reset") {
			t.Fatalf("7d_sonnet reset = %q, want %q", got, sonnetHeaders.Get("Anthropic-Ratelimit-Unified-Reset"))
		}

		overageNoClaimErr := newModelCooldownErrorForCandidates("claude-sonnet-4-5-20250929", "claude", 30*time.Second, nil, true, map[string]string{
			"Anthropic-Ratelimit-Unified-Status":         "rejected",
			"Anthropic-Ratelimit-Unified-5h-Status":      "allowed",
			"Anthropic-Ratelimit-Unified-7d-Status":      "allowed",
			"Anthropic-Ratelimit-Unified-Overage-Status": "rejected",
		})
		overageNoClaimHeaders := overageNoClaimErr.Headers()
		if got := overageNoClaimHeaders.Get("Anthropic-Ratelimit-Unified-Representative-Claim"); got != "overage" {
			t.Fatalf("overage without claim inferred %q, want overage", got)
		}
		if got := overageNoClaimHeaders.Get("Anthropic-Ratelimit-Unified-5h-Status"); got != "allowed" {
			t.Fatalf("overage without claim 5h status = %q, want allowed", got)
		}

		sonnetNoClaimErr := newModelCooldownErrorForCandidates("claude-sonnet-4-5-20250929", "claude", 2*time.Hour, nil, true, map[string]string{
			"Anthropic-Ratelimit-Unified-Status":           "rejected",
			"Anthropic-Ratelimit-Unified-5h-Status":        "allowed",
			"Anthropic-Ratelimit-Unified-7d-Status":        "allowed",
			"Anthropic-Ratelimit-Unified-7d_sonnet-Status": "rejected",
			"Anthropic-Ratelimit-Unified-Overage-Status":   "rejected",
		})
		sonnetNoClaimHeaders := sonnetNoClaimErr.Headers()
		if got := sonnetNoClaimHeaders.Get("Anthropic-Ratelimit-Unified-Representative-Claim"); got != "seven_day_sonnet" {
			t.Fatalf("7d_sonnet without claim inferred %q, want seven_day_sonnet", got)
		}

		fiveAllowedOnlyErr := newModelCooldownErrorForCandidates("claude-sonnet-4-5-20250929", "claude", 2*time.Hour, nil, true, map[string]string{
			"Anthropic-Ratelimit-Unified-Status":    "rejected",
			"Anthropic-Ratelimit-Unified-5h-Status": "allowed",
		})
		fiveAllowedOnlyHeaders := fiveAllowedOnlyErr.Headers()
		if got := fiveAllowedOnlyHeaders.Get("Anthropic-Ratelimit-Unified-Representative-Claim"); got != "seven_day" {
			t.Fatalf("5h=allowed with 2h reset inferred %q, want seven_day", got)
		}
		if got := fiveAllowedOnlyHeaders.Get("Anthropic-Ratelimit-Unified-7d-Status"); got != "rejected" {
			t.Fatalf("5h=allowed with 2h reset 7d status = %q, want rejected", got)
		}
	})

	t.Run("does not emit unified headers for non-Claude or cloudflare cooldowns", func(t *testing.T) {
		manager := NewManager(nil, NewSessionAffinitySelector(&RoundRobinSelector{}), nil)
		manager.RegisterExecutor(&mockCustomErrorExecutor{identifier: "codex"})
		manager.RegisterExecutor(&mockCustomErrorExecutor{identifier: "claude"})

		const codexModel = "issue-7417-codex-model"
		const codexAuthID = "issue-7417-codex-auth"
		const cfModel = "issue-7417-cf-model"
		const cfAuthID = "issue-7417-cf-auth"
		next := time.Now().Add(30 * time.Minute)

		registry.GetGlobalRegistry().RegisterClient(codexAuthID, "codex", []*registry.ModelInfo{{ID: codexModel}})
		registry.GetGlobalRegistry().RegisterClient(cfAuthID, "claude", []*registry.ModelInfo{{ID: cfModel}})
		t.Cleanup(func() {
			registry.GetGlobalRegistry().UnregisterClient(codexAuthID)
			registry.GetGlobalRegistry().UnregisterClient(cfAuthID)
		})

		if _, errRegister := manager.Register(context.Background(), &Auth{
			ID:             codexAuthID,
			Provider:       "codex",
			Status:         StatusActive,
			Unavailable:    true,
			NextRetryAfter: next,
			Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: next},
		}); errRegister != nil {
			t.Fatalf("register codex auth: %v", errRegister)
		}
		if _, errRegister := manager.Register(context.Background(), &Auth{
			ID:             cfAuthID,
			Provider:       "claude",
			Status:         StatusActive,
			Unavailable:    true,
			NextRetryAfter: next,
			ModelStates: map[string]*ModelState{
				cfModel: {
					Status:         StatusError,
					Unavailable:    true,
					NextRetryAfter: next,
					Quota:          QuotaState{Exceeded: true, Reason: "cloudflare challenge", NextRecoverAt: next},
				},
			},
		}); errRegister != nil {
			t.Fatalf("register cloudflare auth: %v", errRegister)
		}

		_, errCodex := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: codexModel}, cliproxyexecutor.Options{})
		var codexCooldown *modelCooldownError
		if !errors.As(errCodex, &codexCooldown) {
			t.Fatalf("codex Execute() error = %T (%v), want *modelCooldownError", errCodex, errCodex)
		}
		if codexCooldown.provider != "" {
			t.Fatalf("codex cooldown provider = %q, want empty mixed provider", codexCooldown.provider)
		}
		if got := codexCooldown.Headers().Get("Anthropic-Ratelimit-Unified-Status"); got != "" {
			t.Fatalf("codex cooldown leaked Anthropic-Ratelimit-Unified-Status = %q", got)
		}

		_, errCF := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: cfModel}, cliproxyexecutor.Options{})
		var cfCooldown *modelCooldownError
		if !errors.As(errCF, &cfCooldown) {
			t.Fatalf("cloudflare Execute() error = %T (%v), want *modelCooldownError", errCF, errCF)
		}
		if got := cfCooldown.Headers().Get("Anthropic-Ratelimit-Unified-Status"); got != "" {
			t.Fatalf("cloudflare challenge cooldown leaked Anthropic-Ratelimit-Unified-Status = %q", got)
		}
	})

	t.Run("does not synthesize unified headers for ordinary per-minute rate limits across affinity and scheduler paths", func(t *testing.T) {
		for _, useAffinity := range []bool{true, false} {
			pathName := "scheduler"
			if useAffinity {
				pathName = "session_affinity"
			}
			t.Run(pathName, func(t *testing.T) {
				var selector Selector
				if useAffinity {
					selector = NewSessionAffinitySelector(&RoundRobinSelector{})
				}
				manager := NewManager(nil, selector, nil)
				manager.RegisterExecutor(&mockCustomErrorExecutor{identifier: "claude"})

				allowedWindowModel := "issue-7417-rpm-allowed-" + pathName
				allowedWindowAuth := "issue-7417-rpm-allowed-auth-" + pathName
				plainRPMModel := "issue-7417-rpm-plain-" + pathName
				plainRPMAuth := "issue-7417-rpm-plain-auth-" + pathName
				modelQuotaModel := "issue-7417-model-unified-" + pathName
				modelQuotaAuth := "issue-7417-model-unified-auth-" + pathName

				registry.GetGlobalRegistry().RegisterClient(allowedWindowAuth, "claude", []*registry.ModelInfo{{ID: allowedWindowModel}})
				registry.GetGlobalRegistry().RegisterClient(plainRPMAuth, "claude", []*registry.ModelInfo{{ID: plainRPMModel}})
				registry.GetGlobalRegistry().RegisterClient(modelQuotaAuth, "claude", []*registry.ModelInfo{{ID: modelQuotaModel}})
				t.Cleanup(func() {
					registry.GetGlobalRegistry().UnregisterClient(allowedWindowAuth)
					registry.GetGlobalRegistry().UnregisterClient(plainRPMAuth)
					registry.GetGlobalRegistry().UnregisterClient(modelQuotaAuth)
				})

				for _, id := range []string{allowedWindowAuth, plainRPMAuth, modelQuotaAuth} {
					if _, errRegister := manager.Register(context.Background(), &Auth{
						ID:       id,
						Provider: "claude",
						Status:   StatusActive,
					}); errRegister != nil {
						t.Fatalf("register %s: %v", id, errRegister)
					}
				}

				shortRetry := 30 * time.Second
				rpmBody := `{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed the requests per minute rate limit. Please try again later."}}`

				// 1. Per-minute 429 with allowed 5h window and future unified reset.
				allowedCtx := internallogging.WithResponseHeadersHolder(context.Background())
				internallogging.SetResponseHeaders(allowedCtx, http.Header{
					"Retry-After":                           {"30"},
					"Anthropic-Ratelimit-Unified-5h-Status": {"allowed"},
					"Anthropic-Ratelimit-Unified-Reset":     {strconv.FormatInt(time.Now().Add(4*time.Hour).Unix(), 10)},
				})
				manager.MarkResult(allowedCtx, Result{
					AuthID:          allowedWindowAuth,
					Provider:        "claude",
					Model:           allowedWindowModel,
					Success:         false,
					CredentialScope: false,
					RetryAfter:      &shortRetry,
					Error:           &Error{Code: "rate_limit_error", Message: rpmBody, HTTPStatus: http.StatusTooManyRequests},
				})

				// 2. Per-minute 429 with only Retry-After and no unified rejection headers.
				plainCtx := internallogging.WithResponseHeadersHolder(context.Background())
				internallogging.SetResponseHeaders(plainCtx, http.Header{
					"Retry-After": {"30"},
				})
				manager.MarkResult(plainCtx, Result{
					AuthID:          plainRPMAuth,
					Provider:        "claude",
					Model:           plainRPMModel,
					Success:         false,
					CredentialScope: false,
					RetryAfter:      &shortRetry,
					Error:           &Error{Code: "rate_limit_error", Message: rpmBody, HTTPStatus: http.StatusTooManyRequests},
				})

				// 3. Model-level cooling (e.g. API-key upstream) that explicitly reports unified 5h rejection.
				fiveHourDelay := 2 * time.Hour
				rejectedCtx := internallogging.WithResponseHeadersHolder(context.Background())
				internallogging.SetResponseHeaders(rejectedCtx, http.Header{
					"Retry-After":                           {"7200"},
					"Anthropic-Ratelimit-Unified-Status":    {"rejected"},
					"Anthropic-Ratelimit-Unified-5h-Status": {"rejected"},
					"Anthropic-Ratelimit-Unified-Reset":     {strconv.FormatInt(time.Now().Add(fiveHourDelay).Unix(), 10)},
				})
				manager.MarkResult(rejectedCtx, Result{
					AuthID:          modelQuotaAuth,
					Provider:        "claude",
					Model:           modelQuotaModel,
					Success:         false,
					CredentialScope: false,
					RetryAfter:      &fiveHourDelay,
					Error:           &Error{Code: "rate_limit_error", Message: "quota exhausted", HTTPStatus: http.StatusTooManyRequests},
				})

				for _, modelID := range []string{allowedWindowModel, plainRPMModel} {
					_, errExec := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: modelID}, cliproxyexecutor.Options{})
					var cdErr *modelCooldownError
					if !errors.As(errExec, &cdErr) {
						t.Fatalf("%s Execute() error = %T (%v), want *modelCooldownError", modelID, errExec, errExec)
					}
					hdr := cdErr.Headers()
					if got := hdr.Get("Retry-After"); got == "" {
						t.Fatalf("%s Retry-After = empty, want positive seconds", modelID)
					}
					for key := range hdr {
						if strings.HasPrefix(strings.ToLower(key), "anthropic-ratelimit-unified-") {
							t.Fatalf("%s ordinary rate-limit cooldown leaked unified header %s=%q", modelID, key, hdr.Get(key))
						}
					}
				}

				_, errModelQuota := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: modelQuotaModel}, cliproxyexecutor.Options{})
				var modelQuotaCooldown *modelCooldownError
				if !errors.As(errModelQuota, &modelQuotaCooldown) {
					t.Fatalf("modelQuota Execute() error = %T (%v), want *modelCooldownError", errModelQuota, errModelQuota)
				}
				if got := modelQuotaCooldown.Headers().Get("Anthropic-Ratelimit-Unified-Status"); got != "rejected" {
					t.Fatalf("modelQuota Anthropic-Ratelimit-Unified-Status = %q, want rejected", got)
				}
				if got := modelQuotaCooldown.Headers().Get("Anthropic-Ratelimit-Unified-Representative-Claim"); got != "five_hour" {
					t.Fatalf("modelQuota Representative-Claim = %q, want five_hour", got)
				}
			})
		}
	})

	t.Run("retains unified headers when late success refreshes passive signals during active cooldown", func(t *testing.T) {
		for _, useAffinity := range []bool{true, false} {
			pathName := "scheduler"
			if useAffinity {
				pathName = "session_affinity"
			}
			t.Run(pathName, func(t *testing.T) {
				var selector Selector
				if useAffinity {
					selector = NewSessionAffinitySelector(&RoundRobinSelector{})
				}
				manager := NewManager(nil, selector, nil)
				manager.RegisterExecutor(&mockCustomErrorExecutor{identifier: "claude"})

				credModel := "issue-7417-late-cred-" + pathName
				credAuth := "issue-7417-late-cred-auth-" + pathName
				restoredModel := "issue-7417-late-restored-" + pathName
				restoredAuth := "issue-7417-late-restored-auth-" + pathName
				modelScopedModel := "issue-7417-late-model-" + pathName
				modelScopedAuth := "issue-7417-late-model-auth-" + pathName

				registry.GetGlobalRegistry().RegisterClient(credAuth, "claude", []*registry.ModelInfo{{ID: credModel}})
				registry.GetGlobalRegistry().RegisterClient(restoredAuth, "claude", []*registry.ModelInfo{{ID: restoredModel}})
				registry.GetGlobalRegistry().RegisterClient(modelScopedAuth, "claude", []*registry.ModelInfo{{ID: modelScopedModel}})
				t.Cleanup(func() {
					registry.GetGlobalRegistry().UnregisterClient(credAuth)
					registry.GetGlobalRegistry().UnregisterClient(restoredAuth)
					registry.GetGlobalRegistry().UnregisterClient(modelScopedAuth)
				})

				if _, errRegister := manager.Register(context.Background(), &Auth{
					ID:       credAuth,
					Provider: "claude",
					Status:   StatusActive,
				}); errRegister != nil {
					t.Fatalf("register credAuth: %v", errRegister)
				}
				recoverAt := time.Now().Add(2 * time.Hour)
				if _, errRegister := manager.Register(context.Background(), &Auth{
					ID:             restoredAuth,
					Provider:       "claude",
					Status:         StatusError,
					Unavailable:    true,
					NextRetryAfter: recoverAt,
					Quota: QuotaState{
						Exceeded:      true,
						Reason:        "credential_quota",
						NextRecoverAt: recoverAt,
					},
				}); errRegister != nil {
					t.Fatalf("register restoredAuth: %v", errRegister)
				}
				if _, errRegister := manager.Register(context.Background(), &Auth{
					ID:       modelScopedAuth,
					Provider: "claude",
					Status:   StatusActive,
				}); errRegister != nil {
					t.Fatalf("register modelScopedAuth: %v", errRegister)
				}

				delay := 2 * time.Hour
				rejectedCtx := internallogging.WithResponseHeadersHolder(context.Background())
				internallogging.SetResponseHeaders(rejectedCtx, http.Header{
					"Anthropic-Ratelimit-Unified-Status":               {"rejected"},
					"Anthropic-Ratelimit-Unified-7d-Status":            {"rejected"},
					"Anthropic-Ratelimit-Unified-5h-Status":            {"allowed"},
					"Anthropic-Ratelimit-Unified-Representative-Claim": {"seven_day"},
					"Anthropic-Ratelimit-Unified-Reset":                {strconv.FormatInt(time.Now().Add(delay).Unix(), 10)},
				})

				// Step 1: Record subscription 429 on credAuth and modelScopedAuth.
				staleSeq := manager.nextExecutionSeq()
				failureSeq := manager.nextExecutionSeq()
				manager.MarkResult(rejectedCtx, Result{
					AuthID:          credAuth,
					Provider:        "claude",
					Model:           credModel,
					Success:         false,
					CredentialScope: true,
					RetryAfter:      &delay,
					ExecutionSeq:    failureSeq,
					Error:           &Error{Code: ErrorCodeCredentialQuota, Message: "rate_limit_error", HTTPStatus: http.StatusTooManyRequests},
				})
				manager.MarkResult(rejectedCtx, Result{
					AuthID:          modelScopedAuth,
					Provider:        "claude",
					Model:           modelScopedModel,
					Success:         false,
					CredentialScope: false,
					RetryAfter:      &delay,
					ExecutionSeq:    failureSeq,
					Error:           &Error{Code: "rate_limit_error", Message: "rate_limit_error", HTTPStatus: http.StatusTooManyRequests},
				})

				// Step 2: Earlier in-flight requests complete with 200 OK and allowed unified headers.
				allowedCtx := internallogging.WithResponseHeadersHolder(context.Background())
				internallogging.SetResponseHeaders(allowedCtx, http.Header{
					"Anthropic-Ratelimit-Unified-Status":    {"allowed"},
					"Anthropic-Ratelimit-Unified-5h-Status": {"allowed"},
					"Anthropic-Ratelimit-Unified-7d-Status": {"allowed"},
				})
				manager.MarkResult(allowedCtx, Result{
					AuthID:       credAuth,
					Provider:     "claude",
					Model:        credModel,
					Success:      true,
					ExecutionSeq: staleSeq,
				})
				manager.MarkResult(allowedCtx, Result{
					AuthID:       restoredAuth,
					Provider:     "claude",
					Model:        restoredModel,
					Success:      true,
					ExecutionSeq: staleSeq,
				})
				manager.MarkResult(allowedCtx, Result{
					AuthID:       modelScopedAuth,
					Provider:     "claude",
					Model:        modelScopedModel,
					Success:      true,
					ExecutionSeq: staleSeq,
				})

				// Step 3: Subsequent requests during active cooldown still emit unified rate-limit headers.
				for _, tc := range []struct {
					model     string
					wantClaim string
				}{
					{model: credModel, wantClaim: "seven_day"},
					{model: restoredModel, wantClaim: "five_hour"},
					{model: modelScopedModel, wantClaim: "seven_day"},
				} {
					_, errExec := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: tc.model}, cliproxyexecutor.Options{})
					var cdErr *modelCooldownError
					if !errors.As(errExec, &cdErr) {
						t.Fatalf("%s Execute() error = %T (%v), want *modelCooldownError", tc.model, errExec, errExec)
					}
					hdr := cdErr.Headers()
					if got := hdr.Get("Anthropic-Ratelimit-Unified-Status"); got != "rejected" {
						t.Fatalf("%s Anthropic-Ratelimit-Unified-Status = %q, want rejected", tc.model, got)
					}
					if got := hdr.Get("Anthropic-Ratelimit-Unified-Representative-Claim"); got != tc.wantClaim {
						t.Fatalf("%s Representative-Claim = %q, want %s", tc.model, got, tc.wantClaim)
					}
				}
			})
		}
	})

	t.Run("does not emit stale unified headers when 503 follows expired model quota cooldown", func(t *testing.T) {
		for _, useAffinity := range []bool{true, false} {
			pathName := "scheduler"
			if useAffinity {
				pathName = "session_affinity"
			}
			t.Run(pathName, func(t *testing.T) {
				var selector Selector
				if useAffinity {
					selector = NewSessionAffinitySelector(&RoundRobinSelector{})
				}
				manager := NewManager(nil, selector, nil)
				manager.RegisterExecutor(&mockCustomErrorExecutor{identifier: "claude"})

				modelID := "issue-7417-expired-503-" + pathName
				authID := "issue-7417-expired-503-auth-" + pathName
				registry.GetGlobalRegistry().RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: modelID}})
				t.Cleanup(func() {
					registry.GetGlobalRegistry().UnregisterClient(authID)
				})

				if _, errRegister := manager.Register(context.Background(), &Auth{
					ID:       authID,
					Provider: "claude",
					Status:   StatusActive,
				}); errRegister != nil {
					t.Fatalf("register auth: %v", errRegister)
				}

				// 1. Model-scoped unified 429 records rejected signals.
				quotaDelay := time.Hour
				rejectedCtx := internallogging.WithResponseHeadersHolder(context.Background())
				internallogging.SetResponseHeaders(rejectedCtx, http.Header{
					"Anthropic-Ratelimit-Unified-Status":               {"rejected"},
					"Anthropic-Ratelimit-Unified-5h-Status":            {"rejected"},
					"Anthropic-Ratelimit-Unified-Representative-Claim": {"five_hour"},
					"Anthropic-Ratelimit-Unified-Reset":                {strconv.FormatInt(time.Now().Add(quotaDelay).Unix(), 10)},
				})
				manager.MarkResult(rejectedCtx, Result{
					AuthID:          authID,
					Provider:        "claude",
					Model:           modelID,
					Success:         false,
					CredentialScope: false,
					RetryAfter:      &quotaDelay,
					Error:           &Error{Code: "rate_limit_error", Message: "quota", HTTPStatus: http.StatusTooManyRequests},
				})

				// 2. Simulate natural expiry of the quota deadline without wall-clock sleep.
				past := time.Now().Add(-time.Minute)
				if _, okModify, errModify := manager.Modify(context.Background(), authID, 0, func(a *Auth) error {
					a.NextRetryAfter = past
					if state := a.ModelStates[modelID]; state != nil {
						state.NextRetryAfter = past
						state.Quota.NextRecoverAt = past
					}
					a.Quota.NextRecoverAt = past
					return nil
				}); errModify != nil || !okModify {
					t.Fatalf("Modify() ok=%v err=%v", okModify, errModify)
				}

				// 3. Subsequent request fails with 503 Service Unavailable and Retry-After.
				serverDelay := 45 * time.Second
				manager.MarkResult(context.Background(), Result{
					AuthID:     authID,
					Provider:   "claude",
					Model:      modelID,
					Success:    false,
					RetryAfter: &serverDelay,
					Error:      &Error{Code: "server_error", Message: "overloaded", HTTPStatus: http.StatusServiceUnavailable},
				})

				// 4. Subsequent selection error must not advertise unified subscription exhaustion.
				_, errExec := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: modelID}, cliproxyexecutor.Options{})
				if errExec == nil {
					t.Fatal("expected unavailable/cooldown error after 503, got nil")
				}
				if carrier, ok := errExec.(interface{ Headers() http.Header }); ok && carrier != nil {
					for key := range carrier.Headers() {
						if strings.HasPrefix(strings.ToLower(key), "anthropic-ratelimit-unified-") {
							t.Fatalf("%s post-expiry 503 leaked unified header %s=%q", pathName, key, carrier.Headers().Get(key))
						}
					}
				}
			})
		}
	})

	t.Run("persists and restores model-scoped unified cooldown headers across restart", func(t *testing.T) {
		for _, useAffinity := range []bool{true, false} {
			pathName := "scheduler"
			if useAffinity {
				pathName = "session_affinity"
			}
			t.Run(pathName, func(t *testing.T) {
				store := NewFileCooldownStateStore(t.TempDir())
				modelID := "issue-7417-restart-model-" + pathName
				authID := "issue-7417-restart-auth-" + pathName
				registry.GetGlobalRegistry().RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: modelID}})
				t.Cleanup(func() {
					registry.GetGlobalRegistry().UnregisterClient(authID)
				})

				writerMgr := NewManager(nil, nil, nil)
				writerMgr.SetCooldownStateStore(store)
				if _, errRegister := writerMgr.Register(context.Background(), &Auth{
					ID:       authID,
					Provider: "claude",
					Status:   StatusActive,
				}); errRegister != nil {
					t.Fatalf("writer Register: %v", errRegister)
				}

				delay := 2 * time.Hour
				rejectedCtx := internallogging.WithResponseHeadersHolder(context.Background())
				internallogging.SetResponseHeaders(rejectedCtx, http.Header{
					"Anthropic-Ratelimit-Unified-Status":               {"rejected"},
					"Anthropic-Ratelimit-Unified-7d-Status":            {"rejected"},
					"Anthropic-Ratelimit-Unified-5h-Status":            {"allowed"},
					"Anthropic-Ratelimit-Unified-Representative-Claim": {"seven_day"},
					"Anthropic-Ratelimit-Unified-Reset":                {strconv.FormatInt(time.Now().Add(delay).Unix(), 10)},
				})
				writerMgr.MarkResult(rejectedCtx, Result{
					AuthID:          authID,
					Provider:        "claude",
					Model:           modelID,
					Success:         false,
					CredentialScope: false,
					RetryAfter:      &delay,
					Error:           &Error{Code: "rate_limit_error", Message: "quota", HTTPStatus: http.StatusTooManyRequests},
				})
				writerMgr.PersistCooldownStates(context.Background())

				var selector Selector
				if useAffinity {
					selector = NewSessionAffinitySelector(&RoundRobinSelector{})
				}
				restoredMgr := NewManager(nil, selector, nil)
				restoredMgr.RegisterExecutor(&mockCustomErrorExecutor{identifier: "claude"})
				restoredMgr.SetCooldownStateStore(store)
				if _, errRegister := restoredMgr.Register(context.Background(), &Auth{
					ID:       authID,
					Provider: "claude",
					Status:   StatusActive,
				}); errRegister != nil {
					t.Fatalf("restored Register: %v", errRegister)
				}
				if errRestore := restoredMgr.RestoreCooldownStates(context.Background()); errRestore != nil {
					t.Fatalf("RestoreCooldownStates: %v", errRestore)
				}

				_, errExec := restoredMgr.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: modelID}, cliproxyexecutor.Options{})
				var cdErr *modelCooldownError
				if !errors.As(errExec, &cdErr) {
					t.Fatalf("%s Execute() error = %T (%v), want *modelCooldownError", pathName, errExec, errExec)
				}
				hdr := cdErr.Headers()
				if got := hdr.Get("Anthropic-Ratelimit-Unified-Status"); got != "rejected" {
					t.Fatalf("%s restored Anthropic-Ratelimit-Unified-Status = %q, want rejected", pathName, got)
				}
				if got := hdr.Get("Anthropic-Ratelimit-Unified-Representative-Claim"); got != "seven_day" {
					t.Fatalf("%s restored Representative-Claim = %q, want seven_day", pathName, got)
				}
				if got := hdr.Get("Anthropic-Ratelimit-Unified-7d-Status"); got != "rejected" {
					t.Fatalf("%s restored 7d-Status = %q, want rejected", pathName, got)
				}
				if got := hdr.Get("Anthropic-Ratelimit-Unified-5h-Status"); got != "allowed" {
					t.Fatalf("%s restored 5h-Status = %q, want allowed", pathName, got)
				}
			})
		}
	})

	t.Run("keeps longer cooldown window signals when shorter out-of-order 429 completes later", func(t *testing.T) {
		for _, useAffinity := range []bool{true, false} {
			pathName := "scheduler"
			if useAffinity {
				pathName = "session_affinity"
			}
			t.Run(pathName, func(t *testing.T) {
				var selector Selector
				if useAffinity {
					selector = NewSessionAffinitySelector(&RoundRobinSelector{})
				}
				manager := NewManager(nil, selector, nil)
				manager.RegisterExecutor(&mockCustomErrorExecutor{identifier: "claude"})

				credModelA := "issue-7417-ooo-cred-a-" + pathName
				credModelB := "issue-7417-ooo-cred-b-" + pathName
				credAuth := "issue-7417-ooo-cred-auth-" + pathName
				modelScopedModel := "issue-7417-ooo-model-" + pathName
				modelScopedAuth := "issue-7417-ooo-model-auth-" + pathName
				rpmFirstModel := "issue-7417-ooo-rpm-" + pathName
				rpmFirstAuth := "issue-7417-ooo-rpm-auth-" + pathName

				registry.GetGlobalRegistry().RegisterClient(credAuth, "claude", []*registry.ModelInfo{{ID: credModelA}, {ID: credModelB}})
				registry.GetGlobalRegistry().RegisterClient(modelScopedAuth, "claude", []*registry.ModelInfo{{ID: modelScopedModel}})
				registry.GetGlobalRegistry().RegisterClient(rpmFirstAuth, "claude", []*registry.ModelInfo{{ID: rpmFirstModel}})
				t.Cleanup(func() {
					registry.GetGlobalRegistry().UnregisterClient(credAuth)
					registry.GetGlobalRegistry().UnregisterClient(modelScopedAuth)
					registry.GetGlobalRegistry().UnregisterClient(rpmFirstAuth)
				})

				for _, id := range []string{credAuth, modelScopedAuth, rpmFirstAuth} {
					if _, errRegister := manager.Register(context.Background(), &Auth{
						ID:       id,
						Provider: "claude",
						Status:   StatusActive,
					}); errRegister != nil {
						t.Fatalf("register %s: %v", id, errRegister)
					}
				}

				longDelay := 2 * time.Hour
				shortDelay := time.Hour
				sevenDayCtx := internallogging.WithResponseHeadersHolder(context.Background())
				internallogging.SetResponseHeaders(sevenDayCtx, http.Header{
					"Anthropic-Ratelimit-Unified-Status":               {"rejected"},
					"Anthropic-Ratelimit-Unified-7d-Status":            {"rejected"},
					"Anthropic-Ratelimit-Unified-5h-Status":            {"allowed"},
					"Anthropic-Ratelimit-Unified-Representative-Claim": {"seven_day"},
					"Anthropic-Ratelimit-Unified-Reset":                {strconv.FormatInt(time.Now().Add(longDelay).Unix(), 10)},
				})
				fiveHourCtx := internallogging.WithResponseHeadersHolder(context.Background())
				internallogging.SetResponseHeaders(fiveHourCtx, http.Header{
					"Anthropic-Ratelimit-Unified-Status":               {"rejected"},
					"Anthropic-Ratelimit-Unified-5h-Status":            {"rejected"},
					"Anthropic-Ratelimit-Unified-7d-Status":            {"allowed"},
					"Anthropic-Ratelimit-Unified-Representative-Claim": {"five_hour"},
					"Anthropic-Ratelimit-Unified-Reset":                {strconv.FormatInt(time.Now().Add(shortDelay).Unix(), 10)},
				})

				// Credential-scoped: 2h seven_day on model A, then shorter 1h five_hour on model B.
				manager.MarkResult(sevenDayCtx, Result{
					AuthID:          credAuth,
					Provider:        "claude",
					Model:           credModelA,
					Success:         false,
					CredentialScope: true,
					RetryAfter:      &longDelay,
					Error:           &Error{Code: ErrorCodeCredentialQuota, Message: "7d quota", HTTPStatus: http.StatusTooManyRequests},
				})
				manager.MarkResult(fiveHourCtx, Result{
					AuthID:          credAuth,
					Provider:        "claude",
					Model:           credModelB,
					Success:         false,
					CredentialScope: true,
					RetryAfter:      &shortDelay,
					Error:           &Error{Code: ErrorCodeCredentialQuota, Message: "5h quota", HTTPStatus: http.StatusTooManyRequests},
				})

				// Model-scoped: 2h seven_day, then shorter 1h five_hour.
				manager.MarkResult(sevenDayCtx, Result{
					AuthID:          modelScopedAuth,
					Provider:        "claude",
					Model:           modelScopedModel,
					Success:         false,
					CredentialScope: false,
					RetryAfter:      &longDelay,
					Error:           &Error{Code: "rate_limit_error", Message: "7d quota", HTTPStatus: http.StatusTooManyRequests},
				})
				manager.MarkResult(fiveHourCtx, Result{
					AuthID:          modelScopedAuth,
					Provider:        "claude",
					Model:           modelScopedModel,
					Success:         false,
					CredentialScope: false,
					RetryAfter:      &shortDelay,
					Error:           &Error{Code: "rate_limit_error", Message: "5h quota", HTTPStatus: http.StatusTooManyRequests},
				})

				// Longer ordinary 10m RPM 429 followed by shorter 1m unified 429: governing deadline stays ordinary RPM.
				rpmLong := 10 * time.Minute
				unifiedShort := time.Minute
				rpmCtx := internallogging.WithResponseHeadersHolder(context.Background())
				internallogging.SetResponseHeaders(rpmCtx, http.Header{"Retry-After": {"600"}})
				manager.MarkResult(rpmCtx, Result{
					AuthID:          rpmFirstAuth,
					Provider:        "claude",
					Model:           rpmFirstModel,
					Success:         false,
					CredentialScope: false,
					RetryAfter:      &rpmLong,
					Error:           &Error{Code: "rate_limit_error", Message: "rpm", HTTPStatus: http.StatusTooManyRequests},
				})
				manager.MarkResult(fiveHourCtx, Result{
					AuthID:          rpmFirstAuth,
					Provider:        "claude",
					Model:           rpmFirstModel,
					Success:         false,
					CredentialScope: false,
					RetryAfter:      &unifiedShort,
					Error:           &Error{Code: "rate_limit_error", Message: "5h", HTTPStatus: http.StatusTooManyRequests},
				})

				for _, m := range []string{credModelA, credModelB, modelScopedModel} {
					_, errExec := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: m}, cliproxyexecutor.Options{})
					var cdErr *modelCooldownError
					if !errors.As(errExec, &cdErr) {
						t.Fatalf("%s Execute() error = %T (%v), want *modelCooldownError", m, errExec, errExec)
					}
					if got := cdErr.Headers().Get("Anthropic-Ratelimit-Unified-Representative-Claim"); got != "seven_day" {
						t.Fatalf("%s Representative-Claim = %q, want seven_day (from governing 2h deadline)", m, got)
					}
					if got := cdErr.Headers().Get("Anthropic-Ratelimit-Unified-7d-Status"); got != "rejected" {
						t.Fatalf("%s 7d-Status = %q, want rejected", m, got)
					}
				}

				_, errRPM := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: rpmFirstModel}, cliproxyexecutor.Options{})
				var rpmCdErr *modelCooldownError
				if !errors.As(errRPM, &rpmCdErr) {
					t.Fatalf("%s Execute() error = %T (%v), want *modelCooldownError", rpmFirstModel, errRPM, errRPM)
				}
				if got := rpmCdErr.Headers().Get("Anthropic-Ratelimit-Unified-Status"); got != "" {
					t.Fatalf("%s leaked unified status %q while 10m ordinary RPM deadline still governs", rpmFirstModel, got)
				}
			})
		}
	})

	t.Run("reverts to independent model layer after propagated credential quota expires", func(t *testing.T) {
		for _, useAffinity := range []bool{true, false} {
			pathName := "scheduler"
			if useAffinity {
				pathName = "session_affinity"
			}
			t.Run(pathName, func(t *testing.T) {
				var selector Selector
				if useAffinity {
					selector = NewSessionAffinitySelector(&RoundRobinSelector{})
				}
				manager := NewManager(nil, selector, nil)
				manager.RegisterExecutor(&mockCustomErrorExecutor{identifier: "claude"})

				modelA := "issue-7417-shadow-a-" + pathName
				modelB := "issue-7417-shadow-b-" + pathName
				authID := "issue-7417-shadow-auth-" + pathName
				registry.GetGlobalRegistry().RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: modelA}, {ID: modelB}})
				t.Cleanup(func() {
					registry.GetGlobalRegistry().UnregisterClient(authID)
				})

				if _, errRegister := manager.Register(context.Background(), &Auth{
					ID:       authID,
					Provider: "claude",
					Status:   StatusActive,
				}); errRegister != nil {
					t.Fatalf("register auth: %v", errRegister)
				}

				// 1. Model A enters a 10m ordinary RPM cooldown (no unified rejection headers).
				rpmDelay := 10 * time.Minute
				rpmCtx := internallogging.WithResponseHeadersHolder(context.Background())
				internallogging.SetResponseHeaders(rpmCtx, http.Header{"Retry-After": {"600"}})
				manager.MarkResult(rpmCtx, Result{
					AuthID:          authID,
					Provider:        "claude",
					Model:           modelA,
					Success:         false,
					CredentialScope: false,
					RetryAfter:      &rpmDelay,
					Error:           &Error{Code: "rate_limit_error", Message: "rpm", HTTPStatus: http.StatusTooManyRequests},
				})

				// 2. Model B triggers a shorter 1m credential-wide 5h quota cooldown.
				credDelay := time.Minute
				fiveHourCtx := internallogging.WithResponseHeadersHolder(context.Background())
				internallogging.SetResponseHeaders(fiveHourCtx, http.Header{
					"Anthropic-Ratelimit-Unified-Status":               {"rejected"},
					"Anthropic-Ratelimit-Unified-5h-Status":            {"rejected"},
					"Anthropic-Ratelimit-Unified-Representative-Claim": {"five_hour"},
					"Anthropic-Ratelimit-Unified-Reset":                {strconv.FormatInt(time.Now().Add(credDelay).Unix(), 10)},
				})
				manager.MarkResult(fiveHourCtx, Result{
					AuthID:          authID,
					Provider:        "claude",
					Model:           modelB,
					Success:         false,
					CredentialScope: true,
					RetryAfter:      &credDelay,
					Error:           &Error{Code: ErrorCodeCredentialQuota, Message: "5h quota", HTTPStatus: http.StatusTooManyRequests},
				})

				// 3. Expire the 1m credential-wide quota deterministically while Model A's 10m RPM block remains live.
				past := time.Now().Add(-time.Second)
				manager.mu.Lock()
				curAuth := manager.auths[authID]
				curAuth.Quota.NextRecoverAt = past
				curAuth.NextRetryAfter = past
				if sb := curAuth.ModelStates[modelB]; sb != nil {
					sb.NextRetryAfter = past
					sb.Quota.NextRecoverAt = past
				}
				snapshot := curAuth.Clone()
				manager.mu.Unlock()
				if manager.scheduler != nil {
					manager.scheduler.upsertAuth(snapshot)
				}

				// 4. Model A is still cooling down from its independent 10m RPM error and must not emit 5h unified headers.
				_, errExecA := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: modelA}, cliproxyexecutor.Options{})
				var cdErrA *modelCooldownError
				if !errors.As(errExecA, &cdErrA) {
					t.Fatalf("%s modelA Execute() error = %T (%v), want *modelCooldownError", pathName, errExecA, errExecA)
				}
				for key := range cdErrA.Headers() {
					if strings.HasPrefix(strings.ToLower(key), "anthropic-ratelimit-unified-") {
						t.Fatalf("%s modelA leaked expired credential-quota header %s=%q", pathName, key, cdErrA.Headers().Get(key))
					}
				}
			})
		}
	})

	t.Run("matches longer independent model quota signals and deadline beneath shorter credential quota", func(t *testing.T) {
		for _, useAffinity := range []bool{true, false} {
			pathName := "scheduler"
			if useAffinity {
				pathName = "session_affinity"
			}
			t.Run(pathName, func(t *testing.T) {
				var selector Selector
				if useAffinity {
					selector = NewSessionAffinitySelector(&RoundRobinSelector{})
				}
				manager := NewManager(nil, selector, nil)
				manager.RegisterExecutor(&mockCustomErrorExecutor{identifier: "claude"})

				modelA := "issue-7417-dual-quota-a-" + pathName
				modelB := "issue-7417-dual-quota-b-" + pathName
				authID := "issue-7417-dual-quota-auth-" + pathName
				registry.GetGlobalRegistry().RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: modelA}, {ID: modelB}})
				t.Cleanup(func() {
					registry.GetGlobalRegistry().UnregisterClient(authID)
				})

				if _, errRegister := manager.Register(context.Background(), &Auth{
					ID:       authID,
					Provider: "claude",
					Status:   StatusActive,
				}); errRegister != nil {
					t.Fatalf("register auth: %v", errRegister)
				}

				// 1. Model A enters a 2h independent model-scoped seven_day quota cooldown.
				twoHours := 2 * time.Hour
				sevenDayCtx := internallogging.WithResponseHeadersHolder(context.Background())
				internallogging.SetResponseHeaders(sevenDayCtx, http.Header{
					"Anthropic-Ratelimit-Unified-Status":               {"rejected"},
					"Anthropic-Ratelimit-Unified-7d-Status":            {"rejected"},
					"Anthropic-Ratelimit-Unified-5h-Status":            {"allowed"},
					"Anthropic-Ratelimit-Unified-Representative-Claim": {"seven_day"},
					"Anthropic-Ratelimit-Unified-Reset":                {strconv.FormatInt(time.Now().Add(twoHours).Unix(), 10)},
				})
				manager.MarkResult(sevenDayCtx, Result{
					AuthID:          authID,
					Provider:        "claude",
					Model:           modelA,
					Success:         false,
					CredentialScope: false,
					RetryAfter:      &twoHours,
					Error:           &Error{Code: "rate_limit_error", Message: "7d model quota", HTTPStatus: http.StatusTooManyRequests},
				})

				// 2. Model B triggers a shorter 1h credential-scoped five_hour quota cooldown.
				oneHour := time.Hour
				fiveHourCtx := internallogging.WithResponseHeadersHolder(context.Background())
				internallogging.SetResponseHeaders(fiveHourCtx, http.Header{
					"Anthropic-Ratelimit-Unified-Status":               {"rejected"},
					"Anthropic-Ratelimit-Unified-5h-Status":            {"rejected"},
					"Anthropic-Ratelimit-Unified-7d-Status":            {"allowed"},
					"Anthropic-Ratelimit-Unified-Representative-Claim": {"five_hour"},
					"Anthropic-Ratelimit-Unified-Reset":                {strconv.FormatInt(time.Now().Add(oneHour).Unix(), 10)},
				})
				manager.MarkResult(fiveHourCtx, Result{
					AuthID:          authID,
					Provider:        "claude",
					Model:           modelB,
					Success:         false,
					CredentialScope: true,
					RetryAfter:      &oneHour,
					Error:           &Error{Code: ErrorCodeCredentialQuota, Message: "5h cred quota", HTTPStatus: http.StatusTooManyRequests},
				})

				// Model A must report seven_day AND ~2h Retry-After / reset (not 1h).
				_, errExecA := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: modelA}, cliproxyexecutor.Options{})
				var cdErrA *modelCooldownError
				if !errors.As(errExecA, &cdErrA) {
					t.Fatalf("%s modelA Execute() error = %T (%v), want *modelCooldownError", pathName, errExecA, errExecA)
				}
				hdrA := cdErrA.Headers()
				if got := hdrA.Get("Anthropic-Ratelimit-Unified-Representative-Claim"); got != "seven_day" {
					t.Fatalf("%s modelA claim = %q, want seven_day", pathName, got)
				}
				retrySecA, errSecA := strconv.ParseInt(hdrA.Get("Retry-After"), 10, 64)
				if errSecA != nil || retrySecA < 7100 {
					t.Fatalf("%s modelA Retry-After = %q, want ~7200s (>= 7100s) matching 2h independent deadline", pathName, hdrA.Get("Retry-After"))
				}

				// Model B must report five_hour AND ~1h Retry-After.
				_, errExecB := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: modelB}, cliproxyexecutor.Options{})
				var cdErrB *modelCooldownError
				if !errors.As(errExecB, &cdErrB) {
					t.Fatalf("%s modelB Execute() error = %T (%v), want *modelCooldownError", pathName, errExecB, errExecB)
				}
				hdrB := cdErrB.Headers()
				if got := hdrB.Get("Anthropic-Ratelimit-Unified-Representative-Claim"); got != "five_hour" {
					t.Fatalf("%s modelB claim = %q, want five_hour", pathName, got)
				}
				retrySecB, errSecB := strconv.ParseInt(hdrB.Get("Retry-After"), 10, 64)
				if errSecB != nil || retrySecB > 3660 || retrySecB < 3500 {
					t.Fatalf("%s modelB Retry-After = %q, want ~3600s", pathName, hdrB.Get("Retry-After"))
				}
			})
		}
	})

	t.Run("keeps active cooldown signals when hintless RetryAfter nil 429 reuses existing deadline", func(t *testing.T) {
		for _, useAffinity := range []bool{true, false} {
			pathName := "scheduler"
			if useAffinity {
				pathName = "session_affinity"
			}
			t.Run(pathName, func(t *testing.T) {
				var selector Selector
				if useAffinity {
					selector = NewSessionAffinitySelector(&RoundRobinSelector{})
				}
				manager := NewManager(nil, selector, nil)
				manager.RegisterExecutor(&mockCustomErrorExecutor{identifier: "claude"})

				credModel := "issue-7417-nilhint-cred-" + pathName
				credAuth := "issue-7417-nilhint-cred-auth-" + pathName
				modelScopedModel := "issue-7417-nilhint-model-" + pathName
				modelScopedAuth := "issue-7417-nilhint-model-auth-" + pathName
				authOnlyModel := "issue-7417-nilhint-authonly-" + pathName
				authOnlyID := "issue-7417-nilhint-authonly-auth-" + pathName

				registry.GetGlobalRegistry().RegisterClient(credAuth, "claude", []*registry.ModelInfo{{ID: credModel}})
				registry.GetGlobalRegistry().RegisterClient(modelScopedAuth, "claude", []*registry.ModelInfo{{ID: modelScopedModel}})
				registry.GetGlobalRegistry().RegisterClient(authOnlyID, "claude", []*registry.ModelInfo{{ID: authOnlyModel}})
				t.Cleanup(func() {
					registry.GetGlobalRegistry().UnregisterClient(credAuth)
					registry.GetGlobalRegistry().UnregisterClient(modelScopedAuth)
					registry.GetGlobalRegistry().UnregisterClient(authOnlyID)
				})

				for _, id := range []string{credAuth, modelScopedAuth, authOnlyID} {
					if _, errRegister := manager.Register(context.Background(), &Auth{
						ID:       id,
						Provider: "claude",
						Status:   StatusActive,
					}); errRegister != nil {
						t.Fatalf("register %s: %v", id, errRegister)
					}
				}

				twoHours := 2 * time.Hour
				sevenDayCtx := internallogging.WithResponseHeadersHolder(context.Background())
				internallogging.SetResponseHeaders(sevenDayCtx, http.Header{
					"Anthropic-Ratelimit-Unified-Status":               {"rejected"},
					"Anthropic-Ratelimit-Unified-7d-Status":            {"rejected"},
					"Anthropic-Ratelimit-Unified-5h-Status":            {"allowed"},
					"Anthropic-Ratelimit-Unified-Representative-Claim": {"seven_day"},
					"Anthropic-Ratelimit-Unified-Reset":                {strconv.FormatInt(time.Now().Add(twoHours).Unix(), 10)},
				})
				fiveHourNoHintCtx := internallogging.WithResponseHeadersHolder(context.Background())
				internallogging.SetResponseHeaders(fiveHourNoHintCtx, http.Header{
					"Anthropic-Ratelimit-Unified-Status":               {"rejected"},
					"Anthropic-Ratelimit-Unified-5h-Status":            {"rejected"},
					"Anthropic-Ratelimit-Unified-7d-Status":            {"allowed"},
					"Anthropic-Ratelimit-Unified-Representative-Claim": {"five_hour"},
				})

				manager.MarkResult(sevenDayCtx, Result{
					AuthID:          credAuth,
					Provider:        "claude",
					Model:           credModel,
					Success:         false,
					CredentialScope: true,
					RetryAfter:      &twoHours,
					Error:           &Error{Code: ErrorCodeCredentialQuota, Message: "7d", HTTPStatus: http.StatusTooManyRequests},
				})
				manager.MarkResult(fiveHourNoHintCtx, Result{
					AuthID:          credAuth,
					Provider:        "claude",
					Model:           credModel,
					Success:         false,
					CredentialScope: true,
					RetryAfter:      nil,
					Error:           &Error{Code: ErrorCodeCredentialQuota, Message: "5h no hint", HTTPStatus: http.StatusTooManyRequests},
				})

				manager.MarkResult(sevenDayCtx, Result{
					AuthID:          modelScopedAuth,
					Provider:        "claude",
					Model:           modelScopedModel,
					Success:         false,
					CredentialScope: false,
					RetryAfter:      &twoHours,
					Error:           &Error{Code: "rate_limit_error", Message: "7d", HTTPStatus: http.StatusTooManyRequests},
				})
				manager.MarkResult(fiveHourNoHintCtx, Result{
					AuthID:          modelScopedAuth,
					Provider:        "claude",
					Model:           modelScopedModel,
					Success:         false,
					CredentialScope: false,
					RetryAfter:      nil,
					Error:           &Error{Code: "rate_limit_error", Message: "5h no hint", HTTPStatus: http.StatusTooManyRequests},
				})

				manager.MarkResult(sevenDayCtx, Result{
					AuthID:     authOnlyID,
					Provider:   "claude",
					Success:    false,
					RetryAfter: &twoHours,
					Error:      &Error{Code: "rate_limit_error", Message: "7d", HTTPStatus: http.StatusTooManyRequests},
				})
				manager.MarkResult(fiveHourNoHintCtx, Result{
					AuthID:     authOnlyID,
					Provider:   "claude",
					Success:    false,
					RetryAfter: nil,
					Error:      &Error{Code: "rate_limit_error", Message: "5h no hint", HTTPStatus: http.StatusTooManyRequests},
				})

				for _, m := range []string{credModel, modelScopedModel, authOnlyModel} {
					_, errExec := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: m}, cliproxyexecutor.Options{})
					var cdErr *modelCooldownError
					if !errors.As(errExec, &cdErr) {
						t.Fatalf("%s Execute() error = %T (%v), want *modelCooldownError", m, errExec, errExec)
					}
					if got := cdErr.Headers().Get("Anthropic-Ratelimit-Unified-Representative-Claim"); got != "seven_day" {
						t.Fatalf("%s Representative-Claim = %q, want seven_day", m, got)
					}
				}
			})
		}
	})
}
