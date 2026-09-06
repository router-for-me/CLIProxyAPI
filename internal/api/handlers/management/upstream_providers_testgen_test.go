package management

import (
	"fmt"
	"strings"
	"testing"
)

func TestGenerateMathQuestionShape(t *testing.T) {
	for i := 0; i < 200; i++ {
		q := generateMathQuestion()
		if q.Question == "" {
			t.Fatal("question text is empty")
		}
		if !strings.Contains(q.Question, "What is") {
			t.Fatalf("question %q does not contain the prompt prefix", q.Question)
		}
		if q.Expected < 0 {
			t.Fatalf("expected answer %d is negative", q.Expected)
		}
		// Recompute the answer from the structured operands + operator.
		var want int
		switch q.Op {
		case "+":
			want = q.A + q.B
		case "×":
			want = q.A * q.B
		case "-":
			want = q.A - q.B
		default:
			t.Fatalf("unexpected operator %q", q.Op)
		}
		if q.Expected != want {
			t.Fatalf("question %q: expected answer %d, computed %d", q.Question, q.Expected, want)
		}
		// The operands printed in the question must match the struct.
		if !strings.Contains(q.Question, fmt.Sprint(q.A)) ||
			!strings.Contains(q.Question, fmt.Sprint(q.B)) {
			t.Fatalf("question %q does not mention operands %d and %d", q.Question, q.A, q.B)
		}
		if want < 0 {
			t.Fatalf("subtraction produced a negative result: %q", q.Question)
		}
	}
}

func TestGenerateMathQuestionVaries(t *testing.T) {
	seen := map[string]struct{}{}
	for i := 0; i < 50; i++ {
		seen[generateMathQuestion().Question] = struct{}{}
	}
	if len(seen) < 10 {
		t.Fatalf("only %d distinct questions in 50 draws; generator looks constant", len(seen))
	}
}
