// Tiny JSON formatter for the inspector drawer. We don't pull in a
// highlighter dep (per design doc YAGNI). 2-space indent, no color.

const SAFE_CIRCULAR_PLACEHOLDER = '[Circular]';

export function prettyJson(value) {
  if (value == null) return '—';
  try {
    const seen = new WeakSet();
    return JSON.stringify(value, (key, val) => {
      if (typeof val === 'object' && val !== null) {
        if (seen.has(val)) return SAFE_CIRCULAR_PLACEHOLDER;
        seen.add(val);
      }
      return val;
    }, 2);
  } catch {
    return String(value);
  }
}

export function rawJson(value) {
  if (value == null) return '—';
  try {
    return JSON.stringify(value);
  } catch {
    return String(value);
  }
}
