import { usePoll } from "../hooks";
import type { Monitor } from "../proxTypes";
import { providerLabel } from "../proxTypes";
import type { QuotaWindow } from "../types";
import { PMark } from "./Pool";
import { Pill, QuotaMeter } from "./ui";
import { relative } from "../format";

const short: Record<string, string> = { five_hour: "5h", daily: "1d", weekly: "7d", monthly: "30d" };

/** Kimi and Grok accounts: quota only, prox does not route them. */
export function MonitoredPanel({ now, search }: { now: number; search: string }) {
  const q = usePoll<Monitor>("/monitor", 30000);
  const s = search.trim().toLowerCase();
  const list = (q.data?.accounts ?? []).filter((a) => !s || [a.display, a.id, a.plan].some((v) => v?.toLowerCase().includes(s)));
  if (list.length === 0) return null;
  return (
    <section className="panel" aria-labelledby="group-monitored">
      <div className="group-head">
        <h2 id="group-monitored">Monitored</h2>
        <span className="faint num">{list.length} account{list.length === 1 ? "" : "s"} · quota only, not routed</span>
      </div>
      <div className="table-wrap" tabIndex={0} role="region" aria-label="Monitored accounts">
        <table className="grid">
          <thead>
            <tr><th scope="col">Account</th><th scope="col">Windows</th><th scope="col" className="hide-sm">Polled</th></tr>
          </thead>
          <tbody>
            {list.map((a) => (
              <tr key={a.id}>
                <td>
                  <span style={{ display: "inline-flex", gap: 6, alignItems: "center" }}>
                    <PMark provider={a.provider} />{providerLabel(a.provider)} · {a.display}
                  </span>
                  {a.plan && <span className="faint" style={{ marginLeft: 6, fontSize: 12 }}>{a.plan}</span>}
                </td>
                <td>
                  {a.poll_error ? (
                    <Pill tone={a.poll_error === "api_billing_no_quota" ? "info" : "critical"}>{a.poll_error.replace(/_/g, " ")}</Pill>
                  ) : (a.windows ?? []).length === 0 ? (
                    <span className="faint">waiting for first poll</span>
                  ) : (
                    <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(150px, 1fr))", gap: 10 }}>
                      {(a.windows ?? []).map((w) => <QuotaMeter key={w.key} w={w as QuotaWindow} label={(short[w.kind] ?? w.kind) + (w.family ? ` ${w.family}` : "")} now={now} compact />)}
                    </div>
                  )}
                </td>
                <td className="faint num hide-sm">{relative(a.last_poll_at, now)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}
