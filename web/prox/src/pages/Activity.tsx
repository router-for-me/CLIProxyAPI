import { useEffect, useState, type KeyboardEvent } from "react";
import type { PageProps } from "../App";
import { request } from "../api";
import { Drawer, Empty, Pill, ProviderMark, providerName, Seg, Skeleton } from "../components/ui";
import { absolute, clock, outcomeLabel, policyLabel, reason, relative } from "../format";
import { usePoll } from "../hooks";
import type { Decision } from "../types";

type Outcome = "" | "new_session" | "affinity" | "inherited" | "failover" | "rebind" | "delegated";

const OUTCOMES: { value: Outcome; label: string }[] = [
  { value: "", label: "All" },
  { value: "new_session", label: "New" },
  { value: "affinity", label: "Affinity" },
  { value: "inherited", label: "Inherited" },
  { value: "failover", label: "Failover" },
  { value: "rebind", label: "Rebound" },
  { value: "delegated", label: "Delegated" },
];

interface ActivityResponse {
  now: string;
  decisions: Decision[] | null;
  labels: Record<string, string>;
}

interface DecisionResponse {
  decision: Decision;
  labels: Record<string, string>;
}

interface Fetched {
  id: number;
  decision?: Decision;
  labels: Record<string, string>;
  error?: string;
}

const named = (map: Record<string, string>) => (id?: string) => (id ? (map[id] ?? id) : "—");

function toneFor(outcome: string): "good" | "warning" | "info" | undefined {
  switch (outcome) {
    case "failover":
      return "warning";
    case "rebind":
    case "host_pinned":
      return "info";
    case "delegated":
      return undefined;
    default:
      return "good";
  }
}

function whyText(d: Decision): string {
  if (d.cause) return reason(d.cause);
  if (d.outcome === "affinity") return "Bound session";
  if (d.outcome === "inherited") return "Parent session's account";
  if (d.outcome === "new_session") return d.ranked?.[0] ? reason(d.ranked[0].reason) : "Top ranked";
  return "";
}

function outcomeSentence(d: Decision): string {
  const why = reason(d.cause) || "no reason recorded";
  switch (d.outcome) {
    case "affinity":
      return "Kept the session on its bound account.";
    case "inherited":
      return "New subagent session inherited its parent's account.";
    case "new_session":
      return "New session; chose the top-ranked account and bound the session to it.";
    case "no_session":
      return "No trustworthy session ID; chose the top-ranked account without binding.";
    case "failover":
      return `Bound account was unavailable for this request (${why}); served elsewhere and kept the binding.`;
    case "rebind":
      return `Bound account became unusable (${why}); moved the session permanently.`;
    case "host_pinned":
      return "CLIProxyAPI pinned this request to one account (WebSocket continuation).";
    case "delegated":
      return `The plugin did not choose: ${why}. CLIProxyAPI's built-in scheduler picked.`;
    default:
      return outcomeLabel[d.outcome] ?? d.outcome;
  }
}

const whyStyle = {
  display: "inline-block",
  maxWidth: 320,
  overflow: "hidden",
  textOverflow: "ellipsis",
  whiteSpace: "nowrap",
  verticalAlign: "bottom",
} as const;

export function ActivityPage({ provider, search, now }: PageProps) {
  const [outcome, setOutcome] = useState<Outcome>("");
  const [selectedId, setSelectedId] = useState<number>();
  const [fetched, setFetched] = useState<Fetched>();

  const feed = usePoll<ActivityResponse>(
    `/activity?limit=300${outcome ? `&outcome=${outcome}` : ""}${provider !== "all" ? `&provider=${provider}` : ""}`,
    5000,
  );
  const decisions = feed.data?.decisions ?? [];
  const labels = feed.data?.labels ?? {};
  const name = named(labels);

  // A selected decision that aged out of the current list is fetched once by id.
  const fetchedId = fetched?.id;
  const needFetch = selectedId !== undefined && !decisions.some((x) => x.id === selectedId);
  useEffect(() => {
    if (selectedId === undefined || !needFetch || fetchedId === selectedId) return;
    let live = true;
    request<DecisionResponse>(`/decision?id=${selectedId}`)
      .then((r) => {
        if (live) setFetched({ id: selectedId, decision: r.decision, labels: r.labels ?? {} });
      })
      .catch((e: unknown) => {
        if (live) setFetched({ id: selectedId, labels: {}, error: e instanceof Error ? e.message : String(e) });
      });
    return () => {
      live = false;
    };
  }, [selectedId, needFetch, fetchedId]);

  if (!feed.data) {
    return (
      <div className="panel">
        {feed.error ? <Empty title="Could not load decisions">{feed.error.message}</Empty> : <Skeleton rows={8} />}
      </div>
    );
  }

  const needle = search.trim().toLowerCase();
  const list = decisions.filter((d) => {
    if (!needle) return true;
    const hay = [d.model, d.session ?? "", d.outcome, outcomeLabel[d.outcome] ?? "", name(d.auth_id), reason(d.cause)].join(" ").toLowerCase();
    return hay.includes(needle);
  });

  const fromList = selectedId === undefined ? undefined : decisions.find((x) => x.id === selectedId);
  const fromFetch = fetched && fetched.id === selectedId ? fetched : undefined;
  const d = fromList ?? fromFetch?.decision;
  const dname = named(fromList ? labels : fromFetch?.labels ?? labels);

  function open(id: number) {
    setSelectedId(id);
  }
  function onRowKey(e: KeyboardEvent<HTMLTableRowElement>, id: number) {
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      open(id);
    }
  }

  return (
    <>
      <section className="panel">
        <div className="panel-head" style={{ flexWrap: "wrap" }}>
          <h2>Routing decisions</h2>
          <span className="sub num">{list.length} shown</span>
          <span className="spacer" />
          <Seg label="Outcome" value={outcome} onChange={setOutcome} options={OUTCOMES} />
        </div>
        <p className="faint" style={{ margin: 0, padding: "8px 16px", fontSize: 12 }}>
          Every scheduler decision for Claude and Codex requests. Results are matched to decisions by session when CLIProxyAPI reports the attempt.
        </p>
        {list.length === 0 ? (
          decisions.length === 0 && !needle ? (
            <Empty title="No decisions yet">Decisions appear as requests arrive.</Empty>
          ) : (
            <Empty title="No decisions match">Change the outcome filter, provider, or search.</Empty>
          )
        ) : (
          <div className="table-wrap" tabIndex={0} role="region" aria-label="Routing decisions">
            <table className="grid stack">
              <caption className="sr-only">Routing decisions</caption>
              <thead>
                <tr>
                  <th scope="col">Time</th>
                  <th scope="col">Request</th>
                  <th scope="col">Outcome</th>
                  <th scope="col">Account</th>
                  <th scope="col" className="hide-md">Why</th>
                  <th scope="col" className="hide-sm">Result</th>
                </tr>
              </thead>
              <tbody>
                {list.map((x) => (
                  <tr
                    key={x.id}
                    className={`clickable ${selectedId === x.id ? "selected" : ""}`}
                    tabIndex={0}
                    onClick={() => open(x.id)}
                    onKeyDown={(e) => onRowKey(e, x.id)}
                  >
                    <td data-label="Time" className="nowrap">
                      <div className="mono num">{clock(x.at)}</div>
                      <div className="faint" style={{ fontSize: 11.5 }}>{relative(x.at, now)}</div>
                    </td>
                    <td data-label="Request">
                      <div className="acct">
                        <ProviderMark provider={x.provider} />
                        <div style={{ minWidth: 0 }}>
                          <div className="acct-name">{x.model}</div>
                          <div className="acct-sub">
                            <span className="mono">{x.session ? x.session.slice(0, 8) : "no session"}</span>
                            {x.session_kind && <span>· {x.session_kind}</span>}
                          </div>
                        </div>
                      </div>
                    </td>
                    <td data-label="Outcome">
                      <div style={{ display: "flex", gap: 6, flexWrap: "wrap" }}>
                        <Pill tone={toneFor(x.outcome)}>{outcomeLabel[x.outcome] ?? x.outcome}</Pill>
                        {!x.applied && x.outcome !== "delegated" && <Pill>shadow</Pill>}
                      </div>
                    </td>
                    <td data-label="Account" className="nowrap">{x.auth_id ? name(x.auth_id) : <span className="faint">CLIProxyAPI chose</span>}</td>
                    <td data-label="Why" className="hide-md">
                      <span style={whyStyle} title={whyText(x)}>{whyText(x) || <span className="faint">—</span>}</span>
                    </td>
                    <td data-label="Result" className="hide-sm nowrap">
                      {x.result ? (
                        <>
                          {name(x.result.auth_id)}{" "}
                          <span style={x.result.failed ? { color: "var(--critical)" } : undefined}>
                            {x.result.failed ? `HTTP ${x.result.status}` : "OK"}
                          </span>
                        </>
                      ) : (
                        <span className="faint">—</span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      {selectedId !== undefined && (
        <Drawer
          title={`Decision #${selectedId}`}
          subtitle={d ? `${absolute(d.at)} · ${providerName(d.provider)} · ${d.model}` : undefined}
          onClose={() => setSelectedId(undefined)}
        >
          {!d ? (
            fromFetch?.error ? (
              <Empty title="Decision not available">{fromFetch.error}</Empty>
            ) : (
              <Skeleton rows={4} />
            )
          ) : (
            <>
              <section>
                <h3 className="section-title">Outcome</h3>
                <p style={{ margin: "0 0 10px" }}>{outcomeSentence(d)}</p>
                <dl className="kv">
                  <dt>Chosen account</dt>
                  <dd>{d.auth_id ? dname(d.auth_id) : "CLIProxyAPI chose"}</dd>
                  {d.bound_auth_id && (
                    <>
                      <dt>Bound account</dt>
                      <dd>{dname(d.bound_auth_id)}</dd>
                    </>
                  )}
                  <dt>Policy</dt>
                  <dd>{policyLabel[d.policy] ?? d.policy}</dd>
                  <dt>Mode</dt>
                  <dd>{d.applied ? "Active (applied)" : "Shadow (proposal only)"}</dd>
                  <dt>Affinity overrode ranking</dt>
                  <dd>
                    {d.affinity_overrode_ranking ? "Yes" : "No"}
                    {d.affinity_overrode_ranking && (
                      <div className="faint" style={{ fontSize: 12, marginTop: 4 }}>
                        The bound account was kept even though another account ranked higher.
                      </div>
                    )}
                  </dd>
                  {d.fallback && (
                    <>
                      <dt>Fallback</dt>
                      <dd>No account had fresh quota; avoided known-unusable accounts</dd>
                    </>
                  )}
                  <dt>Stream</dt>
                  <dd>{d.stream ? "Yes" : "No"}</dd>
                </dl>
              </section>

              <section>
                <h3 className="section-title">Eligible accounts (ranked)</h3>
                {d.ranked && d.ranked.length > 0 ? (
                  <ol className="rank-list">
                    {d.ranked.map((r, i) => (
                      <li key={`${r.auth_id}-${i}`}>
                        <span className="n">{i + 1}</span>
                        <span className="r" style={{ fontWeight: r.auth_id === d.auth_id ? 650 : undefined }}>
                          {dname(r.auth_id)}
                        </span>
                        <span className="faint">{reason(r.reason)}</span>
                      </li>
                    ))}
                  </ol>
                ) : (
                  <div className="faint">None had fresh quota.</div>
                )}
              </section>

              <section>
                <h3 className="section-title">Skipped</h3>
                {d.skipped && d.skipped.length > 0 ? (
                  <ul style={{ margin: 0, paddingLeft: 18, fontSize: 12.5, display: "flex", flexDirection: "column", gap: 4 }}>
                    {d.skipped.map((s, i) => (
                      <li key={`${s.auth_id}-${i}`}>
                        <strong style={{ fontWeight: 600 }}>{dname(s.auth_id)}</strong>{" "}
                        <span className="faint">{reason(s.reason)}</span>
                      </li>
                    ))}
                  </ul>
                ) : (
                  <div className="faint">None</div>
                )}
              </section>

              <section>
                <h3 className="section-title">Observed result</h3>
                {d.result ? (
                  <dl className="kv">
                    <dt>Account</dt>
                    <dd>{dname(d.result.auth_id)}</dd>
                    <dt>HTTP status</dt>
                    <dd className="num" style={d.result.failed ? { color: "var(--critical)" } : undefined}>{d.result.status}</dd>
                    <dt>Outcome</dt>
                    <dd style={d.result.failed ? { color: "var(--critical)" } : undefined}>{d.result.failed ? "Failed" : "Succeeded"}</dd>
                    <dt>When</dt>
                    <dd>{relative(d.result.at, now)} <span className="faint">({absolute(d.result.at)})</span></dd>
                  </dl>
                ) : (
                  <div className="faint">No usage record matched this decision yet (requests without a session cannot be matched).</div>
                )}
              </section>
            </>
          )}
        </Drawer>
      )}
    </>
  );
}
