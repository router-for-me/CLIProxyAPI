// Package jevgate adds a semantic, calibrated tier classifier in front of the
// Auto Router's lexical heuristic. The heuristic always runs and is the
// guaranteed path; this package may override its tier when the classifier's
// confidence clears an operator-set floor. Every failure mode — timeout, bad
// status, unparseable body, missing key — falls back to the heuristic, so the
// gate can never fail a request.
package jevgate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/autorouter/jevclient"
)

// MaxStateChars bounds the latest-user-message text sent to the classifier.
// ~6000 characters is roughly 1.5k tokens: about $0.00006 per uncached request
// at the documented $0.042/Mtok input price, and far below the API's 32k state
// limit.
const MaxStateChars = 6000

// QuestionID is the key the tier question is sent under (and read back from).
const QuestionID = "tier"

// State is the payload sent to the classifier: the latest user turn plus
// derived metadata. System prompts and message history are deliberately
// excluded — the last turn carries the strongest complexity signal, and
// excluding them keeps token cost and privacy exposure to one turn.
type State struct {
	LatestUserMessage string   `json:"latest_user_message"`
	Metadata          Metadata `json:"metadata"`
}

// Metadata is the secondary evidence sent alongside the user turn.
type Metadata struct {
	MessageCount        int    `json:"message_count"`
	HistoryWordEstimate int    `json:"history_word_estimate"`
	HasTools            bool   `json:"has_tools"`
	HasCodeFence        bool   `json:"has_code_fence"`
	HasImages           bool   `json:"has_images"`
	ModelRequested      string `json:"model_requested"`
}

// StateInput is the flat input BuildState consumes, so the caller does not have
// to construct the nested shape.
type StateInput struct {
	LatestUserText      string
	MessageCount        int
	HistoryWordEstimate int
	HasTools            bool
	HasCodeFence        bool
	HasImages           bool
	ModelRequested      string
}

// BuildState assembles the classifier state, truncating the user turn to
// MaxStateChars. Truncation is byte-based; the metadata fields pass through
// unchanged.
func BuildState(in StateInput) State {
	text := in.LatestUserText
	if len(text) > MaxStateChars {
		text = text[:MaxStateChars]
	}
	return State{
		LatestUserMessage: text,
		Metadata: Metadata{
			MessageCount:        in.MessageCount,
			HistoryWordEstimate: in.HistoryWordEstimate,
			HasTools:            in.HasTools,
			HasCodeFence:        in.HasCodeFence,
			HasImages:           in.HasImages,
			ModelRequested:      in.ModelRequested,
		},
	}
}

// TierQuestion returns the single Choice question used for classification. The
// criteria restate the router's existing four-tier rubric rather than inventing
// a parallel taxonomy, which is what lets the gate drop in without touching any
// tier-to-model mapping.
func TierQuestion() jevclient.Question {
	return jevclient.Question{
		Type: "choice",
		Instructions: "Classify the LLM request by the cheapest model tier that can answer it well. " +
			"Consider the latest user message primarily; metadata is secondary evidence.",
		Criteria: map[string]any{
			"simple":    "Greetings, chitchat, one-line factual lookups, trivial rewrites of short text.",
			"medium":    "Summaries, translations, short Q&A, simple code edits on small snippets, single-step tasks.",
			"complex":   "Multi-file or multi-step coding, debugging with context, longer document analysis, agent tool-use turns.",
			"reasoning": "Requires deep step-by-step reasoning: proofs, algorithm analysis, architecture trade-offs, math, planning.",
		},
	}
}

// QuestionHash is the question definition's identity. It participates in the
// cache key, so editing the instructions or criteria invalidates cached verdicts
// automatically. encoding/json sorts map keys, so this is deterministic.
func QuestionHash() string {
	raw, errMarshal := json.Marshal(TierQuestion())
	if errMarshal != nil {
		// Unreachable for this fixed literal shape; an empty hash would disable
		// cache invalidation, so surface it rather than hiding it.
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
