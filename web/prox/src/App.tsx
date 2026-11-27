import { useEffect, useState, useSyncExternalStore, type ReactNode } from "react";
import { hasKey, onKeyChange, request, setKey } from "./api";
import { IconAccounts, IconActivity, IconLogout, IconMenu, IconMoon, IconOverview, IconRefresh, IconSearch, IconSessions, IconSettings, IconSun, IconUsage, Logo } from "./components/icons";
import { Banner, Pill, Seg } from "./components/ui";
import { relative } from "./format";
import { useNow, usePoll, useToasts } from "./hooks";
import { AccountsPage } from "./pages/Accounts";
import { ActivityPage } from "./pages/Activity";
import { OverviewPage } from "./pages/Overview";
import { SessionsPage } from "./pages/Sessions";
import { SettingsPage } from "./pages/Settings";
import { UsagePage } from "./pages/Usage";
import type { Overview, Provider } from "./types";

export type ProviderFilter = "all" | Provider;

export interface PageProps {
  overview: Overview | undefined;
  reloadOverview: () => Promise<void>;
  provider: ProviderFilter;
  search: string;
  now: number;
}

type Route = "overview" | "accounts" | "sessions" | "activity" | "usage" | "settings";
const routes: { id: Route; label: string; icon: () => ReactNode; group: string; filters: boolean }[] = [
  { id: "overview", label: "Overview", icon: IconOverview, group: "Monitor", filters: true },
  { id: "accounts", label: "Accounts & Quotas", icon: IconAccounts, group: "Monitor", filters: true },
  { id: "sessions", label: "Sessions", icon: IconSessions, group: "Monitor", filters: true },
  { id: "activity", label: "Activity", icon: IconActivity, group: "Analyze", filters: true },
  { id: "usage", label: "Usage", icon: IconUsage, group: "Analyze", filters: false },
  { id: "settings", label: "Settings", icon: IconSettings, group: "Configure", filters: false },
];

function currentRoute(): Route {
  const r = window.location.hash.replace(/^#\/?/, "") as Route;
  return routes.some((x) => x.id === r) ? r : "overview";
}

function useRoute(): [Route] {
  const [route, setRoute] = useState(currentRoute);
  useEffect(() => {
    const on = () => setRoute(currentRoute());
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
  }, []);
  return [route];
}

// Theme preference is not sensitive, so it may persist; the management key never does.
function initialTheme(): "dark" | "light" {
  try {
    const t = localStorage.getItem("qr-theme");
    if (t === "light" || t === "dark") return t;
  } catch {
    /* storage disabled */
  }
  return "dark";
}

export function App() {
  const authed = useSyncExternalStore(onKeyChange, hasKey);
  const [theme, setTheme] = useState(initialTheme);
  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    try {
      localStorage.setItem("qr-theme", theme);
    } catch {
      /* ignore */
    }
  }, [theme]);
  const toggle = () => setTheme((t) => (t === "dark" ? "light" : "dark"));
  return (
    <>
      {authed ? <Shell theme={theme} toggleTheme={toggle} /> : <Login />}
      <Toasts />
    </>
  );
}

function Login() {
  const [value, setValue] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    setKey(value.trim());
    try {
      await request("/settings");
      setValue("");
    } catch (err) {
      setKey("");
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }
  return (
    <main className="login">
      <form className="panel login-card" onSubmit={submit}>
        <div className="brand" style={{ padding: 0 }}>
          <span className="brand-mark"><Logo /></span>
          <div>
            <div className="brand-name">prox</div>
            <div className="brand-sub">CLIProxyAPI plugin</div>
          </div>
        </div>
        <h1>Connect with your management key</h1>
        <div className="field">
          <label htmlFor="key">Management key</label>
          <input id="key" className="input" type="password" autoComplete="off" spellCheck={false} value={value} onChange={(e) => setValue(e.target.value)} autoFocus required />
          <span className="hint">The same key as CLIProxyAPI's management panel. It stays in this tab's memory and is forgotten when you close or reload it.</span>
        </div>
        {error && <div className="error-text" role="alert">{error}</div>}
        <button className="btn primary" type="submit" disabled={busy || !value.trim()}>{busy ? "Checking…" : "Connect"}</button>
      </form>
    </main>
  );
}

function Shell({ theme, toggleTheme }: { theme: "dark" | "light"; toggleTheme: () => void }) {
  const [route] = useRoute();
  const [provider, setProvider] = useState<ProviderFilter>("all");
  const [search, setSearch] = useState("");
  const [navOpen, setNavOpen] = useState(false);
  const overview = usePoll<Overview>("/overview", 10000);
  const now = useNow();
  const meta = routes.find((r) => r.id === route)!;
  const ov = overview.data;
  useEffect(() => setNavOpen(false), [route]);
  useEffect(() => {
    document.title = `${meta.label} · prox`;
  }, [meta.label]);

  const groups = [...new Set(routes.map((r) => r.group))];
  const sessions = ov ? ov.summary.claude.active_sessions + ov.summary.codex.active_sessions : undefined;
  const healthy = ov && !ov.plugin.worker_failed && ov.plugin.worker_running && ov.roster.ok;

  return (
    <div className={`shell ${navOpen ? "nav-open" : ""}`}>
      <nav className="sidebar" aria-label="Main">
        <div className="brand">
          <span className="brand-mark"><Logo /></span>
          <div>
            <div className="brand-name">prox</div>
            <div className="brand-sub">CLIProxyAPI plugin{ov ? ` · v${ov.plugin.version}` : ""}</div>
          </div>
        </div>
        {groups.map((g) => (
          <div className="nav-group" key={g}>
            <div className="nav-label">{g}</div>
            {routes.filter((r) => r.group === g).map((r) => (
              <a key={r.id} href={`#/${r.id}`} className="nav-item" aria-current={route === r.id ? "page" : undefined}>
                <r.icon />
                {r.label}
                {r.id === "sessions" && sessions !== undefined && <span className="nav-badge num">{sessions}</span>}
                {r.id === "accounts" && ov && <span className="nav-badge num">{ov.summary.claude.total + ov.summary.codex.total}</span>}
              </a>
            ))}
          </div>
        ))}
        <div className="sidebar-foot">
          <div className="foot-row">
            <span>Routing</span>
            {ov ? <Pill tone={ov.mode === "active" ? "good" : "info"}>{ov.mode === "active" ? "Active" : "Shadow"}</Pill> : <span className="faint">—</span>}
          </div>
          <div className="foot-row">
            <span>Plugin</span>
            {ov ? (
              <Pill tone={healthy ? "good" : "critical"}>
                <span className={`dot ${healthy ? "good" : "critical"}`} />
                {healthy ? "Healthy" : "Degraded"}
              </Pill>
            ) : (
              <span className="faint">—</span>
            )}
          </div>
          <div className="foot-row">
            <button className="btn sm ghost" onClick={toggleTheme} aria-label={`Switch to ${theme === "dark" ? "light" : "dark"} theme`}>
              {theme === "dark" ? <IconSun /> : <IconMoon />} {theme === "dark" ? "Light" : "Dark"}
            </button>
            <button className="btn sm ghost" onClick={() => setKey("")} aria-label="Sign out and forget the management key">
              <IconLogout /> Sign out
            </button>
          </div>
        </div>
      </nav>
      {navOpen && <div className="overlay" style={{ zIndex: 45 }} onClick={() => setNavOpen(false)} />}
      <div className="main">
        <header className="topbar">
          <button className="btn icon ghost menu-btn" aria-label="Open navigation" onClick={() => setNavOpen(true)}>
            <IconMenu />
          </button>
          <h1>{meta.label}</h1>
          {meta.filters && (
            <>
              <Seg
                label="Provider"
                value={provider}
                onChange={setProvider}
                options={[
                  { value: "all", label: "All" },
                  { value: "claude", label: "Claude" },
                  { value: "codex", label: "Codex" },
                ]}
              />
              <div className="search">
                <IconSearch />
                <input className="input" type="search" placeholder="Search accounts, models…" aria-label="Search" value={search} onChange={(e) => setSearch(e.target.value)} />
              </div>
            </>
          )}
          <span className="spacer" />
          <span className="faint nowrap hide-sm" aria-live="polite">
            {overview.error ? <span style={{ color: "var(--critical)" }}>Update failed</span> : overview.updatedAt ? `Updated ${relative(new Date(overview.updatedAt).toISOString(), now)}` : "Loading…"}
          </span>
          <button className="btn icon" aria-label="Reload data" onClick={() => void overview.reload()} disabled={overview.loading}>
            <IconRefresh />
          </button>
        </header>
        <main className="content" id="main">
          {overview.error && overview.error.status !== 401 && (
            <Banner tone="critical">
              {overview.error.message} Showing the last data received{overview.updatedAt ? ` (${relative(new Date(overview.updatedAt).toISOString(), now)})` : ""}.
            </Banner>
          )}
          {ov && ov.mode === "shadow" && route !== "settings" && (
            <Banner tone="info">
              Shadow mode: decisions are recorded but CLIProxyAPI still chooses accounts. Switch to active in <a href="#/settings">Settings</a> when the proposed routing looks right.
            </Banner>
          )}
          {ov && (!ov.plugin.state_writable || ov.plugin.last_save_error) && (
            <Banner tone="warning">
              Session bindings are not being saved ({ov.plugin.state_reason || ov.plugin.last_save_error}). They will not survive a restart. Check <code>state_dir</code> and that only one proxy uses it.
            </Banner>
          )}
          <Page route={route} props={{ overview: ov, reloadOverview: overview.reload, provider, search, now }} />
        </main>
      </div>
    </div>
  );
}

function Page({ route, props }: { route: Route; props: PageProps }) {
  switch (route) {
    case "accounts":
      return <AccountsPage {...props} />;
    case "sessions":
      return <SessionsPage {...props} />;
    case "activity":
      return <ActivityPage {...props} />;
    case "usage":
      return <UsagePage {...props} />;
    case "settings":
      return <SettingsPage {...props} />;
    default:
      return <OverviewPage {...props} />;
  }
}

function Toasts() {
  const list = useToasts();
  return (
    <div className="toast-stack" role="status" aria-live="polite">
      {list.map((t) => (
        <div key={t.id} className={`toast ${t.tone ?? ""}`}>{t.text}</div>
      ))}
    </div>
  );
}
