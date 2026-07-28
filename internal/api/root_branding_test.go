package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// TestHasBranding verifies the fallback gate: the root handler returns the
// legacy JSON envelope only when no branding field is set. Whitespace-only
// values must count as unset so operators can't accidentally flip the root
// to an HTML page by entering stray spaces.
func TestHasBranding(t *testing.T) {
	cases := []struct {
		name string
		b    config.Branding
		want bool
	}{
		{name: "zero", b: config.Branding{}, want: false},
		{name: "whitespace only", b: config.Branding{Title: "   ", Message: "\t", Footer: " ", ThreadsURL: "  "}, want: false},
		{name: "title set", b: config.Branding{Title: "Acme"}, want: true},
		{name: "message set", b: config.Branding{Message: "hi"}, want: true},
		{name: "logo set", b: config.Branding{LogoURL: "https://x/y.png"}, want: true},
		{name: "footer set", b: config.Branding{Footer: "admin@x"}, want: true},
		{name: "threads set", b: config.Branding{ThreadsURL: "https://threads.net/@x"}, want: true},
		{name: "whatsapp set", b: config.Branding{WhatsAppURL: "https://wa.me/1234"}, want: true},
		{name: "telegram set", b: config.Branding{TelegramURL: "https://t.me/x"}, want: true},
		{name: "background color set", b: config.Branding{BackgroundColor: "#000000"}, want: true},
		{name: "title color set", b: config.Branding{TitleColor: "#fff"}, want: true},
		{name: "message color set", b: config.Branding{MessageColor: "#abcdef"}, want: true},
		{name: "footer color set", b: config.Branding{FooterColor: "  #123456  "}, want: true},
		{name: "invalid color still counts as set", b: config.Branding{TitleColor: "red", MessageColor: "#gggggg"}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasBranding(tc.b); got != tc.want {
				t.Fatalf("hasBranding(%+v) = %v, want %v", tc.b, got, tc.want)
			}
		})
	}
}

// TestRenderBrandingPage asserts the HTML page is emitted with the expected
// fields interpolated, that the logo <img> uses the safe URL, and that
// non-http(s) logo URLs are omitted entirely from the output.
func TestRenderBrandingPage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/", nil)

	renderBrandingPage(c, config.Branding{
		Title:       "Acme <LLM>",
		Message:     "Authorized access\nonly.",
		LogoURL:     "javascript:alert(1)",
		Footer:      "admin@example.com",
		ThreadsURL:  "javascript:alert(2)", // must be dropped
		TelegramURL: "https://t.me/acme",   // must render as link
	})

	body := w.Body.String()
	if !strings.Contains(body, "Acme &lt;LLM&gt;") {
		t.Errorf("expected HTML-escaped title in body, got:\n%s", body)
	}
	if !strings.Contains(body, "Authorized access\nonly.") {
		t.Errorf("expected message in body, got:\n%s", body)
	}
	if !strings.Contains(body, "admin@example.com") {
		t.Errorf("expected footer in body, got:\n%s", body)
	}

	// Malicious http(s) logo / threads URLs must NOT appear in the output.
	if strings.Contains(body, "javascript:alert(1)") {
		t.Errorf("non-http(s) logo URL leaked into rendered HTML:\n%s", body)
	}
	if strings.Contains(body, "javascript:alert(2)") {
		t.Errorf("non-http(s) threads URL leaked into rendered HTML:\n%s", body)
	}

	// The safe telegram URL must be rendered as a target=_blank link with the
	// rel attributes protecting against reverse tabnabbing.
	if !strings.Contains(body, `href="https://t.me/acme"`) {
		t.Errorf("expected telegram link in body, got:\n%s", body)
	}
	if !strings.Contains(body, `target="_blank" rel="noopener noreferrer"`) {
		t.Errorf("expected safe rel attributes on social link, got:\n%s", body)
	}
	if !strings.Contains(body, `aria-label="Telegram"`) {
		t.Errorf("expected accessible label for telegram link, got:\n%s", body)
	}

	if !strings.Contains(w.Header().Get("Content-Type"), "text/html") {
		t.Errorf("expected text/html content-type, got %q", w.Header().Get("Content-Type"))
	}
}

// TestRenderBrandingPage_HttpLogo ensures a valid http logo URL is rendered
// as an <img> with referrerpolicy=no-referrer.
func TestRenderBrandingPage_HttpLogo(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/", nil)

	renderBrandingPage(c, config.Branding{
		Title:   "T",
		LogoURL: "https://example.com/logo.png",
	})
	body := w.Body.String()
	if !strings.Contains(body, `<img class="logo" src="https://example.com/logo.png"`) {
		t.Errorf("expected logo <img> tag with safe URL, got:\n%s", body)
	}
	if !strings.Contains(body, `referrerpolicy="no-referrer"`) {
		t.Errorf("expected referrerpolicy=no-referrer on logo img, got:\n%s", body)
	}
}

// TestRenderBrandingPage_SocialIcons verifies each social network produces its
// expected inline SVG marker so all three icons actually paint.
func TestRenderBrandingPage_SocialIcons(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/", nil)

	renderBrandingPage(c, config.Branding{
		Title:       "T",
		ThreadsURL:  "https://threads.net/@acme",
		WhatsAppURL: "https://wa.me/15551234",
		TelegramURL: "https://t.me/acme",
	})
	body := w.Body.String()
	// Each SVG carries an aria-hidden marker, so we count occurrences: every
	// social icon should emit exactly one aria-hidden="true" span.
	if got := strings.Count(body, `aria-hidden="true"`); got != 3 {
		t.Errorf("expected 3 social SVGs (one aria-hidden each), got %d in:\n%s", got, body)
	}
	for _, want := range []string{"https://threads.net/@acme", "https://wa.me/15551234", "https://t.me/acme"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q link in body, got:\n%s", want, body)
		}
	}
}

// TestRenderBrandingPage_Colors verifies valid hex colors are interpolated
// into the rendered <style> block, while invalid values silently fall back
// to the built-in defaults (so an operator pasting "red" or "#zzz" cannot
// break the page or inject CSS).
func TestRenderBrandingPage_Colors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/", nil)

	renderBrandingPage(c, config.Branding{
		Title:           "T",
		BackgroundColor: "#1a2b3c",
		TitleColor:      "#ff00aa",
		MessageColor:    "#112233",
		// Invalid hex must fall back to the default footer color.
		FooterColor: "not-a-color",
	})
	body := w.Body.String()
	for _, want := range []string{
		"background: #1a2b3c;",
		`color: #ff00aa;`,
		"color: #112233;",
		// Default footer color when the configured value is invalid.
		"color: #6b7785;",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in rendered CSS, got:\n%s", want, body)
		}
	}
	// The invalid value itself must NOT appear in the output.
	if strings.Contains(body, "not-a-color") {
		t.Errorf("invalid footer color leaked into rendered HTML:\n%s", body)
	}
}

// TestRenderBrandingPage_EmailFooter verifies that when the operator's Footer
// value parses as a single email address, it is rendered only as a mailto:
// envelope icon in the socials row — NOT also as plain text (which would
// duplicate the address). Non-email footer content keeps rendering as text.
func TestRenderBrandingPage_EmailFooter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/", nil)

	renderBrandingPage(c, config.Branding{
		Title:  "T",
		Footer: "admin@example.com", // email → should become an envelope icon
	})
	body := w.Body.String()

	// The envelope icon mailto link must be present.
	if !strings.Contains(body, `href="mailto:admin@example.com"`) {
		t.Errorf("expected mailto: envelope link in body, got:\n%s", body)
	}
	if !strings.Contains(body, `aria-label="Email"`) {
		t.Errorf("expected Email aria-label on mailto link, got:\n%s", body)
	}
	// The address must NOT also be rendered as plain footer text (would dupe).
	if strings.Contains(body, `<p class="footer">admin@example.com</p>`) {
		t.Errorf("email footer leaked as plain text while also rendered as icon:\n%s", body)
	}
}

// TestRenderBrandingPage_NonEmailFooter verifies free-text footer content
// (copyright lines, notices) keeps rendering as plain text, not as an icon.
func TestRenderBrandingPage_NonEmailFooter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/", nil)

	renderBrandingPage(c, config.Branding{
		Title:  "T",
		Footer: "© 2026 Acme — all rights reserved",
	})
	body := w.Body.String()
	if !strings.Contains(body, `<p class="footer">`) {
		t.Errorf("expected plain footer paragraph for non-email footer, got:\n%s", body)
	}
	if strings.Contains(body, "mailto:") {
		t.Errorf("non-email footer should not produce a mailto link:\n%s", body)
	}
}
