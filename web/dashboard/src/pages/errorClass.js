// errorClass.js — slug → display label + colour for the closed set of error
// classes produced by internal/store/errorclass. The backend persists stable
// slugs only; the wording and palette live here so they can change without a
// migration.
//
// A tone is a CSS colour expression (usually a --var token) rather than a
// literal so the error-class chips can tint themselves from a single custom
// property (--chip-tone) and stay in step with the theme.
//
// Named exports follow the convention used across the pages/ modules.

// Order is the display order of the chip strip on the Errors page: the most
// actionable classes first, the catch-all last.
export const ERROR_CLASSES = [
  { slug: 'rate_limit', label: 'Rate limit', tone: 'var(--warning)' },
  { slug: 'auth', label: 'Auth', tone: 'var(--danger)' },
  { slug: 'permission', label: 'Permission', tone: 'var(--orange)' },
  { slug: 'quota', label: 'Quota', tone: 'var(--success)' },
  { slug: 'invalid_request', label: 'Invalid request', tone: 'var(--text-muted)' },
  { slug: 'not_found', label: 'Not found', tone: 'var(--text-dim)' },
  { slug: 'timeout', label: 'Timeout', tone: 'var(--purple)' },
  { slug: 'connection', label: 'Connection', tone: 'var(--blue)' },
  { slug: 'server_error', label: 'Server error', tone: 'var(--dark-red)' },
  { slug: 'overloaded', label: 'Overloaded', tone: 'var(--magenta)' },
  { slug: 'content_filter', label: 'Content filter', tone: 'var(--accent)' },
  { slug: 'other', label: 'Other', tone: 'var(--text-dim)' },
];

const BY_SLUG = new Map(ERROR_CLASSES.map((c) => [c.slug, c]));

// Neutral tone for the empty (unclassified) bucket and for any slug the
// backend might introduce before this map catches up.
const NEUTRAL_TONE = 'var(--text-dim)';

// classLabel maps a class slug to its human label. The empty slug (legacy rows
// with a NULL/empty error_class) reads as "Unclassified"; an unknown slug falls
// back to the raw slug so nothing is silently hidden.
export function classLabel(slug) {
  if (!slug) return 'Unclassified';
  return BY_SLUG.get(slug)?.label || slug;
}

// classTone maps a class slug to its CSS colour expression.
export function classTone(slug) {
  if (!slug) return NEUTRAL_TONE;
  return BY_SLUG.get(slug)?.tone || NEUTRAL_TONE;
}

// Aliases matching the naming used in the errors-diagnostics plan doc
// (Task 11) so either call site resolves.
export const errorClassLabel = classLabel;
export const errorClassColorVar = classTone;
