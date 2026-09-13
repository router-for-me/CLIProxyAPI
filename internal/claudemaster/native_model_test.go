package claudemaster

import (
	"reflect"
	"strings"
	"testing"
)

func TestNativeArgumentsPinsClaudeModel(t *testing.T) {
	const model = "claude-sonnet-4-6"
	for _, test := range []struct {
		name       string
		args, want []string
	}{
		{"nil", nil, []string{"--model", model}},
		{"empty", []string{}, []string{"--model", model}},
		{"print", []string{"-p", "hello"}, []string{"--model", model, "-p", "hello"}},
		{"remote", []string{"--remote-control", "project"}, []string{"--model", model, "--remote-control", "project"}},
		{"separate", []string{"-p", "hello", "--model", model}, []string{"-p", "hello", "--model", model}},
		{"equals", []string{"--model=" + model, "-p", "hello"}, []string{"--model=" + model, "-p", "hello"}},
		{"terminator", []string{"--", "--model", "sonnet"}, []string{"--model", model, "--", "--model", "sonnet"}},
		{"exact-before-terminator", []string{"--model", model, "--", "--model=literal"}, []string{"--model", model, "--", "--model=literal"}},
		{"similar-flag", []string{"--model-other", "literal"}, []string{"--model", model, "--model-other", "literal"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := append([]string(nil), test.args...)
			got, err := NativeArguments("claude", model, test.args)
			if err != nil || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("unexpected native arguments: got=%v err=%v want=%v", got, err, test.want)
			}
			if !reflect.DeepEqual(append([]string(nil), test.args...), before) {
				t.Fatal("input arguments were modified")
			}
			if len(test.args) > 0 {
				got[len(got)-1] = "changed-output"
				if !reflect.DeepEqual(append([]string(nil), test.args...), before) {
					t.Fatal("output aliases the input arguments")
				}
			}
		})
	}
}

func TestNativeArgumentsRejectsClaudeOverrides(t *testing.T) {
	const model = "claude-sonnet-4-6"
	for _, args := range [][]string{
		{"--model"}, {"--model", ""}, {"--model", " "}, {"--model="}, {"--model= "},
		{"--model", "--print"}, {"--model", "--"},
		{"--model", "sonnet"}, {"--model=sonnet"}, {"--model", "opus"},
		{"--model", "claude-sonnet-5"}, {"--model=claude-sonnet-5"},
		{"--model", " " + model}, {"--model", model + " "},
		{"--model", model, "--model", model},
		{"--model=" + model, "--model=" + model},
		{"--model", model, "--model=" + model},
		{"--model=" + model, "--model", model},
		{"--model", model, "--model"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			before := append([]string(nil), args...)
			got, err := NativeArguments("claude", model, args)
			if err == nil || got != nil {
				t.Fatal("invalid native model override accepted")
			}
			if !reflect.DeepEqual(args, before) {
				t.Fatal("rejected arguments were modified")
			}
		})
	}
}

func TestNativeArgumentsRejectsFallbackForBothProviders(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for _, args := range [][]string{
			{"--fallback-model", "sonnet"}, {"--fallback-model=sonnet"},
			{"--fallback-model"}, {"--fallback-model="},
			{"-p", "hello", "--fallback-model", "opus"},
			{"--model", "claude-sonnet-4-6", "--fallback-model=opus"},
			{"--model", "sonnet", "--fallback-model=opus"},
		} {
			got, err := NativeArguments(provider, "claude-sonnet-4-6", args)
			if err == nil || got != nil || err.Error() != "native fallback models are not supported; inference is pinned to one backend model" {
				t.Fatalf("fallback not rejected with fixed error for %s: %v", provider, err)
			}
		}
	}
}

func TestNativeArgumentsLeavesCodexNativeModelUnchanged(t *testing.T) {
	for _, args := range [][]string{
		nil, {}, {"-p", "hello"}, {"--model", "sonnet"}, {"--model=claude-opus-4-8"},
		{"--model"}, {"--model", "sonnet", "--model", "opus"},
		{"--", "--fallback-model=literal"},
	} {
		before := append([]string(nil), args...)
		got, err := NativeArguments("codex", "gpt-5.6", args)
		if err != nil || !reflect.DeepEqual(got, args) {
			t.Fatalf("Codex native arguments changed: got=%v err=%v", got, err)
		}
		if len(got) > 0 {
			got[0] = "changed-output"
			if !reflect.DeepEqual(args, before) {
				t.Fatal("Codex output aliases caller arguments")
			}
		}
	}
}

func TestNativeArgumentsValidatesProviderAndBackendModel(t *testing.T) {
	for _, test := range []struct{ provider, model string }{
		{"", "claude-sonnet-4-6"}, {"other", "claude-sonnet-4-6"},
		{"claude", ""}, {"claude", " \t"}, {"codex", ""}, {"codex", " \n"},
	} {
		if got, err := NativeArguments(test.provider, test.model, nil); err == nil || got != nil {
			t.Fatal("invalid provider/backend model accepted")
		}
	}
}

func TestNativeArgumentsErrorsDoNotEchoArguments(t *testing.T) {
	for _, args := range [][]string{
		{"--model=PRIVATE-CANARY"}, {"--fallback-model=PRIVATE-CANARY"},
	} {
		_, err := NativeArguments("claude", "claude-sonnet-4-6", args)
		if err == nil || strings.Contains(err.Error(), "PRIVATE-CANARY") {
			t.Fatal("native argument error echoed caller data")
		}
	}
}
