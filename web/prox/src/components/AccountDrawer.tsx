import { useState } from "react";
import { ApiError, request } from "../api";
import { absolute, label, pct, reason, relative } from "../format";
import { toast } from "../hooks";
import { applySettings } from "../settingsApi";
import type { Account, Settings } from "../types";
import { IconRefresh } from "./icons";
import { BurnLine, Drawer, ProviderMark, providerName, QuotaMeter, RoutingPill } from "./ui";

export function AccountDrawer({ a, now, onClose, onChanged }: { a: Account; now: number; onClose: () => void; onChanged: () => void }) {
  const [alias, setAlias] = useState(a.alias ?? "");
  const [priority, setPriority] = useState(String(a.plugin_priority));
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState("");
  const h = a.health;
  const windows = a.windows ?? [];

  async function save() {
    setSaving(true);
    setErr("");
    try {
      const s = await request<Settings>("/settings");
      const aliases = { ...((s.config.account_alias as Record<string, string>) ?? {}) };
      const prios = { ...((s.config.account_priority as Record<string, number>) ?? {}) };
      const trimmed = alias.trim();
      if (trimmed) aliases[a.id] = trimmed;
      else delete aliases[a.id];
      const p = Number(priority);
      if (!Number.isInteger(p)) throw new ApiError(0, "invalid", "Priority must be a whole number.");
      if (p !== 0) prios[a.id] = p;
      else delete prios[a.id];
      const v = await applySettings({ account_alias: aliases, account_priority: prios });
      if (!v.valid) {
        setErr((v.errors ?? []).map((e) => e.message).join("; "));
        return;
      }
      toast("Account settings saved to config.yaml");
      onChanged();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  }

  async function refresh() {
    try {
      await request("/refresh", { method: "POST", body: { auth_id: a.id } });
      toast("Quota refresh scheduled");
      setTimeout(onChanged, 1500);
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e), "critical");
    }
  }

  return (
    <Drawer
      onClose={onClose}
      title={
        <span style={{ display: "inline-flex", alignItems: "center", gap: 8 }}>
          <ProviderMark provider={a.provider} />
          {label(a)}
        </span>
      }
      subtitle={
        <>
          {providerName(a.provider)}
          {a.plan ? ` · ${a.plan}` : ""} · <RoutingPill a={a} />
        </>
      }
    >
      <section>
        <h3 className="section-title">Quota windows</h3>
        {windows.length === 0 ? (
          <div className="muted">No quota data yet. {a.poll_error ? `Last poll failed: ${a.poll_error}.` : "The first poll runs shortly after CPA lists the account."}</div>
        ) : (
          <div className="windows">
            {windows.map((w) => (
              <div className="window-card" key={w.key}>
                <QuotaMeter w={w} label={w.label} now={now} />
                <div className="faint" style={{ fontSize: 11.5 }}>
                  {pct(w.used_percent)} used · {w.resets_at ? absolute(w.resets_at) : "no reset reported"}
                  <br />
                  {w.source === "passive" ? "From live response headers" : "From quota endpoint"} {relative(w.observed_at, now)}
                  {w.stale && <span style={{ color: "var(--warning)" }}> · stale</span>}
                </div>
                <BurnLine e={a.burn?.[w.key]} w={w} now={now} />
              </div>
            ))}
          </div>
        )}
      </section>

      <section>
        <h3 className="section-title">Routing</h3>
        <dl className="kv">
          <dt>Status</dt>
          <dd>{a.routing === "eligible" ? "Eligible for new sessions" : reason(a.routing_reason) || a.routing}</dd>
          <dt>Quota state</dt>
          <dd>{a.quota_state.replace("_", " ")} <span className="faint">({reason(a.quota_reason) || a.quota_reason})</span></dd>
          <dt>New-session rank</dt>
          <dd>{a.rank ? `#${a.rank}${a.preferred ? " · next new session goes here" : ""}` : "Not ranked"}</dd>
          <dt>Bound sessions</dt>
          <dd className="num">{a.active_sessions}</dd>
          <dt>CPA priority</dt>
          <dd className="num">{a.host_priority}</dd>
          <dt>CPA status</dt>
          <dd>{a.disabled ? "Disabled" : a.host_status || "active"}{a.unavailable ? " · cooling down in CPA" : ""}{a.host_message ? ` · ${a.host_message}` : ""}</dd>
        </dl>
      </section>

      <section>
        <h3 className="section-title">Health</h3>
        <dl className="kv">
          <dt>Last success</dt>
          <dd>{relative(h.last_success_at, now)}</dd>
          <dt>Last failure</dt>
          <dd>{h.last_failure_at ? `${relative(h.last_failure_at, now)} · HTTP ${h.last_failure_status ?? "error"}` : "—"}</dd>
          {h.auth_failed && (<><dt>Login</dt><dd style={{ color: "var(--critical)" }}>Rejected by provider; re-login in CPA</dd></>)}
          {h.auth_suspect && (<><dt>Quota endpoint</dt><dd style={{ color: "var(--warning)" }}>Rejected the token (traffic may still work)</dd></>)}
          {h.rate_limited_until && (<><dt>Rate limited</dt><dd>until {absolute(h.rate_limited_until)}</dd></>)}
          {h.circuit_open_until && (<><dt>Paused</dt><dd>after repeated errors, until {absolute(h.circuit_open_until)}</dd></>)}
          {h.blocked_models && h.blocked_models.length > 0 && (<><dt>Unavailable models</dt><dd>{h.blocked_models.join(", ")}</dd></>)}
        </dl>
      </section>

      <section>
        <h3 className="section-title">Polling</h3>
        <dl className="kv">
          <dt>Last quota poll</dt>
          <dd>{a.last_refresh_at ? `${relative(a.last_refresh_at, now)} (${absolute(a.last_refresh_at)})` : "never succeeded"}</dd>
          <dt>Last live observation</dt>
          <dd>{relative(a.last_observed_at, now)}</dd>
          <dt>Next poll</dt>
          <dd>{a.next_poll_at ? relative(a.next_poll_at, now) : "not polled (custom endpoint, runtime-only, or disabled)"}</dd>
          {a.poll_error && (<><dt>Last poll error</dt><dd className="mono">{a.poll_error}</dd></>)}
          <dt>Credential ID</dt>
          <dd className="mono">{a.id}</dd>
        </dl>
        <div style={{ marginTop: 10 }}>
          <button className="btn sm" onClick={refresh}>
            <IconRefresh /> Refresh quota now
          </button>
        </div>
      </section>

      <section>
        <h3 className="section-title">Display and priority</h3>
        <div style={{ display: "grid", gridTemplateColumns: "1fr 120px", gap: 10 }}>
          <div className="field">
            <label htmlFor="alias">Alias</label>
            <input id="alias" className="input" value={alias} maxLength={40} placeholder={a.display} onChange={(e) => setAlias(e.target.value)} />
          </div>
          <div className="field">
            <label htmlFor="prio">Plugin priority</label>
            <input id="prio" className="input num" inputMode="numeric" value={priority} onChange={(e) => setPriority(e.target.value)} />
          </div>
        </div>
        <p className="hint">Aliases are shown instead of the redacted file name. Plugin priority applies only with the manual-priority policy, after CPA's own priority tiers. Saving writes to CPA's config.yaml.</p>
        {err && <div className="error-text" role="alert">{err}</div>}
        <div>
          <button className="btn primary sm" onClick={save} disabled={saving}>{saving ? "Saving…" : "Save"}</button>
        </div>
      </section>
    </Drawer>
  );
}
