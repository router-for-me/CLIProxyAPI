package auth

import (
	"net/http"
	"strings"
	"testing"
)

func TestMetricAuthReason(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		auth *Auth
		want string
	}{
		{name: "nil", auth: nil, want: MetricReasonOther},
		{name: "active", auth: &Auth{Status: StatusActive}, want: MetricReasonOther},
		{
			name: "terminal unauthorized refresh",
			auth: &Auth{
				Status:      StatusError,
				Unavailable: true,
				LastError:   &Error{Code: "unauthorized", HTTPStatus: http.StatusUnauthorized, Message: "refresh rejected"},
			},
			want: MetricReasonUnauthorized,
		},
		{
			name: "invalid grant refresh retry",
			auth: &Auth{
				Status:        StatusError,
				StatusMessage: "invalid grant (retrying)",
				LastError:     &Error{Code: "invalid_grant", HTTPStatus: http.StatusBadRequest, Message: "invalid_grant"},
			},
			want: MetricReasonUnauthorized,
		},
		{
			name: "disabled invalid grant",
			auth: &Auth{
				Status:   StatusDisabled,
				Disabled: true,
				LastError: &Error{
					Code:       "invalid_grant",
					HTTPStatus: http.StatusBadRequest,
					Message:    "invalid_grant",
				},
			},
			want: MetricReasonUnauthorized,
		},
		{
			name: "refresh token invalid message",
			auth: &Auth{
				Status:        StatusError,
				Unavailable:   true,
				StatusMessage: "unauthorized (refresh token invalid)",
				LastError:     &Error{Code: "unauthorized", HTTPStatus: http.StatusUnauthorized},
			},
			want: MetricReasonUnauthorized,
		},
		{
			name: "quota exceeded cooldown",
			auth: &Auth{
				Status:        StatusError,
				Unavailable:   true,
				StatusMessage: "quota exhausted",
				Quota:         QuotaState{Exceeded: true, Reason: "quota"},
			},
			want: MetricReasonQuota,
		},
		{
			name: "credential quota cooldown",
			auth: &Auth{
				Status:      StatusError,
				Unavailable: true,
				Quota:       QuotaState{Exceeded: true, Reason: "credential_quota"},
			},
			want: MetricReasonQuota,
		},
		{
			name: "quota reason without exceeded flag",
			auth: &Auth{
				Status: StatusError,
				Quota:  QuotaState{Reason: "quota"},
			},
			want: MetricReasonQuota,
		},
		{
			name: "transient error is other",
			auth: &Auth{
				Status:        StatusError,
				Unavailable:   true,
				StatusMessage: "transient upstream error",
				LastError:     &Error{HTTPStatus: http.StatusBadGateway, Message: "bad gateway"},
			},
			want: MetricReasonOther,
		},
		{
			name: "unauthorized wins over quota",
			auth: &Auth{
				Status:      StatusError,
				Unavailable: true,
				LastError:   &Error{Code: "unauthorized", HTTPStatus: http.StatusUnauthorized},
				Quota:       QuotaState{Exceeded: true, Reason: "quota"},
			},
			want: MetricReasonUnauthorized,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := MetricAuthReason(tc.auth); got != tc.want {
				t.Fatalf("MetricAuthReason() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMetricAuthProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider string
		want     string
	}{
		{name: "empty", provider: "", want: metricProviderUnknown},
		{name: "codex", provider: "Codex", want: "codex"},
		{name: "openai compatibility", provider: "openai-compatibility", want: "openai-compatibility"},
		{name: "openai compatible alias", provider: "openai-compatible-custom", want: "openai-compatibility"},
		{name: "at-sign rejected", provider: "name@host", want: metricProviderUnknown},
		{name: "json suffix rejected", provider: "cred.json", want: metricProviderUnknown},
		{name: "path rejected", provider: "dir/file", want: metricProviderUnknown},
		{name: "spaces rejected", provider: "my account", want: metricProviderUnknown},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := MetricAuthProvider(&Auth{Provider: tc.provider})
			if got != tc.want {
				t.Fatalf("MetricAuthProvider(%q) = %q, want %q", tc.provider, got, tc.want)
			}
			if strings.Contains(got, "@") || strings.Contains(got, ".json") || strings.Contains(got, "/") {
				t.Fatalf("provider label %q looks identifying", got)
			}
		})
	}

	if got := MetricAuthProvider(nil); got != metricProviderUnknown {
		t.Fatalf("MetricAuthProvider(nil) = %q, want %q", got, metricProviderUnknown)
	}
}
