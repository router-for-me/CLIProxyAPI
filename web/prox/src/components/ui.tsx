import { useEffect, useId, useRef, type ReactNode } from "react";
import { band, clampPct, pct, relative, absolute, windowName } from "../format";
import type { Account, BurnEstimate, Provider, QuotaWindow } from "../types";
import { IconAlert, IconChevron, IconClose, IconInfo } from "./icons";

export function ProviderMark({ provider }: { provider: Provider }) {
  return (
    <span className={`pmark ${provider}`} aria-hidden="true">
      {provider === "claude" ? "C" : "X"}
    </span>
  );
}

export const providerName = (p: Provider) => (p === "claude" ? "Claude" : "Codex");

export function Pill({ tone, children, title }: { tone?: "good" | "warning" | "critical" | "info"; children: ReactNode; title?: string }) {
  return (
    <span className={`pill ${tone ?? ""}`} title={title}>
      {children}
    </span>
  );
}

/** Routing state of an account, always as text plus a colored dot. */
export function RoutingPill({ a }: { a: Account }) {
  if (!a.present) return <Pill title="Credential no longer listed by CPA">Removed</Pill>;
  if (a.quota_state === "exhausted") return <Pill tone="critical" title={a.routing_reason}><span className="dot critical" />Exhausted</Pill>;
  if (a.routing === "blocked") return <Pill tone="critical" title={a.routing_reason}><span className="dot critical" />Blocked</Pill>;
  if (a.routing === "limited") return <Pill tone="warning" title={a.routing_reason}><span className="dot warning" />{limitedText(a)}</Pill>;
  return <Pill tone="good"><span className="dot good" />Eligible</Pill>;
}

function limitedText(a: Account) {
  switch (a.quota_state) {
    case "stale":
      return "Stale";
    case "unknown":
      return "Unknown";
    case "reset_pending":
      return "Reset pending";
  }
  if (a.routing_reason === "rate_limited") return "Rate limited";
  if (a.routing_reason === "circuit_open") return "Paused";
  return "Limited";
}

/**
 * Horizontal meter of REMAINING capacity. Input windows always carry both
 * used and remaining percentages; this component only ever reads `remaining`.
 */
export function QuotaMeter({ w, label, now, compact }: { w: QuotaWindow | undefined; label: string; now: number; compact?: boolean }) {
  if (!w) {
    return (
      <div className="meter empty">
        <span className="meter-label">{label}</span>
        <span className="meter-value">—</span>
        <div className="meter-track" />
        {!compact && <span className="meter-sub">Not reported</span>}
      </div>
    );
  }
  const remaining = clampPct(w.remaining_percent);
  const stale = w.stale || w.reset_passed;
  const tone = w.rejected ? "critical" : band(remaining);
  const sub = w.reset_passed
    ? "Reset passed · awaiting data"
    : w.resets_at
      ? `Resets ${relative(w.resets_at, now)}`
      : remaining >= 100
        ? "No usage this window"
        : "Reset time not reported";
  const title = `${label}: ${pct(remaining)} remaining (${pct(w.used_percent)} used)${w.resets_at ? ` · resets ${absolute(w.resets_at)}` : ""} · ${w.source === "passive" ? "from live traffic" : "from quota poll"} ${relative(w.observed_at, now)}${w.stale ? " · stale" : ""}`;
  return (
    <div className={`meter ${tone} ${stale ? "stale" : ""}`} title={title}>
      <span className="meter-label">{label}</span>
      <span className="meter-value">{pct(remaining)}</span>
      <div
        className="meter-track"
        role="meter"
        aria-label={`${label} remaining`}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={Math.round(remaining)}
        aria-valuetext={`${pct(remaining)} remaining${stale ? ", data stale" : ""}`}
      >
        <div className="meter-fill" style={{ width: `${remaining}%` }} />
      </div>
      {!compact && <span className="meter-sub">{sub}</span>}
    </div>
  );
}

export function findWindow(a: Account, kind: QuotaWindow["kind"]): QuotaWindow | undefined {
  return (a.windows ?? []).find((w) => w.kind === kind);
}

export function longWindow(a: Account): QuotaWindow | undefined {
  return findWindow(a, "weekly") ?? findWindow(a, "monthly");
}

export function modelWindows(a: Account): QuotaWindow[] {
  return (a.windows ?? []).filter((w) => w.kind === "model_weekly");
}

export function BurnLine({ e, w, now }: { e: BurnEstimate | undefined; w: QuotaWindow; now: number }) {
  if (!e || e.status === "insufficient_data") {
    return <div className="estimate faint">Burn rate: not enough history yet{e ? ` (${e.samples} sample${e.samples === 1 ? "" : "s"}, ${e.span_minutes}m)` : ""}</div>;
  }
  if (e.status === "idle") {
    return (
      <div className="estimate">
        Idle in the last hours{e.unused_at_reset_percent !== undefined && <> · <b>{pct(e.unused_at_reset_percent)}</b> would go unused at reset</>}
        <span className="est-tag">estimate</span>
      </div>
    );
  }
  return (
    <div className="estimate">
      Burning <b>{e.rate_percent_per_hour?.toFixed(1)}%/h</b> ·{" "}
      {e.depletes_before_reset ? (
        <>runs out <b>{relative(e.depletes_at, now)}</b>, before the {windowName(w.key)} reset</>
      ) : (
        <>lasts to reset with <b>{pct(e.unused_at_reset_percent)}</b> left</>
      )}
      <span className="est-tag">estimate</span>
    </div>
  );
}

export function Empty({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="empty">
      <strong>{title}</strong>
      {children && <span>{children}</span>}
    </div>
  );
}

export function Banner({ tone, children }: { tone: "warning" | "critical" | "info"; children: ReactNode }) {
  return (
    <div className={`banner ${tone}`} role={tone === "critical" ? "alert" : "status"}>
      {tone === "info" ? <IconInfo /> : <IconAlert />}
      <div>{children}</div>
    </div>
  );
}

export function Switch({ checked, onChange, label }: { checked: boolean; onChange: (v: boolean) => void; label: string }) {
  return (
    <label className="switch">
      <input type="checkbox" role="switch" checked={checked} aria-label={label} onChange={(e) => onChange(e.target.checked)} />
      <span />
    </label>
  );
}

export function Seg<T extends string>({ value, options, onChange, label }: { value: T; options: { value: T; label: ReactNode }[]; onChange: (v: T) => void; label: string }) {
  return (
    <div className="seg" role="group" aria-label={label}>
      {options.map((o) => (
        <button key={o.value} type="button" aria-pressed={value === o.value} onClick={() => onChange(o.value)}>
          {o.label}
        </button>
      ))}
    </div>
  );
}

export type SortDir = "asc" | "desc";
export function SortHeader({ label, k, sort, setSort, className }: { label: string; k: string; sort: { key: string; dir: SortDir }; setSort: (s: { key: string; dir: SortDir }) => void; className?: string }) {
  const active = sort.key === k;
  return (
    <th className={className} aria-sort={active ? (sort.dir === "asc" ? "ascending" : "descending") : "none"} scope="col">
      <button type="button" onClick={() => setSort({ key: k, dir: active && sort.dir === "asc" ? "desc" : "asc" })}>
        {label}
        {active && <IconChevron dir={sort.dir === "asc" ? "up" : "down"} />}
      </button>
    </th>
  );
}

/** Focus-trapping modal surface used by Drawer and Dialog. */
function useModal(onClose: () => void) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const prev = document.activeElement as HTMLElement | null;
    const el = ref.current;
    const focusables = () => Array.from(el?.querySelectorAll<HTMLElement>('button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])') ?? []).filter((x) => !x.hasAttribute("disabled"));
    (el?.querySelector<HTMLElement>("[data-autofocus]") ?? focusables()[0])?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        onClose();
      }
      if (e.key === "Tab") {
        const f = focusables();
        if (f.length === 0) return;
        const first = f[0], last = f[f.length - 1];
        if (e.shiftKey && document.activeElement === first) {
          e.preventDefault();
          last.focus();
        } else if (!e.shiftKey && document.activeElement === last) {
          e.preventDefault();
          first.focus();
        }
      }
    };
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("keydown", onKey);
      prev?.focus?.();
    };
  }, [onClose]);
  return ref;
}

export function Drawer({ title, subtitle, onClose, children }: { title: ReactNode; subtitle?: ReactNode; onClose: () => void; children: ReactNode }) {
  const ref = useModal(onClose);
  const id = useId();
  return (
    <>
      <div className="overlay" onClick={onClose} />
      <div className="drawer" role="dialog" aria-modal="true" aria-labelledby={id} ref={ref}>
        <div className="drawer-head">
          <div style={{ flex: 1, minWidth: 0 }}>
            <h2 id={id}>{title}</h2>
            {subtitle && <div className="muted" style={{ marginTop: 4, fontSize: 12 }}>{subtitle}</div>}
          </div>
          <button className="btn icon ghost" onClick={onClose} aria-label="Close">
            <IconClose />
          </button>
        </div>
        <div className="drawer-body">{children}</div>
      </div>
    </>
  );
}

export function Dialog({ title, onClose, children, actions }: { title: string; onClose: () => void; children: ReactNode; actions: ReactNode }) {
  const ref = useModal(onClose);
  const id = useId();
  return (
    <>
      <div className="overlay" onClick={onClose} />
      <div className="dialog" role="alertdialog" aria-modal="true" aria-labelledby={id} ref={ref}>
        <h2 id={id}>{title}</h2>
        {children}
        <div className="dialog-actions">{actions}</div>
      </div>
    </>
  );
}

export function Skeleton({ rows = 4 }: { rows?: number }) {
  return (
    <div className="panel-body" aria-busy="true" aria-label="Loading">
      {Array.from({ length: rows }, (_, i) => (
        <div key={i} className="skeleton" style={{ width: `${90 - i * 12}%`, margin: "10px 0" }} />
      ))}
    </div>
  );
}
