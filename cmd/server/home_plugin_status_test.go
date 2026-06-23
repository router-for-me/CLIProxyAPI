package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/homeplugins"
)

type recordingHomePluginStatusClient struct {
	payload []byte
	err     error
}

func (c *recordingHomePluginStatusClient) RPushPluginStatus(ctx context.Context, payload []byte) error {
	c.payload = append([]byte(nil), payload...)
	return c.err
}

func TestReportHomePluginStatusPushesNodeReport(t *testing.T) {
	client := &recordingHomePluginStatusClient{}
	report := homeplugins.SyncReport{
		Task:    "plugin-sync",
		Status:  "success",
		OK:      true,
		Plugins: []homeplugins.PluginInstallStatus{{ID: "sample", InstallStatus: "installed"}},
	}

	if errReport := reportHomePluginStatus(context.Background(), client, " node-1 ", report); errReport != nil {
		t.Fatalf("reportHomePluginStatus() error = %v", errReport)
	}
	var payload homeplugins.SyncReport
	if errUnmarshal := json.Unmarshal(client.payload, &payload); errUnmarshal != nil {
		t.Fatalf("unmarshal payload: %v", errUnmarshal)
	}
	if payload.NodeID != "node-1" || !payload.OK || len(payload.Plugins) != 1 {
		t.Fatalf("payload = %+v, want node report", payload)
	}
	if payload.UpdatedAt.IsZero() {
		t.Fatal("payload UpdatedAt is zero")
	}
}

func TestReportHomePluginStatusPushesEmptyReport(t *testing.T) {
	client := &recordingHomePluginStatusClient{}
	report := homeplugins.SyncReport{
		Task:    "plugin-sync",
		Status:  "success",
		OK:      true,
		Plugins: []homeplugins.PluginInstallStatus{},
	}

	if errReport := reportHomePluginStatus(context.Background(), client, "node-1", report); errReport != nil {
		t.Fatalf("reportHomePluginStatus() error = %v", errReport)
	}
	var payload homeplugins.SyncReport
	if errUnmarshal := json.Unmarshal(client.payload, &payload); errUnmarshal != nil {
		t.Fatalf("unmarshal payload: %v", errUnmarshal)
	}
	if payload.NodeID != "node-1" || len(payload.Plugins) != 0 {
		t.Fatalf("payload = %+v, want empty node report", payload)
	}
}

func TestReportHomePluginStatusRequiresNodeID(t *testing.T) {
	client := &recordingHomePluginStatusClient{}
	report := homeplugins.SyncReport{
		Plugins: []homeplugins.PluginInstallStatus{{ID: "sample", InstallStatus: "failed"}},
	}

	errReport := reportHomePluginStatus(context.Background(), client, " ", report)
	if errReport == nil || !strings.Contains(errReport.Error(), "node id") {
		t.Fatalf("reportHomePluginStatus() error = %v, want node id error", errReport)
	}
	if len(client.payload) != 0 {
		t.Fatalf("client payload = %s, want none", client.payload)
	}
}

func TestReportHomePluginStatusPropagatesPushError(t *testing.T) {
	client := &recordingHomePluginStatusClient{err: errors.New("push failed")}
	report := homeplugins.SyncReport{
		Plugins: []homeplugins.PluginInstallStatus{{ID: "sample", InstallStatus: "installed"}},
	}

	errReport := reportHomePluginStatus(context.Background(), client, "node-1", report)
	if !errors.Is(errReport, client.err) {
		t.Fatalf("reportHomePluginStatus() error = %v, want push failed", errReport)
	}
}
