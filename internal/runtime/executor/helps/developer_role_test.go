package helps

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestNormalizeDeveloperRole(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  func(t *testing.T, got []byte)
	}{
		{
			name:  "no messages field",
			input: `{"model":"gpt-4"}`,
			want: func(t *testing.T, got []byte) {
				if string(got) != `{"model":"gpt-4"}` {
					t.Fatalf("got %s", got)
				}
			},
		},
		{
			name:  "empty messages array",
			input: `{"model":"gpt-4","messages":[]}`,
			want: func(t *testing.T, got []byte) {
				if string(got) != `{"model":"gpt-4","messages":[]}` {
					t.Fatalf("got %s", got)
				}
			},
		},
		{
			name:  "no developer role",
			input: `{"messages":[{"role":"system","content":"hello"},{"role":"user","content":"hi"}]}`,
			want: func(t *testing.T, got []byte) {
				if !strings.Contains(string(got), `"role":"system"`) {
					t.Fatal("system role missing")
				}
				if !strings.Contains(string(got), `"role":"user"`) {
					t.Fatal("user role missing")
				}
				if strings.Contains(string(got), `"developer"`) {
					t.Fatal("unexpected developer role")
				}
			},
		},
		{
			name:  "single developer converted to system",
			input: `{"messages":[{"role":"developer","content":"You are a helpful assistant."}]}`,
			want: func(t *testing.T, got []byte) {
				if strings.Contains(string(got), `"developer"`) {
					t.Fatal("developer role was not converted")
				}
				if !strings.Contains(string(got), `"role":"system"`) {
					t.Fatal("expected system role not found")
				}
				if !strings.Contains(string(got), `"content":"You are a helpful assistant."`) {
					t.Fatal("content was modified")
				}
				// Verify JSON is valid.
				var v interface{}
				if err := json.Unmarshal(got, &v); err != nil {
					t.Fatalf("invalid JSON after normalization: %v\noutput: %s", err, got)
				}
			},
		},
		{
			name:  "multiple developers all converted",
			input: `{"messages":[{"role":"developer","content":"first"},{"role":"developer","content":"second"},{"role":"developer","content":"third"}]}`,
			want: func(t *testing.T, got []byte) {
				if strings.Contains(string(got), `"developer"`) {
					t.Fatal("developer role was not fully converted")
				}
				if strings.Count(string(got), `"role":"system"`) != 3 {
					t.Fatalf("expected 3 system roles, got %d\ngot: %s", strings.Count(string(got), `"role":"system"`), got)
				}
				var v interface{}
				if err := json.Unmarshal(got, &v); err != nil {
					t.Fatalf("invalid JSON: %v", err)
				}
			},
		},
		{
			name:  "mixed roles only developer changes",
			input: `{"messages":[{"role":"developer","content":"sys msg"},{"role":"user","content":"hello"},{"role":"assistant","content":"hi there"},{"role":"developer","content":"another"},{"role":"tool","content":"result","tool_call_id":"x"}]}`,
			want: func(t *testing.T, got []byte) {
				// Developer roles -> system.
				if strings.Count(string(got), `"role":"system"`) != 2 {
					t.Fatalf("expected 2 system roles, got output: %s", got)
				}
				// User, assistant, tool unchanged.
				if !strings.Contains(string(got), `"role":"user"`) {
					t.Fatal("user role missing")
				}
				if !strings.Contains(string(got), `"role":"assistant"`) {
					t.Fatal("assistant role missing")
				}
				var v interface{}
				if err := json.Unmarshal(got, &v); err != nil {
					t.Fatalf("invalid JSON: %v", err)
				}
			},
		},
		{
			name:  "developer in content string not affected",
			input: `{"messages":[{"role":"developer","content":"the developer role is special"},{"role":"user","content":"I am a developer"}]}`,
			want: func(t *testing.T, got []byte) {
				// Only the role field should change, not content.
				if !strings.Contains(string(got), `"content":"the developer role is special"`) {
					t.Fatal("content 'the developer role is special' was modified")
				}
				if !strings.Contains(string(got), `"content":"I am a developer"`) {
					t.Fatal("content 'I am a developer' was modified")
				}
				if !strings.Contains(string(got), `"role":"system"`) {
					t.Fatal("system role not found")
				}
				var v interface{}
				if err := json.Unmarshal(got, &v); err != nil {
					t.Fatalf("invalid JSON: %v", err)
				}
			},
		},
		{
			name:  "existing system role unchanged",
			input: `{"messages":[{"role":"system","content":"you are helpful"}]}`,
			want: func(t *testing.T, got []byte) {
				if !strings.Contains(string(got), `"role":"system"`) {
					t.Fatal("system role missing")
				}
				if strings.Contains(string(got), `"developer"`) {
					t.Fatal("unexpected developer")
				}
			},
		},
		{
			name:  "alternating developer and system roles",
			input: `{"messages":[{"role":"developer","content":"a"},{"role":"system","content":"b"},{"role":"developer","content":"c"},{"role":"user","content":"d"}]}`,
			want: func(t *testing.T, got []byte) {
				nSystem := strings.Count(string(got), `"role":"system"`)
				if nSystem != 3 {
					t.Fatalf("expected 3 system roles, got %d\noutput: %s", nSystem, got)
				}
				if strings.Count(string(got), `"developer"`) != 0 {
					t.Fatal("developer not fully removed")
				}
				var v interface{}
				if err := json.Unmarshal(got, &v); err != nil {
					t.Fatalf("invalid JSON: %v", err)
				}
			},
		},
		{
			name:  "payload with top-level fields preserved",
			input: `{"model":"deepseek-v3.1","stream":true,"max_tokens":1024,"temperature":0.7,"messages":[{"role":"developer","content":"system prompt"}]}`,
			want: func(t *testing.T, got []byte) {
				for _, field := range []string{`"model":"deepseek-v3.1"`, `"stream":true`, `"max_tokens":1024`} {
					if !strings.Contains(string(got), field) {
						t.Fatalf("top-level field %q was lost", field)
					}
				}
				if !strings.Contains(string(got), `"role":"system"`) {
					t.Fatal("system role not found")
				}
				var v interface{}
				if err := json.Unmarshal(got, &v); err != nil {
					t.Fatalf("invalid JSON: %v", err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeDeveloperRole([]byte(tt.input))
			tt.want(t, got)
		})
	}
}

// fuzzNormalize never returns a payload containing "developer" as a role value.
func FuzzNormalizeDeveloperRole(f *testing.F) {
	seeds := []string{
		`{"messages":[{"role":"developer","content":"hi"}]}`,
		`{"messages":[]}`,
		`{}`,
		`{"messages":[{"role":"system","content":"x"}]}`,
		`{"messages":[{"role":"developer","content":"a"},{"role":"developer","content":"b"}]}`,
		`{"messages":[{"role":"user","content":"developer"}]}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, payload []byte) {
		// Skip invalid JSON — NormalizeDeveloperRole should handle gracefully.
		out := NormalizeDeveloperRole(payload)
		if out == nil {
			t.Skip("nil output for invalid input is acceptable")
		}

		// Reject any output that still has "developer" as a message role.
		messagesResult := gjson.GetBytes(out, "messages")
		if messagesResult.IsArray() {
			for _, msg := range messagesResult.Array() {
				role := msg.Get("role")
				if role.Exists() && role.String() == "developer" {
					t.Errorf("developer role survived normalization\ninput: %s\noutput: %s", payload, out)
				}
			}
		}
	})
}

func BenchmarkNormalizeDeveloperRole_NoChange(b *testing.B) {
	payload := []byte(`{"model":"deepseek-v3.1","messages":[{"role":"system","content":"hello"},{"role":"user","content":"world"}]}`)
	b.ResetTimer()
	for b.Loop() {
		_ = NormalizeDeveloperRole(payload)
	}
}

func BenchmarkNormalizeDeveloperRole_OneDeveloper(b *testing.B) {
	payload := []byte(`{"model":"deepseek-v3.1","messages":[{"role":"developer","content":"You are a helpful assistant."},{"role":"user","content":"Hello!"}]}`)
	b.ResetTimer()
	for b.Loop() {
		_ = NormalizeDeveloperRole(payload)
	}
}

func BenchmarkNormalizeDeveloperRole_LargePayload(b *testing.B) {
	// Simulate a realistic large chat completions request (~50KB).
	var msgs []map[string]string
	for i := 0; i < 100; i++ {
		role := "user"
		if i == 0 {
			role = "developer"
		}
		msgs = append(msgs, map[string]string{
			"role":    role,
			"content": strings.Repeat("lorem ipsum dolor sit amet ", 20),
		})
	}
	messages, _ := json.Marshal(msgs)
	payload := []byte(`{"model":"deepseek-v3.1","messages":` + string(messages) + `}`)
	b.ResetTimer()
	for b.Loop() {
		_ = NormalizeDeveloperRole(payload)
	}
}
