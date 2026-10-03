package claudemaster

import (
	"reflect"
	"testing"
)

func TestNativeArgumentsLeavesClaudeSelectionUnchanged(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
	}{
		{"nil", nil},
		{"empty", []string{}},
		{"print", []string{"-p", "hello"}},
		{"native-model", []string{"--model", "sonnet"}},
		{"native-model-equals", []string{"--model=opus"}},
		{"native-fallback", []string{"--fallback-model", "haiku"}},
		{"native-fallback-equals", []string{"--fallback-model=haiku"}},
		{"model-and-fallback", []string{"--model", "opus", "--fallback-model", "sonnet"}},
		{"native-validation", []string{"--model"}},
		{"terminator", []string{"--", "--model", "literal", "--fallback-model=literal"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := cloneNativeArguments(test.args)
			got, err := NativeArguments("claude", test.args)
			if err != nil || !reflect.DeepEqual(got, test.args) {
				t.Fatalf("Claude arguments changed: got=%v err=%v want=%v", got, err, test.args)
			}
			if !reflect.DeepEqual(test.args, before) {
				t.Fatal("input arguments were modified")
			}
			if len(got) > 0 {
				got[0] = "changed-output"
				if !reflect.DeepEqual(test.args, before) {
					t.Fatal("output aliases the input arguments")
				}
			}
		})
	}
}

func TestNativeArgumentsRejectsNonClaudeProviders(t *testing.T) {
	args := []string{"--model=PRIVATE-CANARY", "--fallback-model=PRIVATE-CANARY"}
	for _, provider := range []string{"", "codex", "other"} {
		got, err := NativeArguments(provider, args)
		if err == nil || got != nil || err.Error() != "inference provider must be claude" {
			t.Fatalf("invalid provider accepted: provider=%q got=%v err=%v", provider, got, err)
		}
	}
}
