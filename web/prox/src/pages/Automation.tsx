import type { PageProps } from "../App";
import { request } from "../api";
import { PMark } from "../components/Pool";
import { Banner, Empty, Pill, Skeleton } from "../components/ui";
import { absolute, compact, relative } from "../format";
import { toast, usePoll } from "../hooks";
import { providerLabel, type Ignition, type IgnitionPlan, type NotifyEntry, type SinkStatus } from "../proxTypes";

const statusTone: Record<string, "good" | "warning" | "critical" | "info" | undefined> = {
  active: "good", idle: "info", firing: "info", confirming: "info", waiting: "warning", paused: "critical", blocked: "warning", unknown: undefined, disabled: undefined, unavailable: undefined,
};

const statusText: Record<string, string> = {
  active: "Window running", idle: "Idle window", firing: "Firing", confirming: "Confirming", waiting: "Waiting", paused: "Paused", blocked: "Blocked", unknown: "Unknown", disabled: "Off", unavailable: "Unavailable",
};

const kindTone: Record<string, "good" | "warning" | "critical" | "info" | undefined> = {
  success: "good", fired: "info", manual_fired: "info", credential_restored: "good", cpa_cooldown: "info",
  credential_invalid: "critical", rate_limited: "warning", no_window_started: "warning", no_model: "warning", model_refused: "warning", transient_failure: "warning", paused_until_tomorrow: "critical", manual_failure: "critical",
};

export function AutomationPage({ now }: PageProps) {
  const ign = usePoll<Ignition>("/ignition", 10000);
  const log = usePoll<{ entries: NotifyEntry[] }>("/notify/log", 15000);
  const sink = usePoll<SinkStatus>("/sink", 15000);
  const plans = ign.data?.plans ?? [];
  const allOff = plans.length > 0 && plans.every((p) => p.status === "disabled");

  async function fire(p: IgnitionPlan) {
    try {
      await request("/ignition/fire", { method: "POST", body: { auth_id: p.auth_id } });
      toast(`Igniting ${p.display}…`);
      setTimeout(() => void ign.reload(), 1500);
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e), "critical");
    }
  }

  async function testNotify() {
    try {
      await request("/notify/test", { method: "POST", body: {} });
      toast("Test notification queued");
      setTimeout(() => void log.reload(), 2500);
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e), "critical");
    }
  }

  return (
    <>
      <section className="panel">
        <div className="panel-head">
          <h2>Window ignition</h2>
          <span className="sub">starts each idle five-hour window right after it resets</span>
        </div>
        {allOff && <div className="panel-body"><Banner tone="info">Ignition is off. Set <code>plugins.configs.prox.ignition.enabled: true</code> to turn it on.</Banner></div>}
        {!ign.data ? (
          <Skeleton rows={4} />
        ) : plans.length === 0 ? (
          <Empty title="No Claude or Codex accounts" />
        ) : (
          <div className="table-wrap" tabIndex={0} role="region" aria-label="Ignition plans">
            <table className="grid">
              <thead>
                <tr>
                  <th scope="col">Account</th>
                  <th scope="col">Status</th>
                  <th scope="col">Next ignition</th>
                  <th scope="col" className="hide-sm">Five-hour reset</th>
                  <th scope="col" className="hide-sm">Model</th>
                  <th scope="col" className="right">Action</th>
                </tr>
              </thead>
              <tbody>
                {plans.map((p) => (
                  <tr key={p.auth_id}>
                    <td><span style={{ display: "inline-flex", gap: 6, alignItems: "center" }}><PMark provider={p.provider} />{p.display}</span></td>
                    <td><Pill tone={statusTone[p.status]} title={p.reason}>{statusText[p.status] ?? p.status}</Pill>{p.reason && <span className="faint" style={{ marginLeft: 6, fontSize: 12 }}>{p.reason.replace(/_/g, " ")}</span>}</td>
                    <td className="num" title={absolute(p.next_at)}>{p.next_at ? relative(p.next_at, now) : "—"}</td>
                    <td className="num hide-sm" title={absolute(p.resets_at)}>{p.resets_at ? relative(p.resets_at, now) : "—"}</td>
                    <td className="faint hide-sm">{p.model || "auto"}</td>
                    <td className="right">
                      <button className="btn sm" disabled={p.status === "firing" || p.status === "unavailable"} onClick={() => void fire(p)}>Ignite now</button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <section className="panel">
        <div className="panel-head"><h2>Ignition events</h2><span className="sub">newest first</span></div>
        {(ign.data?.events ?? []).length === 0 ? (
          <Empty title="No ignition events yet" />
        ) : (
          <div className="table-wrap" tabIndex={0} role="region" aria-label="Ignition events">
            <table className="grid">
              <thead>
                <tr><th scope="col">When</th><th scope="col">Account</th><th scope="col">Event</th><th scope="col" className="hide-sm">Model</th><th scope="col">Detail</th></tr>
              </thead>
              <tbody>
                {ign.data!.events.map((e, i) => (
                  <tr key={i}>
                    <td className="num" title={absolute(e.at)}>{relative(e.at, now)}</td>
                    <td><span style={{ display: "inline-flex", gap: 6, alignItems: "center" }}><PMark provider={e.provider} />{plans.find((p) => p.auth_id === e.auth_id)?.display ?? e.auth_id}</span></td>
                    <td><Pill tone={kindTone[e.kind]}><span className="ev-kind">{e.kind.replace(/_/g, " ")}</span></Pill></td>
                    <td className="faint hide-sm">{e.model ?? ""}</td>
                    <td className="muted" style={{ fontSize: 12 }}>{e.detail ?? ""}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <section className="split">
        <section className="panel">
          <div className="panel-head">
            <h2>Notifications</h2>
            <span className="sub">Bark and webhook deliveries</span>
            <button className="btn sm" style={{ marginLeft: "auto" }} onClick={() => void testNotify()}>Send test</button>
          </div>
          {(log.data?.entries ?? []).length === 0 ? (
            <Empty title="Nothing sent yet">Configure <code>notify.bark_url</code> or <code>notify.webhook.url</code> to receive alerts.</Empty>
          ) : (
            <div className="table-wrap" tabIndex={0} role="region" aria-label="Notification log">
              <table className="grid">
                <thead><tr><th scope="col">When</th><th scope="col">Message</th><th scope="col">Result</th></tr></thead>
                <tbody>
                  {log.data!.entries.map((e, i) => (
                    <tr key={i}>
                      <td className="num" title={absolute(e.at)}>{relative(e.at, now)}</td>
                      <td>{e.title}</td>
                      <td>{e.delivered ? <Pill tone="good">Delivered</Pill> : <Pill tone="critical" title={e.error}>Failed</Pill>}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </section>
        <section className="panel">
          <div className="panel-head"><h2>ClickHouse</h2><span className="sub">per-request usage rows</span></div>
          <div className="panel-body">
            {!sink.data ? (
              <Skeleton rows={2} />
            ) : !sink.data.enabled ? (
              <span className="faint">Off. Set <code>plugins.configs.prox.clickhouse.url</code> to stream one row per request.</span>
            ) : (
              <div style={{ display: "grid", gap: 6, fontSize: 13 }}>
                <div>{sink.data.error ? <Pill tone="critical" title={sink.data.error}>Failing</Pill> : sink.data.ready ? <Pill tone="good">Connected</Pill> : <Pill tone="info">Waiting for first write</Pill>}</div>
                <div className="muted">Sent <span className="num">{compact(sink.data.sent)}</span> · queued <span className="num">{compact(sink.data.queued)}</span> · dropped <span className="num" style={{ color: sink.data.dropped ? "var(--critical)" : undefined }}>{compact(sink.data.dropped)}</span></div>
                {sink.data.error && <div className="error-text" style={{ fontSize: 12 }}>{sink.data.error}</div>}
              </div>
            )}
          </div>
        </section>
      </section>
      <p className="faint" style={{ fontSize: 12 }}>Providers: {["claude", "codex"].map(providerLabel).join(", ")} ignite; Kimi and Grok are monitored only.</p>
    </>
  );
}
