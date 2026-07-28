package config

import (
	"net/mail"
	"net/url"
	"strings"
)

// trimSpace removes leading/trailing whitespace. Trivial wrapper kept here so
// the Branding helpers in config_types.go stay self-contained for tests.
func trimSpace(s string) string { return strings.TrimSpace(s) }

// toLower returns s lowercased. Trivial wrapper, same motivation as trimSpace.
func toLower(s string) string { return strings.ToLower(s) }

// urlParse parses raw into *url.URL using net/url. Wrapped so the Branding
// helpers can reference it without config_types.go importing net/url directly.
func urlParse(raw string) (*url.URL, error) { return url.Parse(raw) }

// isValidHexColor reports whether s is a CSS hex color of the form #rgb or
// #rrggbb (case-insensitive). Used to guard every operator-supplied color
// field before it is emitted into the rendered HTML page; invalid values
// silently fall back to the built-in defaults.
func isValidHexColor(s string) bool {
	if len(s) != 4 && len(s) != 7 {
		return false
	}
	if s[0] != '#' {
		return false
	}
	for _, r := range s[1:] {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

// hexColorOr returns trimmed s when it is a valid hex color, else fallback.
// Used by the HTML renderer to resolve each color field with a safe default.
func hexColorOr(s, fallback string) string {
	trimmed := trimSpace(s)
	if isValidHexColor(trimmed) {
		return trimmed
	}
	return fallback
}

// BackgroundColorOr returns BackgroundColor when valid, else the page default.
func (b Branding) BackgroundColorOr() string { return hexColorOr(b.BackgroundColor, "#0b0f14") }

// TitleColorOr returns TitleColor when valid, else the accent default.
func (b Branding) TitleColorOr() string { return hexColorOr(b.TitleColor, "#5eead4") }

// MessageColorOr returns MessageColor when valid, else the body default.
func (b Branding) MessageColorOr() string { return hexColorOr(b.MessageColor, "#c9d4e0") }

// FooterColorOr returns FooterColor when valid, else the muted default.
func (b Branding) FooterColorOr() string { return hexColorOr(b.FooterColor, "#6b7785") }

// MailLink returns the operator's Footer value when it parses as a single RFC
// 5322 email address (e.g. "admin@example.com"). In that case the HTML
// renderer surfaces an envelope icon linking to mailto:<address> in the
// footer icon row, instead of (or in addition to) printing the raw Footer
// text. Returns "" when the Footer is not a clean email so non-email footer
// content (copyright lines, free-text notices) keeps rendering as plain text.
func (b Branding) MailLink() string {
	trimmed := trimSpace(b.Footer)
	if trimmed == "" {
		return ""
	}
	addr, err := mail.ParseAddress(trimmed)
	if err != nil {
		return ""
	}
	// mail.ParseAddress accepts "Name <addr@x>" forms; only treat the bare
	// address as a mailto link to avoid surprising the operator with display
	// semantics they did not ask for.
	if addr.Name != "" {
		return ""
	}
	return addr.Address
}
