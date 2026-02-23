// Package registry — task complexity classifier for routing.
//
// Categorizes tasks into FAST/NORMAL/COMPLEX/HIGH_COMPLEX based on token counts.
// Ported from thegent/routing/task_router.py TaskClassifier.
package registry

import "context"

// TaskClassificationRequest holds the inputs for task classification.
type TaskClassificationRequest struct {
	TokensIn  int               `json:"tokens_in"`
	TokensOut int               `json:"tokens_out"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// TaskClassifier categorizes tasks by complexity based on token counts.
type TaskClassifier struct{}

// NewTaskClassifier returns a new TaskClassifier.
func NewTaskClassifier() *TaskClassifier {
	return &TaskClassifier{}
}

// Classify returns the complexity category for the given task.
func (tc *TaskClassifier) Classify(_ context.Context, req *TaskClassificationRequest) (string, error) {
	totalTokens := req.TokensIn + req.TokensOut

	switch {
	case totalTokens < 500:
		return "FAST", nil
	case totalTokens < 5000:
		return "NORMAL", nil
	case totalTokens < 50000:
		return "COMPLEX", nil
	default:
		return "HIGH_COMPLEX", nil
	}
}
