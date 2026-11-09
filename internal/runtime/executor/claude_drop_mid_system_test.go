package executor

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const dropMidSystemPayload = `{"system":"Top rule","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]},{"role":"system","content":"Mid string rule"},{"role":"assistant","content":[{"type":"text","text":"ok"}]},{"role":"System","content":[{"type":"text","text":"Mid array rule"}]},{"role":"user","content":[{"type":"text","text":"continue"}]}]}`

func TestDropMidSystemMessagesRemovesStringAndArrayContent(t *testing.T) {
	out := dropMidSystemMessages([]byte(dropMidSystemPayload))
	if ojson.GetBytes(out, `messages.#(role%"*ystem")`).Exists() {
		t.Fatalf("messages still hold a system role: %s", ojson.GetBytes(out, "messages").Raw)
	}
	if got := ojson.GetBytes(out, "messages.#").Int(); got != 3 {
		t.Fatalf("messages count = %d, want 3", got)
	}
	roles := []string{"user", "assistant", "user"}
	for i, want := range roles {
		if got := ojson.GetBytes(out, "messages."+strconv.Itoa(i)+".role").String(); got != want {
			t.Fatalf("messages[%d].role = %q, want %q", i, got, want)
		}
	}
	if got := ojson.GetBytes(out, "system").String(); got != "Top rule" {
		t.Fatalf("system = %q, want the top-level system untouched", got)
	}
}

func TestDropMidSystemMessagesLeavesCleanPayloadUnchanged(t *testing.T) {
	in := []byte(`{"system":"Top","messages":[{"role":"user","content":"hi"}]}`)
	if out := dropMidSystemMessages(in); string(out) != string(in) {
		t.Fatalf("payload changed: %s", out)
	}
	noMessages := []byte(`{"system":"Top"}`)
	if out := dropMidSystemMessages(noMessages); string(out) != string(noMessages) {
		t.Fatalf("payload without messages changed: %s", out)
	}
}

func TestApplyMidSystemMessagePolicy(t *testing.T) {
	payload := []byte(dropMidSystemPayload)
	auth := func(attrs map[string]string) *cliproxyauth.Auth { return &cliproxyauth.Auth{Attributes: attrs} }

	tests := []struct {
		name        string
		cfg         *config.Config
		auth        *cliproxyauth.Auth
		wantSystem  int // top-level system blocks; -1 = left as the string it was
		wantMessage int
	}{
		{name: "neither set", cfg: &config.Config{}, auth: auth(nil), wantSystem: -1, wantMessage: 5},
		{name: "rebuild attribute", cfg: &config.Config{}, auth: auth(map[string]string{"rebuild_mid_system_message": "true"}), wantSystem: 3, wantMessage: 3},
		{name: "drop attribute", cfg: &config.Config{}, auth: auth(map[string]string{"drop_mid_system_message": "true"}), wantSystem: -1, wantMessage: 3},
		{name: "drop wins over rebuild", cfg: &config.Config{}, auth: auth(map[string]string{"rebuild_mid_system_message": "true", "drop_mid_system_message": "TRUE"}), wantSystem: -1, wantMessage: 3},
		{
			name: "drop from key config",
			cfg: &config.Config{ClaudeKey: []config.ClaudeKey{{
				APIKey: "key-123", BaseURL: "https://upstream.example", RebuildMidSystemMessage: true, DropMidSystemMessage: true,
			}}},
			auth:        auth(map[string]string{"api_key": "key-123", "base_url": "https://upstream.example"}),
			wantSystem:  -1,
			wantMessage: 3,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := applyMidSystemMessagePolicy(tt.cfg, tt.auth, payload)
			if got := int(ojson.GetBytes(out, "messages.#").Int()); got != tt.wantMessage {
				t.Fatalf("messages count = %d, want %d", got, tt.wantMessage)
			}
			system := ojson.GetBytes(out, "system")
			if tt.wantSystem < 0 {
				if system.String() != "Top rule" {
					t.Fatalf("system = %s, want the original string", system.Raw)
				}
				return
			}
			if got := len(system.Array()); got != tt.wantSystem {
				t.Fatalf("system has %d blocks, want %d: %s", got, tt.wantSystem, system.Raw)
			}
		})
	}
}

// A drop-mid-system-message key must not get role=system turns back from
// cloaking: caller system prompts take the <system-reminder> path instead.
func TestCloakingHonorsDropMidSystemMessage(t *testing.T) {
	helps.ResetClaudeDiagnosticsForTest()
	t.Cleanup(helps.ResetClaudeDiagnosticsForTest)

	payload := []byte(`{"model":"claude-opus-5","system":"Caller rule","messages":[` +
		`{"role":"user","content":[{"type":"text","text":"hi"}]},` +
		`{"role":"assistant","content":[{"type":"text","text":"ok"}]},` +
		`{"role":"user","content":[{"type":"text","text":"continue"}]}]}`)

	for _, drop := range []bool{false, true} {
		attrs := map[string]string{"cloak_mode": "always"}
		if drop {
			attrs["drop_mid_system_message"] = "true"
		}
		auth := &cliproxyauth.Auth{ID: "drop-mid-system-cloak", Attributes: attrs}
		ctx := helps.WithClaudeSessionID(context.Background(), "drop-mid-system-cloak-session")
		cfg := &config.Config{}

		body := applyMidSystemMessagePolicy(cfg, auth, payload)
		out, didCloak, errCloaking := applyCloaking(ctx, cfg, auth, body, "sk-ant-oat-test", false, false)
		if errCloaking != nil {
			t.Fatalf("drop=%v: applyCloaking() error = %v", drop, errCloaking)
		}
		if !didCloak {
			t.Fatalf("drop=%v: applyCloaking() cloaked = false, want true", drop)
		}
		hasMidSystem := claudePayloadHasMidSystemMessage(out)
		if drop && hasMidSystem {
			t.Fatalf("drop key still sends a role=system turn: %s", ojson.GetBytes(out, "messages").Raw)
		}
		if !drop && !hasMidSystem {
			t.Fatalf("without drop, cloaking should keep its role=system placement: %s", ojson.GetBytes(out, "messages").Raw)
		}
		if drop && !strings.Contains(ojson.GetBytes(out, "messages.0").Raw, "Caller rule") {
			t.Fatalf("caller system prompt lost for drop key: %s", ojson.GetBytes(out, "messages").Raw)
		}

		counted := relocateClaudeSystemPromptForCountTokensWithPolicy(body, false, false, false, drop)
		if drop && claudePayloadHasMidSystemMessage(counted) {
			t.Fatalf("count_tokens for drop key still has a role=system turn: %s", ojson.GetBytes(counted, "messages").Raw)
		}
		if drop && !strings.Contains(ojson.GetBytes(counted, "messages.0").Raw, "Caller rule") {
			t.Fatalf("count_tokens dropped the caller system prompt: %s", ojson.GetBytes(counted, "messages").Raw)
		}
	}
}

// Two config entries may share a key and base URL and differ only by prefix.
// A config-synthesized auth for the drop-disabled one must not inherit the
// option from the other entry through key/base matching.
func TestDropMidSystemMessageConfigAuthIsAuthoritative(t *testing.T) {
	cfg := &config.Config{ClaudeKey: []config.ClaudeKey{
		{APIKey: "shared-key", BaseURL: "https://upstream.example", Prefix: "a", DropMidSystemMessage: true},
		{APIKey: "shared-key", BaseURL: "https://upstream.example", Prefix: "b"},
	}}
	enabled := &cliproxyauth.Auth{Attributes: map[string]string{
		"source": "config:claude[a]", "config_index": "0", "api_key": "shared-key", "base_url": "https://upstream.example",
		"drop_mid_system_message": "true",
	}}
	disabled := &cliproxyauth.Auth{Attributes: map[string]string{
		"source": "config:claude[b]", "config_index": "1", "api_key": "shared-key", "base_url": "https://upstream.example",
	}}
	if !dropMidSystemMessageEnabled(cfg, enabled) {
		t.Fatal("entry with the option set should drop")
	}
	if dropMidSystemMessageEnabled(cfg, disabled) {
		t.Fatal("drop-disabled entry inherited the option from another entry with the same key and base URL")
	}
}

// With no user turn there is nowhere to put the <system-reminder>, so the
// caller's system prompt must stay in the top-level system field.
func TestCloakingDropKeepsCallerSystemWithoutUserTurn(t *testing.T) {
	helps.ResetClaudeDiagnosticsForTest()
	t.Cleanup(helps.ResetClaudeDiagnosticsForTest)

	payload := []byte(`{"model":"claude-opus-5","system":"Caller rule","messages":[` +
		`{"role":"assistant","content":[{"type":"text","text":"ok"}]}]}`)
	auth := &cliproxyauth.Auth{ID: "drop-no-user", Attributes: map[string]string{"cloak_mode": "always", "drop_mid_system_message": "true"}}
	ctx := helps.WithClaudeSessionID(context.Background(), "drop-no-user-session")
	cfg := &config.Config{}

	body := applyMidSystemMessagePolicy(cfg, auth, payload)
	out, didCloak, errCloaking := applyCloaking(ctx, cfg, auth, body, "sk-ant-oat-test", false, false)
	if errCloaking != nil || !didCloak {
		t.Fatalf("applyCloaking() cloaked=%v err=%v", didCloak, errCloaking)
	}
	if !strings.Contains(ojson.GetBytes(out, "system").Raw, "Caller rule") {
		t.Fatalf("caller system prompt lost: system=%s messages=%s", ojson.GetBytes(out, "system").Raw, ojson.GetBytes(out, "messages").Raw)
	}
	if claudePayloadHasMidSystemMessage(out) {
		t.Fatalf("drop key got a role=system turn: %s", ojson.GetBytes(out, "messages").Raw)
	}

	counted := relocateClaudeSystemPromptForCountTokensWithPolicy(body, false, false, false, true)
	if !strings.Contains(ojson.GetBytes(counted, "system").Raw, "Caller rule") {
		t.Fatalf("count_tokens lost the caller system prompt: %s", counted)
	}
}

// Dropping every turn would leave an empty messages array; keep the payload.
func TestDropMidSystemMessagesKeepsAllSystemPayload(t *testing.T) {
	in := []byte(`{"system":"Top","messages":[{"role":"system","content":"a"},{"role":"system","content":"b"}]}`)
	if out := dropMidSystemMessages(in); string(out) != string(in) {
		t.Fatalf("payload changed: %s", out)
	}
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"drop_mid_system_message": "true"}}
	if out := applyMidSystemMessagePolicy(&config.Config{}, auth, in); ojson.GetBytes(out, "messages.#").Int() != 2 {
		t.Fatalf("policy emptied messages: %s", out)
	}
}

// Rebuild follows the same rule as drop for config-synthesized auths, and key
// config lookup picks the entry whose prefix matches the auth.
func TestMidSystemMessageConfigLookupHonorsPrefix(t *testing.T) {
	cfg := &config.Config{ClaudeKey: []config.ClaudeKey{
		{APIKey: "shared-key", BaseURL: "https://upstream.example", Prefix: "a", RebuildMidSystemMessage: true, DropMidSystemMessage: true},
		{APIKey: "shared-key", BaseURL: "https://upstream.example", Prefix: "b"},
	}}
	configAuth := &cliproxyauth.Auth{Prefix: "b", Attributes: map[string]string{
		"source": "config:claude[b]", "api_key": "shared-key", "base_url": "https://upstream.example",
	}}
	if rebuildMidSystemMessageEnabled(cfg, configAuth) {
		t.Fatal("rebuild-disabled config entry inherited the option from another entry")
	}
	fileAuth := &cliproxyauth.Auth{Prefix: "b", Attributes: map[string]string{
		"api_key": "shared-key", "base_url": "https://upstream.example",
	}}
	if entry := resolveClaudeKeyConfig(cfg, fileAuth); entry == nil || entry.Prefix != "b" {
		t.Fatalf("resolveClaudeKeyConfig picked %+v, want prefix b", entry)
	}
	if rebuildMidSystemMessageEnabled(cfg, fileAuth) || dropMidSystemMessageEnabled(cfg, fileAuth) {
		t.Fatal("prefix b auth inherited options from prefix a")
	}
	if entry := resolveClaudeKeyConfig(cfg, &cliproxyauth.Auth{Prefix: "c", Attributes: map[string]string{"api_key": "shared-key"}}); entry != nil {
		t.Fatalf("unknown prefix matched %+v", entry)
	}
}

// When the first user turn already carries the caller's reminder (a replayed
// request), the prompt must not be duplicated into top-level system.
func TestCloakingDropDoesNotDuplicateExistingReminder(t *testing.T) {
	helps.ResetClaudeDiagnosticsForTest()
	t.Cleanup(helps.ResetClaudeDiagnosticsForTest)

	reminder := claudeCallerSystemReminder("Caller rule")
	payload := []byte(`{"model":"claude-opus-5","system":"Caller rule","messages":[` +
		`{"role":"user","content":[{"type":"text","text":` + marshalJSONStringWithoutHTMLEscape(reminder) + `},{"type":"text","text":"hi"}]}]}`)
	auth := &cliproxyauth.Auth{ID: "drop-existing-reminder", Attributes: map[string]string{"cloak_mode": "always", "drop_mid_system_message": "true"}}
	ctx := helps.WithClaudeSessionID(context.Background(), "drop-existing-reminder-session")
	cfg := &config.Config{}

	body := applyMidSystemMessagePolicy(cfg, auth, payload)
	out, didCloak, errCloaking := applyCloaking(ctx, cfg, auth, body, "sk-ant-oat-test", false, false)
	if errCloaking != nil || !didCloak {
		t.Fatalf("applyCloaking() cloaked=%v err=%v", didCloak, errCloaking)
	}
	if strings.Contains(ojson.GetBytes(out, "system").Raw, "Caller rule") {
		t.Fatalf("caller prompt duplicated into top-level system: %s", ojson.GetBytes(out, "system").Raw)
	}
	if got := strings.Count(string(out), "Caller rule"); got != 1 {
		t.Fatalf("caller prompt appears %d times, want 1: %s", got, out)
	}

	counted := relocateClaudeSystemPromptForCountTokensWithPolicy(body, false, false, false, true)
	if got := strings.Count(string(counted), "Caller rule"); got != 1 {
		t.Fatalf("count_tokens has the caller prompt %d times, want 1: %s", got, counted)
	}
}
