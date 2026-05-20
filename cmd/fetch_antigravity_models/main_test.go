package main

import "testing"

func TestHasAntigravity35FlashPair(t *testing.T) {
	models := []modelEntry{
		{ID: "gemini-3-flash-agent", DisplayName: "Gemini 3.5 Flash (High)"},
		{ID: "gemini-3.5-flash-low", DisplayName: "Gemini 3.5 Flash (Medium)"},
	}

	if !hasAntigravity35FlashPair(models) {
		t.Fatal("expected Gemini 3.5 Flash high/medium pair")
	}
}

func TestHasAntigravity35FlashPairRejectsSingleVariant(t *testing.T) {
	models := []modelEntry{
		{ID: "gemini-3-flash-agent", DisplayName: "Gemini 3.5 Flash (High)"},
	}

	if hasAntigravity35FlashPair(models) {
		t.Fatal("expected single Gemini 3.5 Flash variant to be incomplete")
	}
}
