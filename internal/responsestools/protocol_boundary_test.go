package responsestools

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func prepareNativeMetaBoundary(t *testing.T, body string) Prepared {
	t.Helper()
	p, err := Prepare([]byte(body), ConventionPolicy(Route{Provider: "meta", UpstreamFormat: "codex"}), DefaultLimits(), NewLimiter(DefaultLimits()))
	if err != nil {
		t.Fatal(err)
	}
	if p.Attempt != nil {
		t.Cleanup(p.Attempt.Close)
	}
	return p
}

func TestPrepareNativeSearchCustomDiscovery(t *testing.T) {
	p := prepareNativeMetaBoundary(t, `{"tools":[{"type":"tool_search","execution":"client"}],"input":[{"type":"tool_search_call","id":"tsc_1","call_id":"s1","execution":"client","arguments":{"query":"patch"}},{"type":"tool_search_output","id":"tso_1","call_id":"s1","execution":"client","tools":[{"type":"custom","name":"apply_patch","format":{"type":"text"}}]}]}`)
	var root map[string]any
	if err := json.Unmarshal(p.Body, &root); err != nil {
		t.Fatal(err)
	}
	item := root["input"].([]any)[1].(map[string]any)
	tool := item["tools"].([]any)[0].(map[string]any)
	if tool["type"] != "function" {
		t.Fatalf("native Meta discovery leaked custom declaration; attempt=%v body=%s", p.Attempt != nil, p.Body)
	}
	if p.Attempt == nil {
		t.Fatal("custom discovery must retain its response bridge")
	}
	_, aliases := p.Attempt.WireAliases()
	if len(aliases) != 1 || aliases[0] != tool["name"] {
		t.Fatalf("discovered wrapper missing from wire contract: %v", aliases)
	}
}

func TestPrepareNativeSearchCustomDeferLoading(t *testing.T) {
	p := prepareNativeMetaBoundary(t, `{"tools":[{"type":"tool_search","execution":"client"},{"type":"custom","name":"apply_patch","defer_loading":true,"format":{"type":"text"}}],"input":[]}`)
	var root map[string]any
	if err := json.Unmarshal(p.Body, &root); err != nil {
		t.Fatal(err)
	}
	tool := root["tools"].([]any)[1].(map[string]any)
	if tool["defer_loading"] != true {
		t.Fatalf("native search custom wrapping lost defer_loading: %s", p.Body)
	}
}

func TestRewriteSearchResponseRejectsMalformedArguments(t *testing.T) {
	p, err := Prepare([]byte(`{"tools":[{"type":"tool_search","execution":"client"}],"input":[]}`), RoutePolicy{ClientSearch: ClientSearchBridge}, DefaultLimits(), NewLimiter(DefaultLimits()))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Attempt.Close()
	for _, arguments := range []string{"{"} {
		t.Run(arguments, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"output": []any{map[string]any{"type": "function_call", "id": "fc_search", "name": "tool_search", "call_id": "s1", "arguments": arguments}}})
			got, err := p.Attempt.RewriteResponse(raw)
			var compat *ToolCompatibilityError
			if !errors.As(err, &compat) || compat.StatusCode() != 502 || compat.Reason != ReasonUpstreamContract {
				t.Fatalf("want 502 upstream_contract, got body=%s err=%v", got, err)
			}
		})
	}
}

func TestRepairInputItemIDsEscapedValue(t *testing.T) {
	_, literalChanged, literalErr := RepairInputItemIDs([]byte(`{"input":[{"type":"tool_search_call","id":"fc_x","call_id":"s1","arguments":{}}]}`))
	if literalErr != nil || !literalChanged {
		t.Fatalf("literal positive control failed: changed=%v err=%v", literalChanged, literalErr)
	}
	body := []byte(`{"metadata":{"n":9007199254740993,"s":"\u4e2d","f":25000.0},"input":[{"type":"tool_search_call","id":"\u0066\u0063\u005fx","call_id":"s1","arguments":{}}]}`)
	original := string(body)
	want := strings.Replace(original, `"\u0066\u0063\u005fx"`, `"tsc_x"`, 1)
	got, changed, err := RepairInputItemIDs(body)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || string(got) != want || string(body) != original {
		t.Fatalf("escaped repair changed bytes outside the id: changed=%v got=%s want=%s", changed, got, want)
	}
	again, changed, err := RepairInputItemIDs(got)
	if err != nil || changed || string(again) != want {
		t.Fatalf("escaped repair is not idempotent: changed=%v err=%v", changed, err)
	}
}

func TestPrepareRejectsInitialAttemptBudget(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxAttemptBytes = 64
	if err := ValidateLimits(limits); err != nil {
		t.Fatal(err)
	}
	limiter := NewLimiter(limits)
	p, err := Prepare([]byte(`{"tools":[{"type":"custom","name":"apply_patch"}],"input":[]}`), RoutePolicy{CustomTools: CustomToolsFunction}, limits, limiter)
	if p.Attempt != nil {
		defer p.Attempt.Close()
	}
	if err == nil {
		_, b := limiter.Usage()
		t.Fatalf("initial lease exceeds per-attempt limit: reserved=%d limit=%d", b, limits.MaxAttemptBytes)
	}
	var compat *ToolCompatibilityError
	if !errors.As(err, &compat) || compat.StatusCode() != 413 || compat.Reason != ReasonAttemptBudget {
		t.Fatalf("want 413 attempt_budget, got %v", err)
	}
	if attempts, retained := limiter.Usage(); attempts != 0 || retained != 0 {
		t.Fatalf("rejected admission leaked usage: %d attempts, %d bytes", attempts, retained)
	}
}

func TestPrepareNativeSearchDiscoveredPolicies(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tool   string
		policy RoutePolicy
	}{
		{"strip", `{"type":"custom","name":"patch"}`, RoutePolicy{ClientSearch: ClientSearchNative, CustomTools: CustomToolsStrip}},
		{"inline", `{"type":"function","name":"read","parameters":{"$defs":{"path":{"type":"string"}},"type":"object","properties":{"path":{"$ref":"#/$defs/path"}}}}`,
			RoutePolicy{ClientSearch: ClientSearchNative, Schema: SchemaPolicy{LocalRefs: LocalRefsInline}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"tools":[{"type":"tool_search","execution":"client"}],"input":[{"type":"tool_search_call","id":"tsc_1","call_id":"s1","arguments":{}},{"type":"tool_search_output","id":"tso_1","call_id":"s1","tools":[` + tc.tool + `]}]}`)
			p, err := Prepare(body, tc.policy, DefaultLimits(), NewLimiter(DefaultLimits()))
			if err != nil {
				t.Fatal(err)
			}
			if p.Attempt != nil {
				defer p.Attempt.Close()
			}
			var root map[string]any
			if err := json.Unmarshal(p.Body, &root); err != nil {
				t.Fatal(err)
			}
			tools := root["input"].([]any)[1].(map[string]any)["tools"].([]any)
			if tc.name == "strip" {
				if len(tools) != 0 {
					t.Fatalf("strip policy left a discovered custom declaration: %s", p.Body)
				}
			} else {
				path := tools[0].(map[string]any)["parameters"].(map[string]any)["properties"].(map[string]any)["path"].(map[string]any)
				if path["$ref"] != nil || path["type"] != "string" {
					t.Fatalf("inline policy skipped discovered schema: %s", p.Body)
				}
			}
		})
	}
}

func TestLimiterInitialAttemptBoundaryAndHotUpdate(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxAttemptBytes = 64
	limiter := NewLimiter(limits)
	lease, err := limiter.Acquire(64)
	if err != nil {
		t.Fatalf("exact ceiling must be admitted: %v", err)
	}
	defer lease.Close()
	limits.MaxAttemptBytes = 32
	limiter.UpdateLimits(limits)
	refused, err := limiter.Acquire(33)
	if refused != nil {
		refused.Close()
	}
	if err == nil {
		t.Fatal("new admission bypassed lowered attempt limit")
	}
	if count, retained := limiter.Usage(); count != 1 || retained != 64 {
		t.Fatalf("hot update/refusal changed active usage: %d, %d", count, retained)
	}
}

func TestResponsesToolBoundaryControls(t *testing.T) {
	t.Run("native_discovery_function", func(t *testing.T) {
		p := prepareNativeMetaBoundary(t, `{"tools":[{"type":"tool_search","execution":"client"}],"input":[{"type":"tool_search_call","id":"tsc_1","call_id":"s1","arguments":{}},{"type":"tool_search_output","id":"tso_1","call_id":"s1","tools":[{"type":"function","name":"read","parameters":{"type":"object"}}]}]}`)
		if p.Attempt != nil {
			t.Fatal("healthy native function discovery should pass through")
		}
	})
	t.Run("valid_search_response", func(t *testing.T) {
		p, err := Prepare([]byte(`{"tools":[{"type":"tool_search","execution":"client"}],"input":[]}`), RoutePolicy{ClientSearch: ClientSearchBridge}, DefaultLimits(), NewLimiter(DefaultLimits()))
		if err != nil {
			t.Fatal(err)
		}
		defer p.Attempt.Close()
		if _, err := p.Attempt.RewriteResponse([]byte(`{"output":[{"type":"function_call","id":"fc_1","name":"tool_search","call_id":"s1","arguments":"{\"query\":\"x\"}"}]}`)); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("stream_rejects_invalid_json_search", func(t *testing.T) {
		p, err := Prepare([]byte(`{"tools":[{"type":"tool_search","execution":"client"}],"input":[]}`), RoutePolicy{ClientSearch: ClientSearchBridge}, DefaultLimits(), NewLimiter(DefaultLimits()))
		if err != nil {
			t.Fatal(err)
		}
		defer p.Attempt.Close()
		if _, err := p.Attempt.Feed([]byte(`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","name":"tool_search","call_id":"s1","arguments":""}}`)); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Attempt.Feed([]byte(`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","name":"tool_search","call_id":"s1","arguments":"{"}}`)); err == nil {
			t.Fatal("stream must reject invalid JSON")
		}
	})
}
