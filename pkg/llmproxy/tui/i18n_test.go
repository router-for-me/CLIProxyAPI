package tui

import "testing"

func TestLocaleKeyParity(t *testing.T) {
	t.Cleanup(func() {
		SetLocale("en")
	})

	required := []string{"zh", "en", "fa"}
	base := locales["en"]
	if len(base) == 0 {
		t.Fatal("en locale is empty")
	}

	for _, code := range required {
		loc, ok := locales[code]
		if !ok {
			t.Fatalf("missing locale: %s", code)
		}
		if len(loc) != len(base) {
			t.Fatalf("locale %s key count mismatch: got=%d want=%d", code, len(loc), len(base))
		}
		for key := range base {
			if _, exists := loc[key]; !exists {
				t.Fatalf("locale %s missing key: %s", code, key)
			}
		}
	}
}

func TestTabNameParity(t *testing.T) {
	if len(zhTabNames) != len(enTabNames) {
		t.Fatalf("zh/en tab name count mismatch: got zh=%d en=%d", len(zhTabNames), len(enTabNames))
	}
	if len(faTabNames) != len(enTabNames) {
		t.Fatalf("fa/en tab name count mismatch: got fa=%d en=%d", len(faTabNames), len(enTabNames))
	}
}

func TestToggleLocaleCyclesAllLanguages(t *testing.T) {
	t.Cleanup(func() {
		SetLocale("en")
	})

	SetLocale("en")
	ToggleLocale()
	if CurrentLocale() != "zh" {
		t.Fatalf("expected zh after first toggle, got %s", CurrentLocale())
	}
	ToggleLocale()
	if CurrentLocale() != "fa" {
		t.Fatalf("expected fa after second toggle, got %s", CurrentLocale())
	}
	ToggleLocale()
	if CurrentLocale() != "en" {
		t.Fatalf("expected en after third toggle, got %s", CurrentLocale())
	}
}
