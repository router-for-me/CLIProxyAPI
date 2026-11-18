/**
 * Session-only OAuth login proxy: local validation, request planning and display masking.
 *
 * The validator mirrors the backend so an invalid value never leaves the browser:
 * `validateOAuthSessionProxyURL` (internal/api/handlers/management/auth_files_v8.go) on top of
 * `proxyutil.Parse` / `proxyutil.ValidRequestProxy` (sdk/proxyutil/proxy.go), which in turn rely
 * on Go's `net/url.Parse`. The Go parsing steps are reproduced here rather than using the
 * WHATWG `URL` parser, which normalizes input (drops default ports, accepts `host:`) and would
 * disagree with the backend.
 */

import { isRecord } from '@/utils/helpers';

const PROXY_SCHEMES = new Set(['http', 'https', 'socks5', 'socks5h']);

/** Go `net/url` userinfo characters (validUserinfo); `%` escapes are checked separately. */
const USERINFO_CHAR = /^[A-Za-z0-9\-._:~!$&'()*+,;=%@]$/;
/** ASCII characters Go accepts unescaped in a host (encoding_table.go, encodeHost). */
const HOST_CHAR = /^[A-Za-z0-9\-._~!"$&'()*+,;=:<>[\]]$/;
const HEX = /^[0-9A-Fa-f]$/;

export type LoginProxyCheck =
  { kind: 'empty' } | { kind: 'valid'; url: string } | { kind: 'invalid' };

/** Go url.unescape well-formedness; host mode only allows escaping non-ASCII bytes (or %25). */
const hasValidEscapes = (value: string, hostMode: boolean): boolean => {
  for (let i = 0; i < value.length; i += 1) {
    if (value[i] !== '%') continue;
    if (!HEX.test(value[i + 1] ?? '') || !HEX.test(value[i + 2] ?? '')) return false;
    if (hostMode && parseInt(value[i + 1], 16) < 8 && value.slice(i, i + 3) !== '%25') {
      return false;
    }
    i += 2;
  }
  return true;
};

/** Go validOptionalPort: empty, or `:` followed by digits only (a bare `:` is allowed). */
const isValidOptionalPort = (value: string): boolean => value === '' || /^:[0-9]*$/.test(value);

const hasControlChar = (value: string): boolean =>
  Array.from(value).some((char) => {
    const code = char.charCodeAt(0);
    return code < 0x20 || code === 0x7f;
  });

const isIPv6Literal = (value: string): boolean => {
  if (!value.includes(':')) return false;
  try {
    // The WHATWG IPv6 parser accepts the same textual forms as Go's netip for plain addresses.
    return new URL(`http://[${value}]/`).hostname !== '';
  } catch {
    return false;
  }
};

/** Go zone rules (RFC 6874): `%25` starts the zone; escapes may only spell host bytes or space. */
const isValidZone = (zone: string): boolean => {
  // `zone` starts with `%25`; netip rejects an empty zone after it.
  if (zone.length <= 3) return false;
  for (let i = 0; i < zone.length; i += 1) {
    const char = zone[i];
    if (char === '%') {
      if (!HEX.test(zone[i + 1] ?? '') || !HEX.test(zone[i + 2] ?? '')) return false;
      const escaped = zone.slice(i, i + 3);
      const byte = parseInt(escaped.slice(1), 16);
      if (
        escaped !== '%25' &&
        byte !== 0x20 &&
        !(byte < 0x80 && HOST_CHAR.test(String.fromCharCode(byte)))
      ) {
        return false;
      }
      i += 2;
    } else if (char <= '\x7f' && !HOST_CHAR.test(char)) {
      return false;
    }
  }
  return true;
};

const decodeForTrim = (value: string): string => {
  try {
    return decodeURIComponent(value);
  } catch {
    return value;
  }
};

/** Go url.parseHost; false wherever Go would return an error. */
const isValidHost = (scheme: string, host: string): boolean => {
  const openBracket = host.lastIndexOf('[');
  if (openBracket > 0) return false;
  if (openBracket === 0) {
    const closeBracket = host.lastIndexOf(']');
    if (closeBracket < 0) return false;
    if (!isValidOptionalPort(host.slice(closeBracket + 1))) return false;
    // Only an IPv6 address (not IPv4) may be bracketed, optionally with a zone identifier.
    const literal = host.slice(1, closeBracket);
    const zoneIndex = literal.indexOf('%25');
    if (zoneIndex !== -1 && !isValidZone(literal.slice(zoneIndex))) return false;
    return isIPv6Literal(zoneIndex === -1 ? literal : literal.slice(0, zoneIndex));
  }
  const firstColon = host.indexOf(':');
  if (firstColon !== -1) {
    const lastColon = host.lastIndexOf(':');
    // Go 1.26 rejects extra colons for http(s) only (urlstrictcolons); other schemes split on
    // the last colon.
    const portStart = scheme === 'http' || scheme === 'https' ? firstColon : lastColon;
    if (!isValidOptionalPort(host.slice(portStart))) return false;
  }
  if (!hasValidEscapes(host, true)) return false;
  return Array.from(host).every((char) => char === '%' || char > '\x7f' || HOST_CHAR.test(char));
};

/** Go url.splitHostPort as used by URL.Hostname() and URL.Port(). */
const splitHostPort = (host: string): { hostname: string; port: string } => {
  let hostname = host;
  let port = '';
  const colon = host.lastIndexOf(':');
  if (colon !== -1 && isValidOptionalPort(host.slice(colon))) {
    hostname = host.slice(0, colon);
    port = host.slice(colon + 1);
  }
  if (hostname.startsWith('[') && hostname.endsWith(']')) hostname = hostname.slice(1, -1);
  return { hostname, port };
};

/**
 * Validates a login proxy exactly as the backend does for `proxy_url` on
 * `POST /oauth/auth-url`. Accepted: `http`, `https`, `socks5`, `socks5h` with a host, an optional
 * port 1-65535 (required for socks5/socks5h), optional userinfo, and no path (other than `/`),
 * query or fragment. The returned URL is the trimmed input, which is what the backend stores.
 */
export function checkLoginProxyUrl(raw: string): LoginProxyCheck {
  const trimmed = raw.trim();
  if (!trimmed) return { kind: 'empty' };
  const invalid: LoginProxyCheck = { kind: 'invalid' };

  // url.Parse: fragment first, then control characters, then the scheme.
  const hashIndex = trimmed.indexOf('#');
  if (hashIndex !== -1 && hashIndex < trimmed.length - 1) return invalid;
  const withoutFragment = hashIndex === -1 ? trimmed : trimmed.slice(0, hashIndex);
  if (hasControlChar(withoutFragment)) return invalid;

  const schemeMatch = /^([A-Za-z][A-Za-z0-9+.-]*):/.exec(withoutFragment);
  if (!schemeMatch) return invalid;
  const scheme = schemeMatch[1].toLowerCase();
  if (!PROXY_SCHEMES.has(scheme)) return invalid;
  let rest = withoutFragment.slice(schemeMatch[0].length);

  // A lone trailing `?` is an empty forced query; anything after `?` is a query.
  if (rest.endsWith('?') && rest.indexOf('?') === rest.length - 1) {
    rest = rest.slice(0, -1);
  } else if (rest.includes('?')) {
    return invalid;
  }

  // Without `//` Go yields an opaque URL or an omitted host, both of which have no Host.
  if (!rest.startsWith('//')) return invalid;
  let authority = rest.slice(2);
  const slashIndex = authority.indexOf('/');
  const path = slashIndex === -1 ? '' : authority.slice(slashIndex);
  if (slashIndex !== -1) authority = authority.slice(0, slashIndex);
  if (path !== '' && path !== '/') return invalid;

  const atIndex = authority.lastIndexOf('@');
  const host = atIndex === -1 ? authority : authority.slice(atIndex + 1);
  if (atIndex !== -1) {
    const userinfo = authority.slice(0, atIndex);
    if (!Array.from(userinfo).every((char) => USERINFO_CHAR.test(char))) return invalid;
    if (!hasValidEscapes(userinfo, false)) return invalid;
  }
  if (!isValidHost(scheme, host)) return invalid;

  // proxyutil.Parse: Host must be non-empty. ValidRequestProxy: Hostname non-blank, port range.
  if (host === '') return invalid;
  const { hostname, port } = splitHostPort(host);
  if (decodeForTrim(hostname).trim() === '') return invalid;
  if (port !== '') {
    const number = Number(port);
    if (!Number.isInteger(number) || number < 1 || number > 65535) return invalid;
  }
  // validateOAuthSessionProxyURL: no dangling `:`; socks5(h) needs an explicit port.
  if (host.endsWith(':')) return invalid;
  if ((scheme === 'socks5' || scheme === 'socks5h') && port === '') return invalid;

  return { kind: 'valid', url: trimmed };
}

export type LoginProxyPlan =
  { kind: 'none' } | { kind: 'proxy'; url: string } | { kind: 'invalid' };

/**
 * Decides what any OAuth login start does with the page-level proxy value, including plugins.
 * Invalid proxies block login; there is no fallback to a login without the proxy.
 */
export function planLoginProxy(raw: string): LoginProxyPlan {
  const check = checkLoginProxyUrl(raw);
  if (check.kind === 'empty') return { kind: 'none' };
  if (check.kind === 'invalid') return { kind: 'invalid' };
  return { kind: 'proxy', url: check.url };
}

/**
 * Whether the field shows its inline "invalid proxy" message. A locally invalid value is flagged
 * once the user leaves the field or a login was refused; a value the backend refused stays
 * flagged until it is edited.
 */
export function shouldShowLoginProxyError(
  value: string,
  options: { touched: boolean; rejectedValue: string | null }
): boolean {
  const trimmed = value.trim();
  if (options.rejectedValue !== null && trimmed === options.rejectedValue) return true;
  if (checkLoginProxyUrl(value).kind !== 'invalid') return false;
  return options.touched || options.rejectedValue !== null;
}

/** The backend's 400 for a proxy it refuses: `{"error": "invalid proxy_url"}`. */
export function isInvalidProxyUrlError(error: unknown): boolean {
  if (!isRecord(error) || error.status !== 400) return false;
  return error.apiCode === 'invalid proxy_url' || error.message === 'invalid proxy_url';
}

/**
 * Display form of a proxy URL: scheme and host:port only. Any userinfo is replaced by `***` so
 * neither username nor password is shown; path, query and fragment are dropped.
 */
export function maskProxyUrl(raw: string): string {
  const trimmed = raw.trim();
  const schemeEnd = trimmed.indexOf('://');
  const prefix = schemeEnd === -1 ? '' : trimmed.slice(0, schemeEnd + 3);
  let rest = schemeEnd === -1 ? trimmed : trimmed.slice(schemeEnd + 3);
  // Mask through the last `@` anywhere: credentials may themselves contain `/`, `?` or `#`.
  const atIndex = rest.lastIndexOf('@');
  const hasUserinfo = atIndex !== -1;
  if (hasUserinfo) rest = rest.slice(atIndex + 1);
  const hostEnd = rest.search(/[/?#]/);
  const host = hostEnd === -1 ? rest : rest.slice(0, hostEnd);
  return `${prefix}${hasUserinfo ? '***@' : ''}${host}`;
}
