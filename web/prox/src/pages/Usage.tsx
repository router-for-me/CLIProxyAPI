import { useState } from "react";
import type { PageProps } from "../App";
import { TimeChart } from "../components/Chart";
import { Empty, ProviderMark, providerName, Seg, Skeleton } from "../components/ui";
import { compact, pct } from "../format";
import { usePoll } from "../hooks";
import type { Counts, Provider, Usage } from "../types";

type Range = "6h" | "24h" | "7d";

export function UsagePage({ now: _now }: PageProps) {
  const [range, setRange] = useState<Range>("24h");
  const [provider, setProvider] = useState<Provider>("claude");
  const q = usePoll<Usage>(`/usage?range=${range}`, 30000);
  const u = q.data;
  return (
    <>
      <div style={{ display: "flex", gap: 10, flexWrap: "wrap", alignItems: "center" }}>
        <Seg label="Provider" value={provider} onChange={setProvider} options={[
          { value: "claude", label: <><ProviderMark provider="claude" />Claude</> },
          { value: "codex", label: <><ProviderMark provider="codex" />Codex</> },
        ]} />
        <Seg label="Time range" value={range} onChange={setRange} options={[
          { value: "6h", label: "6 hours" },
          { value: "24h", label: "24 hours" },
          { value: "7d", label: "7 days" },
        ]} />
        <span className="faint" style={{ fontSize: 12 }}>Only metrics CLIProxyAPI reports per upstream attempt. Token counts are not converted to cost.</span>
      </div>
      {!u ? (
        <div className="panel"><Skeleton rows={6} /></div>
      ) : (
        <ProviderUsage u={u} p={provider} />
      )}
    </>
  );
}

function ProviderUsage({ u, p }: { u: Usage; p: Provider }) {
  const d = u.providers[p];
  const times = d.series.map((x) => x.at);
  const t = d.totals;
  const vals = (k: keyof Counts) => d.series.map((x) => x[k]);
  const share = d.cache_read_share;
  const accounts = Object.entries(u.accounts)
    .filter(([id]) => u.labels[id] !== undefined)
    .map(([id, c]) => ({ id, name: u.labels[id] ?? id, c }))
    .filter((x) => x.c.requests > 0)
    .sort((a, b) => b.c.requests - a.c.requests);
  return (
    <>
      <section className="stats" aria-label={`${providerName(p)} totals`}>
        <Tile label="Requests" value={compact(t.requests)} foot={`${t.failures} failed (${t.requests ? pct((t.failures / t.requests) * 100) : "—"})`} />
        <Tile label="Input tokens" value={compact(t.input_tokens)} foot={`${compact(t.output_tokens)} output`} />
        <Tile label="Cache reads" value={compact(t.cache_read_tokens)} foot={`${compact(t.cache_creation_tokens)} cache writes`} />
        <Tile label="Cache read share" value={share === null ? "—" : pct(share * 100)} foot={share === null ? "No prompt tokens yet" : d.cache_share_basis} />
        <Tile label="Affinity hits" value={compact(t.affinity_hits)} foot={`${t.new_sessions} new sessions`} />
        <Tile label="Account switches" value={compact(t.failovers + t.rebinds)} foot={`${t.failovers} failovers · ${t.rebinds} rebinds`} />
      </section>
      <div className="charts">
        <ChartPanel title="Requests" sub={`per ${u.step.replace("0s", "").replace("h0m", "h")}`}>
          <TimeChart title="Requests over time" kind="bars" times={times} series={[
            { key: "ok", label: "Succeeded", color: "var(--s1)", values: d.series.map((x) => Math.max(0, x.requests - x.failures)) },
            { key: "fail", label: "Failed", color: "var(--critical)", values: vals("failures") },
          ]} />
        </ChartPanel>
        <ChartPanel title="Tokens" sub="input, output, cache reads">
          <TimeChart title="Tokens over time" kind="lines" times={times} series={[
            { key: "in", label: "Input", color: "var(--s1)", values: vals("input_tokens") },
            { key: "out", label: "Output", color: "var(--s2)", values: vals("output_tokens") },
            { key: "cache", label: "Cache read", color: "var(--s3)", values: vals("cache_read_tokens") },
          ]} />
        </ChartPanel>
        <ChartPanel title="Session routing" sub="affinity vs new sessions">
          <TimeChart title="Session routing over time" kind="bars" times={times} series={[
            { key: "aff", label: "Affinity", color: "var(--s1)", values: vals("affinity_hits") },
            { key: "new", label: "New session", color: "var(--s3)", values: vals("new_sessions") },
          ]} />
        </ChartPanel>
        <ChartPanel title="Account switches" sub="failovers and rebinds">
          <TimeChart title="Account switches over time" kind="bars" times={times} series={[
            { key: "fo", label: "Failovers", color: "var(--s2)", values: vals("failovers") },
            { key: "rb", label: "Rebinds", color: "var(--s1)", values: vals("rebinds") },
          ]} />
        </ChartPanel>
      </div>
      <section className="panel">
        <div className="panel-head"><h2>By account</h2><span className="sub">this range</span></div>
        {accounts.length === 0 ? (
          <Empty title="No traffic in this range" />
        ) : (
          <div className="table-wrap" tabIndex={0} role="region" aria-label="Usage by account">
            <table className="grid">
              <caption className="sr-only">Usage by account</caption>
              <thead>
                <tr>
                  <th scope="col">Account</th>
                  <th scope="col" className="right">Requests</th>
                  <th scope="col" className="right">Failed</th>
                  <th scope="col" className="right">Input</th>
                  <th scope="col" className="right">Output</th>
                  <th scope="col" className="right hide-sm">Cache read</th>
                  <th scope="col" className="right hide-sm">Cache write</th>
                </tr>
              </thead>
              <tbody>
                {accounts.map(({ id, name, c }) => (
                  <tr key={id}>
                    <td>{name}</td>
                    <td className="right num">{compact(c.requests)}</td>
                    <td className="right num" style={{ color: c.failures ? "var(--critical)" : undefined }}>{compact(c.failures)}</td>
                    <td className="right num">{compact(c.input_tokens)}</td>
                    <td className="right num">{compact(c.output_tokens)}</td>
                    <td className="right num hide-sm">{compact(c.cache_read_tokens)}</td>
                    <td className="right num hide-sm">{compact(c.cache_creation_tokens)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </>
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

function ChartPanel({ title, sub, children }: { title: string; sub: string; children: React.ReactNode }) {
  return (
    <section className="panel">
      <div className="panel-head"><h2>{title}</h2><span className="sub">{sub}</span></div>
      <div className="panel-body">{children}</div>
    </section>
  );
}
