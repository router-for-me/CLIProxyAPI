package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func issue7785JSONResponse(req *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func TestIssue7785RefreshSkipsLoadCodeAssistWhenCreditsDisabled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		auth := &cliproxyauth.Auth{
			ID:       "issue7785-credits-disabled-active",
			Provider: "antigravity",
			Status:   cliproxyauth.StatusActive,
			Metadata: map[string]any{
				"access_token":  "expired-access",
				"refresh_token": "issue7785-refresh-credits-disabled",
				"project_id":    "existing-project",
				"expired":       time.Now().Add(-time.Hour).Format(time.RFC3339),
			},
		}
		t.Cleanup(func() {
			cliproxyauth.SetAntigravityCreditsHint(auth.ID, cliproxyauth.AntigravityCreditsHint{})
		})

		var tokenCalls atomic.Int32
		var loadCodeAssistCalls atomic.Int32
		transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.String() {
			case "https://oauth2.googleapis.com/token":
				tokenCalls.Add(1)
				return issue7785JSONResponse(req, `{"access_token":"new-access","expires_in":3600}`), nil
			case "https://daily-cloudcode-pa.googleapis.com/v1internal:loadCodeAssist":
				loadCodeAssistCalls.Add(1)
				return issue7785JSONResponse(req, `{"paidTier":{"id":"pro","availableCredits":[{"creditType":"GOOGLE_ONE_AI","creditAmount":"1000","minimumCreditAmountForUsage":"50"}]}}`), nil
			default:
				return nil, fmt.Errorf("unexpected intercepted request: %s", req.URL)
			}
		})

		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", transport)
		exec := NewAntigravityExecutor(&config.Config{
			QuotaExceeded: config.QuotaExceeded{AntigravityCredits: false},
		})

		updated, errRefresh := exec.Refresh(ctx, auth)
		synctest.Wait()

		if errRefresh != nil {
			t.Fatalf("Refresh() error = %v, want nil", errRefresh)
		}
		if updated == nil || metaStringValue(updated.Metadata, "access_token") != "new-access" {
			t.Fatalf("Refresh() updated = %#v, want access_token=new-access", updated)
		}
		if got := tokenCalls.Load(); got != 1 {
			t.Fatalf("oauth2 token calls = %d, want 1", got)
		}
		if got := loadCodeAssistCalls.Load(); got != 0 {
			t.Fatalf("loadCodeAssist calls = %d, want 0 when antigravity-credits is false", got)
		}
		if _, loaded := antigravityCreditsHintRefreshByID.Load(auth.ID); loaded {
			t.Fatalf("expected no queued credits refresh state when antigravity-credits is false")
		}
	})
}

func TestIssue7785RefreshSkipsCloudCodePAForDisabledCredentials(t *testing.T) {
	cases := []struct {
		name      string
		disabled  bool
		status    cliproxyauth.Status
		projectID string
	}{
		{
			name:      "Disabled and StatusDisabled with project_id",
			disabled:  true,
			status:    cliproxyauth.StatusDisabled,
			projectID: "existing-project",
		},
		{
			name:      "Disabled flag only with project_id",
			disabled:  true,
			status:    cliproxyauth.StatusActive,
			projectID: "existing-project",
		},
		{
			name:      "StatusDisabled only with project_id",
			disabled:  false,
			status:    cliproxyauth.StatusDisabled,
			projectID: "existing-project",
		},
		{
			name:      "Disabled and StatusDisabled missing project_id",
			disabled:  true,
			status:    cliproxyauth.StatusDisabled,
			projectID: "",
		},
		{
			name:      "StatusDisabled only missing project_id",
			disabled:  false,
			status:    cliproxyauth.StatusDisabled,
			projectID: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				resetAntigravityCreditsRetryState()
				t.Cleanup(resetAntigravityCreditsRetryState)

				meta := map[string]any{
					"access_token":  "expired-access",
					"refresh_token": "issue7785-refresh-" + tc.name,
					"expired":       time.Now().Add(-time.Hour).Format(time.RFC3339),
				}
				if tc.projectID != "" {
					meta["project_id"] = tc.projectID
				}
				auth := &cliproxyauth.Auth{
					ID:       "issue7785-disabled-" + tc.name,
					Provider: "antigravity",
					Disabled: tc.disabled,
					Status:   tc.status,
					Metadata: meta,
				}
				t.Cleanup(func() {
					cliproxyauth.SetAntigravityCreditsHint(auth.ID, cliproxyauth.AntigravityCreditsHint{})
				})

				var tokenCalls atomic.Int32
				var loadCodeAssistCalls atomic.Int32
				var onboardUserCalls atomic.Int32
				transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
					switch {
					case req.URL.String() == "https://oauth2.googleapis.com/token":
						tokenCalls.Add(1)
						return issue7785JSONResponse(req, `{"access_token":"new-access","expires_in":3600}`), nil
					case strings.HasSuffix(req.URL.Path, ":loadCodeAssist"):
						loadCodeAssistCalls.Add(1)
						return &http.Response{
							StatusCode: http.StatusForbidden,
							Header:     http.Header{"Content-Type": {"application/json"}},
							Body:       io.NopCloser(strings.NewReader(antigravityTOSBody)),
							Request:    req,
						}, nil
					case strings.HasSuffix(req.URL.Path, ":onboardUser"):
						onboardUserCalls.Add(1)
						return issue7785JSONResponse(req, `{"done":true,"response":{"cloudaicompanionProject":{"id":"onboarded-project"}}}`), nil
					default:
						return nil, fmt.Errorf("unexpected intercepted request: %s", req.URL)
					}
				})

				cfg := &config.Config{
					QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
				}
				cfg.Antigravity.TOSPauseWindow = "1h"
				ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", transport)
				exec := NewAntigravityExecutor(cfg)

				updated, errRefresh := exec.Refresh(ctx, auth)
				synctest.Wait()

				if errRefresh != nil {
					t.Fatalf("Refresh() error = %v, want nil (OAuth /token refresh must remain active)", errRefresh)
				}
				if updated == nil || metaStringValue(updated.Metadata, "access_token") != "new-access" {
					t.Fatalf("Refresh() updated = %#v, want access_token=new-access", updated)
				}
				if got := tokenCalls.Load(); got != 1 {
					t.Fatalf("oauth2 token calls = %d, want 1", got)
				}
				if got := loadCodeAssistCalls.Load(); got != 0 {
					t.Fatalf("loadCodeAssist calls = %d, want 0 for disabled credential", got)
				}
				if got := onboardUserCalls.Load(); got != 0 {
					t.Fatalf("onboardUser calls = %d, want 0 for disabled credential", got)
				}
				if antigravityInTOSPause(cfg, auth) {
					t.Fatalf("antigravityInTOSPause() = true, want false (disabled credential must not hit loadCodeAssist and re-arm TOS pause)")
				}
			})
		})
	}
}

func TestIssue7785DirectCreditsAndPreparePathsRespectConfigAndDisabled(t *testing.T) {
	t.Run("queueAntigravityCreditsRefresh skips when credits disabled", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			resetAntigravityCreditsRetryState()
			t.Cleanup(resetAntigravityCreditsRetryState)

			var loadCodeAssistCalls atomic.Int32
			transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				loadCodeAssistCalls.Add(1)
				return issue7785JSONResponse(req, `{"paidTier":{"id":"pro","availableCredits":[]}}`), nil
			})
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", transport)
			exec := NewAntigravityExecutor(&config.Config{
				QuotaExceeded: config.QuotaExceeded{AntigravityCredits: false},
			})
			auth := &cliproxyauth.Auth{ID: "issue7785-queue-cfg-false", Status: cliproxyauth.StatusActive}

			exec.queueAntigravityCreditsRefresh(ctx, auth, "token", 0)
			synctest.Wait()

			if got := loadCodeAssistCalls.Load(); got != 0 {
				t.Fatalf("loadCodeAssist calls = %d, want 0", got)
			}
		})
	})

	t.Run("queueAntigravityCreditsRefresh skips when auth disabled", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			resetAntigravityCreditsRetryState()
			t.Cleanup(resetAntigravityCreditsRetryState)

			var loadCodeAssistCalls atomic.Int32
			transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				loadCodeAssistCalls.Add(1)
				return issue7785JSONResponse(req, `{"paidTier":{"id":"pro","availableCredits":[]}}`), nil
			})
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", transport)
			exec := NewAntigravityExecutor(&config.Config{
				QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
			})
			auth := &cliproxyauth.Auth{ID: "issue7785-queue-disabled", Status: cliproxyauth.StatusDisabled}

			exec.queueAntigravityCreditsRefresh(ctx, auth, "token", 0)
			synctest.Wait()

			if got := loadCodeAssistCalls.Load(); got != 0 {
				t.Fatalf("loadCodeAssist calls = %d, want 0", got)
			}
		})
	})

	t.Run("updateAntigravityCreditsBalanceForTask skips when credits disabled", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		var loadCodeAssistCalls atomic.Int32
		transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			loadCodeAssistCalls.Add(1)
			return issue7785JSONResponse(req, `{"paidTier":{"id":"pro","availableCredits":[]}}`), nil
		})
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", transport)
		exec := NewAntigravityExecutor(&config.Config{
			QuotaExceeded: config.QuotaExceeded{AntigravityCredits: false},
		})
		auth := &cliproxyauth.Auth{ID: "issue7785-update-cfg-false", Status: cliproxyauth.StatusActive}

		exec.updateAntigravityCreditsBalanceForTask(ctx, auth, "token", nil)

		if got := loadCodeAssistCalls.Load(); got != 0 {
			t.Fatalf("loadCodeAssist calls = %d, want 0", got)
		}
	})

	t.Run("updateAntigravityCreditsBalanceForTask skips when auth disabled", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)

		var loadCodeAssistCalls atomic.Int32
		transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			loadCodeAssistCalls.Add(1)
			return issue7785JSONResponse(req, `{"paidTier":{"id":"pro","availableCredits":[]}}`), nil
		})
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", transport)
		exec := NewAntigravityExecutor(&config.Config{
			QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
		})
		auth := &cliproxyauth.Auth{ID: "issue7785-update-disabled", Disabled: true, Status: cliproxyauth.StatusDisabled}

		exec.updateAntigravityCreditsBalanceForTask(ctx, auth, "token", nil)

		if got := loadCodeAssistCalls.Load(); got != 0 {
			t.Fatalf("loadCodeAssist calls = %d, want 0", got)
		}
	})

	t.Run("PrepareRequestAuth skips post-refresh credits query when credits disabled", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			resetAntigravityCreditsRetryState()
			t.Cleanup(resetAntigravityCreditsRetryState)

			var tokenCalls atomic.Int32
			var loadCodeAssistCalls atomic.Int32
			transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				switch {
				case req.URL.String() == "https://oauth2.googleapis.com/token":
					tokenCalls.Add(1)
					return issue7785JSONResponse(req, `{"access_token":"fresh-access","expires_in":3600}`), nil
				case strings.HasSuffix(req.URL.Path, ":loadCodeAssist"):
					loadCodeAssistCalls.Add(1)
					return issue7785JSONResponse(req, `{"cloudaicompanionProject":"discovered-project"}`), nil
				default:
					return nil, fmt.Errorf("unexpected intercepted request: %s", req.URL)
				}
			})

			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", transport)
			exec := NewAntigravityExecutor(&config.Config{
				QuotaExceeded: config.QuotaExceeded{AntigravityCredits: false},
			})
			auth := &cliproxyauth.Auth{
				ID:       "issue7785-prepare-cfg-false",
				Provider: "antigravity",
				Status:   cliproxyauth.StatusActive,
				Metadata: map[string]any{
					"access_token":  "expired-access",
					"refresh_token": "issue7785-prepare-refresh",
					"expired":       time.Now().Add(-time.Hour).Format(time.RFC3339),
				},
			}
			t.Cleanup(func() {
				cliproxyauth.SetAntigravityCreditsHint(auth.ID, cliproxyauth.AntigravityCreditsHint{})
			})

			updated, errPrepare := exec.PrepareRequestAuth(ctx, auth)
			synctest.Wait()

			if errPrepare != nil {
				t.Fatalf("PrepareRequestAuth() error = %v, want nil", errPrepare)
			}
			if updated == nil || antigravityProjectIDFromAuth(updated) != "discovered-project" {
				t.Fatalf("PrepareRequestAuth() updated = %#v, want project_id=discovered-project", updated)
			}
			if got := tokenCalls.Load(); got != 1 {
				t.Fatalf("oauth2 token calls = %d, want 1", got)
			}
			if got := loadCodeAssistCalls.Load(); got != 1 {
				t.Fatalf("loadCodeAssist calls = %d, want 1 (project discovery only, no credits query)", got)
			}
		})
	})

	t.Run("maybeRefreshAntigravityCreditsHint skips disabled auth in home KV mode", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			resetAntigravityCreditsRetryState()
			t.Cleanup(resetAntigravityCreditsRetryState)

			kvClient := newFakeAntigravityKVClient()
			useFakeAntigravityKVClient(t, kvClient, true, nil)

			var loadCodeAssistCalls atomic.Int32
			transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				loadCodeAssistCalls.Add(1)
				return issue7785JSONResponse(req, `{"paidTier":{"id":"pro","availableCredits":[]}}`), nil
			})
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", transport)
			exec := NewAntigravityExecutor(&config.Config{
				QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
			})
			auth := &cliproxyauth.Auth{
				ID:       "issue7785-home-disabled",
				Provider: "antigravity",
				Status:   cliproxyauth.StatusDisabled,
				Metadata: map[string]any{
					"access_token": "token",
					"project_id":   "existing-project",
				},
			}

			exec.maybeRefreshAntigravityCreditsHint(ctx, auth, "token")
			synctest.Wait()

			if kvClient.setNXCount != 0 {
				t.Fatalf("KVSetNX count = %d, want 0 for disabled auth", kvClient.setNXCount)
			}
			if got := loadCodeAssistCalls.Load(); got != 0 {
				t.Fatalf("loadCodeAssist calls = %d, want 0 for disabled auth", got)
			}
		})
	})
}
