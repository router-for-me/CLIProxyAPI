import { useId, useMemo, useState } from "react";
import type { PageProps } from "../App";
import { request } from "../api";
import { Banner, Dialog, Empty, Pill, ProviderMark, Skeleton, SortHeader, type SortDir } from "../components/ui";
import { usePoll, toast } from "../hooks";
import { absolute, goDuration, reason, relative } from "../format";
import type { SessionRow } from "../types";

interface SessionsResponse {
  now: string;
  sessions: SessionRow[] | null;
  ttl: string;
  enabled: boolean;
  mode: string;
}

type SortKey = "created_at" | "last_seen" | "requests";

function sortRows(list: SessionRow[], key: SortKey, dir: SortDir): SessionRow[] {
  const mul = dir === "asc" ? 1 : -1;
  const cmp: Record<SortKey, (a: SessionRow, b: SessionRow) => number> = {
    created_at: (a, b) => Date.parse(a.created_at) - Date.parse(b.created_at),
    last_seen: (a, b) => Date.parse(a.last_seen) - Date.parse(b.last_seen),
    requests: (a, b) => a.requests - b.requests,
  };
  return [...list].sort((a, b) => cmp[key](a, b) * mul || a.session.localeCompare(b.session));
}

export function SessionsPage({ provider, search, now }: PageProps) {
  const q = usePoll<SessionsResponse>("/sessions", 10000);
  const [sort, setSort] = useState<{ key: string; dir: SortDir }>({ key: "last_seen", dir: "desc" });
  const [clearOpen, setClearOpen] = useState(false);
  const [confirmText, setConfirmText] = useState("");
  const [clearing, setClearing] = useState(false);
  const confirmId = useId();

  const all = q.data?.sessions ?? [];
  const term = search.trim().toLowerCase();
  const filtered = useMemo(
    () =>
      all.filter((r) => {
        if (provider !== "all" && r.provider !== provider) return false;
        if (!term) return true;
        return [r.session, r.account, r.model, r.client ?? "", r.kind].some((v) => v.toLowerCase().includes(term));
      }),
    [all, provider, term],
  );
  const rows = useMemo(() => sortRows(filtered, sort.key as SortKey, sort.dir), [filtered, sort]);

  if (!q.data) return <div className="panel"><Skeleton rows={6} /></div>;
  const ttl = goDuration(q.data.ttl);

  async function release(row: SessionRow) {
    try {
      await request("/sessions/clear", { method: "POST", body: { session: row.session } });
      toast("Session released; its next request is scheduled as new");
      await q.reload();
    } catch (err) {
      toast(err instanceof Error ? err.message : String(err), "critical");
    }
  }

  async function clearAll() {
    setClearing(true);
    try {
      const r = await request<{ cleared: number }>("/sessions/clear", {
        method: "POST",
        body: { confirm: "clear sessions", provider: provider === "all" ? "" : provider },
      });
      toast(`Cleared ${r.cleared} binding${r.cleared === 1 ? "" : "s"}`);
      setClearOpen(false);
      setConfirmText("");
      await q.reload();
    } catch (err) {
      toast(err instanceof Error ? err.message : String(err), "critical");
    } finally {
      setClearing(false);
    }
  }

  return (
    <>
      {q.data.enabled ? (
        <p className="faint" style={{ margin: 0, fontSize: 12 }}>
          Sessions stay on their account while it is usable. Temporary errors fail over one request without moving the session. Exhaustion, login failure, or removal moves it (rebind). Bindings expire after <strong>{ttl}</strong> of inactivity.
        </p>
      ) : (
        <Banner tone="warning">Session affinity is disabled in Settings; requests are ranked independently.</Banner>
      )}
      <section className="panel">
        <div className="panel-head" style={{ flexWrap: "wrap" }}>
          <h2>Bound sessions</h2>
          <span className="sub num">{rows.length} shown</span>
          <span className="spacer" />
          <button className="btn sm danger" onClick={() => setClearOpen(true)} disabled={rows.length === 0}>
            Clear all…
          </button>
        </div>
        {rows.length === 0 ? (
          <Empty title="No active sessions">Sessions appear when Claude Code or Codex sends a request with a session header.</Empty>
        ) : (
          <div className="table-wrap" tabIndex={0} role="region" aria-label="Sessions">
            <table className="grid stack">
              <caption className="sr-only">Sessions bound to accounts, with status, age, and expiry</caption>
              <thead>
                <tr>
                  <th scope="col">Session</th>
                  <th scope="col">Provider / model</th>
                  <th scope="col">Account</th>
                  <SortHeader label="Age" k="created_at" sort={sort} setSort={setSort} />
                  <SortHeader label="Last activity" k="last_seen" sort={sort} setSort={setSort} />
                  <SortHeader label="Requests" k="requests" sort={sort} setSort={setSort} className="right hide-sm" />
                  <th scope="col">Status</th>
                  <th scope="col" className="hide-md">Expires</th>
                  <th scope="col"><span className="sr-only">Actions</span></th>
                </tr>
              </thead>
              <tbody>
                {rows.map((row) => (
                  <tr key={row.session}>
                    <td data-label="Session">
                      <div className="mono">{row.session}</div>
                      <div className="faint" style={{ fontSize: 11.5 }}>
                        {row.kind} · {row.client ?? "unknown client"}
                        {row.parent && (
                          <>
                            {" "}· child of <span className="mono">{row.parent}</span>
                          </>
                        )}
                        {!row.applied && (
                          <>
                            {" "}
                            <Pill tone="info">proposed</Pill>
                          </>
                        )}
                      </div>
                    </td>
                    <td data-label="Model" className="nowrap">
                      <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
                        <ProviderMark provider={row.provider} />
                        <span>{row.model}</span>
                      </div>
                    </td>
                    <td data-label="Account">
                      <div>{row.account}</div>
                      {row.actual_auth_id && row.actual_auth_id !== row.auth_id && (
                        <div className="faint" style={{ fontSize: 11.5 }}>last served by another account</div>
                      )}
                    </td>
                    <td data-label="Age" className="nowrap num">{relative(row.created_at, now)}</td>
                    <td data-label="Last active" className="nowrap num">{relative(row.last_seen, now)}</td>
                    <td data-label="Requests" className="right num hide-sm">{row.requests}</td>
                    <td data-label="Status">
                      {!row.account_usable ? (
                        <Pill tone="warning">Account unusable</Pill>
                      ) : row.rebinds > 0 ? (
                        <>
                          <Pill tone="info">Rebound</Pill>
                          <div className="faint" style={{ fontSize: 11.5, marginTop: 3 }}>
                            {reason(row.last_rebind_cause)}
                            {row.last_rebind_at && <> · {relative(row.last_rebind_at, now)}</>}
                          </div>
                        </>
                      ) : row.failovers > 0 ? (
                        <>
                          <Pill tone="good">Kept</Pill>
                          <div className="faint" style={{ fontSize: 11.5, marginTop: 3 }}>
                            {row.failovers} temporary failover{row.failovers === 1 ? "" : "s"}, binding kept
                          </div>
                        </>
                      ) : (
                        <Pill tone="good">Bound</Pill>
                      )}
                    </td>
                    <td data-label="Expires" className="nowrap num hide-md" title={absolute(row.expires_at)}>{relative(row.expires_at, now)}</td>
                    <td data-label="" className="right">
                      <button
                        className="btn sm ghost"
                        aria-label={`Release session ${row.session}`}
                        onClick={() => void release(row)}
                      >
                        Release
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
      {clearOpen && (
        <Dialog
          title="Clear session bindings?"
          onClose={() => {
            if (!clearing) setClearOpen(false);
          }}
          actions={
            <>
              <button className="btn" onClick={() => setClearOpen(false)} disabled={clearing}>
                Cancel
              </button>
              <button
                className="btn danger solid"
                onClick={() => void clearAll()}
                disabled={confirmText !== "clear sessions" || clearing}
              >
                {clearing ? "Clearing…" : "Clear bindings"}
              </button>
            </>
          }
        >
          <p>
            Every listed session
            {provider === "all" ? "" : ` for ${provider === "claude" ? "Claude" : "Codex"}`} will be scheduled as a new session on its next request and may lose provider-side prompt cache.
          </p>
          <div className="field">
            <label htmlFor={confirmId}>Type clear sessions to confirm</label>
            <input
              id={confirmId}
              className="input"
              type="text"
              autoComplete="off"
              spellCheck={false}
              value={confirmText}
              onChange={(e) => setConfirmText(e.target.value)}
            />
          </div>
        </Dialog>
      )}
    </>
  );
}
