package executor

import (
	"slices"
	"testing"
)

func TestWithClaudeLatestAliasContextBeta(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		model     string
		betas     []string
		want1M    bool
		wantCount int
	}{
		{name: "sonnet latest", model: "sonnet-latest", want1M: true, wantCount: 1},
		{name: "opus latest", model: "opus-latest", betas: []string{"other-beta"}, want1M: true, wantCount: 2},
		{name: "already requested", model: "sonnet-latest", betas: []string{claudeContext1MBeta}, want1M: true, wantCount: 1},
		{name: "ordinary model", model: "claude-sonnet-5", betas: []string{"other-beta"}, want1M: false, wantCount: 1},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := withClaudeLatestAliasContextBeta(tt.model, slices.Clone(tt.betas))
			if slices.Contains(got, claudeContext1MBeta) != tt.want1M {
				t.Fatalf("1M beta presence = %v, want %v; betas=%v", slices.Contains(got, claudeContext1MBeta), tt.want1M, got)
			}
			if len(got) != tt.wantCount {
				t.Fatalf("len(betas) = %d, want %d; betas=%v", len(got), tt.wantCount, got)
			}
		})
	}
}
