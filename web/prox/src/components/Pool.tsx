import { band, clampPct, compact, pct, relative } from "../format";
import { usePoll } from "../hooks";
import { providerLabel, usd, type Pool, type PoolProvider, type PoolWindow } from "../proxTypes";
import { Pill } from "./ui";

const windowLabel: Record<string, string> = { five_hour: "5h", daily: "1d", weekly: "7d", monthly: "30d" };

export function PMark({ provider }: { provider: string }) {
  const letter = ({ claude: "C", codex: "X", kimi: "K", xai: "G" } as Record<string, string>)[provider] ?? provider.slice(0, 1).toUpperCase();
  return <span className={`pmark ${provider}`} aria-hidden="true">{letter}</span>;
}

/** Pooled headroom per provider: the first thing an operator needs. */
export function PoolStrip({ now }: { now: number }) {
  const q = usePoll<Pool>("/pool", 15000);
  const providers = q.data?.providers ?? [];
  if (!q.data) return null;
  if (providers.length === 0) return null;
  return (
    <section className="pool" aria-label="Pooled quota by provider">
      {providers.map((p) => <PoolCard key={p.provider} p={p} now={now} />)}
    </section>
  );
}

function PoolCard({ p, now }: { p: PoolProvider; now: number }) {
  const windows = (p.windows ?? []).filter((w) => w.accounts > 0);
  return (
    <div className="pool-card">
      <div className="pool-head">
        <PMark provider={p.provider} />
        <span className="pool-name">{providerLabel(p.provider)}</span>
        <Pill tone={p.usable === 0 ? "critical" : p.usable < p.accounts ? "warning" : "good"}>{p.usable}/{p.accounts} usable</Pill>
      </div>
      {windows.length === 0 ? (
        <div className="faint" style={{ fontSize: 12 }}>No quota data yet</div>
      ) : (
        windows.map((w) => <PoolMeter key={w.kind} w={w} now={now} />)
      )}
      <div className="pool-foot">
        <span title="API-equivalent cost over the last 24 hours">{usd(p.cost_today_usd)} <span className="faint">24h</span></span>
        <span title="API-equivalent cost over the last 7 days">{usd(p.cost_7d_usd)} <span className="faint">7d</span></span>
        <span className="faint num">{compact(p.requests_today)} req</span>
      </div>
    </div>
  );
}

function PoolMeter({ w, now }: { w: PoolWindow; now: number }) {
  const rem = clampPct(w.remaining_percent);
  const tone = band(rem);
  // Unused or full windows carry no decision value; dim them.
  const idle = w.min_remaining_percent >= 99.5;
  return (
    <div className={`pool-meter ${idle ? "idle" : ""}`} title={`${w.accounts} accounts · min ${pct(w.min_remaining_percent)} · max ${pct(w.max_remaining_percent)}${w.exhausted ? ` · ${w.exhausted} exhausted` : ""}`}>
      <span className="pool-wl">{windowLabel[w.kind] ?? w.kind}</span>
      <div className="meter-track"><div className={`meter-fill ${tone}`} style={{ width: `${rem}%` }} /></div>
      <span className="pool-val num">{pct(rem)}</span>
      <span className="pool-reset faint num">{w.next_reset ? relative(w.next_reset, now) : "—"}</span>
    </div>
  );
}
