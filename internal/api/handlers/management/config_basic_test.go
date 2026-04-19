package management

import "testing"

func TestNormalizeRoutingStrategy(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		input  string
		want   string
		wantOK bool
	}{
		{name: "default empty", input: "", want: "round-robin", wantOK: true},
		{name: "round robin alias", input: "rr", want: "round-robin", wantOK: true},
		{name: "fill first alias", input: "ff", want: "fill-first", wantOK: true},
		{name: "burst sync alias", input: "burst-sync-sticky", want: "oauth-quota-burst-sync-sticky", wantOK: true},
		{name: "reserve alias", input: "reserve-staggered", want: "oauth-quota-reserve-staggered", wantOK: true},
		{name: "weekly guarded alias", input: "weekly-guarded-sticky", want: "oauth-quota-weekly-guarded-sticky", wantOK: true},
		{name: "invalid", input: "unknown-strategy", want: "", wantOK: false},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got, ok := normalizeRoutingStrategy(testCase.input)
			if ok != testCase.wantOK {
				t.Fatalf("normalizeRoutingStrategy(%q) ok = %v, want %v", testCase.input, ok, testCase.wantOK)
			}
			if got != testCase.want {
				t.Fatalf("normalizeRoutingStrategy(%q) = %q, want %q", testCase.input, got, testCase.want)
			}
		})
	}
}
