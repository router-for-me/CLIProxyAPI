package reasoning

import (
	"testing"

	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCanonicalBillingModelNames(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "thinking on",
			in:   "qwen3-max@thinking:on",
			want: []string{"qwen3-max@thinking:on"},
		},
		{
			name: "shuffled temperature and thinking",
			in:   "qwen3-max@temperature:0.2@thinking:on",
			want: []string{"qwen3-max@thinking:on"},
		},
		{
			name: "thinking first then temperature",
			in:   "qwen3-max@thinking:on@temperature:0.2",
			want: []string{"qwen3-max@thinking:on"},
		},
		{
			name: "budget normalizes to on",
			in:   "qwen3-max@thinking:8192",
			want: []string{"qwen3-max@thinking:on"},
		},
		{
			name: "minus one normalizes to on",
			in:   "qwen3-max@thinking:-1",
			want: []string{"qwen3-max@thinking:on"},
		},
		{
			name: "adaptive normalizes to on",
			in:   "qwen3-max@thinking:adaptive",
			want: []string{"qwen3-max@thinking:on"},
		},
		{
			name: "thinking off",
			in:   "qwen3-max@thinking:off",
			want: []string{"qwen3-max@thinking:off"},
		},
		{
			name: "effort none becomes thinking off",
			in:   "qwen3-max@effort:none",
			want: []string{"qwen3-max@thinking:off"},
		},
		{
			name: "effort high implies thinking on",
			in:   "qwen3-max@effort:high",
			want: []string{"qwen3-max@effort:high@thinking:on", "qwen3-max@thinking:on"},
		},
		{
			name: "effort and thinking keys sorted",
			in:   "qwen3-max@thinking:on@effort:high@temperature:0.2",
			want: []string{"qwen3-max@effort:high@thinking:on", "qwen3-max@thinking:on"},
		},
		{
			name: "duplicate last wins then normalize",
			in:   "qwen3-max@thinking:off@thinking:on@effort:low@effort:high",
			want: []string{"qwen3-max@effort:high@thinking:on", "qwen3-max@thinking:on"},
		},
		{
			name: "legacy thinking alias",
			in:   "claude-3-7-sonnet-thinking",
			want: []string{"claude-3-7-sonnet@thinking:on"},
		},
		{
			name: "legacy thinking budget matches explicit budget",
			in:   "gemini-2.5-flash-thinking-8192",
			want: []string{"gemini-2.5-flash@thinking:on"},
		},
		{
			name: "legacy nothinking",
			in:   "claude-3-7-sonnet-nothinking",
			want: []string{"claude-3-7-sonnet@thinking:off"},
		},
		{
			name: "temperature only has no reasoning state",
			in:   "qwen3-max@temperature:0.7",
			want: nil,
		},
	}

	geminiSettings := model_setting.GetGeminiSettings()
	oldGemini := geminiSettings.ThinkingAdapterEnabled
	geminiSettings.ThinkingAdapterEnabled = true
	t.Cleanup(func() { geminiSettings.ThinkingAdapterEnabled = oldGemini })

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, CanonicalBillingModelNames(tt.in))
		})
	}

	assert.Equal(t,
		CanonicalBillingModelNames("gemini-2.5-flash@thinking:8192"),
		CanonicalBillingModelNames("gemini-2.5-flash-thinking-8192"),
	)
	assert.Equal(t, "gpt-5.1-codex-max", BaseModelName("gpt-5.1-codex-max"))
	assert.Empty(t, CanonicalBillingModelNames("gpt-5.1-codex-max"))
}

func TestParseOpenAIReasoningEffortPreservesCodexMax(t *testing.T) {
	effort, base := ParseOpenAIReasoningEffortFromModelSuffix("gpt-5.1-codex-max")
	assert.Empty(t, effort)
	assert.Equal(t, "gpt-5.1-codex-max", base)
}

func TestBaseModelNameStripsModifiers(t *testing.T) {
	require.Equal(t, "qwen3-max", BaseModelName("qwen3-max@thinking:on@temperature:0.2"))
}

func TestExemptAtNameIsOpaqueForBillingIdentity(t *testing.T) {
	settings := model_setting.GetGlobalSettings()
	original := append([]string(nil), settings.ThinkingModelBlacklist...)
	t.Cleanup(func() { settings.ThinkingModelBlacklist = original })
	settings.ThinkingModelBlacklist = append(original, "re:.*@sha256:.*")

	const model = "opaque@sha256:deadbeef"
	assert.Equal(t, model, BaseModelName(model))
	assert.Empty(t, CanonicalBillingModelNames(model))
	assert.Equal(t, "kimi-k2-thinking", BaseModelName("kimi-k2-thinking"))
	assert.Empty(t, CanonicalBillingModelNames("kimi-k2-thinking"))
}
