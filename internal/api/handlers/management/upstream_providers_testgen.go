package management

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

// mathQuestion is one generated probe question. Expected carries the exact
// integer answer so the handler can return it for the dashboard's
// comparison display.
type mathQuestion struct {
	Question string
	Expected int
	A        int
	B        int
	Op       string
}

// generateMathQuestion builds a random arithmetic probe: two 11..99
// operands combined with +, ×, or a subtraction guaranteed non-negative.
// Random content keeps the probe off upstream prompt caches, so repeated
// tests against the same entry always exercise the real path.
func generateMathQuestion() mathQuestion {
	a := randInt(11, 99)
	b := randInt(11, 99)
	switch randInt(0, 2) {
	case 0:
		return mathQuestion{
			Question: fmt.Sprintf("What is %d + %d? Reply with just the number.", a, b),
			Expected: a + b, A: a, B: b, Op: "+",
		}
	case 1:
		return mathQuestion{
			Question: fmt.Sprintf("What is %d × %d? Reply with just the number.", a, b),
			Expected: a * b, A: a, B: b, Op: "×",
		}
	default:
		if b > a {
			a, b = b, a
		}
		return mathQuestion{
			Question: fmt.Sprintf("What is %d - %d? Reply with just the number.", a, b),
			Expected: a - b, A: a, B: b, Op: "-",
		}
	}
}

// randInt returns a crypto-random integer in [min, max]. Falls back to min
// on entropy failure; the probe is best-effort, not security material.
func randInt(min, max int) int {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(max-min+1)))
	if err != nil {
		return min
	}
	return min + int(n.Int64())
}
