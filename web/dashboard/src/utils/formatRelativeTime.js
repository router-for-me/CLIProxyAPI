// formatRelativeTime renders a short "2m ago" / "just now" string from an
// RFC3339 timestamp. Returns '—' when the input is empty/invalid.
//
// This is the shared copy of a helper that historically was duplicated across
// ModelsCatalogPage, UpstreamProvidersPage, and PricingSourcesModal. New
// consumers (e.g. the alerts dropdown) should import from here rather than
// adding a fourth copy.
export function formatRelativeTime(iso) {
  if (!iso) return '—';
  const t = new Date(iso);
  const ms = Date.now() - t.getTime();
  if (Number.isNaN(ms)) return '—';
  if (ms < 30 * 1000) return 'just now';
  const mins = Math.floor(ms / 60000);
  if (mins < 1) return 'just now';
  if (mins < 60) return `${mins}m ago`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  return `${days}d ago`;
}
