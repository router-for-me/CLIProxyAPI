package auth

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestQuotaScheduleReplacementIsolatesPassiveObservation(t *testing.T) {
	for _, mode := range []string{"file_snapshot", "credential_quota"} {
		for _, changed := range []bool{false, true} {
			t.Run(mode+"/changed="+strconv.FormatBool(changed), func(t *testing.T) {
				ctx := WithSkipPersist(context.Background())
				manager := NewManager(nil, &GreedySelector{}, nil)
				oldMetadata := map[string]any{"type": "claude", "access_token": "token-a", "account_uuid": "account-a", "organization_uuid": "org-a"}
				path := filepath.Join(t.TempDir(), "credential.json")
				oldData, err := json.Marshal(oldMetadata)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(path, oldData, 0600); err != nil {
					t.Fatal(err)
				}
				auth, err := manager.Register(ctx, &Auth{ID: "credential.json", Provider: "claude", Status: StatusActive, Metadata: oldMetadata, Attributes: map[string]string{"path": path, "source": path}})
				if err != nil {
					t.Fatal(err)
				}
				now := time.Now().UTC().Truncate(time.Second)
				weekly := now.Add(24 * time.Hour)
				headers := http.Header{}
				headers.Set("Anthropic-Ratelimit-Unified-7d-Reset", strconv.FormatInt(weekly.Unix(), 10))
				observedCtx := internallogging.WithResponseHeadersHolder(ctx)
				internallogging.SetResponseHeaders(observedCtx, headers)
				result := Result{AuthID: auth.ID, Provider: "claude", Success: true}
				if mode == "credential_quota" {
					delay := 10 * time.Minute
					result.Success = false
					result.CredentialScope = true
					result.RetryAfter = &delay
					result.Error = &Error{HTTPStatus: 429, Message: "quota"}
				}
				manager.MarkResult(observedCtx, result)
				before, _ := manager.GetByID(auth.ID)
				if !EffectiveQuotaResetSchedule(before).WeeklyResetAt.Equal(weekly) || before.Quota.ObservedAt.IsZero() {
					t.Fatalf("passive observation setup failed: %+v", before)
				}
				if mode == "credential_quota" && (!before.Quota.Exceeded || before.Quota.Reason != "credential_quota") {
					t.Fatalf("credential cooldown setup failed: %+v", before.Quota)
				}
				metadata := map[string]any{"type": "claude", "access_token": "token-b", "account_uuid": "account-a", "organization_uuid": "org-a"}
				if changed {
					metadata["account_uuid"] = "account-b"
				}
				incoming := &Auth{ID: auth.ID, Provider: "claude", Status: StatusActive, Metadata: metadata, Attributes: map[string]string{"path": path, "source": path}}
				if mode == "file_snapshot" {
					data, err := json.Marshal(metadata)
					if err != nil {
						t.Fatal(err)
					}
					if err = os.WriteFile(path, data, 0600); err != nil {
						t.Fatal(err)
					}
					incoming.BindFileSnapshot(path, data)
				} else {
					incoming = before.Clone()
					incoming.Metadata = metadata
				}
				updated, err := manager.Update(ctx, incoming)
				if err != nil {
					t.Fatal(err)
				}
				if updated.Quota.Exceeded != before.Quota.Exceeded || updated.Quota.Reason != before.Quota.Reason || !updated.Quota.NextRecoverAt.Equal(before.Quota.NextRecoverAt) || updated.Unavailable != before.Unavailable || !updated.NextRetryAfter.Equal(before.NextRetryAfter) || !reflect.DeepEqual(updated.ModelStates, before.ModelStates) {
					t.Fatal("account observation isolation changed cooldown restrictions")
				}
				schedule := EffectiveQuotaResetSchedule(updated)
				if changed {
					if !schedule.WeeklyResetAt.IsZero() || !schedule.FiveHourResetAt.IsZero() {
						t.Errorf("previous account passive headers revived reset schedule: %+v", schedule)
					}
				} else if !schedule.WeeklyResetAt.Equal(weekly) || !updated.Quota.ObservedAt.Equal(before.Quota.ObservedAt) || !reflect.DeepEqual(updated.Quota.Signals, before.Quota.Signals) {
					t.Fatal("same account lost passive observation")
				}
				if mode == "file_snapshot" {
					other := &Auth{ID: "other", Provider: "claude", Status: StatusActive, QuotaResetSchedule: QuotaResetSchedule{WeeklyResetAt: now.Add(48 * time.Hour), ObservedAt: now}}
					picked, err := (&GreedySelector{}).Pick(ctx, "claude", "", cliproxyexecutor.Options{}, []*Auth{updated, other})
					if err != nil {
						t.Fatal(err)
					}
					want := updated.ID
					if changed {
						want = other.ID
					}
					if picked.ID != want {
						t.Fatalf("replacement used prior-account greedy rank: got=%s want=%s", picked.ID, want)
					}
				}
			})
		}
	}
}
