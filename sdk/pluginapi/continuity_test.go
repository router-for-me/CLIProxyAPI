package pluginapi

import (
	"context"
	"testing"

	"github.com/MoeForge/moego/json"
)

type continuityStub struct{}

func (continuityStub) Continuity(context.Context, SchedulerContinuityRequest) (SchedulerContinuityResponse, error) {
	return SchedulerContinuityResponse{Decision: SchedulerContinuityReady}, nil
}

func TestSchedulerContinuityResponseUnmarshalJSONSupportsPascalAndSnakeCase(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want SchedulerContinuityResponse
	}{
		{
			name: "snake_case",
			raw:  `{"decision":"wait","poll_interval_ms":250,"wait_budget_ms":5000,"reason":"5h_cooldown"}`,
			want: SchedulerContinuityResponse{
				Decision:       SchedulerContinuityWait,
				PollIntervalMs: 250,
				WaitBudgetMs:   5000,
				Reason:         "5h_cooldown",
			},
		},
		{
			name: "pascal_case",
			raw:  `{"Decision":"ready","PollIntervalMs":100,"WaitBudgetMs":1000,"Reason":"recovered"}`,
			want: SchedulerContinuityResponse{
				Decision:       SchedulerContinuityReady,
				PollIntervalMs: 100,
				WaitBudgetMs:   1000,
				Reason:         "recovered",
			},
		},
		{
			name: "disabled",
			raw:  `{"decision":"disabled"}`,
			want: SchedulerContinuityResponse{
				Decision: SchedulerContinuityDisabled,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got SchedulerContinuityResponse
			if errUnmarshal := json.Unmarshal([]byte(tc.raw), &got); errUnmarshal != nil {
				t.Fatalf("Unmarshal() error = %v", errUnmarshal)
			}
			if got != tc.want {
				t.Fatalf("got = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestCapabilitiesAcceptsSchedulerContinuity(t *testing.T) {
	caps := Capabilities{
		SchedulerContinuity: continuityStub{},
	}
	if caps.SchedulerContinuity == nil {
		t.Fatal("SchedulerContinuity = nil, want non-nil")
	}
}
