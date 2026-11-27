import { useMemo, useState } from "react";
import type { PageProps } from "../App";
import { request } from "../api";
import { AccountDrawer } from "../components/AccountDrawer";
import { AccountTable } from "../components/AccountTable";
import { IconStar } from "../components/icons";
import { Empty, Pill, ProviderMark, providerName, Skeleton } from "../components/ui";
import { absolute, policyLabel, reason, relative } from "../format";
import { toast } from "../hooks";
import type { Account, NextUp, Overview, Provider } from "../types";

export function matches(a: Account, provider: string, search: string) {
  if (provider !== "all" && a.provider !== provider) return false;
  const q = search.trim().toLowerCase();
  if (!q) return true;
  return [a.alias, a.display, a.plan, a.id].some((v) => v?.toLowerCase().includes(q));
}

export async function refreshAccount(a: Account, after: () => void) {
  try {
    await request("/refresh", { method: "POST", body: { auth_id: a.id } });
    toast(`Refreshing ${a.alias || a.display}…`);
    setTimeout(after, 1500);
  } catch (e) {
    toast(e instanceof Error ? e.message : String(e), "critical");
  }
}

export function OverviewPage({ overview, reloadOverview, provider, search, now }: PageProps) {
  const [open, setOpen] = useState<string>();
  if (!overview) return <div className="panel"><Skeleton rows={6} /></div>;
  const ov = overview;
  const accounts = ov.accounts.filter((a) => matches(a, provider, search));
  const providers = (["claude", "codex"] as Provider[]).filter((p) => provider === "all" || provider === p);
  const opened = ov.accounts.find((a) => a.id === open);
  return (
    <>
      <Stats ov={ov} now={now} />
      <section className="split" aria-label="Next account for a new session">
        {providers.map((p) => (
          <NextPanel key={p} n={ov.next_up.find((x) => x.provider === p)} accounts={ov.accounts} now={now} />
        ))}
      </section>
      {providers.map((p) => {
        const list = accounts.filter((a) => a.provider === p);
        const s = ov.summary[p];
        return (
          <section className="panel" key={p} aria-labelledby={`group-${p}`}>
            <div className="group-head">
              <ProviderMark provider={p} />
              <h2 id={`group-${p}`}>{providerName(p)}</h2>
              <span className="faint num">{s.total} account{s.total === 1 ? "" : "s"}</span>
              <div className="counts">
                {s.eligible > 0 && <Pill tone="good">{s.eligible} eligible</Pill>}
                {s.limited + s.unknown > 0 && <Pill tone="warning">{s.limited + s.unknown} limited</Pill>}
                {s.exhausted > 0 && <Pill tone="critical">{s.exhausted} exhausted</Pill>}
                {s.blocked > 0 && <Pill tone="critical">{s.blocked} blocked</Pill>}
                {s.expiring_soon > 0 && <Pill tone="warning" title="Usable quota resets within 24 hours">{s.expiring_soon} expiring soon</Pill>}
              </div>
              <span style={{ marginLeft: "auto" }} className="faint num hide-sm">{s.active_sessions} active session{s.active_sessions === 1 ? "" : "s"}</span>
            </div>
            {list.length === 0 ? (
              <Empty title={s.total === 0 ? `No ${providerName(p)} accounts` : "No accounts match"}>
                {s.total === 0 ? `Add ${providerName(p)} OAuth credentials in CLIProxyAPI; they appear here within a few seconds.` : "Clear the search to see all accounts."}
              </Empty>
            ) : (
              <AccountTable regionLabel={`${providerName(p)} accounts`} accounts={list} now={now} onOpen={(a) => setOpen(a.id)} onRefresh={(a) => refreshAccount(a, reloadOverview)} selected={open} />
            )}
          </section>
        );
      })}
      {opened && <AccountDrawer a={opened} now={now} onClose={() => setOpen(undefined)} onChanged={reloadOverview} />}
    </>
  );
}

function Stats({ ov, now }: { ov: Overview; now: number }) {
  const next = ov.upcoming?.[0];
  const sessions = ov.summary.claude.active_sessions + ov.summary.codex.active_sessions;
  const healthy = !ov.plugin.worker_failed && ov.plugin.worker_running && ov.roster.ok;
  const tile = (p: Provider) => {
    const s = ov.summary[p];
    return (
      <div className="stat">
        <div className="stat-label"><ProviderMark provider={p} />{providerName(p)} accounts</div>
        <div className="stat-value">{s.total}<small>{s.eligible} eligible</small></div>
        <div className="stat-foot">
          {s.exhausted > 0 ? <span style={{ color: "var(--critical)" }}>{s.exhausted} exhausted</span> : "none exhausted"}
          {s.blocked > 0 && <> · <span style={{ color: "var(--critical)" }}>{s.blocked} blocked</span></>}
        </div>
      </div>
    );
  };
  return (
    <section className="stats" aria-label="Summary">
      {tile("claude")}
      {tile("codex")}
      <div className="stat">
        <div className="stat-label">Active sessions</div>
        <div className="stat-value">{sessions}<small>bound</small></div>
        <div className="stat-foot">Claude {ov.summary.claude.active_sessions} · Codex {ov.summary.codex.active_sessions}</div>
      </div>
      <div className="stat">
        <div className="stat-label">Next reset</div>
        <div className="stat-value">{next ? relative(next.resets_at, now).replace(/^in /, "") : "—"}</div>
        <div className="stat-foot">{next ? `${next.account} · ${absolute(next.resets_at)}` : "None in the next 24 hours"}</div>
      </div>
      <div className="stat">
        <div className="stat-label">Last quota data</div>
        <div className="stat-value">{ov.last_refresh ? relative(ov.last_refresh, now).replace(/ ago$/, "") : "—"}<small>{ov.last_refresh && relative(ov.last_refresh, now).endsWith(" ago") ? "ago" : ""}</small></div>
        <div className="stat-foot">{ov.roster.ok ? "Polling and live headers" : `Credential list failed: ${ov.roster.error}`}</div>
      </div>
      <div className="stat">
        <div className="stat-label">Routing</div>
        <div className="stat-value" style={{ fontSize: 16 }}>{policyLabel[ov.policy] ?? ov.policy}</div>
        <div className="stat-foot">
          <span className={`dot ${healthy ? "good" : "critical"}`} style={{ display: "inline-block", marginRight: 6 }} />
          {ov.mode === "active" ? "Active" : "Shadow"} · {healthy ? "healthy" : "degraded"} · {ov.counters.routed} routed
        </div>
      </div>
    </section>
  );
}

function NextPanel({ n, accounts, now }: { n: NextUp | undefined; accounts: Account[]; now: number }) {
  const byId = useMemo(() => new Map(accounts.map((a) => [a.id, a])), [accounts]);
  if (!n) return null;
  const top = n.auth_id ? byId.get(n.auth_id) : undefined;
  const ranked = (n.ranked ?? []).slice(1, 4);
  const name = (id: string) => {
    const a = byId.get(id);
    return a ? a.alias || a.display : id;
  };
  return (
    <div className="panel">
      <div className="panel-head">
        <ProviderMark provider={n.provider} />
        <h2>Next {providerName(n.provider)} session</h2>
        <span className="sub">{policyLabel[n.policy] ?? n.policy}{n.model ? ` · ${n.model}` : ""}</span>
      </div>
      <div className="panel-body next">
        {top ? (
          <>
            <div className="next-top">
              <span className="star"><IconStar /></span>
              <span className="next-name">{top.alias || top.display}</span>
              {top.plan && <Pill>{top.plan}</Pill>}
              {n.fallback && <Pill tone="warning">fallback</Pill>}
            </div>
            <div className="next-reason">{n.fallback ? n.reason : capitalize(n.ranked?.[0]?.reason ?? n.reason)}</div>
          </>
        ) : (
          <div className="next-reason">
            <strong style={{ color: "var(--text)" }}>CLIProxyAPI decides.</strong> {n.reason}.
          </div>
        )}
        {ranked.length > 0 && (
          <ol className="rank-list" start={2} aria-label="Runners-up">
            {ranked.map((r, i) => (
              <li key={r.auth_id}>
                <span className="n">{i + 2}</span>
                <span className="r"><span style={{ color: "var(--text)" }}>{name(r.auth_id)}</span> — {r.reason}</span>
                <span />
              </li>
            ))}
          </ol>
        )}
        {(n.skipped ?? []).length > 0 && (
          <div className="faint" style={{ fontSize: 12 }}>
            Not considered: {(n.skipped ?? []).slice(0, 3).map((s) => `${name(s.auth_id)} (${reason(s.reason).toLowerCase()})`).join(", ")}
            {(n.skipped ?? []).length > 3 && ` and ${(n.skipped ?? []).length - 3} more`}
          </div>
        )}
        {top?.next_reset && <div className="faint" style={{ fontSize: 12 }}>Applies to new sessions only. Usable quota on this account resets {relative(top.next_reset, now)}.</div>}
      </div>
    </div>
  );
}

function capitalize(s: string) {
  return s ? s.charAt(0).toUpperCase() + s.slice(1) : s;
}
