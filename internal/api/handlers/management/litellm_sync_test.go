package management

import (
	"testing"
)

func TestParseLiteLLMUsers(t *testing.T) {
	body := `{
		"total_count": 2,
		"total_pages": 1,
		"users": [
			{"user_id":"u1","user_alias":"alice","user_email":"a@b.c","user_role":"internal_user","max_budget":10.5,"budget_duration":"7d","rpm_limit":60,"tpm_limit":1000,"spend":3.25,"models":["gpt-4o"],"metadata":{"team":"x"}},
			{"user_id":"u2","user_alias":"bob","user_email":"b@c.d","max_parallel_requests":4}
		]
	}`
	users, totalPages, err := parseLiteLLMUsers([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if totalPages != 1 {
		t.Fatalf("totalPages = %d; want 1", totalPages)
	}
	if len(users) != 2 {
		t.Fatalf("len = %d; want 2", len(users))
	}
	u := users[0]
	if u.ID != "u1" || u.UserAlias != "alice" || u.UserRole != "internal_user" {
		t.Fatalf("user 0 wrong: %+v", u)
	}
	if u.MaxBudget == nil || *u.MaxBudget != 10.5 {
		t.Fatalf("max_budget wrong: %+v", u.MaxBudget)
	}
	if u.BudgetDuration != "7d" {
		t.Fatalf("budget_duration = %q", u.BudgetDuration)
	}
	if u.RPMLimit == nil || *u.RPMLimit != 60 {
		t.Fatalf("rpm_limit wrong: %+v", u.RPMLimit)
	}
	if u.Spend != 3.25 {
		t.Fatalf("spend = %v; want 3.25", u.Spend)
	}
	if len(u.Models) != 1 || u.Models[0] != "gpt-4o" {
		t.Fatalf("models wrong: %+v", u.Models)
	}
	// Default role when absent.
	if users[1].UserRole != "internal_user" {
		t.Fatalf("default role wrong: %q", users[1].UserRole)
	}
	if users[1].MaxParallelRequests == nil || *users[1].MaxParallelRequests != 4 {
		t.Fatalf("max_parallel_requests wrong: %+v", users[1].MaxParallelRequests)
	}
}

func TestParseLiteLLMUsersSkipsMissingID(t *testing.T) {
	body := `{"users":[{"user_alias":"no-id"},{"user_id":"ok1"}]}`
	users, _, err := parseLiteLLMUsers([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(users) != 1 || users[0].ID != "ok1" {
		t.Fatalf("expected only the row with an id: %+v", users)
	}
}

func TestParseLiteLLMKeys(t *testing.T) {
	// Realistic /key/list row shaped like LiteLLM OpenAPI UserAPIKeyAuth:
	// identity via "token" (the SHA-256 hash), optional plaintext via "api_key",
	// budget via "max_budget", "blocked" bool -> revoked status.
	body := `{
		"keys": [
			{
				"token":"abc123hash000111","key_name":"prod","key_alias":"pa",
				"user_id":"u1","spend":4.5,"max_budget":25.0,
				"expires":"2030-01-01T00:00:00Z","models":["gpt-4o"],
				"rpm_limit":30,"tpm_limit":500,"budget_duration":"1d",
				"aliases":{"gpt-4o":"primary"},"api_key":"sk-LitellmKeySecret0123456789"
			}
		]
	}`
	carriers, _, err := parseLiteLLMKeys([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(carriers) != 1 {
		t.Fatalf("len = %d; want 1", len(carriers))
	}
	c := carriers[0]
	// No key_id present; the stable ID derives from the token hash.
	if c.Key.ID != "abc123hash000111" {
		t.Fatalf("key id (from token) = %q; want abc123hash000111", c.Key.ID)
	}
	if c.Key.Name != "prod" || c.Key.UserID != "u1" || c.Key.Spend != 4.5 {
		t.Fatalf("key wrong: %+v", c.Key)
	}
	// Plaintext secret carried (so the upsert derives its own hash).
	if c.Secret != "sk-LitellmKeySecret0123456789" {
		t.Fatalf("secret = %q", c.Secret)
	}
	if c.Key.ExpiresAt == nil {
		t.Fatal("expires should be parsed")
	}
	if c.Policy == nil {
		t.Fatal("policy should be built")
	}
	if c.Policy.RPMLimit == nil || *c.Policy.RPMLimit != 30 || c.Policy.TPMLimit == nil || *c.Policy.TPMLimit != 500 {
		t.Fatalf("policy limits wrong: %+v", c.Policy)
	}
	if c.Policy.BudgetUSD == nil || *c.Policy.BudgetUSD != 25.0 || c.Policy.BudgetDuration != "1d" {
		t.Fatalf("budget wrong: %+v", c.Policy)
	}
	if c.Policy.Aliases["gpt-4o"] != "primary" {
		t.Fatalf("aliases wrong: %+v", c.Policy.Aliases)
	}
	if len(c.Policy.AllowedModels) != 1 || c.Policy.AllowedModels[0] != "gpt-4o" {
		t.Fatalf("allowed models wrong: %+v", c.Policy.AllowedModels)
	}
}

func TestParseLiteLLMKeysBlockedMapsToRevoked(t *testing.T) {
	body := `{"keys":[{"token":"abchash","key_name":"blocked-key","blocked":true}]}`
	carriers, _, err := parseLiteLLMKeys([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(carriers) != 1 {
		t.Fatalf("len = %d; want 1", len(carriers))
	}
	if carriers[0].Key.Status != "revoked" {
		t.Fatalf("blocked key status = %q; want revoked", carriers[0].Key.Status)
	}
}

func TestParseLiteLLMKeysLegacyKeyIDShape(t *testing.T) {
	// Some deploy/gateway variants still expose key_id + a hashed value in
	// "key". The parser should keep supporting that too.
	body := `{"data":[{"key_id":"k1","key_name":"h1","key":"61f1b1cbe3aa30af"}]}`
	carriers, _, err := parseLiteLLMKeys([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(carriers) != 1 {
		t.Fatalf("len = %d; want 1", len(carriers))
	}
	c := carriers[0]
	if c.Key.ID != "k1" {
		t.Fatalf("legacy key_id not honored: %q", c.Key.ID)
	}
	if c.Secret != "" {
		t.Fatalf("secret should be empty for hash-in-key: %q", c.Secret)
	}
	if c.HashOverride != "61f1b1cbe3aa30af" {
		t.Fatalf("hashOverride = %q", c.HashOverride)
	}
}

func TestParseLiteLLMKeysHashOnly(t *testing.T) {
	// A remote that returns only hashed tokens (no plaintext key).
	body := `{"data":[{"key_id":"k1","key_name":"h1","token":"abcd1234efgh"}]}`
	carriers, _, err := parseLiteLLMKeys([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(carriers) != 1 {
		t.Fatalf("len = %d; want 1", len(carriers))
	}
	c := carriers[0]
	if c.Secret != "" {
		t.Fatalf("secret should be empty for hash-only: %q", c.Secret)
	}
	if c.HashOverride != "abcd1234efgh" {
		t.Fatalf("hashOverride = %q", c.HashOverride)
	}
	if c.PrefixOverride == "" {
		t.Fatal("prefixOverride should be non-empty")
	}
}

func TestParseLiteLLMKeysSkipsUnusable(t *testing.T) {
	// A row with neither a key identity is skipped.
	body := `{"data":[{"key_name":"nothing-useful"}]}`
	carriers, _, err := parseLiteLLMKeys([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(carriers) != 0 {
		t.Fatalf("len = %d; want 0", len(carriers))
	}
}

func TestParseLiteLLMKeysUnderKeysLabel(t *testing.T) {
	// Some LiteLLM versions render the array under "keys" rather than "data".
	body := `{"keys":[{"key_id":"k1","key_name":"vk","token":"tok123"}]}`
	carriers, _, err := parseLiteLLMKeys([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(carriers) != 1 {
		t.Fatalf("len = %d; want 1", len(carriers))
	}
	if carriers[0].Key.ID != "k1" || carriers[0].HashOverride != "tok123" {
		t.Fatalf("keys-label parse wrong: %+v", carriers[0])
	}
}

func TestParseLiteLLMKeysBareArray(t *testing.T) {
	// A top-level array response.
	body := `[{"key_id":"k1","key_name":"arr","token":"tokArr"}]`
	carriers, _, err := parseLiteLLMKeys([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(carriers) != 1 {
		t.Fatalf("len = %d; want 1", len(carriers))
	}
	if carriers[0].Key.ID != "k1" {
		t.Fatalf("bare-array parse wrong: %+v", carriers[0])
	}
}

func TestParseLiteLLMKeysKeyIsHash(t *testing.T) {
	// Some responses put the hashed token directly in "key" with no separate
	// "token"; it should be treated as a hash override, not a plaintext secret.
	body := `{"data":[{"key_id":"k1","key_name":"hsh","key":"61f1b1cbe3aa30af"}]}`
	carriers, _, err := parseLiteLLMKeys([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(carriers) != 1 {
		t.Fatalf("len = %d; want 1", len(carriers))
	}
	c := carriers[0]
	if c.Secret != "" {
		t.Fatalf("secret should be empty for hash-in-key: %q", c.Secret)
	}
	if c.HashOverride != "61f1b1cbe3aa30af" {
		t.Fatalf("hashOverride = %q; want the key-as-hash", c.HashOverride)
	}
}

func TestParseLiteLLMKeysSkipsNonObjectElements(t *testing.T) {
	// Some LiteLLM versions interleave null / non-object entries in the keys
	// array. Those must not fail the whole parse or drop the valid rows.
	body := `{"keys":[null,{"key_id":"k1","key_name":"valid","token":"tokValid"},{"key_id":"k2","key_name":"also-valid","token":"tokValid2"}],"total_count":2}`
	carriers, _, err := parseLiteLLMKeys([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(carriers) != 2 {
		t.Fatalf("len = %d; want 2 (null entry skipped, object entries kept)", len(carriers))
	}
	seen := map[string]bool{}
	for _, c := range carriers {
		seen[c.Key.ID] = true
	}
	if !seen["k1"] || !seen["k2"] {
		t.Fatalf("expected both valid keys, got %v", seen)
	}
}

func TestParseLiteLLMKeysBareStringTokens(t *testing.T) {
	// Per the OpenAPI anyOf, array items may be bare token strings (the
	// SHA-256 hash), not objects. All-string arrays must yield one carrier per
	// token rather than nothing.
	body := `{"keys":["tok111","tok222","tok333"],"total_count":3}`
	carriers, _, err := parseLiteLLMKeys([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(carriers) != 3 {
		t.Fatalf("len = %d; want 3", len(carriers))
	}
	for i, c := range carriers {
		want := []string{"tok111", "tok222", "tok333"}[i]
		if c.Key.ID != want || c.HashOverride != want {
			t.Fatalf("carrier[%d] id/hash = %q/%q; want %q", i, c.Key.ID, c.HashOverride, want)
		}
		if c.Secret != "" {
			t.Fatalf("carrier[%d] should not carry a secret for a bare token", i)
		}
		if c.Key.Name == "" {
			t.Fatalf("carrier[%d] should carry a readable name (got empty)", i)
		}
	}
}

func TestParseLiteLLMKeysMixedObjectAndString(t *testing.T) {
	// A mix of object rows and bare tokens, plus a null, must all be handled.
	body := `{"keys":[null,{"token":"objTok","key_name":"obj-key"},"strTok"]}`
	carriers, _, err := parseLiteLLMKeys([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(carriers) != 2 {
		t.Fatalf("len = %d; want 2 (null skipped, object + string kept)", len(carriers))
	}
	ids := map[string]bool{}
	for _, c := range carriers {
		ids[c.Key.ID] = true
	}
	if !ids["objTok"] || !ids["strTok"] {
		t.Fatalf("expected objTok + strTok, got %v", ids)
	}
}
