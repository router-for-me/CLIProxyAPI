import React, { useEffect, useMemo, useState } from 'react';
import { getBranding, putBranding, ApiError } from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import { useToast } from '../components/Toast.jsx';
import { Spinner, ErrorBanner } from '../components/Primitives.jsx';

// BrandingPage — edit the operator-customizable HTML page served at GET /.
//
// The server returns the legacy JSON root response ({message, endpoints}) when
// every branding field is empty. Setting any field flips GET / to render the
// HTML branding page instead (title + logo + message + email/socials).
//
// All four fields are free-form text. logo-url is rendered server-side as an
// <img> only when its scheme is http(s); non-http(s) values are silently
// dropped by the server, so the dashboard duplicates the check in the live
// preview to set operator expectations.
//
// Mutations go through PUT /v0/management/branding with full-replace semantics
// (the whole object is submitted; PATCH routes to the same handler). The
// server trims every field and persists to config.yaml, then hot-reloads.
export default function BrandingPage() {
  const { data, error, loading, reload } = useAsync(() => getBranding(), []);
  const toast = useToast();

  const loaded = data?.branding || null;
  const [title, setTitle] = useState('');
  const [message, setMessage] = useState('');
  const [logoUrl, setLogoUrl] = useState('');
  const [footer, setFooter] = useState('');
  const [threadsUrl, setThreadsUrl] = useState('');
  const [whatsappUrl, setWhatsappUrl] = useState('');
  const [telegramUrl, setTelegramUrl] = useState('');
  const [backgroundColor, setBackgroundColor] = useState('');
  const [titleColor, setTitleColor] = useState('');
  const [messageColor, setMessageColor] = useState('');
  const [footerColor, setFooterColor] = useState('');
  const [dirty, setDirty] = useState(false);
  const [saving, setSaving] = useState(false);

  // Hydrate the form once the server-persisted branding arrives. Subsequent
  // reloads (e.g. after Save) re-hydrate and clear the dirty flag, so the
  // "Save" button disables until the operator edits again.
  useEffect(() => {
    if (!loaded) return;
    setTitle(loaded.title || '');
    setMessage(loaded.message || '');
    setLogoUrl(loaded['logo-url'] || loaded.logoUrl || '');
    setFooter(loaded.footer || '');
    setThreadsUrl(loaded['threads-url'] || loaded.threadsUrl || '');
    setWhatsappUrl(loaded['whatsapp-url'] || loaded.whatsappUrl || '');
    setTelegramUrl(loaded['telegram-url'] || loaded.telegramUrl || '');
    setBackgroundColor(loaded['background-color'] || loaded.backgroundColor || '');
    setTitleColor(loaded['title-color'] || loaded.titleColor || '');
    setMessageColor(loaded['message-color'] || loaded.messageColor || '');
    setFooterColor(loaded['footer-color'] || loaded.footerColor || '');
    setDirty(false);
  }, [loaded]);

  function markDirty(setter) {
    return (e) => {
      setDirty(true);
      setter(e.target.value);
    };
  }

  function isHttpUrl(raw) {
    try {
      const u = new URL(raw);
      return u.protocol === 'http:' || u.protocol === 'https:';
    } catch {
      return false;
    }
  }

  // isEmail mirrors the Go-side config.Branding.MailLink() rule: a bare RFC 5322
  // address with no display name. When the Footer value matches, the preview
  // surfaces an envelope icon linking to mailto: (no plain footer text) so the
  // dashboard matches what GET / will render server-side.
  function isEmail(raw) {
    const t = (raw || '').trim();
    if (!t) return false;
    // Reject "Name <addr@x>" display-name forms; the Go helper does the same.
    if (/[<>]/.test(t)) return false;
    // Pragmatic single-address check: one "@", a dot in the domain, no spaces.
    const at = t.lastIndexOf('@');
    if (at < 1) return false;
    const domain = t.slice(at + 1);
    if (!domain.includes('.') || /\s/.test(t)) return false;
    return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(t);
  }

  // isHexColor mirrors the Go-side isValidHexColor: #rgb or #rrggbb. This is a
  // preview-time guard so the live card swaps invalid values for the built-in
  // defaults, exactly as the server-rendered HTML page will.
  function isHexColor(s) {
    return /^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6})$/.test(s);
  }

  // hexColorOr returns trimmed s when valid, else fallback. Mirror of the Go
  // config.Branding.*ColorOr() helpers.
  function hexColorOr(s, fallback) {
    const t = (s || '').trim();
    return isHexColor(t) ? t : fallback;
  }

  const preview = useMemo(() => {
    const trimmed = {
      title: title.trim(),
      message: message.trim(),
      logoUrl: logoUrl.trim(),
      footer: footer.trim(),
      threadsUrl: threadsUrl.trim(),
      whatsappUrl: whatsappUrl.trim(),
      telegramUrl: telegramUrl.trim(),
    };
    const valid = (u) => (isHttpUrl(u) ? u : '');
    const mailAddr = isEmail(trimmed.footer) ? trimmed.footer : '';
    return {
      ...trimmed,
      imageUrl: valid(trimmed.logoUrl),
      socials: [
        { label: 'Threads', key: 'threads', url: valid(trimmed.threadsUrl) },
        { label: 'WhatsApp', key: 'whatsapp', url: valid(trimmed.whatsappUrl) },
        { label: 'Telegram', key: 'telegram', url: valid(trimmed.telegramUrl) },
      ].filter((s) => s.url),
      // When the footer is an email, show only the envelope icon (mirroring the
      // server): bare footer text is suppressed. Non-email footer stays text.
      mailLink: mailAddr,
      footerText: mailAddr ? '' : trimmed.footer,
      // Resolve each color with the same fallback the server applies.
      backgroundColor: hexColorOr(backgroundColor, '#0b0f14'),
      titleColor: hexColorOr(titleColor, '#5eead4'),
      messageColor: hexColorOr(messageColor, '#c9d4e0'),
      footerColor: hexColorOr(footerColor, '#6b7785'),
    };
  }, [title, message, logoUrl, footer, threadsUrl, whatsappUrl, telegramUrl, backgroundColor, titleColor, messageColor, footerColor]);

  const hasAny = Boolean(
    preview.title || preview.message || preview.logoUrl || preview.footer ||
    preview.threadsUrl || preview.whatsappUrl || preview.telegramUrl ||
    (backgroundColor || '').trim() || (titleColor || '').trim() ||
    (messageColor || '').trim() || (footerColor || '').trim(),
  );

  async function save() {
    setSaving(true);
    try {
      await putBranding({
        title: title.trim(),
        message: message.trim(),
        'logo-url': logoUrl.trim(),
        footer: footer.trim(),
        'threads-url': threadsUrl.trim(),
        'whatsapp-url': whatsappUrl.trim(),
        'telegram-url': telegramUrl.trim(),
        'background-color': backgroundColor.trim(),
        'title-color': titleColor.trim(),
        'message-color': messageColor.trim(),
        'footer-color': footerColor.trim(),
      });
      toast.success('Branding saved. GET / now reflects the new page.');
      setDirty(false);
      // Re-fetch so the form reflects the server-side trim + persisted state.
      reload();
    } catch (err) {
      const msg = err instanceof ApiError ? err.message : 'Failed to save branding';
      toast.error(msg);
    } finally {
      setSaving(false);
    }
  }

  function resetToDefaults() {
    // Confirm before clearing — the legacy JSON response has no branding.
    const ok = window.confirm(
      'Clear all branding fields? GET / will return the legacy JSON response.',
    );
    if (!ok) return;
    setTitle('');
    setMessage('');
    setLogoUrl('');
    setFooter('');
    setThreadsUrl('');
    setWhatsappUrl('');
    setTelegramUrl('');
    setBackgroundColor('');
    setTitleColor('');
    setMessageColor('');
    setFooterColor('');
    setDirty(true);
    // Persist immediately so the root endpoint reverts now, not on next Save.
    putBranding({
      title: '', message: '', 'logo-url': '', footer: '',
      'threads-url': '', 'whatsapp-url': '', 'telegram-url': '',
      'background-color': '', 'title-color': '', 'message-color': '', 'footer-color': '',
    })
      .then(() => {
        toast.success('Branding cleared. GET / returns the legacy JSON response.');
        setDirty(false);
        reload();
      })
      .catch((err) => {
        const msg = err instanceof ApiError ? err.message : 'Failed to clear branding';
        toast.error(msg);
      });
  }

  return (
    <>
      <div className="main__header">
        <div>
          <h1 className="main__title">Branding</h1>
          <div className="main__subtitle">
            Customize the HTML page served at <code className="mono">GET /</code>.
            Leave every field blank to keep the legacy JSON response.
          </div>
        </div>
        <div className="row gap-sm">
          <button onClick={reload}>Refresh</button>
          <button className="danger" onClick={resetToDefaults}>Reset to defaults</button>
        </div>
      </div>

      {loading && <Spinner label="Loading branding…" />}
      <ErrorBanner error={error} onRetry={reload} />

      {!loading && !error && (
        <div className="grid grid--2">
          <div className="card">
            <h3 className="card__title">Branding fields</h3>
            <p className="muted" style={{ marginTop: 4, marginBottom: 16 }}>
              All fields are optional. The HTML page is served whenever at
              least one field is set; otherwise the legacy JSON envelope is
              returned.
            </p>

            <div className="form__row">
              <label className="form__label" htmlFor="branding-title">Title</label>
              <input
                id="branding-title"
                type="text"
                value={title}
                onChange={markDirty(setTitle)}
                placeholder="e.g. Acme LLM Gateway"
                maxLength={200}
              />
            </div>

            <div className="form__row">
              <label className="form__label" htmlFor="branding-logo-url">Logo URL</label>
              <input
                id="branding-logo-url"
                type="text"
                value={logoUrl}
                onChange={markDirty(setLogoUrl)}
                placeholder="https://example.com/logo.png"
                spellCheck={false}
              />
              <div className="form__hint">
                Only <code className="mono">http</code>/<code className="mono">https</code> URLs are
                rendered as an image; other values are ignored.
              </div>
            </div>

            <div className="form__row">
              <label className="form__label" htmlFor="branding-message">Message</label>
              <textarea
                id="branding-message"
                rows={4}
                value={message}
                onChange={markDirty(setMessage)}
                placeholder="Authorized access only."
                maxLength={2000}
              />
            </div>

            <div className="form__row">
              <label className="form__label" htmlFor="branding-footer">Email</label>
              <input
                id="branding-footer"
                type="text"
                value={footer}
                onChange={markDirty(setFooter)}
                placeholder="admin@example.com (renders as an envelope icon)"
                maxLength={200}
              />
            </div>

            <div className="form__row">
              <label className="form__label" htmlFor="branding-threads">Threads URL</label>
              <input
                id="branding-threads"
                type="text"
                value={threadsUrl}
                onChange={markDirty(setThreadsUrl)}
                placeholder="https://threads.net/@acme"
                spellCheck={false}
              />
            </div>

            <div className="form__row">
              <label className="form__label" htmlFor="branding-whatsapp">WhatsApp URL</label>
              <input
                id="branding-whatsapp"
                type="text"
                value={whatsappUrl}
                onChange={markDirty(setWhatsappUrl)}
                placeholder="https://wa.me/15551234"
                spellCheck={false}
              />
            </div>

            <div className="form__row">
              <label className="form__label" htmlFor="branding-telegram">Telegram URL</label>
              <input
                id="branding-telegram"
                type="text"
                value={telegramUrl}
                onChange={markDirty(setTelegramUrl)}
                placeholder="https://t.me/acme"
                spellCheck={false}
              />
              <div className="form__hint">
                Social URLs render as inline icons in the footer. Only
                <code className="mono"> http</code>/<code className="mono">https</code> URLs are
                rendered; other schemes are ignored.
              </div>
            </div>

            {/* Color overrides. Each row pairs a native color picker with a hex
                text input so the operator can either pick visually or paste an
                exact value. The server validates strict hex (#rgb | #rrggbb)
                and falls back to the documented default when invalid/empty. */}
            <ColorRow
              id="branding-background-color"
              label="Background color"
              value={backgroundColor}
              onChange={markDirty(setBackgroundColor)}
              fallback="#0b0f14"
            />
            <ColorRow
              id="branding-title-color"
              label="Title color"
              value={titleColor}
              onChange={markDirty(setTitleColor)}
              fallback="#5eead4"
            />
            <ColorRow
              id="branding-message-color"
              label="Message color"
              value={messageColor}
              onChange={markDirty(setMessageColor)}
              fallback="#c9d4e0"
            />
            <ColorRow
              id="branding-footer-color"
              label="Footer color"
              value={footerColor}
              onChange={markDirty(setFooterColor)}
              fallback="#6b7785"
            />

            <div className="form__actions">
              <button
                className="primary"
                onClick={save}
                disabled={!dirty || saving}
              >
                {saving ? 'Saving…' : 'Save'}
              </button>
            </div>
          </div>

          <div className="card">
            <h3 className="card__title">Live preview</h3>
            <p className="muted" style={{ marginTop: 4, marginBottom: 16 }}>
              Approximation of the page rendered at <code className="mono">GET /</code>.
            </p>
            <BrandingPreview preview={preview} hasAny={hasAny} />
          </div>
        </div>
      )}
    </>
  );
}

// BrandingPreview — a pure-React approximation of the dark-tech card the Go
// server renders for GET /. The server is the source of truth; this view just
// reflects the operator's in-progress edits before they commit.
function BrandingPreview({ preview, hasAny }) {
  if (!hasAny) {
    return (
      <div className="empty-state">
        <div className="empty-state__title">No branding configured</div>
        <div className="dim" style={{ marginTop: 4, fontSize: 12 }}>
          GET / returns the legacy JSON response.
        </div>
      </div>
    );
  }
  return (
    <div
      style={{
        background: preview.backgroundColor,
        border: '1px solid #1f2733',
        borderRadius: 12,
        padding: 28,
        textAlign: 'center',
        minHeight: 220,
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
      }}
    >
      <div style={{ maxWidth: 420, width: '100%' }}>
        {preview.imageUrl && (
          <img
            src={preview.imageUrl}
            alt=""
            referrerPolicy="no-referrer"
            style={{
              maxWidth: 72,
              maxHeight: 72,
              margin: '0 auto 14px',
              display: 'block',
              borderRadius: 10,
            }}
          />
        )}
        {preview.title && (
          <h1
            style={{
              fontFamily: 'var(--mono, monospace)',
              fontSize: '1.35rem',
              margin: '0 0 8px',
              color: preview.titleColor,
            }}
          >
            {preview.title}
          </h1>
        )}
        {(preview.title || preview.imageUrl) && (
          <hr style={{ width: 44, height: 2, background: '#1f2733', border: 0, margin: '14px auto' }} />
        )}
        {preview.message && (
          <p style={{ whiteSpace: 'pre-wrap', color: preview.messageColor, margin: '0 0 14px' }}>
            {preview.message}
          </p>
        )}
        {preview.footerText && (
          <p style={{ color: preview.footerColor, fontSize: '.85rem', margin: 0 }}>{preview.footerText}</p>
        )}
        {(preview.socials.length > 0 || preview.mailLink) && (
          <div style={{ display: 'flex', gap: 20, justifyContent: 'center', marginTop: 16 }}>
            {preview.socials.map((s) => (
              <a
                key={s.key}
                href={s.url}
                target="_blank"
                rel="noopener noreferrer"
                aria-label={s.label}
                title={s.label}
                style={{ color: preview.footerColor, lineHeight: 0, display: 'inline-flex' }}
              >
                {SOCIAL_ICONS[s.key] || null}
              </a>
            ))}
            {preview.mailLink && (
              <a
                href={`mailto:${preview.mailLink}`}
                aria-label="Email"
                title={preview.mailLink}
                style={{ color: preview.footerColor, lineHeight: 0, display: 'inline-flex' }}
              >
                {SOCIAL_ICONS.mail}
              </a>
            )}
          </div>
        )}
      </div>
    </div>
  );
}

// ColorRow renders a label + native <input type="color"> picker side-by-side
// with a hex text input. The picker edits the raw value (must be #rrggbb); the
// hex field accepts #rgb or #rrggbb. The fallback swatch is shown alongside
// the label so the operator can compare their pick with the built-in default.
function ColorRow({ id, label, value, onChange, fallback }) {
  // Picker requires #rrggbb; pad #rgb and fall back to the default when the
  // current value isn't valid hex (the picker will then override on change).
  function pickerValue() {
    const v = (value || '').trim();
    if (/^#[0-9a-fA-F]{6}$/.test(v)) return v;
    if (/^#[0-9a-fA-F]{3}$/.test(v)) {
      return '#' + v[1] + v[1] + v[2] + v[2] + v[3] + v[3];
    }
    return fallback;
  }

  return (
    <div className="form__row">
      <label className="form__label" htmlFor={id}>{label}</label>
      <div className="row gap-sm" style={{ alignItems: 'center' }}>
        <input
          id={id}
          type="color"
          value={pickerValue()}
          onChange={(e) => onChange({ target: { value: e.target.value } })}
          aria-label={`${label} picker`}
          style={{ width: 'auto', height: 38, padding: 0, flex: '0 0 auto' }}
        />
        <input
          type="text"
          value={value}
          onChange={onChange}
          placeholder={`${fallback} (default)`}
          spellCheck={false}
          style={{ flex: '1 1 auto' }}
          aria-label={`${label} hex value`}
        />
      </div>
    </div>
  );
}

// SOCIAL_ICONS — inline SVG (currentColor) for each supported footer network.
// Social glyphs use the simple-icons brand-accurate silhouettes. The envelope
// is a stroke-based glyph. Kept in sync with brandingIcons in root_branding.go.
const SOCIAL_ICONS = {
  threads: (
    <svg width="24" height="24" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d="M18.263 11.097c-.03-3.486-1.92-5.586-5.111-5.586-2.13 0-3.922.963-4.863 2.499l2.062 1.438c.535-.843 1.272-1.543 2.628-1.543 1.528 0 2.318.85 2.544 2.431a15 15 0 0 0-2.236-.173c-4.125 0-6.068 1.867-6.068 4.336s1.943 3.99 4.804 3.99c3.139 0 5.013-2.115 5.781-4.735.798.361 1.348 1.204 1.348 2.47 0 3.387-3.907 5.232-7.22 5.232-4.885 0-8.077-3.207-8.077-8.424 0-6.392 4.223-10.487 9.9-10.487 3.808 0 5.69 1.671 6.97 3.914l2.108-1.475C21.44 2.078 18.331 0 13.663 0 6.227 0 1.168 5.277 1.168 12.934c0 7 4.953 11.066 10.856 11.066 4.878 0 9.809-2.846 9.809-7.716 0-2.545-1.46-4.231-3.569-5.187m-6.33 4.855c-1.077 0-2.026-.512-2.026-1.453 0-1.483 1.822-1.934 3.606-1.934.678 0 1.34.045 1.927.173-.422 1.927-1.671 3.215-3.508 3.214Z"/></svg>
  ),
  whatsapp: (
    <svg width="24" height="24" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d="M17.472 14.382c-.297-.149-1.758-.867-2.03-.967-.273-.099-.471-.148-.67.15-.197.297-.767.966-.94 1.164-.173.199-.347.223-.644.075-.297-.15-1.255-.463-2.39-1.475-.883-.788-1.48-1.761-1.653-2.059-.173-.297-.018-.458.13-.606.134-.133.298-.347.446-.52.149-.174.198-.298.298-.497.099-.198.05-.371-.025-.52-.075-.149-.669-1.612-.916-2.207-.242-.579-.487-.5-.669-.51-.173-.008-.371-.01-.57-.01-.198 0-.52.074-.792.372-.272.297-1.04 1.016-1.04 2.479 0 1.462 1.065 2.875 1.213 3.074.149.198 2.096 3.2 5.077 4.487.709.306 1.262.489 1.694.625.712.227 1.36.195 1.871.118.571-.085 1.758-.719 2.006-1.413.248-.694.248-1.289.173-1.413-.074-.124-.272-.198-.57-.347m-5.421 7.403h-.004a9.87 9.87 0 01-5.031-1.378l-.361-.214-3.741.982.998-3.648-.235-.374a9.86 9.86 0 01-1.51-5.26c.001-5.45 4.436-9.884 9.888-9.884 2.64 0 5.122 1.03 6.988 2.898a9.825 9.825 0 012.893 6.994c-.003 5.45-4.437 9.884-9.885 9.884m8.413-18.297A11.815 11.815 0 0012.05 0C5.495 0 .16 5.335.157 11.892c0 2.096.547 4.142 1.588 5.945L.057 24l6.305-1.654a11.882 11.882 0 005.683 1.448h.005c6.554 0 11.89-5.335 11.893-11.893a11.821 11.821 0 00-3.48-8.413Z"/></svg>
  ),
  telegram: (
    <svg width="24" height="24" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d="M11.944 0A12 12 0 0 0 0 12a12 12 0 0 0 12 12 12 12 0 0 0 12-12A12 12 0 0 0 12 0a12 12 0 0 0-.056 0zm4.962 7.224c.1-.002.321.023.465.14a.506.506 0 0 1 .171.325c.016.093.036.306.02.472-.18 1.898-.962 6.502-1.36 8.627-.168.9-.499 1.201-.82 1.23-.696.065-1.225-.46-1.9-.902-1.056-.693-1.653-1.124-2.678-1.8-1.185-.78-.417-1.21.258-1.91.177-.184 3.247-2.977 3.307-3.23.007-.032.014-.15-.056-.212s-.174-.041-.249-.024c-.106.024-1.793 1.14-5.061 3.345-.48.33-.913.49-1.302.48-.428-.008-1.252-.241-1.865-.44-.752-.245-1.349-.374-1.297-.789.027-.216.325-.437.893-.663 3.498-1.524 5.83-2.529 6.998-3.014 3.332-1.386 4.025-1.627 4.476-1.635z"/></svg>
  ),
  mail: (
    <svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="3" y="5" width="18" height="14" rx="2"/><path d="M3 7l9 6 9-6"/></svg>
  ),
};

