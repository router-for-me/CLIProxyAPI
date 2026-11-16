package pluginhost

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type continuityCaptureClient struct {
	method string
	raw    []byte
}

func (c *continuityCaptureClient) Call(_ context.Context, method string, raw []byte) ([]byte, error) {
	c.method, c.raw = method, append([]byte(nil), raw...)
	return []byte(`{"ok":true,"result":{"action":"ready","reason":"weekly_only_available"}}`), nil
}
func (*continuityCaptureClient) Shutdown() {}

func TestContinuityRPCRequestRetainsCandidateIdentity(t *testing.T) {
	client := &continuityCaptureClient{}
	adapter := &rpcPluginAdapter{client: client}
	resp, err := adapter.Continuity(context.Background(), pluginapi.SchedulerContinuityRequest{RequestID: "request-1", PreviousAttemptRejected429: true, Pick: pluginapi.SchedulerPickRequest{Providers: []string{"codex"}, Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "weekly", Provider: "codex", Metadata: map[string]any{"auth_index": "idx-weekly", "drop": make(chan struct{})}}}}})
	if err != nil || resp.Action != "ready" || client.method != pluginabi.MethodSchedulerContinuity {
		t.Fatalf("response=%#v method=%q err=%v", resp, client.method, err)
	}
	var decoded pluginapi.SchedulerContinuityRequest
	if err := json.Unmarshal(client.raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.RequestID != "request-1" || !decoded.PreviousAttemptRejected429 || len(decoded.Pick.Candidates) != 1 || decoded.Pick.Candidates[0].Provider != "codex" || decoded.Pick.Candidates[0].Metadata["auth_index"] != "idx-weekly" {
		t.Fatalf("request=%s", client.raw)
	}
	if _, ok := decoded.Pick.Candidates[0].Metadata["drop"]; ok {
		t.Fatalf("unsafe metadata survived: %s", client.raw)
	}
}
