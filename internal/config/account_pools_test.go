package config

import (
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestClientAccountPoolsConfigRoundTrip(t *testing.T) {
	raw := []byte(`config-version: 8
client:
  account-pools:
    - name: reviewer
      auth-files: [reviewer.json]
      auth-kind: oauth
  key-scopes:
    - key-prefix: review-
      providers: [claude]
      pools: [reviewer]
    - key-prefix: deny-
      providers: [claude]
      pools: []
    - key-prefix: legacy-
      providers: [claude]
`)
	if err := ValidateV8Config(raw); err != nil {
		t.Fatalf("v8 validation rejected account pools: %v", err)
	}
	cfg, err := ParseConfigBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	clone := cfg.CloneForRuntime()
	encoded, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := ParseConfigBytes(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.Client, reloaded.Client) {
		t.Fatal("pool config lost on round trip")
	}
	if cfg.Client.AccountPools == nil || (*cfg.Client.KeyScopes)[1].Pools == nil || (*cfg.Client.KeyScopes)[2].Pools != nil {
		t.Fatal("pool config or empty/omitted distinction lost")
	}
	(*cfg.Client.AccountPools)[0].AuthFiles[0] = "other.json"
	(*(*cfg.Client.KeyScopes)[0].Pools)[0] = "other"
	if (*clone.Client.AccountPools)[0].AuthFiles[0] != "reviewer.json" || (*(*clone.Client.KeyScopes)[0].Pools)[0] != "reviewer" {
		t.Fatal("runtime snapshot shares pool config")
	}
}

// An entry that names no provider is skipped, unless it sets pools: then it
// still applies, so its keys fail closed instead of falling back to a shorter
// prefix or to no scope at all.
func TestKeyScopeKeepsPooledEntryWithoutProviders(t *testing.T) {
	pools := []string{"reviewer"}
	cfg := ClientConfig{KeyScopes: &[]ClientKeyScope{
		{KeyPrefix: "sk-", Providers: []string{"claude"}},
		{KeyPrefix: "sk-empty-"},
		{KeyPrefix: "sk-pooled-", Pools: &pools},
	}}
	if scope, ok := cfg.KeyScope("sk-empty-key"); !ok || scope.KeyPrefix != "sk-" {
		t.Fatalf("entry without providers or pools = %+v, %t; want the shorter prefix", scope, ok)
	}
	if scope, ok := cfg.KeyScope("sk-pooled-key"); !ok || scope.KeyPrefix != "sk-pooled-" {
		t.Fatalf("pooled entry without providers = %+v, %t; want it selected", scope, ok)
	}
}
