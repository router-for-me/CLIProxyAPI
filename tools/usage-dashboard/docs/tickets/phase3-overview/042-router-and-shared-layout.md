# Ticket 3.2 — Router and shared layout

**Phase**: 3 — Overview
**Blocks**: 3.3, 3.4, 3.7
**Blocked by**: 3.1
**Files touched**:
- `tools/usage-dashboard/frontend/src/main.tsx` (add RouterProvider + QueryClientProvider)
- `tools/usage-dashboard/frontend/src/App.tsx` (becomes Router config)
- `tools/usage-dashboard/frontend/src/pages/Dashboard.tsx` (new placeholder)
- `tools/usage-dashboard/frontend/src/pages/Usage.tsx` (new placeholder)
- `tools/usage-dashboard/frontend/src/components/Header.tsx` (new)
- `tools/usage-dashboard/frontend/src/components/Layout.tsx` (new)

---

## 🎯 Goal

React Router v6 with routes `/` and `/usage`, sharing a `Layout` with the
`Header` (view nav, time-range selector, refresh button). Header state
(drawer visibility, current range, etc.) lives in a Zustand `uiStore`,
added in Ticket 3.3.

---

## 🔴 Mandatory TDD discipline

Red → Green → Refactor. The first test is a route-level render test.

---

## 🪜 Steps

### Step 1 — Red: routing test

```tsx
// frontend/src/App.test.tsx
import { render, screen } from "@testing-library/react";
import { createMemoryHistory } from "history";
import { RouterProvider, createMemoryRouter } from "react-router-dom";
import { describe, it, expect } from "vitest";
import App from "./App";

describe("App routing", () => {
  it("renders Dashboard at /", async () => {
    const router = createMemoryRouter([{ path: "/", element: <App /> }]);
    render(<RouterProvider router={router} />);
    expect(await screen.findByText(/overview placeholder/i)).toBeInTheDocument();
  });

  it("renders Usage at /usage", async () => {
    const router = createMemoryRouter(
      [{ path: "/usage", element: <App /> }],
      { initialEntries: ["/usage"] },
    );
    render(<RouterProvider router={router} />);
    expect(await screen.findByText(/usage detail placeholder/i)).toBeInTheDocument();
  });

  it("shows nav with links to / and /usage", () => {
    render(<App />);
    expect(screen.getByRole("link", { name: /overview|概览/i })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /usage|用量明细/i })).toBeInTheDocument();
  });
});
```

**Verify red**: `pnpm test` fails — `App.tsx` has no router.

Commit: `test(routing): red — routes + nav`

### Step 2 — Green: install router + implement

```bash
pnpm add react-router-dom@^6
```

`src/App.tsx`:
```tsx
import { NavLink, Outlet } from "react-router-dom";
import { Header } from "./components/Header";

export default function App() {
  return (
    <div className="min-h-screen bg-background text-foreground">
      <Header />
      <main className="p-4">
        <Outlet />
      </main>
    </div>
  );
}
```

`src/main.tsx`:
```tsx
import React from "react";
import ReactDOM from "react-dom/client";
import { createBrowserRouter, RouterProvider } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import App from "./App";
import { Dashboard } from "./pages/Dashboard";
import { Usage } from "./pages/Usage";
import "./styles/globals.css";

const router = createBrowserRouter([
  {
    path: "/",
    element: <App />,
    children: [
      { index: true, element: <Dashboard /> },
      { path: "usage", element: <Usage /> },
    ],
  },
]);

const qc = new QueryClient();

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </React.StrictMode>,
);
```

`src/components/Header.tsx`:
```tsx
import { NavLink } from "react-router-dom";

export function Header() {
  return (
    <header className="border-b border-border bg-card">
      <div className="mx-auto flex max-w-7xl items-center gap-4 px-4 py-3">
        <h1 className="text-sm font-semibold">CLIProxyAPI 用量统计</h1>
        <nav className="flex gap-2">
          <NavLink to="/" end className={({isActive}) =>
            `px-2 py-1 rounded ${isActive ? "bg-primary text-primary-foreground" : "text-muted-foreground"}`}>
            概览
          </NavLink>
          <NavLink to="/usage" className={({isActive}) =>
            `px-2 py-1 rounded ${isActive ? "bg-primary text-primary-foreground" : "text-muted-foreground"}`}>
            用量明细
          </NavLink>
        </nav>
      </div>
    </header>
  );
}
```

`src/pages/Dashboard.tsx` and `src/pages/Usage.tsx`: minimal placeholders
that render `overview placeholder` and `usage detail placeholder`.

**Verify green**:
```bash
pnpm test
pnpm build
```

Commit: `feat(routing): router + shared layout — green`

### Step 3 — Refactor: lazy-load pages

```tsx
const Dashboard = lazy(() => import("./pages/Dashboard").then(m => ({default: m.Dashboard})));
const Usage = lazy(() => import("./pages/Usage").then(m => ({default: m.Usage})));
```

Wrap `<Outlet />` in `<Suspense fallback={<div>加载中…</div>}>`.

Verify `pnpm build` produces separate chunks.

Commit: `perf(routing): lazy-load page components`

---

## ✅ Ticket completion gate

| # | Check | Command |
|---|-------|---------|
| 1 | Lint | `pnpm lint` |
| 2 | Type Check | `pnpm typecheck` |
| 3 | Build | `pnpm build` (2 chunks: Dashboard, Usage) |
| 4 | Unit Tests | `pnpm test` (App.test.tsx, all 3 tests pass) |
| 5 | Integration Tests | Manual: navigate `/` and `/usage`, both render |
| 6 | Functional Tests | Active nav link highlights current route |
| 7 | Contract Tests | N/A |
| 8 | E2E | N/A (placeholder pages) |
| 9 | Code Review | No state managed in App; state stays in stores (Ticket 3.3) |

All green → Tickets 3.3, 3.4, 3.7.
