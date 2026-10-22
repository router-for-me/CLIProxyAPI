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
