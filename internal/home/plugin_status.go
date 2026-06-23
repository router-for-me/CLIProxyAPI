package home

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/homeplugins"
)

const pluginStatusReportTimeout = 10 * time.Second

// PluginStatusClient defines the interface for pushing plugin status reports.
type PluginStatusClient interface {
	RPushPluginStatus(ctx context.Context, payload []byte) error
}

// ReportPluginStatus marshals the given report, sets NodeID and UpdatedAt,
// and pushes it to the provided client with a timeout.
func ReportPluginStatus(ctx context.Context, client PluginStatusClient, nodeID string, report homeplugins.SyncReport) error {
	if client == nil {
		return fmt.Errorf("home plugin status client is unavailable")
	}
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return fmt.Errorf("home plugin status node id is empty")
	}
	report.NodeID = nodeID
	report.UpdatedAt = time.Now().UTC()
	raw, errMarshal := json.Marshal(report)
	if errMarshal != nil {
		return errMarshal
	}
	if ctx == nil {
		ctx = context.Background()
	}
	reportCtx, cancel := context.WithTimeout(ctx, pluginStatusReportTimeout)
	defer cancel()
	return client.RPushPluginStatus(reportCtx, raw)
}
