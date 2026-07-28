package api

import (
	"html/template"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// brandingPageHTML is the single-shot template rendered at GET / when any
// branding field is configured. It mirrors the dashboard's dark-tech look
// (accent #5eead4, monospace heading) and stays self-contained (no external
// CSS/JS/assets) so it works on cold start without network access.
//
// All interpolated values are auto-escaped by html/template. The logo URL and
// every social URL are additionally restricted to http/https schemes before
// being passed in (see config.Branding.LogoURLSafe / SocialLinks). Color
// values are validated as strict hex (#rgb | #rrggbb) and fall back to the
// built-in defaults when missing or invalid (see config.Branding.*ColorOr).
//
// brandFooter renders either: the footer text line (when set), the social icon
// row (when at least one social link is present), or both. Each social link is
// emitted as <a href=URL target=_blank rel=noopener noreferrer><svg/></a>.
const brandingPageHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
  :root { color-scheme: dark; }
  * { box-sizing: border-box; }
  html, body { height: 100%; margin: 0; }
  body {
    background: {{.BackgroundColor}};
    color: #e6edf3;
    font: 15px/1.55 -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
    display: flex; align-items: center; justify-content: center;
    padding: 32px 20px;
  }
  .card {
    width: 100%; max-width: 560px;
    background: #11161d; border: 1px solid #1f2733; border-radius: 14px;
    padding: 36px 32px; text-align: center;
    box-shadow: 0 18px 48px rgba(0,0,0,.45);
  }
  .logo { max-width: 88px; max-height: 88px; margin: 0 auto 18px; display: block; border-radius: 12px; }
  h1 {
    font-family: "JetBrains Mono", "SFMono-Regular", ui-monospace, Menlo, Consolas, monospace;
    font-size: 1.6rem; margin: 0 0 10px; letter-spacing: -.01em; color: {{.TitleColor}};
  }
  .message { white-space: pre-wrap; color: {{.MessageColor}}; margin: 0 0 20px; }
  .footer { color: {{.FooterColor}}; font-size: .85rem; margin-top: 22px; }
  .rule { width: 44px; height: 2px; background: #1f2733; margin: 18px auto; border: 0; }
  .socials {
    display: flex; gap: 20px; justify-content: center; align-items: center;
    margin-top: 22px;
  }
  .socials a {
    color: {{.FooterColor}}; transition: color .12s ease; line-height: 0; display: inline-flex;
  }
  .socials a:hover { color: {{.TitleColor}}; }
  @media (prefers-reduced-motion: reduce) { .socials a { transition: none; } }
</style>
</head>
<body>
  <main class="card">
    {{if .LogoURL}}<img class="logo" src="{{.LogoURL}}" alt="" referrerpolicy="no-referrer">{{end}}
    {{if .Title}}<h1>{{.Title}}</h1>{{end}}
    {{if or .Title .LogoURL}}<hr class="rule">{{end}}
    {{if .Message}}<p class="message">{{.Message}}</p>{{end}}
    {{if .FooterText}}<p class="footer">{{.FooterText}}</p>{{end}}
    {{if or .Socials .MailLink}}
    <div class="socials">
      {{range .Socials}}
      <a href="{{.URL}}" target="_blank" rel="noopener noreferrer" aria-label="{{.Label}}" title="{{.Label}}">{{iconForSocial .Key}}</a>
      {{end}}
      {{if .MailLink}}<a href="mailto:{{.MailLink}}" aria-label="Email" title="{{.MailLink}}">{{iconForSocial "mail"}}</a>{{end}}
    </div>
    {{end}}
  </main>
</body>
</html>`

// brandingIcons holds the inline SVG for each supported footer link. Keys
// match the Key field returned by config.Branding.SocialLinks (threads /
// whatsapp / telegram). The "mail" key is used when the operator's Footer
// value parses as a single email address; it links via mailto:.
//
// Social glyphs come from simple-icons (designed for fill="currentColor",
// viewBox 0 0 24 24) so they reproduce each brand's official silhouette. The
// envelope is a stroke-based glyph to match the line-style affordance used
// elsewhere in the page.
var brandingIcons = map[string]string{
	// Threads (brand-accurate glyph, simple-icons).
	"threads": `<svg width="24" height="24" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d="M18.263 11.097c-.03-3.486-1.92-5.586-5.111-5.586-2.13 0-3.922.963-4.863 2.499l2.062 1.438c.535-.843 1.272-1.543 2.628-1.543 1.528 0 2.318.85 2.544 2.431a15 15 0 0 0-2.236-.173c-4.125 0-6.068 1.867-6.068 4.336s1.943 3.99 4.804 3.99c3.139 0 5.013-2.115 5.781-4.735.798.361 1.348 1.204 1.348 2.47 0 3.387-3.907 5.232-7.22 5.232-4.885 0-8.077-3.207-8.077-8.424 0-6.392 4.223-10.487 9.9-10.487 3.808 0 5.69 1.671 6.97 3.914l2.108-1.475C21.44 2.078 18.331 0 13.663 0 6.227 0 1.168 5.277 1.168 12.934c0 7 4.953 11.066 10.856 11.066 4.878 0 9.809-2.846 9.809-7.716 0-2.545-1.46-4.231-3.569-5.187m-6.33 4.855c-1.077 0-2.026-.512-2.026-1.453 0-1.483 1.822-1.934 3.606-1.934.678 0 1.34.045 1.927.173-.422 1.927-1.671 3.215-3.508 3.214Z"/></svg>`,
	// WhatsApp (brand-accurate glyph, simple-icons).
	"whatsapp": `<svg width="24" height="24" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d="M17.472 14.382c-.297-.149-1.758-.867-2.03-.967-.273-.099-.471-.148-.67.15-.197.297-.767.966-.94 1.164-.173.199-.347.223-.644.075-.297-.15-1.255-.463-2.39-1.475-.883-.788-1.48-1.761-1.653-2.059-.173-.297-.018-.458.13-.606.134-.133.298-.347.446-.52.149-.174.198-.298.298-.497.099-.198.05-.371-.025-.52-.075-.149-.669-1.612-.916-2.207-.242-.579-.487-.5-.669-.51-.173-.008-.371-.01-.57-.01-.198 0-.52.074-.792.372-.272.297-1.04 1.016-1.04 2.479 0 1.462 1.065 2.875 1.213 3.074.149.198 2.096 3.2 5.077 4.487.709.306 1.262.489 1.694.625.712.227 1.36.195 1.871.118.571-.085 1.758-.719 2.006-1.413.248-.694.248-1.289.173-1.413-.074-.124-.272-.198-.57-.347m-5.421 7.403h-.004a9.87 9.87 0 01-5.031-1.378l-.361-.214-3.741.982.998-3.648-.235-.374a9.86 9.86 0 01-1.51-5.26c.001-5.45 4.436-9.884 9.888-9.884 2.64 0 5.122 1.03 6.988 2.898a9.825 9.825 0 012.893 6.994c-.003 5.45-4.437 9.884-9.885 9.884m8.413-18.297A11.815 11.815 0 0012.05 0C5.495 0 .16 5.335.157 11.892c0 2.096.547 4.142 1.588 5.945L.057 24l6.305-1.654a11.882 11.882 0 005.683 1.448h.005c6.554 0 11.89-5.335 11.893-11.893a11.821 11.821 0 00-3.48-8.413Z"/></svg>`,
	// Telegram (brand-accurate glyph, simple-icons).
	"telegram": `<svg width="24" height="24" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d="M11.944 0A12 12 0 0 0 0 12a12 12 0 0 0 12 12 12 12 0 0 0 12-12A12 12 0 0 0 12 0a12 12 0 0 0-.056 0zm4.962 7.224c.1-.002.321.023.465.14a.506.506 0 0 1 .171.325c.016.093.036.306.02.472-.18 1.898-.962 6.502-1.36 8.627-.168.9-.499 1.201-.82 1.23-.696.065-1.225-.46-1.9-.902-1.056-.693-1.653-1.124-2.678-1.8-1.185-.78-.417-1.21.258-1.91.177-.184 3.247-2.977 3.307-3.23.007-.032.014-.15-.056-.212s-.174-.041-.249-.024c-.106.024-1.793 1.14-5.061 3.345-.48.33-.913.49-1.302.48-.428-.008-1.252-.241-1.865-.44-.752-.245-1.349-.374-1.297-.789.027-.216.325-.437.893-.663 3.498-1.524 5.83-2.529 6.998-3.014 3.332-1.386 4.025-1.627 4.476-1.635z"/></svg>`,
	// Envelope — used when the operator's Footer parses as a single email
	// address; links via mailto: in the icon row.
	"mail": `<svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="3" y="5" width="18" height="14" rx="2"/><path d="M3 7l9 6 9-6"/></svg>`,
}

func iconForSocial(key string) template.HTML {
	if svg, ok := brandingIcons[key]; ok {
		// SVG markup is trusted (defined in source, not user input). Returning
		// template.HTML skips re-escaping so the inline SVG renders as-is.
		return template.HTML(svg)
	}
	return template.HTML("")
}

var (
	brandingTemplateOnce sync.Once
	brandingTemplate     *template.Template
)

// brandingPageModel is the view model passed to the branding template.
// LogoURL is set to "" when the configured value does not parse as an
// http(s) URL, so the image is silently dropped rather than reflecting a
// potentially untrusted scheme (javascript:, data:, ...). Socials is the
// scheme-gated list from config.Branding.SocialLinks(). Colors are resolved
// via config.Branding.*ColorOr() so invalid/empty hex falls back to defaults.
type brandingPageModel struct {
	Title   string
	Message string
	LogoURL string
	// FooterText is the footer line shown as plain text. It is empty when the
	// Footer value parses as an email — in that case MailLink is set and an
	// envelope icon is rendered in the socials row instead of bare text, so a
	// contact email is presented as a clickable icon rather than duplicated.
	FooterText string
	MailLink   string
	Socials    []struct {
		Label, URL, Key string
	}
	BackgroundColor string
	TitleColor      string
	MessageColor    string
	FooterColor     string
}

func brandingPageTemplate() *template.Template {
	brandingTemplateOnce.Do(func() {
		brandingTemplate = template.Must(template.New("branding").Funcs(template.FuncMap{
			"iconForSocial": iconForSocial,
		}).Parse(brandingPageHTML))
	})
	return brandingTemplate
}

// hasBranding reports whether any branding field is set. When false, the
// legacy JSON root response is returned so default installs behave unchanged.
func hasBranding(b config.Branding) bool {
	return strings.TrimSpace(b.Title) != "" ||
		strings.TrimSpace(b.Message) != "" ||
		strings.TrimSpace(b.LogoURL) != "" ||
		strings.TrimSpace(b.Footer) != "" ||
		strings.TrimSpace(b.ThreadsURL) != "" ||
		strings.TrimSpace(b.WhatsAppURL) != "" ||
		strings.TrimSpace(b.TelegramURL) != "" ||
		strings.TrimSpace(b.BackgroundColor) != "" ||
		strings.TrimSpace(b.TitleColor) != "" ||
		strings.TrimSpace(b.MessageColor) != "" ||
		strings.TrimSpace(b.FooterColor) != ""
}

// renderBrandingPage renders the branding HTML page for the given branding
// configuration. It is exported via the Server so the route handler stays thin.
func renderBrandingPage(c *gin.Context, b config.Branding) {
	footerText := strings.TrimSpace(b.Footer)
	mailLink := b.MailLink()
	// When the footer is an email, surface it only as a mailto: envelope icon
	// in the socials row (not also as plain text) so the address isn't shown
	// twice. Non-email footer content keeps rendering as plain text.
	if mailLink != "" {
		footerText = ""
	}
	model := brandingPageModel{
		Title:           strings.TrimSpace(b.Title),
		Message:         strings.TrimSpace(b.Message),
		LogoURL:         b.LogoURLSafe(),
		FooterText:      footerText,
		MailLink:        mailLink,
		Socials:         b.SocialLinks(),
		BackgroundColor: b.BackgroundColorOr(),
		TitleColor:      b.TitleColorOr(),
		MessageColor:    b.MessageColorOr(),
		FooterColor:     b.FooterColorOr(),
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	c.Status(http.StatusOK)
	_ = brandingPageTemplate().Execute(c.Writer, model)
}
