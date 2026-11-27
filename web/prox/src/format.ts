// Formatting helpers. Pure functions; tested in format.test.ts.

export function clampPct(v: number | undefined | null): number {
  if (v === undefined || v === null || Number.isNaN(v)) return 0;
  return Math.max(0, Math.min(100, v));
}

export function pct(v: number | undefined | null): string {
  if (v === undefined || v === null || Number.isNaN(v)) return "—";
  const c = clampPct(v);
  return `${c < 10 && c > 0 && c % 1 !== 0 ? c.toFixed(1) : Math.round(c)}%`;
}

/** Status band for remaining capacity. */
export function band(remaining: number): "good" | "warning" | "critical" {
  if (remaining <= 10) return "critical";
  if (remaining <= 35) return "warning";
  return "good";
}

export function duration(ms: number): string {
  const neg = ms < 0;
  let s = Math.round(Math.abs(ms) / 1000);
  const d = Math.floor(s / 86400);
  s -= d * 86400;
  const h = Math.floor(s / 3600);
  s -= h * 3600;
  const m = Math.floor(s / 60);
  s -= m * 60;
  let out: string;
  if (d > 0) out = `${d}d ${h}h`;
  else if (h > 0) out = `${h}h ${m}m`;
  else if (m > 0) out = `${m}m`;
  else out = `${s}s`;
  return neg ? `-${out}` : out;
}

/** "in 3h 20m" / "4m ago". */
export function relative(iso: string | undefined | null, now: number): string {
  if (!iso) return "—";
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return "—";
  const diff = t - now;
  if (Math.abs(diff) < 5000) return "now";
  return diff > 0 ? `in ${duration(diff)}` : `${duration(-diff)} ago`;
}

const dateFmt = new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
const timeFmt = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit" });

export function absolute(iso: string | undefined | null): string {
  if (!iso) return "—";
  const t = Date.parse(iso);
  return Number.isNaN(t) ? "—" : dateFmt.format(t);
}

export function clock(iso: string | undefined | null): string {
  if (!iso) return "—";
  const t = Date.parse(iso);
  return Number.isNaN(t) ? "—" : timeFmt.format(t);
}

export function compact(n: number | undefined | null): string {
  if (n === undefined || n === null) return "—";
  const a = Math.abs(n);
  if (a >= 1e9) return `${(n / 1e9).toFixed(1)}B`;
  if (a >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
  if (a >= 1e4) return `${Math.round(n / 1e3)}k`;
  if (a >= 1e3) return `${(n / 1e3).toFixed(1)}k`;
  return String(n);
}

const reasons: Record<string, string> = {
  quota_unknown: "No quota data yet",
  disabled: "Disabled in CPA",
  cooldown_stale: "Fresh quota is available, but the CPA cooldown remains. Clear the cooldown in CPA",
  rate_limited: "Rate limited (temporary)",
  circuit_open: "Repeated errors; paused briefly",
  awaiting_reset_verification: "Reset passed; waiting for fresh data",
  quota_auth_suspect: "Quota endpoint rejected the token",
  not_offered: "Not offered by CPA (cooldown or already tried)",
  removed: "Credential removed",
  no_trustworthy_quota: "No fresh quota; CPA chose",
  provider_not_supported: "Provider not handled",
  mixed_provider_candidates: "Mixed providers; CPA chose",
  affinity_disabled: "Affinity disabled",
};

/** Human text for router reason codes such as "exhausted:weekly_exhausted". */
export function reason(code: string | undefined | null): string {
  if (!code) return "";
  if (reasons[code]) return reasons[code];
  const [head, tail] = splitOnce(code, ":");
  switch (head) {
    case "exhausted":
      return `Exhausted (${windowName(tail.replace(/_exhausted$/, ""))})`;
    case "auth_failed_401":
    case "auth_failed_403":
      return `Login rejected (${head.slice(-3)})`;
    case "model_unavailable":
      return tail ? `${cap(tail)} unavailable on this account` : "Model unavailable on this account";
    case "low_headroom":
      return `Only ${tail}% left; below the new-session minimum`;
    case "learned":
      return `${reason(tail)} (from observed traffic)`;
    case "bound_unusable_no_alternative":
      return `Bound account unusable and no alternative: ${reason(tail)}`;
  }
  if (head.startsWith("auth_failed")) return "Login rejected";
  if (head.startsWith("quota_")) {
    const state = head.slice(6);
    return `Quota ${state.replace("_", " ")}${tail ? ` (${windowName(tail.replace(/_(stale|reset_passed)$/, ""))})` : ""}`;
  }
  return code.replace(/_/g, " ");
}

function splitOnce(s: string, sep: string): [string, string] {
  const i = s.indexOf(sep);
  return i < 0 ? [s, ""] : [s.slice(0, i), s.slice(i + 1)];
}

function cap(s: string) {
  return s.charAt(0).toUpperCase() + s.slice(1);
}

export function windowName(key: string): string {
  if (key === "five_hour") return "5-hour";
  if (key === "weekly") return "weekly";
  if (key === "monthly") return "monthly";
  if (key.startsWith("model_weekly:")) return `weekly ${cap(key.slice(13))}`;
  return key.replace(/_/g, " ");
}

export const outcomeLabel: Record<string, string> = {
  affinity: "Affinity",
  inherited: "Inherited",
  new_session: "New session",
  no_session: "No session",
  failover: "Failover",
  rebind: "Rebound",
  host_pinned: "Host pinned",
  delegated: "Delegated",
};

export const policyLabel: Record<string, string> = {
  reset_first: "Reset-first",
  quota_pressure: "Quota pressure",
  balanced: "Balanced",
  manual_priority: "Manual priority",
};

export function label(a: { alias?: string; display: string }): string {
  return a.alias || a.display;
}

/** "3h0m0s" (Go duration) → "3h". */
export function goDuration(d: string | undefined): string {
  if (!d) return "—";
  return d.replace(/(\d)h0m0s$/, "$1h").replace(/(\d)m0s$/, "$1m").replace(/^0s$/, "0s");
}
