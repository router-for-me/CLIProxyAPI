package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
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
