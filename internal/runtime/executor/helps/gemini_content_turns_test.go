package helps

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

var leadingGeminiUserContentOutput []byte

func TestEnsureGeminiLeadingUserContentReusesLargeValidPayload(t *testing.T) {
	input := []byte(`{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"video/mp4","data":"` + strings.Repeat("A", 4<<20) + `"}}]}]}`)

	output := EnsureGeminiLeadingUserContent(input, "contents")
	if &output[0] != &input[0] {
		t.Fatal("valid request should reuse the input payload")
	}

	result := testing.Benchmark(func(b *testing.B) {
		for b.Loop() {
			leadingGeminiUserContentOutput = EnsureGeminiLeadingUserContent(input, "contents")
		}
	})
	if allocated := result.AllocedBytesPerOp(); allocated >= 1<<20 {
		t.Fatalf("valid 4 MiB request allocated %d bytes/op, want less than 1 MiB", allocated)
	}
}

func TestEnsureGeminiLeadingUserContent(t *testing.T) {
	tests := []struct {
		name             string
		inputJSON        string
		path             string
		wantRoles        string
		wantLeadingEmpty bool
	}{
		{
			name:      "user first is unchanged",
			inputJSON: `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`,
			path:      "contents",
			wantRoles: "user",
		},
		{
			name:             "leading model functionCall gets empty user",
			inputJSON:        `{"contents":[{"role":"model","parts":[{"functionCall":{"name":"run"}}]},{"role":"user","parts":[{"functionResponse":{"name":"run"}}]}]}`,
			path:             "contents",
			wantRoles:        "user,model,user",
			wantLeadingEmpty: true,
		},
		{
			name:             "leading model text gets empty user and preserves following turns",
			inputJSON:        `{"contents":[{"role":"model","parts":[{"text":"answer"}]},{"role":"user","parts":[{"text":"continue"}]}]}`,
			path:             "contents",
			wantRoles:        "user,model,user",
			wantLeadingEmpty: true,
		},
		{
			name:             "nested contents are normalized",
			inputJSON:        `{"request":{"contents":[{"role":"model","parts":[{"text":"answer"}]},{"role":"user","parts":[{"text":"continue"}]}]}}`,
			path:             "request.contents",
			wantRoles:        "request.user,model,user",
			wantLeadingEmpty: true,
		},
		{
			name:      "empty contents are unchanged",
			inputJSON: `{"contents":[]}`,
			path:      "contents",
			wantRoles: "",
		},
		{
			name:      "missing contents are unchanged",
			inputJSON: `{"model":"test"}`,
			path:      "contents",
			wantRoles: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := EnsureGeminiLeadingUserContent([]byte(tt.inputJSON), tt.path)
			contents := gjson.GetBytes(out, tt.path).Array()
			roles := make([]string, 0, len(contents))
			for _, content := range contents {
				roles = append(roles, content.Get("role").String())
			}
			expectedRoles := strings.TrimPrefix(tt.wantRoles, "request.")
			if got := strings.Join(roles, ","); got != expectedRoles {
				t.Fatalf("roles = %q, want %q; output=%s", got, expectedRoles, out)
			}
			if tt.wantLeadingEmpty {
				text := gjson.GetBytes(out, tt.path+".0.parts.0.text")
				if !text.Exists() || text.String() != "" {
					t.Fatalf("leading empty user part missing; output=%s", out)
				}
			}
		})
	}
}
