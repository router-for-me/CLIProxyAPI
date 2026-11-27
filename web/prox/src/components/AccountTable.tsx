import { useMemo, useState } from "react";
import { absolute, label, relative, reason } from "../format";
import type { Account } from "../types";
import { IconRefresh, IconStar } from "./icons";
import { findWindow, longWindow, modelWindows, ProviderMark, QuotaMeter, RoutingPill, SortHeader, type SortDir } from "./ui";

export type SortKey = "name" | "reset" | "remaining" | "sessions" | "availability";

const availabilityOrder: Record<string, number> = { eligible: 0, limited: 1, blocked: 2, removed: 3 };

export function sortAccounts(list: Account[], key: SortKey, dir: SortDir): Account[] {
  const mul = dir === "asc" ? 1 : -1;
  const time = (a: Account) => (a.next_reset ? Date.parse(a.next_reset) : Number.POSITIVE_INFINITY);
  const cmp: Record<SortKey, (a: Account, b: Account) => number> = {
    name: (a, b) => label(a).localeCompare(label(b)),
    reset: (a, b) => time(a) - time(b),
    remaining: (a, b) => a.remaining_percent - b.remaining_percent,
    sessions: (a, b) => a.active_sessions - b.active_sessions,
    availability: (a, b) => availabilityOrder[a.routing] - availabilityOrder[b.routing] || (a.rank || 999) - (b.rank || 999),
  };
  return [...list].sort((a, b) => cmp[key](a, b) * mul || a.id.localeCompare(b.id));
}

export function AccountTable({
  accounts,
  now,
  onOpen,
  onRefresh,
  selected,
  initialSort = { key: "availability", dir: "asc" },
  regionLabel = "Accounts",
}: {
  accounts: Account[];
  now: number;
  onOpen: (a: Account) => void;
  onRefresh?: (a: Account) => void;
  selected?: string;
  initialSort?: { key: SortKey; dir: SortDir };
  regionLabel?: string;
}) {
  const [sort, setSort] = useState<{ key: string; dir: SortDir }>(initialSort);
  const rows = useMemo(() => sortAccounts(accounts, sort.key as SortKey, sort.dir), [accounts, sort]);
  const showModel = accounts.some((a) => modelWindows(a).length > 0);
  return (
    <div className="table-wrap" tabIndex={0} role="region" aria-label={regionLabel}>
      <table className="grid stack">
        <thead>
          <tr>
            <SortHeader label="Account" k="name" sort={sort} setSort={setSort} />
            <th scope="col" className="w-meter">5-hour</th>
            <SortHeader label="Weekly" k="remaining" sort={sort} setSort={setSort} className="w-meter" />
            {showModel && <th scope="col" className="w-meter hide-md">Model</th>}
            <SortHeader label="Next reset" k="reset" sort={sort} setSort={setSort} />
            <SortHeader label="Sessions" k="sessions" sort={sort} setSort={setSort} className="right hide-sm" />
            <SortHeader label="Status" k="availability" sort={sort} setSort={setSort} />
            <th scope="col" className="hide-md">Refreshed</th>
            {onRefresh && <th scope="col"><span className="sr-only">Actions</span></th>}
          </tr>
        </thead>
        <tbody>
          {rows.map((a) => {
            const long = longWindow(a);
            const models = modelWindows(a);
            const tightest = [...models].sort((x, y) => x.remaining_percent - y.remaining_percent)[0];
            const refreshed = a.last_observed_at && (!a.last_refresh_at || a.last_observed_at > a.last_refresh_at) ? a.last_observed_at : a.last_refresh_at;
            return (
              <tr
                key={a.id}
                className={`clickable ${a.expiring_soon ? "row-expiring" : ""} ${selected === a.id ? "selected" : ""}`}
                onClick={() => onOpen(a)}
                onKeyDown={(e) => {
                  if (e.key === "Enter" || e.key === " ") {
                    e.preventDefault();
                    onOpen(a);
                  }
                }}
                tabIndex={0}
                aria-label={`${label(a)}, ${a.routing}. Open details`}
              >
                <td data-label="">
                  <div className="acct">
                    <ProviderMark provider={a.provider} />
                    <div style={{ minWidth: 0 }}>
                      <div className="acct-name" title={a.alias ? a.display : undefined}>
                        {label(a)}
                        {a.cooldown_stale && (
                          <span
                            className="pill warning"
                            title="Quota is fresh and has remaining capacity, but CPA still holds this account in cooldown. Clear the cooldown in CPA."
                            aria-label="Stale CPA cooldown"
                            style={{ marginLeft: 6, verticalAlign: -1 }}
                          >
                            CPA cooldown stale
                          </span>
                        )}
                        {a.preferred && (
                          <span className="star" role="img" title="Next new session goes here" aria-label="preferred for new sessions" style={{ marginLeft: 6, verticalAlign: -1, display: "inline-flex" }}>
                            <IconStar />
                          </span>
                        )}
                      </div>
                      <div className="acct-sub">
                        {a.plan && <span>{a.plan}</span>}
                        {a.plan && a.rank > 0 && <span>·</span>}
                        {a.rank > 0 && <span>#{a.rank} for new sessions</span>}
                        {!a.plan && !a.rank && <span>{a.provider === "claude" ? "Claude" : "Codex"}</span>}
                      </div>
                    </div>
                  </div>
                </td>
                <td data-label="" className="w-meter"><QuotaMeter w={findWindow(a, "five_hour")} label="5-hour" now={now} /></td>
                <td data-label="" className="w-meter"><QuotaMeter w={long} label={long?.kind === "monthly" ? "Monthly" : "Weekly"} now={now} /></td>
                {showModel && (
                  <td data-label="" className="w-meter hide-md">
                    {tightest ? <QuotaMeter w={tightest} label={tightest.label + (models.length > 1 ? ` +${models.length - 1}` : "")} now={now} /> : <span className="faint">—</span>}
                  </td>
                )}
                <td data-label="Next reset" className="nowrap">
                  <div className="num">{a.next_reset ? relative(a.next_reset, now) : "—"}</div>
                  <div className="faint num" style={{ fontSize: 11.5 }}>{absolute(a.next_reset)}{a.expiring_soon && <span style={{ color: "var(--warning)" }}> · use soon</span>}</div>
                </td>
                <td data-label="Sessions" className="right num">{a.active_sessions}</td>
                <td data-label="Status">
                  <RoutingPill a={a} />
                  {a.routing !== "eligible" && a.routing_reason && <div className="faint" style={{ fontSize: 11, marginTop: 3, maxWidth: 180, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{reason(a.routing_reason)}</div>}
                  {a.poll_error && <div title={a.poll_error} style={{ color: "var(--critical)", fontSize: 11, marginTop: 3 }}>Poll failing: {a.poll_error}</div>}
                </td>
                <td data-label="Refreshed" className="nowrap faint hide-md">{refreshed && relative(refreshed, now)}{a.poll_error ? <span title={a.poll_error} style={{ color: "var(--critical)" }}>{refreshed ? " · " : ""}failed</span> : !refreshed && "never"}</td>
                {onRefresh && (
                  <td data-label="" className="right">
                    <button
                      className="btn icon sm ghost"
                      aria-label={`Refresh quota for ${label(a)}`}
                      title="Refresh quota now"
                      onClick={(e) => {
                        e.stopPropagation();
                        onRefresh(a);
                      }}
                    >
                      <IconRefresh />
                    </button>
                  </td>
                )}
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
