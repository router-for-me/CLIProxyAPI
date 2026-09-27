package precompact

import "testing"

func TestBudget(t *testing.T) {
	cases := []struct {
		name      string
		window    int
		threshold float64
		body      string
		want      int
	}{
		{"basic", 200000, 0.85, `{}`, 170000},
		{"with max_tokens", 200000, 0.85, `{"max_tokens":8000}`, 162000},
		{"invalid threshold falls back to 0.85", 200000, 0, `{}`, 170000},
		{"max_completion_tokens honored", 100000, 0.85, `{"max_completion_tokens":5000}`, 80000},
		{"never below one", 10, 0.85, `{"max_tokens":9}`, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Budget(c.window, c.threshold, []byte(c.body))
			if got != c.want {
				t.Fatalf("Budget(%d,%v,%s) = %d, want %d", c.window, c.threshold, c.body, got, c.want)
			}
		})
	}
}

func TestModelWindowDefaultsWithoutRegistry(t *testing.T) {
	if got := ModelWindow(nil, "some-model"); got != 200000 {
		t.Fatalf("ModelWindow(nil,..) = %d, want 200000", got)
	}
}
