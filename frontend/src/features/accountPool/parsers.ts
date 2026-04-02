/**
 * Parsers for account pool batch import text formats
 */

export interface ParsedAccount {
  email: string;
  password: string;
  recovery_email: string;
  totp_secret: string;
}

export interface ParseError {
  line: number;
  raw: string;
  reason: string;
}

export interface ParseAccountResult {
  accounts: ParsedAccount[];
  errors: ParseError[];
}

export interface ParsedProxy {
  proxy_url: string;
  type: string;
}

export interface ParseProxyResult {
  proxies: ParsedProxy[];
  errors: ParseError[];
}

const DELIMITER = '----';

/**
 * Parse batch account import text.
 *
 * Accepted line formats (fields separated by `----`):
 *   4 fields: email----password----recovery_email----totp_secret
 *   3 fields: email----password----totp_secret  (recovery_email defaults to '')
 */
export function parseAccountLines(text: string): ParseAccountResult {
  const accounts: ParsedAccount[] = [];
  const errors: ParseError[] = [];

  const lines = text.split('\n');

  lines.forEach((raw, index) => {
    const trimmed = raw.trim();
    if (!trimmed) return;

    const lineNumber = index + 1;
    const parts = trimmed.split(DELIMITER);

    if (parts.length === 4) {
      accounts.push({
        email: parts[0].trim(),
        password: parts[1].trim(),
        recovery_email: parts[2].trim(),
        totp_secret: parts[3].trim()
      });
    } else if (parts.length === 3) {
      accounts.push({
        email: parts[0].trim(),
        password: parts[1].trim(),
        recovery_email: '',
        totp_secret: parts[2].trim()
      });
    } else {
      errors.push({
        line: lineNumber,
        raw: trimmed,
        reason: `Expected 3 or 4 fields separated by "${DELIMITER}", got ${parts.length}`
      });
    }
  });

  return { accounts, errors };
}

/**
 * Parse batch proxy import text.
 *
 * Accepted line formats (fields separated by `----`):
 *   2 fields: proxy_url----type
 *   1 field:  proxy_url  (type defaults to defaultType)
 */
export function parseProxyLines(text: string, defaultType: string): ParseProxyResult {
  const proxies: ParsedProxy[] = [];
  const errors: ParseError[] = [];

  const lines = text.split('\n');

  lines.forEach((raw, index) => {
    const trimmed = raw.trim();
    if (!trimmed) return;

    const lineNumber = index + 1;
    const parts = trimmed.split(DELIMITER);

    if (parts.length === 2) {
      proxies.push({
        proxy_url: parts[0].trim(),
        type: parts[1].trim()
      });
    } else if (parts.length === 1) {
      proxies.push({
        proxy_url: parts[0].trim(),
        type: defaultType
      });
    } else {
      errors.push({
        line: lineNumber,
        raw: trimmed,
        reason: `Expected 1 or 2 fields separated by "${DELIMITER}", got ${parts.length}`
      });
    }
  });

  return { proxies, errors };
}
