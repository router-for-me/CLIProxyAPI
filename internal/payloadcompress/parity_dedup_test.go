package payloadcompress

import (
	"strings"
	"testing"
)

// dedupBlock is a suffix block that clears the default size floor.
const dedupBlock = "alpha line one with enough padding to pass the minimum size\n" +
	"bravo line two with enough padding to pass the minimum size\n" +
	"charlie line three with enough padding to pass the minimum size\n" +
	"delta line four with enough padding to pass the minimum size"

// Upstream session-dedup scans every non-system message, so an assistant message
// repeated in an earlier user turn must be elided too.
func TestDedupIncludesAssistantMessage(t *testing.T) {
	texts := []string{
		"Earlier user turn:\n" + dedupBlock,
		"Assistant answer:\n" + dedupBlock,
	}
	kinds := []segmentKind{kindUser, kindAssistant}

	got := (&dedupEngine{}).rewrite(texts, kinds, nil)
	if got == nil {
		t.Fatal("dedup should elide an assistant block already seen in a user turn")
	}
	if _, ok := got[0]; ok {
		t.Fatal("the first copy must stay intact")
	}
	out, ok := got[1]
	if !ok {
		t.Fatal("assistant segment was not rewritten")
	}
	want := "Assistant answer:\n" + dedupRefPrefix
	if !strings.HasPrefix(out, want) {
		t.Fatalf("assistant segment not replaced: %q", out)
	}
	if strings.Contains(out, dedupBlock) {
		t.Fatal("assistant segment still holds the original block")
	}
}

// System prompts are excluded from ownership and rewriting: they must never be
// touched, and they must not make a later user message look like a duplicate.
func TestDedupLeavesSystemMessageUntouched(t *testing.T) {
	first := []string{"User turn:\n" + dedupBlock, "System prompt:\n" + dedupBlock}
	if got := (&dedupEngine{}).rewrite(first, []segmentKind{kindUser, kindSystem}, nil); got != nil {
		t.Fatalf("system message was rewritten or owned: %v", got)
	}

	second := []string{"System prompt:\n" + dedupBlock, "User turn:\n" + dedupBlock}
	if got := (&dedupEngine{}).rewrite(second, []segmentKind{kindSystem, kindUser}, nil); got != nil {
		t.Fatalf("system message was used as an owner: %v", got)
	}
}

// Upstream pass 2 replaces the first occurrence of the longest duplicate block in
// the message, not necessarily the suffix occurrence.
func TestDedupReplacesFirstOccurrence(t *testing.T) {
	texts := []string{
		"Earlier turn:\n" + dedupBlock,
		"Lead in:\n" + dedupBlock + "\nMIDDLE\n" + dedupBlock,
	}
	kinds := []segmentKind{kindUser, kindUser}

	got := (&dedupEngine{}).rewrite(texts, kinds, nil)
	out, ok := got[1]
	if !ok {
		t.Fatal("duplicate block was not replaced")
	}
	wantPrefix := "Lead in:\n" + dedupRefPrefix
	if !strings.HasPrefix(out, wantPrefix) {
		t.Fatalf("first occurrence was not replaced: %q", out)
	}
	if !strings.HasSuffix(out, "\nMIDDLE\n"+dedupBlock) {
		t.Fatal("the suffix copy must stay intact")
	}
}
