package plugini18n

import (
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

const (
	LocaleHeader = "X-Locale"
	LocaleQuery  = "locale"
)

type acceptLanguageEntry struct {
	value string
	q     float64
	order int
}

func LocaleFromRequest(headers http.Header, query url.Values) string {
	locales := PreferredLocales(headers, query)
	if len(locales) == 0 {
		return ""
	}
	return locales[0]
}

func PreferredLocales(headers http.Header, query url.Values) []string {
	out := make([]string, 0, 4)
	seen := map[string]struct{}{}
	add := func(value string) {
		value = NormalizeLocale(value)
		if value == "" || value == "*" {
			return
		}
		key := localeKey(value)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}

	if query != nil {
		add(query.Get(LocaleQuery))
	}
	if headers != nil {
		add(headers.Get(LocaleHeader))
		for _, value := range parseAcceptLanguage(headers.Values("Accept-Language")) {
			add(value)
		}
	}
	return out
}

func NormalizeLocale(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.ReplaceAll(value, "_", "-")
	if strings.ContainsAny(value, " \t\r\n") {
		return ""
	}
	return value
}

func Lookup[T any](values map[string]T, locales ...string) (T, bool) {
	value, _, ok := LookupMatch(values, locales...)
	return value, ok
}

func LookupMatch[T any](values map[string]T, locales ...string) (T, string, bool) {
	var zero T
	if len(values) == 0 {
		return zero, "", false
	}
	normalized := make(map[string]T, len(values))
	for key, value := range values {
		key = localeKey(key)
		if key == "" {
			continue
		}
		normalized[key] = value
	}
	for _, locale := range locales {
		requested := localeKey(locale)
		for _, candidate := range localeCandidates(locale) {
			if value, ok := normalized[candidate]; ok {
				return value, requested, true
			}
		}
	}
	return zero, "", false
}

func SelectString(base string, values map[string]string, locales ...string) string {
	if value, ok := Lookup(values, locales...); ok && strings.TrimSpace(value) != "" {
		return value
	}
	return base
}

func AppendLocale(rawURL string, locale string) string {
	rawURL = strings.TrimSpace(rawURL)
	locale = NormalizeLocale(locale)
	if rawURL == "" || locale == "" {
		return rawURL
	}
	parsed, errParse := url.Parse(rawURL)
	if errParse != nil {
		return rawURL
	}
	query := parsed.Query()
	if strings.TrimSpace(query.Get(LocaleQuery)) == "" {
		query.Set(LocaleQuery, locale)
		parsed.RawQuery = query.Encode()
	}
	return parsed.String()
}

func parseAcceptLanguage(values []string) []string {
	entries := make([]acceptLanguageEntry, 0)
	order := 0
	for _, header := range values {
		for _, part := range strings.Split(header, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			locale, q := parseAcceptLanguagePart(part)
			if locale == "" || q <= 0 {
				continue
			}
			entries = append(entries, acceptLanguageEntry{value: locale, q: q, order: order})
			order++
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].q == entries[j].q {
			return entries[i].order < entries[j].order
		}
		return entries[i].q > entries[j].q
	})
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.value)
	}
	return out
}

func parseAcceptLanguagePart(value string) (string, float64) {
	parts := strings.Split(value, ";")
	locale := NormalizeLocale(parts[0])
	q := 1.0
	for _, part := range parts[1:] {
		keyValue := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(keyValue) != 2 || strings.TrimSpace(keyValue[0]) != "q" {
			continue
		}
		parsed, errParse := strconv.ParseFloat(strings.TrimSpace(keyValue[1]), 64)
		if errParse != nil || math.IsNaN(parsed) || parsed < 0 || parsed > 1 {
			return locale, 0
		}
		q = parsed
	}
	return locale, q
}

func localeCandidates(locale string) []string {
	locale = localeKey(locale)
	if locale == "" {
		return nil
	}
	out := []string{locale}
	if index := strings.Index(locale, "-"); index > 0 {
		out = append(out, locale[:index])
	}
	return out
}

func localeKey(value string) string {
	return strings.ToLower(NormalizeLocale(value))
}
