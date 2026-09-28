package jevgate

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildStateTruncatesLatestUserMessage(t *testing.T) {
	long := strings.Repeat("x", MaxStateChars+500)
	st := BuildState(StateInput{LatestUserText: long, MessageCount: 3})
	if len(st.LatestUserMessage) != MaxStateChars {
		t.Errorf("len = %d, want %d", len(st.LatestUserMessage), MaxStateChars)
	}
}

func TestBuildStateCarriesMetadataFlags(t *testing.T) {
	st := BuildState(StateInput{
		LatestUserText:      "add a retry to the fetch helper",
		MessageCount:        12,
		HistoryWordEstimate: 3400,
		HasTools:            true,
		HasCodeFence:        true,
		HasImages:           false,
		ModelRequested:      "router:smart-router",
	})
	if st.Metadata.MessageCount != 12 {
		t.Errorf("message_count = %d, want 12", st.Metadata.MessageCount)
	}
	if st.Metadata.HistoryWordEstimate != 3400 {
		t.Errorf("history_word_estimate = %d, want 3400", st.Metadata.HistoryWordEstimate)
	}
	if !st.Metadata.HasTools {
		t.Error("has_tools = false, want true")
	}
	if !st.Metadata.HasCodeFence {
		t.Error("has_code_fence = false, want true")
	}
	if st.Metadata.HasImages {
		t.Error("has_images = true, want false")
	}
	if st.Metadata.ModelRequested != "router:smart-router" {
		t.Errorf("model_requested = %q", st.Metadata.ModelRequested)
	}
}

func TestBuildStateEmptyUserTextIsValid(t *testing.T) {
	st := BuildState(StateInput{MessageCount: 0})
	if st.LatestUserMessage != "" {
		t.Errorf("latest_user_message = %q, want empty", st.LatestUserMessage)
	}
	if _, err := json.Marshal(st); err != nil {
		t.Fatalf("state must marshal: %v", err)
	}
}

func TestTierQuestionCoversAllFourTiers(t *testing.T) {
	q := TierQuestion()
	if q.Type != "choice" {
		t.Errorf("type = %q, want choice", q.Type)
	}
	for _, tier := range []string{"simple", "medium", "complex", "reasoning"} {
		if _, ok := q.Criteria[tier]; !ok {
			t.Errorf("criteria missing tier %q", tier)
		}
	}
	if strings.TrimSpace(q.Instructions) == "" {
		t.Error("instructions must not be empty")
	}
}

func TestQuestionHashIsStableAndSensitive(t *testing.T) {
	if QuestionHash() != QuestionHash() {
		t.Error("QuestionHash must be deterministic")
	}
	if QuestionHash() == "" {
		t.Error("QuestionHash must not be empty")
	}
}
