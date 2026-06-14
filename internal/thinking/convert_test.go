package thinking

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestConvertLevelToBudget(t *testing.T) {
	tests := []struct {
		level      string
		wantBudget int
		wantOK     bool
	}{
		{level: "none", wantBudget: 0, wantOK: true},
		{level: "auto", wantBudget: -1, wantOK: true},
		{level: "minimal", wantBudget: 512, wantOK: true},
		{level: "low", wantBudget: 1024, wantOK: true},
		{level: "medium", wantBudget: 8192, wantOK: true},
		{level: "high", wantBudget: 24576, wantOK: true},
		{level: "xhigh", wantBudget: 32768, wantOK: true},
		{level: "max", wantBudget: 128000, wantOK: true},
		{level: "HIGH", wantBudget: 24576, wantOK: true},
		{level: "Medium", wantBudget: 8192, wantOK: true},
		{level: "unknown", wantBudget: 0, wantOK: false},
		{level: "ultra", wantBudget: 0, wantOK: false},
		{level: "", wantBudget: 0, wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.level, func(t *testing.T) {
			budget, ok := ConvertLevelToBudget(tt.level)
			if budget != tt.wantBudget {
				t.Errorf("budget = %d, want %d", budget, tt.wantBudget)
			}
			if ok != tt.wantOK {
				t.Errorf("ok = %v, want %v", ok, tt.wantOK)
			}
		})
	}
}

func TestConvertBudgetToLevel(t *testing.T) {
	tests := []struct {
		budget    int
		wantLevel string
		wantOK    bool
	}{
		{budget: -2, wantLevel: "", wantOK: false},
		{budget: -1, wantLevel: "auto", wantOK: true},
		{budget: 0, wantLevel: "none", wantOK: true},
		{budget: 1, wantLevel: "minimal", wantOK: true},
		{budget: 256, wantLevel: "minimal", wantOK: true},
		{budget: 512, wantLevel: "minimal", wantOK: true},
		{budget: 513, wantLevel: "low", wantOK: true},
		{budget: 1024, wantLevel: "low", wantOK: true},
		{budget: 1025, wantLevel: "medium", wantOK: true},
		{budget: 4096, wantLevel: "medium", wantOK: true},
		{budget: 8192, wantLevel: "medium", wantOK: true},
		{budget: 8193, wantLevel: "high", wantOK: true},
		{budget: 24576, wantLevel: "high", wantOK: true},
		{budget: 24577, wantLevel: "xhigh", wantOK: true},
		{budget: 32768, wantLevel: "xhigh", wantOK: true},
		{budget: 100000, wantLevel: "xhigh", wantOK: true},
	}

	for _, tt := range tests {
		t.Run(tt.wantLevel, func(t *testing.T) {
			level, ok := ConvertBudgetToLevel(tt.budget)
			if level != tt.wantLevel {
				t.Errorf("level = %q, want %q", level, tt.wantLevel)
			}
			if ok != tt.wantOK {
				t.Errorf("ok = %v, want %v", ok, tt.wantOK)
			}
		})
	}
}

func TestHasLevel(t *testing.T) {
	levels := []string{"low", "medium", "high"}

	tests := []struct {
		name   string
		target string
		want   bool
	}{
		{name: "exact match", target: "low", want: true},
		{name: "case insensitive", target: "HIGH", want: true},
		{name: "with whitespace", target: "  medium  ", want: false},
		{name: "not in list", target: "xhigh", want: false},
		{name: "empty string", target: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasLevel(levels, tt.target); got != tt.want {
				t.Errorf("HasLevel() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMapToClaudeEffort(t *testing.T) {
	tests := []struct {
		name        string
		level       string
		supportsMax bool
		want        string
		wantOK      bool
	}{
		{name: "empty", level: "", supportsMax: false, want: "", wantOK: false},
		{name: "minimal", level: "minimal", supportsMax: false, want: "low", wantOK: true},
		{name: "low", level: "low", supportsMax: false, want: "low", wantOK: true},
		{name: "medium", level: "medium", supportsMax: false, want: "medium", wantOK: true},
		{name: "high", level: "high", supportsMax: false, want: "high", wantOK: true},
		{name: "xhigh without max", level: "xhigh", supportsMax: false, want: "high", wantOK: true},
		{name: "xhigh with max", level: "xhigh", supportsMax: true, want: "max", wantOK: true},
		{name: "max without max", level: "max", supportsMax: false, want: "high", wantOK: true},
		{name: "max with max", level: "max", supportsMax: true, want: "max", wantOK: true},
		{name: "auto", level: "auto", supportsMax: false, want: "high", wantOK: true},
		{name: "case insensitive", level: "HIGH", supportsMax: false, want: "high", wantOK: true},
		{name: "unknown", level: "ultra", supportsMax: false, want: "", wantOK: false},
		{name: "minimal with max", level: "minimal", supportsMax: true, want: "low", wantOK: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := MapToClaudeEffort(tt.level, tt.supportsMax)
			if got != tt.want {
				t.Errorf("MapToClaudeEffort() = %q, want %q", got, tt.want)
			}
			if ok != tt.wantOK {
				t.Errorf("MapToClaudeEffort() ok = %v, want %v", ok, tt.wantOK)
			}
		})
	}
}

func TestDetectModelCapability(t *testing.T) {
	tests := []struct {
		name  string
		model *registry.ModelInfo
		want  ModelCapability
	}{
		{
			name:  "nil model info",
			model: nil,
			want:  CapabilityUnknown,
		},
		{
			name:  "nil thinking support",
			model: &registry.ModelInfo{ID: "test-model"},
			want:  CapabilityNone,
		},
		{
			name: "budget only",
			model: &registry.ModelInfo{
				ID:       "budget-model",
				Thinking: &registry.ThinkingSupport{Min: 128, Max: 20000},
			},
			want: CapabilityBudgetOnly,
		},
		{
			name: "level only",
			model: &registry.ModelInfo{
				ID:       "level-model",
				Thinking: &registry.ThinkingSupport{Levels: []string{"low", "medium", "high"}},
			},
			want: CapabilityLevelOnly,
		},
		{
			name: "hybrid (budget + levels)",
			model: &registry.ModelInfo{
				ID:       "hybrid-model",
				Thinking: &registry.ThinkingSupport{Min: 128, Max: 32768, Levels: []string{"low", "high"}},
			},
			want: CapabilityHybrid,
		},
		{
			name: "no budget and no levels",
			model: &registry.ModelInfo{
				ID:       "no-opts-model",
				Thinking: &registry.ThinkingSupport{},
			},
			want: CapabilityNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectModelCapability(tt.model)
			if got != tt.want {
				t.Errorf("detectModelCapability() = %v, want %v", got, tt.want)
			}
		})
	}
}
