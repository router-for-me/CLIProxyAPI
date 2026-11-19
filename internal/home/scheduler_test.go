package home

import (
	"fmt"
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestAuthDispatchTransportsSchedulerContext(t *testing.T) {
	requests := make(chan string, 1)
	client, _ := newRedisCommandTestClient(t, func(args []string) string {
		if len(args) > 0 && strings.EqualFold(args[0], "HELLO") {
			return "%0\r\n"
		}
		if len(args) >= 2 && strings.EqualFold(args[0], "RPOP") {
			requests <- args[1]
			payload := `{"auth":{"id":"wire-auth"},"selection_id":"wire-selection"}`
			return fmt.Sprintf("$%d\r\n%s\r\n", len(payload), payload)
		}
		return "+OK\r\n"
	})
	want := SchedulerContext{Stream: true, RequestID: "request-1", TraceID: "trace-1", ExecutionSessionID: "execution-1"}
	ctx := WithSchedulerContext(t.Context(), want)
	if _, errRPop := client.RPopAuth(ctx, "gpt", "session-1", nil, 1); errRPop != nil {
		t.Fatal(errRPop)
	}
	var req authDispatchRequest
	if errUnmarshal := json.Unmarshal([]byte(<-requests), &req); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if req.SchedulerContext == nil || *req.SchedulerContext != want || req.SessionID != "session-1" {
		t.Fatalf("request=%+v", req)
	}
}

func TestSchedulerProtocolNegotiationAndHotReload(t *testing.T) {
	client := &Client{}
	if errDecode := client.updateSchedulerProtocol([]byte("home-scheduler-protocol: 1\n")); errDecode != nil || client.SchedulerProtocol() != 1 {
		t.Fatalf("protocol=%d error=%v", client.SchedulerProtocol(), errDecode)
	}
	if errDecode := client.updateSchedulerProtocol([]byte("home-scheduler-protocol: invalid\n")); errDecode == nil {
		t.Fatal("malformed capabilities accepted")
	}
	if client.SchedulerProtocol() != 1 {
		t.Fatal("failed decode changed capability state")
	}
	if errApply := client.handleSubscriptionPayload(t.Context(), redisChannelConfig, "host: localhost\n", func([]byte) error { return nil }); errApply != nil || client.SchedulerProtocol() != 0 {
		t.Fatalf("legacy config protocol=%d error=%v", client.SchedulerProtocol(), errApply)
	}
	if errApply := client.handleSubscriptionPayload(t.Context(), redisChannelConfig, "home-scheduler-protocol: 1\n", func([]byte) error { return nil }); errApply != nil || client.SchedulerProtocol() != 1 {
		t.Fatalf("new config protocol=%d error=%v", client.SchedulerProtocol(), errApply)
	}
}

func TestSchedulerContinuityResponseDecodingIsStrict(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		wantError     bool
	}{
		{"ready", `{"handled":true,"response":{"decision":"ready"}}`, false},
		{"not handled", `{"handled":false,"response":{}}`, false},
		{"invalid json", `{"handled":`, true},
		{"missing handled", `{}`, true},
		{"missing response", `{"handled":true}`, true},
		{"null response", `{"handled":true,"response":null}`, true},
		{"typed error", `{"error":{"type":"invalid_credential","message":"invalid key"}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := make(chan string, 1)
			client, _ := newRedisCommandTestClient(t, func(args []string) string {
				if len(args) > 0 && strings.EqualFold(args[0], "HELLO") {
					return "%0\r\n"
				}
				if len(args) >= 2 && strings.EqualFold(args[0], "RPOP") {
					requests <- args[1]
					return fmt.Sprintf("$%d\r\n%s\r\n", len(tc.payload), tc.payload)
				}
				return "+OK\r\n"
			})
			client.schedulerProtocol.Store(1)
			_, _, errEvaluate := client.EvaluateSchedulerContinuity(t.Context(), SchedulerContinuityQuery{Request: pluginapi.SchedulerContinuityRequest{Model: "gpt", Phase: "pre_admission"}})
			if (errEvaluate != nil) != tc.wantError {
				t.Fatalf("error=%v, wantError=%t", errEvaluate, tc.wantError)
			}
			var query struct {
				Type string `json:"type"`
			}
			select {
			case raw := <-requests:
				if errUnmarshal := json.Unmarshal([]byte(raw), &query); errUnmarshal != nil || query.Type != "scheduler-continuity" {
					t.Fatalf("query=%+v error=%v", query, errUnmarshal)
				}
			default:
				t.Fatalf("no observation request reached the server: %v", errEvaluate)
			}
		})
	}
}
