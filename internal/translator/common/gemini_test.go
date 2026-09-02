package common

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestMergeAdjacentGeminiContents(t *testing.T) {
	t.Run("empty and single item", func(t *testing.T) {
		if got := MergeAdjacentGeminiContents(nil); len(got) != 0 {
			t.Fatalf("expected 0 items, got %d", len(got))
		}
		single := [][]byte{[]byte(`{"role":"user","parts":[{"text":"hello"}]}`)}
		if got := MergeAdjacentGeminiContents(single); len(got) != 1 {
			t.Fatalf("expected 1 item, got %d", len(got))
		}
	})

	t.Run("merges consecutive user turns", func(t *testing.T) {
		contents := [][]byte{
			[]byte(`{"role":"user","parts":[{"text":"first prompt"}]}`),
			[]byte(`{"role":"user","parts":[{"text":"<system-reminder>rule 1</system-reminder>"}]}`),
			[]byte(`{"role":"user","parts":[{"text":"<system-reminder>rule 2</system-reminder>"}]}`),
			[]byte(`{"role":"model","parts":[{"text":"assistant answer"}]}`),
			[]byte(`{"role":"user","parts":[{"text":"follow-up"}]}`),
		}

		merged := MergeAdjacentGeminiContents(contents)
		if len(merged) != 3 {
			t.Fatalf("expected 3 merged turns, got %d: %s", len(merged), JoinRawArray(merged))
		}

		// Turn 0: user with 3 parts
		turn0 := gjson.ParseBytes(merged[0])
		if turn0.Get("role").String() != "user" {
			t.Fatalf("expected role user, got %s", turn0.Get("role").String())
		}
		parts0 := turn0.Get("parts").Array()
		if len(parts0) != 3 {
			t.Fatalf("expected 3 parts in turn 0, got %d", len(parts0))
		}
		if parts0[0].Get("text").String() != "first prompt" ||
			parts0[1].Get("text").String() != "<system-reminder>rule 1</system-reminder>" ||
			parts0[2].Get("text").String() != "<system-reminder>rule 2</system-reminder>" {
			t.Fatalf("unexpected parts in turn 0: %v", parts0)
		}

		// Turn 1: model with 1 part
		turn1 := gjson.ParseBytes(merged[1])
		if turn1.Get("role").String() != "model" {
			t.Fatalf("expected role model, got %s", turn1.Get("role").String())
		}

		// Turn 2: user with 1 part
		turn2 := gjson.ParseBytes(merged[2])
		if turn2.Get("role").String() != "user" {
			t.Fatalf("expected role user, got %s", turn2.Get("role").String())
		}
	})

	t.Run("does not merge consecutive model turns to protect signature indices", func(t *testing.T) {
		contents := [][]byte{
			[]byte(`{"role":"user","parts":[{"text":"question"}]}`),
			[]byte(`{"role":"model","parts":[{"text":"thought","thought":true}]}`),
			[]byte(`{"role":"model","parts":[{"text":"answer"}]}`),
		}

		merged := MergeAdjacentGeminiContents(contents)
		if len(merged) != 3 {
			t.Fatalf("expected 3 turns (model turns kept unmerged), got %d: %s", len(merged), JoinRawArray(merged))
		}
	})

	t.Run("skips empty contents or contents with empty parts", func(t *testing.T) {
		contents := [][]byte{
			[]byte(``),
			[]byte(`{"role":"user","parts":[]}`),
			[]byte(`{"role":"user","parts":[{"text":"hello"}]}`),
		}
		merged := MergeAdjacentGeminiContents(contents)
		if len(merged) != 1 {
			t.Fatalf("expected 1 turn, got %d", len(merged))
		}
	})
}
