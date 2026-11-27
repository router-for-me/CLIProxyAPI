import { useState } from "react";
import type { PageProps } from "../App";
import { AccountDrawer } from "../components/AccountDrawer";
import { AccountTable } from "../components/AccountTable";
import { IconRefresh } from "../components/icons";
import { Empty, Seg, Skeleton } from "../components/ui";
import { request } from "../api";
import { toast } from "../hooks";
import { matches, refreshAccount } from "./Overview";

type StatusFilter = "all" | "eligible" | "limited" | "blocked";

export function AccountsPage({ overview, reloadOverview, provider, search, now }: PageProps) {
  const [open, setOpen] = useState<string>();
  const [status, setStatus] = useState<StatusFilter>("all");
  if (!overview) return <div className="panel"><Skeleton rows={8} /></div>;
  const list = overview.accounts.filter((a) => matches(a, provider, search) && (status === "all" || (status === "blocked" ? a.routing === "blocked" || a.routing === "removed" || a.quota_state === "exhausted" : a.routing === status && a.quota_state !== "exhausted")));
  const opened = overview.accounts.find((a) => a.id === open);
  async function refreshAll() {
    try {
      const r = await request<{ scheduled: number }>("/refresh", { method: "POST", body: {} });
      toast(`Refreshing ${r.scheduled} account${r.scheduled === 1 ? "" : "s"}…`);
      setTimeout(reloadOverview, 2500);
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e), "critical");
    }
  }
  return (
    <>
      <section className="panel">
        <div className="panel-head" style={{ flexWrap: "wrap" }}>
          <h2>All accounts</h2>
          <span className="sub num">{list.length} shown</span>
          <span style={{ flex: 1 }} />
          <Seg label="Status" value={status} onChange={setStatus} options={[
            { value: "all", label: "All" },
            { value: "eligible", label: "Eligible" },
            { value: "limited", label: "Limited" },
            { value: "blocked", label: "Exhausted / blocked" },
          ]} />
          <button className="btn sm" onClick={refreshAll}><IconRefresh /> Refresh all</button>
        </div>
        {list.length === 0 ? (
          <Empty title="No accounts match">Change the provider, status, or search filters.</Empty>
        ) : (
          <AccountTable accounts={list} now={now} onOpen={(a) => setOpen(a.id)} onRefresh={(a) => refreshAccount(a, reloadOverview)} selected={open} initialSort={{ key: "reset", dir: "asc" }} />
        )}
      </section>
      <p className="faint" style={{ margin: 0, fontSize: 12 }}>
        Bars show capacity <strong>remaining</strong>. A striped bar means the data is stale or its reset time has passed and fresh data has not arrived yet. A yellow edge marks accounts whose usable quota resets within 24 hours.
      </p>
      {opened && <AccountDrawer a={opened} now={now} onClose={() => setOpen(undefined)} onChanged={reloadOverview} />}
    </>
  );
}
