package helps

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

var (
	leadingGeminiUserContentOutput  []byte
	trailingGeminiUserContentOutput []byte
)

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

func TestEnsureGeminiTrailingUserContentReusesLargeValidPayload(t *testing.T) {
	input := []byte(`{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"video/mp4","data":"` + strings.Repeat("A", 4<<20) + `"}}]}]}`)

	output := EnsureGeminiTrailingUserContent(input, "contents")
	if &output[0] != &input[0] {
		t.Fatal("valid request should reuse the input payload")
	}

	result := testing.Benchmark(func(b *testing.B) {
		for b.Loop() {
			trailingGeminiUserContentOutput = EnsureGeminiTrailingUserContent(input, "contents")
		}
	})
	if allocated := result.AllocedBytesPerOp(); allocated >= 1<<20 {
		t.Fatalf("valid 4 MiB request allocated %d bytes/op, want less than 1 MiB", allocated)
	}
}

func TestEnsureGeminiTrailingUserContent(t *testing.T) {
	tests := []struct {
		name              string
		inputJSON         string
		path              string
		wantRoles         string
		wantTrailingEmpty bool
	}{
		{
			name:      "user last is unchanged",
			inputJSON: `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`,
			path:      "contents",
			wantRoles: "user",
		},
		{
			name:      "trailing functionResponse preserves model role without adding user",
			inputJSON: `{"contents":[{"role":"model","parts":[{"functionCall":{"name":"run"}}]},{"role":"model","parts":[{"functionResponse":{"name":"run","response":{"result":"ok"}}}]}]}`,
			path:      "contents",
			wantRoles: "model,model",
		},
		{
			name:              "trailing model functionCall gets empty user",
			inputJSON:         `{"contents":[{"role":"user","parts":[{"text":"hello"}]},{"role":"model","parts":[{"functionCall":{"name":"run"}}]}]}`,
			path:              "contents",
			wantRoles:         "user,model,user",
			wantTrailingEmpty: true,
		},
		{
			name:              "trailing model text gets empty user and preserves preceding turns",
			inputJSON:         `{"contents":[{"role":"user","parts":[{"text":"hello"}]},{"role":"model","parts":[{"text":"answer"}]}]}`,
			path:              "contents",
			wantRoles:         "user,model,user",
			wantTrailingEmpty: true,
		},
		{
			name:              "nested contents are normalized",
			inputJSON:         `{"request":{"contents":[{"role":"user","parts":[{"text":"hello"}]},{"role":"model","parts":[{"text":"answer"}]}]}}`,
			path:              "request.contents",
			wantRoles:         "request.user,model,user",
			wantTrailingEmpty: true,
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
			out := EnsureGeminiTrailingUserContent([]byte(tt.inputJSON), tt.path)
			contents := gjson.GetBytes(out, tt.path).Array()
			roles := make([]string, 0, len(contents))
			for _, content := range contents {
				roles = append(roles, content.Get("role").String())
			}
			expectedRoles := strings.TrimPrefix(tt.wantRoles, "request.")
			if got := strings.Join(roles, ","); got != expectedRoles {
				t.Fatalf("roles = %q, want %q; output=%s", got, expectedRoles, out)
			}
			if tt.wantTrailingEmpty {
				lastIdx := len(contents) - 1
				text := contents[lastIdx].Get("parts.0.text")
				if !text.Exists() || text.String() != "" {
					t.Fatalf("trailing empty user part missing; output=%s", out)
				}
			}
		})
	}
}

func TestEnsureGeminiBoundaryUserContent(t *testing.T) {
	// A single model turn should get both leading and trailing user turns
	inputJSON := `{"contents":[{"role":"model","parts":[{"text":"single answer"}]}]}`
	out := EnsureGeminiBoundaryUserContent([]byte(inputJSON), "contents")
	contents := gjson.GetBytes(out, "contents").Array()
	if len(contents) != 3 {
		t.Fatalf("contents len = %d, want 3; out=%s", len(contents), out)
	}
	if contents[0].Get("role").String() != "user" || contents[0].Get("parts.0.text").String() != "" {
		t.Fatalf("leading user turn missing: %s", out)
	}
	if contents[1].Get("role").String() != "model" || contents[1].Get("parts.0.text").String() != "single answer" {
		t.Fatalf("middle model turn corrupted: %s", out)
	}
	if contents[2].Get("role").String() != "user" || contents[2].Get("parts.0.text").String() != "" {
		t.Fatalf("trailing user turn missing: %s", out)
	}
}
