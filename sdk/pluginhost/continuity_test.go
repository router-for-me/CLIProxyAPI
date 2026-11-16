package pluginhost

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

var _ interface {
	HasSchedulerContinuity() bool
	SchedulerContinuity(context.Context, pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error)
} = (*Host)(nil)

func TestPublicHostContinuityNilSafe(t *testing.T) {
	for _, h := range []*Host{nil, {}} {
		if h.HasSchedulerContinuity() {
			t.Fatal("empty host advertised continuity")
		}
		resp, handled, err := h.SchedulerContinuity(context.Background(), pluginapi.SchedulerContinuityRequest{})
		if err != nil || handled || resp.Action != "" {
			t.Fatalf("empty host continuity: %+v %v %v", resp, handled, err)
		}
	}
}
