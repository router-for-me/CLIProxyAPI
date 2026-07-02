package helps

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestNormalizeDatelineText_CanonicalIsIdentity(t *testing.T) {
	// Arrange
	in := "Today's date is 2026-07-01."

	// Act
	out, hits := NormalizeDatelineText(in)

	// Assert
	if out != in {
		t.Fatalf("canonical text should be unchanged, got %q", out)
	}
	if len(hits) != 0 {
		t.Fatalf("canonical text should produce no hits, got %v", hits)
	}
}

func TestNormalizeDatelineText_ApostropheVariantsNormalized(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		apo  string
	}{
		{"curly_u2019", "Today’s date is 2026-07-01.", "Today's date is 2026-07-01.", "u2019"},
		{"modifier_u02bc", "Todayʼs date is 2026-07-01.", "Today's date is 2026-07-01.", "u02bc"},
		{"prime_u02b9", "Todayʹs date is 2026-07-01.", "Today's date is 2026-07-01.", "u02b9"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Act
			out, hits := NormalizeDatelineText(tc.in)

			// Assert
			if out != tc.want {
				t.Fatalf("got %q, want %q", out, tc.want)
			}
			if len(hits) != 1 || hits[0].ApostropheVariant != tc.apo || hits[0].DateSeparator != "-" {
				t.Fatalf("unexpected hits: %+v", hits)
			}
		})
	}
}

func TestNormalizeDatelineText_SlashSeparatorNormalized(t *testing.T) {
	// Arrange
	in := "Today's date is 2026/07/01."

	// Act
	out, hits := NormalizeDatelineText(in)

	// Assert
	if out != "Today's date is 2026-07-01." {
		t.Fatalf("slash separator not normalized: %q", out)
	}
	if len(hits) != 1 || hits[0].DateSeparator != "/" {
		t.Fatalf("unexpected hits: %+v", hits)
	}
}

func TestNormalizeDatelineText_MixedSeparatorNotMatched(t *testing.T) {
	// Arrange: a genuinely inconsistent separator must not match (avoids
	// touching user prose that merely resembles the sentence).
	in := "Today's date is 2026-07/01."

	// Act
	out, hits := NormalizeDatelineText(in)

	// Assert
	if out != in {
		t.Fatalf("mixed separator should be untouched, got %q", out)
	}
	if len(hits) != 0 {
		t.Fatalf("expected no hits, got %v", hits)
	}
}

func TestNormalizeDateline_SystemString(t *testing.T) {
	// Arrange
	body := []byte(`{"system":"You are Claude.\nToday’s date is 2026-07-01.","messages":[]}`)

	// Act
	out, hits, changed := NormalizeDateline(body)

	// Assert
	if !changed || len(hits) != 1 {
		t.Fatalf("expected one change, changed=%v hits=%v", changed, hits)
	}
	got := gjson.GetBytes(out, "system").String()
	if got != "You are Claude.\nToday's date is 2026-07-01." {
		t.Fatalf("system not normalized: %q", got)
	}
}

func TestNormalizeDateline_SystemArrayTextBlock(t *testing.T) {
	// Arrange
	body := []byte(`{"system":[{"type":"text","text":"Today’s date is 2026/07/01."}]}`)

	// Act
	out, _, changed := NormalizeDateline(body)

	// Assert
	if !changed {
		t.Fatalf("expected change in system array text block")
	}
	got := gjson.GetBytes(out, "system.0.text").String()
	if got != "Today's date is 2026-07-01." {
		t.Fatalf("system array text not normalized: %q", got)
	}
}

func TestNormalizeDateline_MessagesScopedToSystemReminder(t *testing.T) {
	// Arrange: the SAME sentence appears twice inside one content string — once
	// inside a <system-reminder> (must normalize) and once in free user prose
	// (must be preserved byte-for-byte).
	body := []byte(`{"messages":[{"role":"user","content":"<system-reminder>Today’s date is 2026/07/01.</system-reminder> and I typed Today’s date is 2026/07/01. myself"}]}`)

	// Act
	out, hits, changed := NormalizeDateline(body)

	// Assert
	if !changed || len(hits) != 1 {
		t.Fatalf("expected exactly one scoped change, changed=%v hits=%v", changed, hits)
	}
	got := gjson.GetBytes(out, "messages.0.content").String()
	want := "<system-reminder>Today's date is 2026-07-01.</system-reminder> and I typed Today’s date is 2026/07/01. myself"
	if got != want {
		t.Fatalf("scoping wrong.\n got: %q\nwant: %q", got, want)
	}
}

func TestNormalizeDateline_EmptyAndNoMatchAreIdentity(t *testing.T) {
	// Arrange
	empty := []byte("")
	noMatch := []byte(`{"system":"nothing to see here","messages":[{"role":"user","content":"hi"}]}`)

	// Act
	e, _, ec := NormalizeDateline(empty)
	n, _, nc := NormalizeDateline(noMatch)

	// Assert
	if ec || len(e) != 0 {
		t.Fatalf("empty body should be identity")
	}
	if nc || string(n) != string(noMatch) {
		t.Fatalf("no-match body should be identity, got %q", n)
	}
}
