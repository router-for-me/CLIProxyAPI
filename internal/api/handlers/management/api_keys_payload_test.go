package management

import (
	"reflect"
	"testing"
)

func TestParseAPIKeysPayload(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		body         string
		wantKeys     []string
		wantPolicies int // count only — full equality covered below for one case
		wantOK       bool
	}{
		{
			name:     "plain string array",
			body:     `["sk-aaa","sk-bbb"]`,
			wantKeys: []string{"sk-aaa", "sk-bbb"},
			wantOK:   true,
		},
		{
			name:     "plain string array dedupes and trims",
			body:     `["sk-aaa","  sk-aaa  ","sk-bbb"]`,
			wantKeys: []string{"sk-aaa", "sk-bbb"},
			wantOK:   true,
		},
		{
			name:     "wrapped items array",
			body:     `{"items":["sk-aaa","sk-bbb"]}`,
			wantKeys: []string{"sk-aaa", "sk-bbb"},
			wantOK:   true,
		},
		{
			name:         "structured array camelCase",
			body:         `[{"key":"sk-narrow","allowedModels":["gpt-4o*"]},{"key":"sk-open"}]`,
			wantKeys:     []string{"sk-narrow", "sk-open"},
			wantPolicies: 1,
			wantOK:       true,
		},
		{
			name:         "structured array kebab-case",
			body:         `[{"key":"sk-narrow","allowed-models":["gpt-4o*","claude-3-*"]}]`,
			wantKeys:     []string{"sk-narrow"},
			wantPolicies: 1,
			wantOK:       true,
		},
		{
			name:         "structured array snake_case",
			body:         `[{"key":"sk-narrow","allowed_models":["gpt-4o*"]}]`,
			wantKeys:     []string{"sk-narrow"},
			wantPolicies: 1,
			wantOK:       true,
		},
		{
			name:         "structured wrapped items",
			body:         `{"items":[{"key":"sk-narrow","allowedModels":["gpt-4o*"]}]}`,
			wantKeys:     []string{"sk-narrow"},
			wantPolicies: 1,
			wantOK:       true,
		},
		{
			name:   "empty plain array clears keys",
			body:   `[]`,
			wantOK: true, // legacy plain-list path accepts empty -> clears keys
		},
		{
			name:   "garbage fails",
			body:   `not json`,
			wantOK: false,
		},
		{
			name:   "object without items fails",
			body:   `{"foo":"bar"}`,
			wantOK: false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			keys, policies, ok := parseAPIKeysPayload([]byte(tc.body))
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (keys=%v policies=%v)", ok, tc.wantOK, keys, policies)
			}
			if !ok {
				return
			}
			if tc.wantKeys != nil && !reflect.DeepEqual(keys, tc.wantKeys) {
				t.Fatalf("keys = %v, want %v", keys, tc.wantKeys)
			}
			if tc.wantPolicies != len(policies) {
				t.Fatalf("policies count = %d, want %d (got %v)", len(policies), tc.wantPolicies, policies)
			}
		})
	}
}

func TestParseAPIKeysPayload_StructuredPolicyShape(t *testing.T) {
	body := `[{"key":"sk-narrow","allowedModels":["gpt-4o*","claude-3-*"]},{"key":"sk-open"}]`
	keys, policies, ok := parseAPIKeysPayload([]byte(body))
	if !ok {
		t.Fatalf("expected ok")
	}
	if want := []string{"sk-narrow", "sk-open"}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
	if len(policies) != 1 {
		t.Fatalf("expected 1 policy, got %d", len(policies))
	}
	if policies[0].Key != "sk-narrow" {
		t.Fatalf("policy key = %q, want sk-narrow", policies[0].Key)
	}
	if want := []string{"gpt-4o*", "claude-3-*"}; !reflect.DeepEqual(policies[0].AllowedModels, want) {
		t.Fatalf("policy AllowedModels = %v, want %v", policies[0].AllowedModels, want)
	}
}

func TestParseAPIKeysPayload_StructuredDuplicateKeyRejected(t *testing.T) {
	t.Parallel()

	// Duplicate structured rows must be rejected. The first row is
	// unrestricted (no allowedModels) and the second row would carry a
	// restriction; silently dropping the second row used to widen access.
	body := []byte(`[{"key":"sk-dup"},{"key":"sk-dup","allowedModels":["gpt-4o*"]}]`)
	_, _, ok := parseAPIKeysPayload(body)
	if ok {
		t.Fatalf("expected duplicate structured rows to be rejected; got ok=true")
	}
}

func TestParseAPIKeysPayload_StructuredWrappedDuplicateKeyRejected(t *testing.T) {
	t.Parallel()

	// Round-4 contract: duplicate structured rows are rejected. The
	// existing test covers the bare array form; this test pins down that
	// the wrapped {items:[...]} form goes through the same rejection,
	// because both shapes share the same dedupe loop.
	body := []byte("{\"items\":[{\"key\":\"sk-dup\"},{\"key\":\"sk-dup\",\"allowedModels\":[\"gpt-4o*\"]}]}")
	_, _, ok := parseAPIKeysPayload(body)
	if ok {
		t.Fatalf("expected duplicate wrapped structured rows to be rejected; got ok=true")
	}
}

func TestParseAPIKeysPayload_StructuredDuplicateKeyAfterTrimRejected(t *testing.T) {
	t.Parallel()

	// Whitespace-padded duplicates should also be rejected. The parser
	// trims keys before deduping, so " sk-dup " and "sk-dup" map to the
	// same canonical key and the second occurrence is a duplicate. This
	// pins down that the trim-then-dedupe pipeline cannot be bypassed by
	// constructing a payload where one row's key has surrounding
	// whitespace.
	body := []byte("[{\"key\":\"sk-dup\"},{\"key\":\"  sk-dup  \",\"allowedModels\":[\"gpt-4o*\"]}]")
	_, _, ok := parseAPIKeysPayload(body)
	if ok {
		t.Fatalf("expected whitespace-padded duplicate to be rejected; got ok=true")
	}
}
