package cliproxy

import (
	"testing"

	internalredisqueue "github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	internalusage "github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestServiceApplyUsageStatisticsConfigHonorsConfig(t *testing.T) {
	prevStatsEnabled := internalusage.StatisticsEnabled()
	prevQueueEnabled := internalredisqueue.UsageStatisticsEnabled()
	t.Cleanup(func() {
		internalusage.SetStatisticsEnabled(prevStatsEnabled)
		internalredisqueue.SetUsageStatisticsEnabled(prevQueueEnabled)
	})

	service := &Service{}
	service.applyUsageStatisticsConfig(&config.Config{UsageStatisticsEnabled: false})
	if internalusage.StatisticsEnabled() {
		t.Fatalf("statistics enabled = true, want false")
	}
	if internalredisqueue.UsageStatisticsEnabled() {
		t.Fatalf("redisqueue usage enabled = true, want false")
	}

	service.applyUsageStatisticsConfig(&config.Config{UsageStatisticsEnabled: true})
	if !internalusage.StatisticsEnabled() {
		t.Fatalf("statistics enabled = false, want true")
	}
	if !internalredisqueue.UsageStatisticsEnabled() {
		t.Fatalf("redisqueue usage enabled = false, want true")
	}
}
