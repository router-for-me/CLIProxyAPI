import { useMemo, useState } from "react";
import type { PageProps } from "../App";
import { TimeChart } from "../components/Chart";
import { PMark } from "../components/Pool";
import { Empty, Seg, Skeleton } from "../components/ui";
import { compact, pct } from "../format";
import { usePoll } from "../hooks";
import { providerLabel, totalTokens, usd, type CostRow, type Costs } from "../proxTypes";
import type { Overview } from "../types";

type Range = "24h" | "7d" | "30d" | "90d";
type Group = "model" | "auth" | "client" | "provider";

const stepFor: Record<Range, string> = { "24h": "1h", "7d": "24h", "30d": "24h", "90d": "24h" };
const palette = ["var(--s1)", "var(--s2)", "var(--s3)", "#a371f7", "#e3b341", "#6cb6ff", "#f778ba", "var(--text-3)"];

export function CostPage({ overview }: PageProps) {
  const [range, setRange] = useState<Range>("7d");
  const [group, setGroup] = useState<Group>("model");
  const series = usePoll<Costs>(`/costs?range=${range}&group=provider&step=${stepFor[range]}`, 30000);
  const table = usePoll<Costs>(`/costs?range=${range}&group=${group === "auth" ? "auth" : group === "provider" ? "provider" : `provider,${group}`}`, 30000);
  const labels = useMemo(() => accountLabels(overview), [overview]);
  const t = table.data?.total;
  return (
    <>
      <div className="toolbar">
        <Seg label="Time range" value={range} onChange={setRange} options={[
          { value: "24h", label: "24 hours" }, { value: "7d", label: "7 days" }, { value: "30d", label: "30 days" }, { value: "90d", label: "90 days" },
        ]} />
        <Seg label="Group by" value={group} onChange={setGroup} options={[
          { value: "model", label: "Model" }, { value: "auth", label: "Account" }, { value: "client", label: "Client" }, { value: "provider", label: "Provider" },
        ]} />
        <span className="faint" style={{ fontSize: 12 }}>Cost is what the same tokens would cost on each provider's API, not what your subscriptions charge.</span>
      </div>
      {!t ? (
        <div className="panel"><Skeleton rows={6} /></div>
      ) : (
        <>
          <section className="stats" aria-label="Totals">
            <Tile label="API-equivalent cost" value={usd(t.cost_usd)} foot={t.unpriced ? `${compact(t.unpriced)} requests on unpriced models` : "all models priced"} />
            <Tile label="Requests" value={compact(t.requests)} foot={`${compact(t.failures)} failed (${t.requests ? pct((t.failures / t.requests) * 100) : "—"})`} />
            <Tile label="Total tokens" value={compact(totalTokens(t.tokens))} foot={`${compact(t.tokens.output)} output · ${compact(t.tokens.reasoning)} reasoning`} />
            <Tile label="Uncached input" value={compact(t.tokens.input)} foot={`${compact(t.tokens.cache_write)} cache writes`} />
            <Tile label="Cache reads" value={compact(t.tokens.cache_read)} foot="tokens served from prompt cache" />
            <Tile label="Cache read share" value={table.data?.cache_read_share == null ? "—" : pct(table.data.cache_read_share * 100)} foot="of all input tokens; low means affinity is breaking" />
          </section>
          <CostChart data={series.data} />
          <section className="panel">
            <div className="panel-head"><h2>By {group === "auth" ? "account" : group}</h2><span className="sub">this range, highest cost first</span></div>
            <CostTable rows={table.data!.rows} group={group} labels={labels} />
          </section>
        </>
      )}
    </>
  );
}

function accountLabels(ov: Overview | undefined): Record<string, string> {
  const out: Record<string, string> = {};
  for (const a of ov?.accounts ?? []) out[a.id] = a.alias || a.display;
  return out;
}

function CostChart({ data }: { data: Costs | undefined }) {
  const { times, series } = useMemo(() => {
    const rows = data?.rows ?? [];
    const times = [...new Set(rows.map((r) => r.start))].sort();
    const providers = [...new Set(rows.map((r) => r.provider))].sort();
    const idx = new Map(times.map((t, i) => [t, i]));
    const series = providers.map((p, i) => {
      const values = new Array(times.length).fill(0);
      for (const r of rows) if (r.provider === p) values[idx.get(r.start)!] += r.cost_usd;
      return { key: p, label: providerLabel(p), color: palette[i % palette.length], values };
    });
    return { times, series };
  }, [data]);
  return (
    <section className="panel">
      <div className="panel-head"><h2>Cost over time</h2><span className="sub">by provider</span></div>
      <div className="panel-body">
        {times.length === 0 ? <Empty title="No requests in this range" /> : <TimeChart title="API-equivalent cost over time" kind="bars" times={times} series={series} format={usd} />}
      </div>
    </section>
  );
}

function CostTable({ rows, group, labels }: { rows: CostRow[]; group: Group; labels: Record<string, string> }) {
  const sorted = [...rows].sort((a, b) => b.cost_usd - a.cost_usd || b.requests - a.requests);
  const max = Math.max(0.000001, ...sorted.map((r) => r.cost_usd));
  if (sorted.length === 0) return <Empty title="No requests in this range" />;
  const name = (r: CostRow) => {
    switch (group) {
      case "auth":
        return labels[r.auth_id] ?? r.auth_id;
      case "client":
        return r.client;
      case "provider":
        return providerLabel(r.provider);
      default:
        return r.model || "unknown";
    }
  };
  return (
    <div className="table-wrap" tabIndex={0} role="region" aria-label="Cost table">
      <table className="grid">
        <thead>
          <tr>
            <th scope="col">{group === "auth" ? "Account" : group[0].toUpperCase() + group.slice(1)}</th>
            <th scope="col" className="right">Cost</th>
            <th scope="col" className="right">Requests</th>
            <th scope="col" className="right">Input</th>
            <th scope="col" className="right">Output</th>
            <th scope="col" className="right hide-sm">Cache read</th>
            <th scope="col" className="right hide-sm">Cache write</th>
            <th scope="col" className="right hide-sm">Cache share</th>
          </tr>
        </thead>
        <tbody>
          {sorted.map((r) => {
            const inTotal = r.tokens.input + r.tokens.cache_read + r.tokens.cache_write;
            return (
              <tr key={`${r.provider}|${r.auth_id}|${r.model}|${r.client}`}>
                <td><span className="row" style={{ gap: 6, display: "inline-flex", alignItems: "center" }}><PMark provider={r.provider} />{name(r)}</span></td>
                <td className="right num bar-cell">
                  <span className="bar" style={{ width: `${(r.cost_usd / max) * 100}%` }} />
                  {r.unpriced && !r.cost_usd ? <span className="faint" title="No price configured for this model">unpriced</span> : usd(r.cost_usd)}
                </td>
                <td className="right num">{compact(r.requests)}{r.failures ? <span style={{ color: "var(--critical)" }}> · {compact(r.failures)}</span> : null}</td>
                <td className="right num">{compact(r.tokens.input)}</td>
                <td className="right num">{compact(r.tokens.output)}</td>
                <td className="right num hide-sm">{compact(r.tokens.cache_read)}</td>
                <td className="right num hide-sm">{compact(r.tokens.cache_write)}</td>
                <td className="right num hide-sm">{inTotal ? pct((r.tokens.cache_read / inTotal) * 100) : "—"}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function Tile({ label, value, foot }: { label: string; value: string; foot: string }) {
  return (
    <div className="stat">
      <div className="stat-label">{label}</div>
      <div className="stat-value">{value}</div>
      <div className="stat-foot" title={foot}>{foot}</div>
    </div>
  );
}
