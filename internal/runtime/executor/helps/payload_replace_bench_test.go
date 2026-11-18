package helps

import (
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func BenchmarkPayloadReplace(b *testing.B) {
	for _, scenario := range []struct {
		name      string
		protocol  string
		messages  int
		positions []int
		all       bool
		match     bool
	}{
		{"ChatShortFirstTwo", "openai", 8, []int{1, 2}, false, true},
		{"ChatLongFirstTwo", "openai", 1000, []int{1, 2}, false, true},
		{"ChatLongFirstTwoNoMatch", "openai", 1000, []int{1, 2}, false, false},
		{"ChatLongLast", "openai", 1000, []int{500}, false, true},
		{"ChatAllMatch", "openai", 256, nil, true, true},
		{"ChatAllNoMatch", "openai", 1000, nil, true, false},
		{"GeminiLongFirstTwo", "gemini", 1000, []int{1, 2}, false, true},
		{"GeminiAllMatch", "gemini", 256, nil, true, true},
		{"GeminiAllNoMatch", "gemini", 1000, nil, true, false},
	} {
		b.Run(scenario.name, func(b *testing.B) {
			text := "hello " + strings.Repeat("ordinary text ", 32)
			if scenario.match {
				text = "you are hero " + text
			}
			messages := make([]any, scenario.messages)
			for i := range messages {
				role := "user"
				if i%2 == 0 {
					role = "assistant"
				}
				if scenario.protocol == "gemini" {
					if role == "assistant" {
						role = "model"
					}
					messages[i] = map[string]any{"role": role, "parts": []any{map[string]any{"text": text}, map[string]any{"text": text}}}
				} else {
					messages[i] = map[string]any{"role": role, "content": text}
				}
			}
			key := "messages"
			if scenario.protocol == "gemini" {
				key = "contents"
			}
			body, errMarshal := json.Marshal(map[string]any{key: messages})
			if errMarshal != nil {
				b.Fatal(errMarshal)
			}
			cfg := &config.Config{Payload: config.PayloadConfig{Replace: []config.PayloadReplaceRule{{
				Models:       []config.PayloadModelRule{{Name: "*"}},
				Target:       config.PayloadReplaceTarget{Role: "user", Positions: scenario.positions, All: scenario.all},
				Replacements: []config.PayloadReplacement{{"you are hero": "you are fool"}, {"hello": "hi"}},
			}}}}
			if !scenario.match {
				cfg.Payload.Replace[0].Replacements = cfg.Payload.Replace[0].Replacements[:1]
			}
			b.ReportAllocs()
			for b.Loop() {
				_ = ApplyPayloadConfigWithRoot(cfg, "model", scenario.protocol, "", body, nil, "", "")
			}
		})
	}
}
