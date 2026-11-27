import { useEffect, useMemo, useState } from "react";
import type { PageProps } from "../App";
import { request } from "../api";
import { IconDownload } from "../components/icons";
import { Banner, Dialog, Pill, ProviderMark, providerName, Seg, Skeleton, Switch } from "../components/ui";
import { clock, outcomeLabel, policyLabel, reason } from "../format";
import { toast, usePoll } from "../hooks";
import { applySettings, validate, type Validation } from "../settingsApi";
import type { Decision, NextUp, Provider, SchemaField, Settings } from "../types";

type Draft = Record<string, unknown>;

const groupOrder = ["Routing", "Affinity", "Policies", "Quota", "Diagnostics", "Storage"];
const enumLabels: Record<string, string> = {
  ...policyLabel,
  shadow: "Shadow (record only)",
  active: "Active (route requests)",
  model: "Per model",
  provider: "Per provider (all models)",
  conservative: "Conservative",
  eager: "Eager",
  never: "Never",
};
const disruptive = new Set(["mode", "affinity_enabled", "affinity_scope", "affinity_max_sessions", "affinity_ttl"]);

export function SettingsPage({ reloadOverview }: PageProps) {
  const q = usePoll<Settings>("/settings", 60000);
  const [draft, setDraft] = useState<Draft>({});
  const [check, setCheck] = useState<Validation>();
  const [saving, setSaving] = useState(false);
  const [confirm, setConfirm] = useState(false);
  const s = q.data;

  const patch = useMemo(() => {
    if (!s) return {};
    const out: Draft = {};
    for (const [k, v] of Object.entries(draft)) if (JSON.stringify(s.config[k]) !== JSON.stringify(v)) out[k] = v;
    return out;
  }, [draft, s]);
  const dirty = Object.keys(patch).length > 0;

  useEffect(() => {
    if (!dirty) {
      setCheck(undefined);
      return;
    }
    const t = window.setTimeout(() => {
      validate(patch).then(setCheck, (e) => setCheck({ valid: false, errors: [{ field: "", message: e.message }] }));
    }, 250);
    return () => window.clearTimeout(t);
  }, [patch, dirty]);

  if (!s) return <div className="panel"><Skeleton rows={10} /></div>;
  const value = (k: string) => (k in draft ? draft[k] : s.config[k]);
  const set = (k: string, v: unknown) => setDraft((d) => ({ ...d, [k]: v }));
  const errFor = (k: string) => check?.errors?.find((e) => e.field === k)?.message;
  const needsConfirm = Object.keys(patch).some((k) => disruptive.has(k));

  async function save() {
    setSaving(true);
    try {
      const v = await applySettings(patch);
      if (!v.valid) {
        setCheck(v);
        return;
      }
      toast("Saved to config.yaml; plugin reloaded with the new settings");
      setDraft({});
      setConfirm(false);
      await q.reload();
      await reloadOverview();
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e), "critical");
    } finally {
      setSaving(false);
    }
  }

  async function exportDiagnostics() {
    try {
      const data = await request<unknown>("/export");
      const blob = new Blob([JSON.stringify(data, null, 2)], { type: "application/json" });
      const a = document.createElement("a");
      a.href = URL.createObjectURL(blob);
      a.download = `prox-diagnostics-${new Date().toISOString().slice(0, 19).replace(/[:T]/g, "-")}.json`;
      a.click();
      setTimeout(() => URL.revokeObjectURL(a.href), 1000);
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e), "critical");
    }
  }

  const groups = groupOrder.map((g) => ({ g, fields: s.schema.filter((f) => f.group === g) })).filter((x) => x.fields.length);
  return (
    <>
      <div className="settings-grid">
        <div style={{ display: "flex", flexDirection: "column", gap: 16 }}>
          <p className="faint" style={{ margin: 0, fontSize: 12 }}>
            Settings are saved to CLIProxyAPI's <code>config.yaml</code> under <code>plugins.configs.prox</code> and applied by a hot reload. Bindings, quota data, and history are kept across the reload.
          </p>
          {groups.map(({ g, fields }) => (
            <section className="panel" key={g} aria-labelledby={`g-${g}`}>
              <div className="panel-head"><h2 id={`g-${g}`}>{g}</h2></div>
              {fields.map((f) => (
                <Field key={f.name} f={f} value={value(f.name)} onChange={(v) => set(f.name, v)} changed={f.name in patch} error={errFor(f.name)} />
              ))}
            </section>
          ))}
        </div>
        <div style={{ display: "flex", flexDirection: "column", gap: 16, position: "sticky", top: 70 }}>
          <Simulator draftStrategy={String(value("strategy"))} current={String(s.config.strategy)} />
          <section className="panel">
            <div className="panel-head"><h2>Diagnostics</h2></div>
            <div className="panel-body" style={{ display: "flex", flexDirection: "column", gap: 8 }}>
              <span className="muted" style={{ fontSize: 12 }}>A JSON bundle of plugin state for bug reports. Accounts are replaced by numbered handles; it contains no tokens, keys, e-mail addresses, or session IDs.</span>
              <div><button className="btn sm" onClick={exportDiagnostics}><IconDownload /> Export diagnostics</button></div>
              <span className="faint mono" style={{ fontSize: 11 }}>config generation {s.generation}</span>
            </div>
          </section>
        </div>
      </div>
      {dirty && (
        <div className="savebar" role="region" aria-label="Unsaved changes">
          <div style={{ flex: 1, minWidth: 0 }}>
            <strong>{Object.keys(patch).length} unsaved change{Object.keys(patch).length === 1 ? "" : "s"}</strong>
            <div className="muted" style={{ fontSize: 12, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
              {check === undefined ? "Checking…" : check.valid ? (check.changes ?? []).map((c) => `${c.field}: ${c.effect}`).join(" · ") : <span className="error-text">{(check.errors ?? []).map((e) => (e.field ? `${e.field}: ` : "") + e.message).join("; ")}</span>}
            </div>
          </div>
          <button className="btn" onClick={() => setDraft({})} disabled={saving}>Discard</button>
          <button className="btn primary" disabled={saving || !check?.valid} onClick={() => (needsConfirm ? setConfirm(true) : void save())}>
            {saving ? "Applying…" : "Apply"}
          </button>
        </div>
      )}
      {confirm && (
        <Dialog
          title="Apply routing changes?"
          onClose={() => setConfirm(false)}
          actions={
            <>
              <button className="btn" onClick={() => setConfirm(false)}>Cancel</button>
              <button className="btn primary" onClick={() => void save()} disabled={saving} data-autofocus>{saving ? "Applying…" : "Apply now"}</button>
            </>
          }
        >
          <p className="muted" style={{ margin: 0 }}>These settings change how live traffic is routed:</p>
          <ul style={{ margin: 0, paddingLeft: 18, display: "flex", flexDirection: "column", gap: 6 }}>
            {(check?.changes ?? []).filter((c) => disruptive.has(c.field)).map((c) => (
              <li key={c.field}>
                <strong>{c.field}</strong>: {String(c.from)} → {String(c.to)}
                <div className="faint" style={{ fontSize: 12 }}>{c.effect}</div>
              </li>
            ))}
          </ul>
        </Dialog>
      )}
    </>
  );
}

function Field({ f, value, onChange, changed, error }: { f: SchemaField; value: unknown; onChange: (v: unknown) => void; changed: boolean; error?: string }) {
  const id = `f-${f.name}`;
  let control: React.ReactNode;
  if (f.type === "boolean") {
    control = <Switch checked={Boolean(value)} onChange={onChange} label={f.name} />;
  } else if (f.type === "enum") {
    control = (
      <select id={id} className="input" value={String(value)} onChange={(e) => onChange(e.target.value)}>
        {(f.enum ?? []).map((v) => <option key={v} value={v}>{enumLabels[v] ?? v}</option>)}
      </select>
    );
  } else if (f.type === "integer" || f.type === "number") {
    control = (
      <input id={id} className="input num" inputMode="decimal" value={String(value ?? "")} aria-invalid={!!error}
        onChange={(e) => {
          const n = Number(e.target.value);
          onChange(e.target.value.trim() === "" || Number.isNaN(n) ? e.target.value : n);
        }} />
    );
  } else {
    control = <input id={id} className="input mono" value={String(value ?? "")} aria-invalid={!!error} spellCheck={false} onChange={(e) => onChange(e.target.value)} />;
  }
  return (
    <div className={`setting ${changed ? "changed" : ""}`}>
      <div>
        <div className="setting-name">
          <label htmlFor={f.type === "boolean" ? undefined : id} className="mono" style={{ fontSize: 12.5 }}>{f.name}</label>
          {f.restart && <Pill>restart</Pill>}
          {changed && <Pill tone="info">changed</Pill>}
        </div>
        <div className="setting-desc">{f.description}</div>
        <div className="setting-effect">{f.effect}{f.min && f.max ? ` Range ${f.min}–${f.max}.` : ""}</div>
        {error && <div className="error-text" role="alert">{error}</div>}
      </div>
      <div className="setting-control">{control}</div>
    </div>
  );
}

interface SimResult {
  current_policy: string;
  mode: string;
  results: NextUp[] | null;
  recent: Decision[] | null;
  labels: Record<string, string>;
}

function Simulator({ draftStrategy, current }: { draftStrategy: string; current: string }) {
  const [provider, setProvider] = useState<Provider>("claude");
  const [model, setModel] = useState("");
  const [res, setRes] = useState<SimResult>();
  const [err, setErr] = useState("");
  useEffect(() => {
    let live = true;
    const t = window.setTimeout(() => {
      request<SimResult>("/simulate", { method: "POST", body: { provider, model: model.trim() } }).then(
        (r) => live && (setRes(r), setErr("")),
        (e) => live && setErr(e.message),
      );
    }, 300);
    return () => {
      live = false;
      window.clearTimeout(t);
    };
  }, [provider, model, current]);
  const name = (id?: string) => (id ? res?.labels[id] ?? id : "CLIProxyAPI decides");
  return (
    <section className="panel" aria-labelledby="sim">
      <div className="panel-head" style={{ flexWrap: "wrap" }}>
        <h2 id="sim">Policy preview</h2>
        <span className="sub">no changes applied</span>
      </div>
      <div className="panel-body" style={{ display: "flex", flexDirection: "column", gap: 12 }}>
        <div style={{ display: "flex", gap: 8, flexWrap: "wrap" }}>
          <Seg label="Provider" value={provider} onChange={setProvider} options={[
            { value: "claude", label: <><ProviderMark provider="claude" />Claude</> },
            { value: "codex", label: <><ProviderMark provider="codex" />Codex</> },
          ]} />
          <input className="input" style={{ flex: 1, minWidth: 120 }} placeholder="model (optional)" aria-label="Model to simulate" value={model} onChange={(e) => setModel(e.target.value)} />
        </div>
        {err && <Banner tone="critical">{err}</Banner>}
        {!res ? (
          <div className="skeleton" />
        ) : (
          <table className="grid">
            <caption className="sr-only">Where a new {providerName(provider)} session would go under each policy</caption>
            <thead><tr><th scope="col">Policy</th><th scope="col">New session goes to</th></tr></thead>
            <tbody>
              {(res.results ?? []).map((r) => (
                <tr key={r.policy} className={r.policy === draftStrategy ? "selected" : ""}>
                  <td className="nowrap">
                    {policyLabel[r.policy] ?? r.policy}
                    {r.policy === current && <div className="faint" style={{ fontSize: 11 }}>current</div>}
                  </td>
                  <td>
                    <div style={{ fontWeight: 600 }}>{name(r.auth_id)}</div>
                    <div className="faint" style={{ fontSize: 11.5 }}>{r.fallback ? r.reason : r.ranked?.[0]?.reason ?? r.reason}</div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        {res && (res.recent ?? []).length > 0 && (
          <div>
            <h3 className="section-title" style={{ marginTop: 4 }}>Recent new-session routing: proposed vs actual</h3>
            <ul style={{ listStyle: "none", margin: 0, padding: 0, display: "flex", flexDirection: "column", gap: 4, fontSize: 12 }}>
              {(res.recent ?? []).slice(0, 8).map((d) => {
                const actual = d.result?.auth_id;
                const same = !actual || actual === d.auth_id;
                return (
                  <li key={d.id} style={{ display: "grid", gridTemplateColumns: "88px 1fr", gap: 8 }}>
                    <span className="faint mono">{clock(d.at)}</span>
                    <span>
                      <span className="muted">{outcomeLabel[d.outcome] ?? d.outcome}:</span> {d.auth_id ? name(d.auth_id) : <span className="faint">{reason(d.cause)}</span>}
                      {actual && !same && <> → <span style={{ color: "var(--warning)" }}>served by {name(actual)}</span></>}
                      {!d.applied && d.auth_id && <span className="faint"> (proposed)</span>}
                    </span>
                  </li>
                );
              })}
            </ul>
          </div>
        )}
      </div>
    </section>
  );
}
