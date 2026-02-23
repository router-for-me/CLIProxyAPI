package thinking

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/registry"
	log "github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
)

type redactionTestApplier struct{}

func (redactionTestApplier) Apply(body []byte, _ ThinkingConfig, _ *registry.ModelInfo) ([]byte, error) {
	return body, nil
}

func TestThinkingValidateLogsRedactSensitiveValues(t *testing.T) {
	hook := test.NewLocal(log.StandardLogger())
	defer hook.Reset()

	previousLevel := log.GetLevel()
	log.SetLevel(log.DebugLevel)
	defer log.SetLevel(previousLevel)

	providerSecret := "provider-secret-l6-validate"
	modelSecret := "model-secret-l6-validate"

	convertAutoToMidRange(
		ThinkingConfig{Mode: ModeAuto, Budget: -1},
		&registry.ThinkingSupport{Levels: []string{"low", "high"}},
		providerSecret,
		modelSecret,
	)

	convertAutoToMidRange(
		ThinkingConfig{Mode: ModeAuto, Budget: -1},
		&registry.ThinkingSupport{Min: 1000, Max: 3000},
		providerSecret,
		modelSecret,
	)

	clampLevel(
		LevelMedium,
		&registry.ModelInfo{
			ID: modelSecret,
			Thinking: &registry.ThinkingSupport{
				Levels: []string{"low", "high"},
			},
		},
		providerSecret,
	)

	clampBudget(
		0,
		&registry.ModelInfo{
			ID: modelSecret,
			Thinking: &registry.ThinkingSupport{
				Min:         1024,
				Max:         8192,
				ZeroAllowed: false,
			},
		},
		providerSecret,
	)

	logClamp(providerSecret, modelSecret, 9999, 8192, 1024, 8192)

	assertLogFieldRedacted(t, hook, "thinking: mode converted, dynamic not allowed, using medium level |", "provider")
	assertLogFieldRedacted(t, hook, "thinking: mode converted, dynamic not allowed, using medium level |", "model")
	assertLogFieldRedacted(t, hook, "thinking: mode converted, dynamic not allowed, using medium level |", "clamped_to")

	assertLogFieldRedacted(t, hook, "thinking: mode converted, dynamic not allowed |", "provider")
	assertLogFieldRedacted(t, hook, "thinking: mode converted, dynamic not allowed |", "model")
	assertLogFieldRedacted(t, hook, "thinking: mode converted, dynamic not allowed |", "clamped_to")

	assertLogFieldRedacted(t, hook, "thinking: level clamped |", "provider")
	assertLogFieldRedacted(t, hook, "thinking: level clamped |", "model")
	assertLogFieldRedacted(t, hook, "thinking: level clamped |", "original_value")
	assertLogFieldRedacted(t, hook, "thinking: level clamped |", "clamped_to")

	assertLogFieldRedacted(t, hook, "thinking: budget zero not allowed |", "provider")
	assertLogFieldRedacted(t, hook, "thinking: budget zero not allowed |", "model")
	assertLogFieldRedacted(t, hook, "thinking: budget zero not allowed |", "original_value")
	assertLogFieldRedacted(t, hook, "thinking: budget zero not allowed |", "min")
	assertLogFieldRedacted(t, hook, "thinking: budget zero not allowed |", "max")
	assertLogFieldRedacted(t, hook, "thinking: budget zero not allowed |", "clamped_to")

	assertLogFieldRedacted(t, hook, "thinking: budget clamped |", "provider")
	assertLogFieldRedacted(t, hook, "thinking: budget clamped |", "model")
	assertLogFieldRedacted(t, hook, "thinking: budget clamped |", "original_value")
	assertLogFieldRedacted(t, hook, "thinking: budget clamped |", "min")
	assertLogFieldRedacted(t, hook, "thinking: budget clamped |", "max")
	assertLogFieldRedacted(t, hook, "thinking: budget clamped |", "clamped_to")
}

func TestThinkingApplyLogsRedactSensitiveValues(t *testing.T) {
	hook := test.NewLocal(log.StandardLogger())
	defer hook.Reset()

	previousLevel := log.GetLevel()
	log.SetLevel(log.DebugLevel)
	defer log.SetLevel(previousLevel)

	previousClaude := GetProviderApplier("claude")
	RegisterProvider("claude", redactionTestApplier{})
	defer RegisterProvider("claude", previousClaude)

	modelSecret := "model-secret-l6-apply"
	suffixSecret := "suffix-secret-l6-apply"

	reg := registry.GetGlobalRegistry()
	clientID := "redaction-test-client-l6-apply"
	reg.RegisterClient(clientID, "claude", []*registry.ModelInfo{
		{
			ID: modelSecret,
			Thinking: &registry.ThinkingSupport{
				Min:         1000,
				Max:         3000,
				ZeroAllowed: false,
			},
		},
	})
	defer reg.RegisterClient(clientID, "claude", nil)

	_, err := ApplyThinking(
		[]byte(`{"thinking":{"budget_tokens":2000}}`),
		modelSecret,
		"claude",
		"claude",
		"claude",
	)
	if err != nil {
		t.Fatalf("ApplyThinking success path returned error: %v", err)
	}

	_ = parseSuffixToConfig(suffixSecret, "claude", modelSecret)

	_, err = applyUserDefinedModel(
		[]byte(`{}`),
		nil,
		"claude",
		"claude",
		SuffixResult{ModelName: modelSecret},
	)
	if err != nil {
		t.Fatalf("applyUserDefinedModel no-config path returned error: %v", err)
	}

	_, err = applyUserDefinedModel(
		[]byte(`{"thinking":{"budget_tokens":2000}}`),
		nil,
		"claude",
		"lane6-unknown-provider",
		SuffixResult{ModelName: modelSecret, HasSuffix: true, RawSuffix: "high"},
	)
	if err != nil {
		t.Fatalf("applyUserDefinedModel unknown-provider path returned error: %v", err)
	}

	_, err = applyUserDefinedModel(
		[]byte(`{"thinking":{"budget_tokens":2000}}`),
		nil,
		"claude",
		"claude",
		SuffixResult{ModelName: modelSecret},
	)
	if err != nil {
		t.Fatalf("applyUserDefinedModel apply path returned error: %v", err)
	}

	assertLogFieldRedacted(t, hook, "thinking: processed config to apply |", "provider")
	assertLogFieldRedacted(t, hook, "thinking: processed config to apply |", "model")
	assertLogFieldRedacted(t, hook, "thinking: processed config to apply |", "mode")
	assertLogFieldRedacted(t, hook, "thinking: processed config to apply |", "budget")
	assertLogFieldRedacted(t, hook, "thinking: processed config to apply |", "level")

	assertLogFieldRedacted(t, hook, "thinking: unknown suffix format, treating as no config |", "provider")
	assertLogFieldRedacted(t, hook, "thinking: unknown suffix format, treating as no config |", "model")
	assertLogFieldRedacted(t, hook, "thinking: unknown suffix format, treating as no config |", "raw_suffix")

	assertLogFieldRedacted(t, hook, "thinking: user-defined model, passthrough (no config) |", "provider")
	assertLogFieldRedacted(t, hook, "thinking: user-defined model, passthrough (no config) |", "model")

	assertLogFieldRedacted(t, hook, "thinking: user-defined model, passthrough (unknown provider) |", "provider")
	assertLogFieldRedacted(t, hook, "thinking: user-defined model, passthrough (unknown provider) |", "model")

	assertLogFieldRedacted(t, hook, "thinking: applying config for user-defined model (skip validation)", "provider")
	assertLogFieldRedacted(t, hook, "thinking: applying config for user-defined model (skip validation)", "model")
	assertLogFieldRedacted(t, hook, "thinking: applying config for user-defined model (skip validation)", "mode")
	assertLogFieldRedacted(t, hook, "thinking: applying config for user-defined model (skip validation)", "budget")
	assertLogFieldRedacted(t, hook, "thinking: applying config for user-defined model (skip validation)", "level")
}

func assertLogFieldRedacted(t *testing.T, hook *test.Hook, message, field string) {
	t.Helper()
	for _, entry := range hook.AllEntries() {
		if entry.Message != message {
			continue
		}
		value, ok := entry.Data[field]
		if !ok && field == "level" {
			value, ok = entry.Data["fields.level"]
		}
		if !ok {
			t.Fatalf("log %q missing field %q", message, field)
		}
		if value != redactedLogValue {
			t.Fatalf("log %q field %q = %v, want %q", message, field, value, redactedLogValue)
		}
		return
	}
	t.Fatalf("log %q not found", message)
}
