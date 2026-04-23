package handlers

import "testing"

func TestParseRequestBodyDetails(t *testing.T) {
	tests := []struct {
		name      string
		rawJSON   string
		wantModel string
		wantHas   bool
		wantValue bool
	}{
		{
			name:      "stream true",
			rawJSON:   `{"model":"test-model","stream":true}`,
			wantModel: "test-model",
			wantHas:   true,
			wantValue: true,
		},
		{
			name:      "stream false",
			rawJSON:   `{"model":"test-model","stream":false}`,
			wantModel: "test-model",
			wantHas:   true,
			wantValue: false,
		},
		{
			name:      "stream missing",
			rawJSON:   `{"model":"test-model"}`,
			wantModel: "test-model",
			wantHas:   false,
			wantValue: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			details := ParseRequestBodyDetails([]byte(tt.rawJSON))
			if details.Model != tt.wantModel {
				t.Fatalf("model = %q, want %q", details.Model, tt.wantModel)
			}
			if details.HasStream != tt.wantHas {
				t.Fatalf("HasStream = %v, want %v", details.HasStream, tt.wantHas)
			}
			if details.Stream != tt.wantValue {
				t.Fatalf("Stream = %v, want %v", details.Stream, tt.wantValue)
			}
		})
	}
}

func TestOpenAIChatRequestBodyDetailsUsesResponsesFormat(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "chat completions payload",
			body: `{"model":"gpt-test","messages":[{"role":"user","content":"hi"}],"stream":true}`,
			want: false,
		},
		{
			name: "responses input payload",
			body: `{"model":"gpt-test","input":"hi","stream":false}`,
			want: true,
		},
		{
			name: "responses instructions payload",
			body: `{"model":"gpt-test","instructions":"be brief"}`,
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			details := ParseOpenAIChatRequestBodyDetails([]byte(tt.body))
			if got := details.UsesResponsesFormat(); got != tt.want {
				t.Fatalf("UsesResponsesFormat() = %v, want %v", got, tt.want)
			}
		})
	}
}
