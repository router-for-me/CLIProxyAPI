package helps

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizeReplayJSONNumbers(t *testing.T) {
	for _, tt := range []struct {
		input string
		want  string
	}{
		{input: `{"a":[1.0,1000,0.000001,0.0000001,-0.0],"literal":"1.0"}`, want: `{"a":[1,1000,0.000001,1e-7,0],"literal":"1.0"}`},
		{input: `[9007199254740993,9007199254740992]`, want: `[9007199254740993,9007199254740992]`},
		{input: `[-12.50,-125e-1,1e21,1e20]`, want: `[-12.5,-12.5,1e+21,100000000000000000000]`},
		{input: `[0.10000000000000001,0.1]`, want: `[0.10000000000000001,0.1]`},
		{input: `[1e100000000000000000000,10e99999999999999999999]`, want: `[1e+100000000000000000000,1e+100000000000000000000]`},
		{input: `[1e-100000000000000000000,10e-100000000000000000001]`, want: `[1e-100000000000000000000,1e-100000000000000000000]`},
	} {
		t.Run(tt.input, func(t *testing.T) {
			decoder := json.NewDecoder(strings.NewReader(tt.input))
			decoder.UseNumber()
			var value any
			if errDecode := decoder.Decode(&value); errDecode != nil {
				t.Fatal(errDecode)
			}
			got, errMarshal := json.Marshal(NormalizeReplayJSONNumbers(value))
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			if string(got) != tt.want {
				t.Fatalf("normalized = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestNormalizeReplayJSONNumbersBoundsExponentParsing(t *testing.T) {
	for _, tt := range []struct {
		name  string
		input string
		want  string
	}{
		{name: "positive limit", input: "10e" + strings.Repeat("9", 64), want: "1e+1" + strings.Repeat("0", 64)},
		{name: "negative limit", input: "0.1e-" + strings.Repeat("9", 64), want: "1e-1" + strings.Repeat("0", 64)},
		{name: "over limit", input: "10e" + strings.Repeat("9", 65)},
		{name: "huge positive", input: "10e+" + strings.Repeat("9", 1_000_000)},
		{name: "huge negative", input: "0.1e-" + strings.Repeat("9", 1_000_000)},
		{name: "padded positive", input: "10e+" + strings.Repeat("0", 1_000_000) + "2", want: "1000"},
		{name: "padded negative", input: "10e-" + strings.Repeat("0", 1_000_000) + "2", want: "0.1"},
		{name: "padded zero", input: "10e-" + strings.Repeat("0", 1_000_000), want: "10"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.want == "" {
				tt.want = tt.input
			}
			decoder := json.NewDecoder(strings.NewReader(tt.input))
			decoder.UseNumber()
			var value any
			if errDecode := decoder.Decode(&value); errDecode != nil {
				t.Fatal(errDecode)
			}
			got, errMarshal := json.Marshal(NormalizeReplayJSONNumbers(value))
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			if string(got) != tt.want {
				t.Fatalf("normalized number differs from expected value (got %d bytes, want %d)", len(got), len(tt.want))
			}
		})
	}
}
