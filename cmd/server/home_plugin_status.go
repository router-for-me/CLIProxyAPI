package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/homeplugins"
)

const homePluginStatusReportTimeout = 10 * time.Second

type homePluginStatusClient interface {
	RPushPluginStatus(ctx context.Context, payload []byte) error
}

func reportHomePluginStatus(ctx context.Context, client homePluginStatusClient, nodeID string, report homeplugins.SyncReport) error {
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
	reportCtx, cancel := context.WithTimeout(ctx, homePluginStatusReportTimeout)
	defer cancel()
	return client.RPushPluginStatus(reportCtx, raw)
}
