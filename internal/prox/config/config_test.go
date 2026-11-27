package config

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestLegacyConfigLoadsUnchanged(t *testing.T) {
	cfg, err := Decode([]byte("enabled: true\npriority: 10\nmode: active\npoll_interval: 2m\nmax_age: 4m\nrequest_timeout: 5s\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != ModeActive || cfg.PollInterval != 2*time.Minute || cfg.MaxAge != 4*time.Minute || cfg.RequestTimeout != 5*time.Second {
		t.Fatalf("legacy keys not honored: %+v", cfg)
	}
	if cfg.Strategy != StrategyResetFirst || !cfg.AffinityEnabled || cfg.RebindPolicy != RebindConservative {
		t.Fatalf("new keys must default safely: %+v", cfg)
	}
	empty, err := Decode(nil)
	if err != nil || empty.Mode != ModeShadow {
		t.Fatalf("empty config must default to shadow: %+v %v", empty, err)
	}
}

func TestValidation(t *testing.T) {
	for _, raw := range []string{
		"mode: on", "poll_interval: 30s", "poll_interval: 2h", "max_age: 1m\npoll_interval: 5m",
		"request_timeout: 0s", "strategy: random", "affinity_ttl: 1m", "affinity_scope: global",
		"rebind_policy: always", "pressure_min_hours: 0", "balanced_five_hour_soft_limit: 20",
		"affinity_max_sessions: 5", "activity_retention: 1", "history_retention: 10m", "state_dir: ''",
		"account_priority: {a: 5000}", "account_alias: {a: \"<script>\"}", "mode: [", "poll_interval: soon",
	} {
		if _, err := Decode([]byte(raw)); err == nil {
			t.Fatalf("accepted %q", raw)
		} else {
			var fe Errors
			if !errors.As(err, &fe) || len(fe) == 0 {
				t.Fatalf("%q: errors must be field errors, got %v", raw, err)
			}
		}
	}
	cfg, err := Decode([]byte("account_priority: {claude-a.json: 5}\naccount_alias: {claude-a.json: Work Max}\nstrategy: manual_priority\nmanual_priority_base: quota_pressure\n"))
	if err != nil || cfg.AccountPriority["claude-a.json"] != 5 || cfg.AccountAlias["claude-a.json"] != "Work Max" {
		t.Fatalf("maps not decoded: %+v %v", cfg, err)
	}
}

func TestJSONPatchAndGeneration(t *testing.T) {
	base := Default()
	next, err := DecodeJSONPatch(base, []byte(`{"strategy":"balanced","affinity_ttl":"30m","affinity_inherit_parent":false,"account_priority":{"x.json":3}}`))
	if err != nil {
		t.Fatal(err)
	}
	if next.Strategy != StrategyBalanced || next.AffinityTTL != 30*time.Minute || next.AffinityInheritParent || next.AccountPriority["x.json"] != 3 {
		t.Fatalf("patch not applied: %+v", next)
	}
	if next.Generation() == base.Generation() {
		t.Fatal("generation must change with settings")
	}
	if _, err := DecodeJSONPatch(base, []byte(`{"affinity_ttl":"1s"}`)); err == nil || !strings.Contains(err.Error(), "affinity_ttl") {
		t.Fatalf("invalid patch accepted: %v", err)
	}
	if _, err := DecodeJSONPatch(base, []byte(`[]`)); err == nil {
		t.Fatal("non-object patch accepted")
	}
	reset, err := DecodeJSONPatch(next, []byte(`{"strategy":null}`))
	if err != nil || reset.Strategy != StrategyResetFirst {
		t.Fatalf("null must restore the default: %+v %v", reset, err)
	}
}

func TestSchemaCoversEveryScalarSetting(t *testing.T) {
	names := map[string]bool{}
	for _, f := range Schema() {
		names[f.Name] = true
		if f.Description == "" || f.Effect == "" {
			t.Fatalf("%s lacks description or effect", f.Name)
		}
	}
	for key, value := range Default().Public() {
		if _, isMap := value.(map[string]any); isMap {
			continue
		}
		if !names[key] {
			t.Fatalf("setting %s missing from schema", key)
		}
	}
}

func TestPassiveSkipSettings(t *testing.T) {
	for _, raw := range []string{"passive_skip: -1s", "passive_skip: 2h", "passive_skip_max: 0s", "passive_skip_max: 25h"} {
		if _, err := Decode([]byte(raw)); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	cfg, err := Decode([]byte("passive_skip: 0s\npassive_skip_max: 1h"))
	if err != nil {
		t.Fatal(err)
	}
	patched, err := DecodeJSONPatch(cfg, []byte(`{"passive_skip":"20s"}`))
	if err != nil || patched.Public()["passive_skip"] != "20s" || patched.Public()["passive_skip_max"] != "1h0m0s" {
		t.Fatalf("patch: %+v %v", patched, err)
	}
}
