package plugini18n

import (
	"net/http"
	"net/url"
	"reflect"
	"testing"
)

func TestPreferredLocalesPrioritizesQueryHeaderAndAcceptLanguage(t *testing.T) {
	t.Parallel()

	headers := http.Header{}
	headers.Set(LocaleHeader, "en")
	headers.Set("Accept-Language", "zh-TW;q=0.8, ru;q=0.9")
	query := url.Values{LocaleQuery: {"zh-CN"}}

	got := PreferredLocales(headers, query)
	want := []string{"zh-CN", "en", "ru", "zh-TW"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PreferredLocales() = %#v, want %#v", got, want)
	}
}

func TestLocaleFromRequestUsesQueryHeaderAcceptLanguagePrecedence(t *testing.T) {
	t.Parallel()

	headers := http.Header{}
	headers.Set(LocaleHeader, "ZH_cn")
	headers.Set("Accept-Language", "en;q=0.9, zh-CN;q=0.8")
	query := url.Values{LocaleQuery: {"en-US"}}

	if got := LocaleFromRequest(headers, query); got != "en-US" {
		t.Fatalf("LocaleFromRequest() = %q, want en-US", got)
	}
	got := PreferredLocales(headers, query)
	want := []string{"en-US", "ZH-cn", "en"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PreferredLocales() = %#v, want %#v", got, want)
	}
}

func TestPreferredLocalesFiltersInvalidAcceptLanguageEntries(t *testing.T) {
	t.Parallel()

	headers := http.Header{}
	headers.Add("Accept-Language", "*, , zh;q=0, en;q=abc, de;q=1.5, ja;q=-0.1, it;q=NaN, es;q=0.5, fr")

	got := PreferredLocales(headers, nil)
	want := []string{"fr", "es"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PreferredLocales() = %#v, want %#v", got, want)
	}
}

func TestLookupMatchesExactCaseInsensitiveAndLanguageFallback(t *testing.T) {
	t.Parallel()

	values := map[string]string{
		"en-US": "American English",
		"zh":    "Chinese",
	}
	if got, ok := Lookup(values, "en-us"); !ok || got != "American English" {
		t.Fatalf("Lookup(en-us) = %q/%v, want exact match", got, ok)
	}
	if got, ok := Lookup(values, "zh-CN"); !ok || got != "Chinese" {
		t.Fatalf("Lookup(zh-CN) = %q/%v, want language fallback", got, ok)
	}
	if got, matchedLocale, ok := LookupMatch(values, "fr", "zh-CN"); !ok || got != "Chinese" || matchedLocale != "zh-cn" {
		t.Fatalf("LookupMatch(fr, zh-CN) = %q/%q/%v, want Chinese/zh-cn/true", got, matchedLocale, ok)
	}
	if got, matchedLocale, ok := LookupMatch(values, "ZH_cn"); !ok || got != "Chinese" || matchedLocale != "zh-cn" {
		t.Fatalf("LookupMatch(ZH_cn) = %q/%q/%v, want Chinese/zh-cn/true", got, matchedLocale, ok)
	}
}

func TestLookupFallsBackThroughIntermediateLocaleSubtags(t *testing.T) {
	t.Parallel()

	values := map[string]string{
		"zh":      "Chinese",
		"zh-Hant": "Traditional Chinese",
	}

	got, matchedLocale, ok := LookupMatch(values, "zh-Hant-HK")
	if !ok || got != "Traditional Chinese" || matchedLocale != "zh-hant-hk" {
		t.Fatalf("LookupMatch(zh-Hant-HK) = %q/%q/%v, want Traditional Chinese/zh-hant-hk/true", got, matchedLocale, ok)
	}
}

func TestAppendLocalePreservesExistingQuery(t *testing.T) {
	t.Parallel()

	got := AppendLocale("/v0/resource/plugins/demo/page?mode=full", "zh-CN")
	want := "/v0/resource/plugins/demo/page?locale=zh-CN&mode=full"
	if got != want {
		t.Fatalf("AppendLocale() = %q, want %q", got, want)
	}
	if gotExisting := AppendLocale("/page?locale=en", "zh-CN"); gotExisting != "/page?locale=en" {
		t.Fatalf("AppendLocale() existing locale = %q, want unchanged", gotExisting)
	}
}
