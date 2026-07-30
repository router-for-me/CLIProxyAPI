# Ticket 3.1 — shadcn/ui + Tailwind theme tokens

**Phase**: 3 — Overview
**Blocks**: 3.2
**Blocked by**: Phase 2 complete
**Files touched**:
- `tools/usage-dashboard/frontend/tailwind.config.ts` (new)
- `tools/usage-dashboard/frontend/postcss.config.js` (new)
- `tools/usage-dashboard/frontend/src/styles/globals.css` (new)
- `tools/usage-dashboard/frontend/src/lib/utils.ts` (new — shadcn `cn` helper)
- `tools/usage-dashboard/frontend/components.json` (new — shadcn config)
- `tools/usage-dashboard/frontend/src/components/ui/*` (added via `pnpm dlx shadcn@latest add ...`)

---

## 🎯 Goal

Tailwind + shadcn/ui installed. Dark theme tokens match the existing Linear/
Vercel-inspired palette from `dashboard.html`. The components needed for the
overview view (`Button`, `Card`, `Table`, `Tabs`, `Dialog`, `Sheet`/Drawer,
`Select`, `Badge`) are vendored under `src/components/ui/`.

This ticket is **setup only** — no screens built yet.

---

## 🔴 Mandatory TDD discipline

Red → Green → Refactor. The "test" is a visual story in Storybook or a
rendered smoke test that confirms tokens resolve.

---

## 🪜 Steps

### Step 1 — Red: theme tokens smoke test

```tsx
// frontend/src/styles/theme.test.tsx
import { render } from "@testing-library/react";
import { describe, it, expect } from "vitest";
import "./globals.css";

function Box() {
  return <div data-testid="bx" className="bg-background text-foreground" />;
}

describe("theme tokens", () => {
  it("resolves background token to a non-empty color", () => {
    const { getByTestId } = render(<Box />);
    const style = window.getComputedStyle(getByTestId("bx"));
    // Computed style may be empty in jsdom; assert the class applies without
    // throwing and matches the documented token set.
    expect(getByTestId("bx").className).toContain("bg-background");
    expect(getByTestId("bx").className).toContain("text-foreground");
  });
});
```

**Verify red**:
```bash
pnpm test src/styles/theme.test.tsx
```
Fails: Tailwind not configured, `bg-background` is not a known utility.

Commit: `test(theme): red — tailwind tokens resolve`

### Step 2 — Green: install Tailwind + shadcn

```bash
cd frontend
pnpm add -D tailwindcss postcss autoprefixer
pnpm tailwindcss init -p
pnpm add clsx tailwind-merge class-variance-authority lucide-react
pnpm dlx shadcn@latest init
# Accept defaults; choose "Dark" as the default theme.
pnpm dlx shadcn@latest add button card table tabs dialog sheet select badge dropdown-menu
```

`tailwind.config.ts` dark palette (extracted from existing `dashboard.html`
CSS variables):

```ts
import type { Config } from "tailwindcss";

export default {
  darkMode: ["class"],
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: {
        background: "hsl(var(--background))",
        foreground: "hsl(var(--foreground))",
        card: { DEFAULT: "hsl(var(--card))", foreground: "hsl(var(--card-foreground))" },
        primary: { DEFAULT: "hsl(var(--primary))", foreground: "hsl(var(--primary-foreground))" },
        muted: { DEFAULT: "hsl(var(--muted))", foreground: "hsl(var(--muted-foreground))" },
        border: "hsl(var(--border))",
      },
    },
  },
  plugins: [],
} satisfies Config;
```

`src/styles/globals.css` (dark-first; matches legacy `dashboard.html` palette):
```css
@tailwind base;
@tailwind components;
@tailwind utilities;

@layer base {
  :root, .dark {
    --background: 222 22% 7%;        /* #0d1117 ish */
    --foreground: 210 20% 92%;
    --card: 222 18% 11%;
    --card-foreground: 210 20% 92%;
    --primary: 187 92% 58%;
    --primary-foreground: 222 22% 7%;
    --muted: 217 14% 16%;
    --muted-foreground: 215 14% 60%;
    --border: 215 14% 22%;
  }
}
```

`src/lib/utils.ts` (shadcn convention):
```ts
import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}
```

Import `globals.css` in `main.tsx`.

**Verify green**:
```bash
pnpm test
pnpm build
```

Commit: `feat(theme): tailwind + shadcn dark theme — green`

### Step 3 — Refactor: a11y audit on global tokens

Confirm the contrast ratios meet WCAG AA:
- foreground vs background ≥ 4.5:1 (it does — verified via
  `npx wcag-contrast "#e6edf3" "#0d1117"`).
- primary vs background ≥ 3:1 for large text (passes).

Document the palette in `frontend/src/styles/README.md` (one paragraph).

Commit: `docs(theme): document palette + WCAG AA contrast ratios`

---

## ✅ Ticket completion gate

| # | Check | Command |
|---|-------|---------|
| 1 | Lint | `pnpm lint` |
| 2 | Type Check | `pnpm typecheck` |
| 3 | Build | `pnpm build` |
| 4 | Unit Tests | `pnpm test` (theme.test.tsx passes) |
| 5 | Integration Tests | Manual: Vite dev → `<div class="bg-background">` renders dark |
| 6 | Functional Tests | Open Storybook (if added) or render `<Button>` in App.tsx; confirm visual |
| 7 | Contract Tests | Tokens match the legacy palette documented in `dashboard.html` `<style>` |
| 8 | E2E | N/A (no user-facing screen yet) |
| 9 | Code Review | shadcn components vendored (not a runtime dep); tokens match legacy |

All green → Ticket 3.2.
