// Package registry provides model definitions and lookup helpers for various AI providers.
// task_classifier.go classifies tasks by complexity based on token counts.
//
// Ported from thegent/src/thegent/routing/task_router.py (TaskClassifier class).
package registry

import "context"

// TaskClassificationRequest carries token counts and optional metadata for classification.
type TaskClassificationRequest struct {
	TokensIn  int
	TokensOut int
	Metadata  map[string]string
}

// TaskClassifier categorises tasks into complexity tiers.
// Tiers map to separate Pareto frontiers (cheap/fast models for FAST,
// high-quality models for HIGH_COMPLEX).
//
// Boundaries (total tokens):
//   - FAST:         < 500
//   - NORMAL:       500 – 4 999
//   - COMPLEX:      5 000 – 49 999
//   - HIGH_COMPLEX: ≥ 50 000
type TaskClassifier struct{}

// NewTaskClassifier returns a new TaskClassifier.
func NewTaskClassifier() *TaskClassifier {
	return &TaskClassifier{}
}

// Classify returns the complexity category for a task based on total token count.
func (tc *TaskClassifier) Classify(_ context.Context, req *TaskClassificationRequest) (string, error) {
	total := req.TokensIn + req.TokensOut
	switch {
	case total < 500:
		return "FAST", nil
	case total < 5000:
		return "NORMAL", nil
	case total < 50000:
		return "COMPLEX", nil
	default:
		return "HIGH_COMPLEX", nil
	}
}
