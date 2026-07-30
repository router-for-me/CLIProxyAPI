# Ticket 0.2 — Frontend Vite skeleton

**Phase**: 0 — Foundation
**Blocks**: 0.3
**Blocked by**: 0.1
**Files touched** (new only):
- `tools/usage-dashboard/frontend/package.json`
- `tools/usage-dashboard/frontend/pnpm-lock.yaml`
- `tools/usage-dashboard/frontend/vite.config.ts`
- `tools/usage-dashboard/frontend/tsconfig.json`
- `tools/usage-dashboard/frontend/tsconfig.node.json`
- `tools/usage-dashboard/frontend/index.html`
- `tools/usage-dashboard/frontend/src/main.tsx`
- `tools/usage-dashboard/frontend/src/App.tsx`
- `tools/usage-dashboard/frontend/src/App.test.tsx`
- `tools/usage-dashboard/frontend/.gitignore`

**Files NOT touched**: anything outside `frontend/`

---

## 🎯 Goal

`cd frontend && pnpm install && pnpm dev` serves a React + TS placeholder at
`http://localhost:5173`. The dev server proxies `/api` and `/static` to
`http://127.0.0.1:8320` so when the legacy back end is also running, the
placeholder can already call `/api/v1/health`.

This ticket introduces **no** shadcn/ui, no Tailwind, no router, no state
library — those land in Phase 2/3. Only React + Vite + TS + Vitest.

---

## 🔴 Mandatory TDD discipline

Red → Green → Refactor, one commit per step. No exceptions.

---

## 🪜 Steps

### Step 1 — Red: App component test

```tsx
// frontend/src/App.test.tsx
import { render, screen } from "@testing-library/react";
import { describe, it, expect } from "vitest";
import App from "./App";

describe("App", () => {
  it("renders the dashboard placeholder", () => {
    render(<App />);
    expect(screen.getByText(/usage dashboard/i)).toBeInTheDocument();
  });
});
```

**Verify red**:
```bash
cd frontend
pnpm install   # fails: no package.json yet, or vitest not installed
```

Commit: `test(frontend): red — App placeholder`

### Step 2 — Green: scaffold Vite project

```bash
cd frontend
pnpm create vite@latest . --template react-ts
# overwrite when prompted; then add testing deps
pnpm add -D vitest @testing-library/react @testing-library/jest-dom jsdom
pnpm add @tanstack/react-query
```

`package.json` scripts:
```json
{
  "scripts": {
    "dev": "vite",
    "build": "tsc -b && vite build",
    "preview": "vite preview",
    "test": "vitest run",
    "test:watch": "vitest",
    "lint": "eslint src --ext ts,tsx",
    "typecheck": "tsc --noEmit"
  }
}
```

`vite.config.ts`:
```ts
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      "/api": "http://127.0.0.1:8320",
      "/static": "http://127.0.0.1:8320",
    },
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test-setup.ts"],
  },
});
```

`src/App.tsx`:
```tsx
export default function App() {
  return <h1>Usage Dashboard (Vite placeholder)</h1>;
}
```

`src/test-setup.ts`:
```ts
import "@testing-library/jest-dom/vitest";
```

**Verify green**:
```bash
pnpm test
```

Commit: `feat(frontend): vite + react + ts + vitest — green`

### Step 3 — Refactor: tsconfig strict + .gitignore

- `tsconfig.json`: set `"strict": true`, `"noUncheckedIndexedAccess": true`.
- `.gitignore`: `node_modules/`, `dist/`, `*.local`.

**Verify refactor**:
```bash
pnpm typecheck
pnpm build   # produces dist/, removed from git
```

Commit: `chore(frontend): strict tsconfig, gitignore`

---

## ✅ Ticket completion gate

| # | Check | Command |
|---|-------|---------|
| 1 | Lint | `pnpm lint` |
| 2 | Type Check | `pnpm typecheck` |
| 3 | Build | `pnpm build` (produces `dist/index.html`) |
| 4 | Unit Tests | `pnpm test` |
| 5 | Integration Tests | Run legacy back end in parallel terminal (`uv run python usage_dashboard.py serve`), then `pnpm dev`; open `:5173` and verify the proxy works via browser DevTools hitting `/api/v1/health` returns 200 |
| 6 | Functional Tests | Manual: placeholder text visible at `http://localhost:5173` |
| 7 | Contract Tests | N/A (no API contract yet) |
| 8 | E2E | N/A (placeholder only) |
| 9 | Code Review | Confirm no changes outside `frontend/` |

All green → move to Ticket 0.3.
