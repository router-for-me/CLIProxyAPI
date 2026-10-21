# Repository Guidelines

## Project Scope & Structure

This is a React 19 + TypeScript + Vite management frontend for CLI Proxy API, not the proxy itself. It exclusively uses the backend v8 Management API under `/v8/management` and the v8 configuration layout; do not add v0 fallbacks or legacy config adapters. Plugin resources and custom HTTP extensions are exceptions: preserve their backend-declared paths. The one sanctioned v0 touchpoint is `src/services/api/legacyBackendProbe.ts`: after a v8 login 404 it only detects an outdated backend to show a clearer error, and must never supply application data or act as a login fallback.

- `src/features/`: feature-owned pages, components, hooks, types, and logic. Current features are `dashboard`, `providers`, `authFiles`, `oauth`, `quota`, `config`, `logs`, and `plugins`. Prefer this layout for new feature work.
- `src/pages/`: remaining route pages outside the feature layout (login, system, and the auth-file OAuth excluded-model/model-alias editors); `LogsPage.tsx` there is only a re-export of the logs feature. Follow nearby conventions when modifying these; do not migrate unrelated code.
- `src/components/`, `src/hooks/`, `src/utils/`: shared UI, hooks, and utilities. Keep feature-specific code within its feature rather than promoting it prematurely.
- `src/services/api/`: API client, domain endpoints, and backend data normalization. `src/services/storage/`: browser persistence.
- `src/stores/`: Zustand state. `src/types/`: shared types. `src/styles/`: global styles and theme tokens.
- `src/App.tsx`: hash-router setup. `src/router/MainRoutes.tsx`: authenticated route table. `ProtectedRoute` and `MainLayout` guard and wrap the authenticated app.
- `src/assets/`: bundled assets, including provider icons in `icons/`.
- `src/i18n/locales/`: `en.json`, `zh-CN.json`, `zh-TW.json`, `ru.json`, `vi.json`, and `ko.json`, registered in `src/i18n/index.ts`; fallback language is `zh-CN`. Update all six files when adding or changing translation keys, including accessible labels.

## Build, Test, and Development Commands

Use Bun; `package.json` pins `bun@1.3.14`, and CI uses Node.js 24. Keep dependency changes consistent with `bun.lock`; do not introduce another package manager's lockfile.

- `bun install --frozen-lockfile`: install locked dependencies.
- `bun run dev`: start Vite at `http://localhost:5173` by default.
- `bun run build`: TypeScript compilation followed by the production Vite build.
- `bun run preview`: serve the built output locally.
- `bun run test`: run all Bun tests.
- `bun test tests/apiError.test.ts`: example focused test run.
- `bun run lint`: run ESLint over TypeScript/TSX files. Some rules emit warnings; the current command does not enforce zero warnings.
- `bun run type-check`: run `tsc --noEmit`; the main TypeScript config includes `src`, not `tests`.
- `bun run verify`: run tests, lint, and build (which includes TypeScript compilation).
- `bun run format`: format all `src/**/*.{ts,tsx,css,scss}`. Prefer targeted formatting of changed files during routine work to avoid unrelated diffs.

## Deployment Constraints

The production artifact is a single `dist/index.html` with JS/CSS and bundled assets inlined by `vite-plugin-singlefile`. The release workflow renames it to `management.html` for backend hosting; `vX.Y.Z` tags trigger releases.

Preserve hash routing and single-file deployment. Changes to assets, imports, code splitting, or build configuration must not introduce required external build artifacts. Do not edit generated `dist/` files. App version is injected as `__APP_VERSION__` from `VERSION`, then git tags, then the package version, falling back to `dev`.

## API Contracts & State

- Treat backend contracts as the source of truth. Inspect `../CLIProxyAPI` before changing endpoint names, payloads, provider keys, OAuth callback parameters, auth-file semantics, or plugin/config contracts. If that checkout is unavailable, report the missing evidence rather than guessing; do not modify the backend unless requested.
- Reuse `apiClient` from `src/services/api/client.ts` for Management API requests through domain modules. It centralizes the API prefix, bearer authentication, error normalization, and response-header handling. Avoid bypassing it with ad hoc requests in components.
- Preserve the client's event integration: `unauthorized` handles 401s, `server-version-update` carries version/build metadata, and `server-plugin-support-update` carries plugin capability information. Keep plugin routes gated by backend support.
- Normalize backend fields on read and serialize on write in the API layer; consult `transformers.ts` and the relevant domain module. Keep raw backend field-name handling out of ordinary UI components.
- `useConfigStore.fetchConfig(forceRefresh?: boolean)` uses a full-config TTL cache and in-flight request deduplication, not section-based fetching. Reuse it where appropriate and invalidate/update caches after mutations using existing store actions.
- Preserve stale-request guards and cache cleanup when switching connections or logging out. Old asynchronous responses must not overwrite the new session's state.
- Provider UI capabilities live in `src/features/providers/descriptors.ts`; `adapters.ts` maps provider configs to the shared resource model. Extend these existing abstractions rather than scattering provider-specific conditionals across components.

## Coding Style & UI Conventions

Use 2-space indentation, semicolons, single quotes, ES5 trailing commas, and 100-character line width. Prefer typed React components and `unknown` with narrowing for untrusted data; avoid introducing `any` unless an unavoidable boundary requires it. Use the `@/` alias for `src` imports.

Component files use PascalCase, hooks use `useName`, and API modules use domain names such as `oauth.ts`. SCSS Modules sit beside their page or component as `Name.module.scss`. Vite automatically injects `src/styles/variables.scss` into SCSS; new modules do not need to import it again. Reuse shared components from `src/components/ui/` and existing theme tokens before adding new primitives or hard-coded colors.

Keep user-facing text in i18n. Preserve keyboard interaction, accessible names, focus behavior, and reduced-motion handling when modifying interactive UI. For motion, reuse the timing tokens in `src/styles/themes.scss` (`--ease-out-strong`, `--dur-press`, `--dur-hover`) and the helpers in `src/hooks/motion.ts` (`prefersReducedMotion`, `useRevealOnScroll`, `useRevealGroup`, `useCountUp`) instead of ad hoc durations and easings.

## Testing & Verification

Tests are centralized under `tests/` as `*.test.ts` and use `bun:test`; shared fixtures live in `tests/fixtures/` and helpers in `tests/helpers/`. Existing suites cover pure logic, React server-side static rendering via `renderToStaticMarkup`, and source/contract checks. There is no configured browser DOM test harness; static markup tests do not verify browser interactions. Prefer extracting testable logic and following nearby test patterns rather than introducing a new framework by default.

For code changes, add or update relevant regression tests, run focused tests while iterating, and run `bun run verify` before handoff. For UI changes, also verify the affected route in a browser and include screenshots or notes. Report commands actually run, failures, and anything not verified; if a backend or browser is unavailable, state the limitation explicitly. Documentation-only changes can be checked with diff/content validation instead of a full build.

## Security

Never commit real management keys, provider credentials, auth files, or other secrets; redact them from logs, screenshots, and test fixtures. Management keys are entered at runtime and persisted according to the remember-password setting. `src/services/storage/secureStorage.ts` provides reversible obfuscation, not encryption or a security boundary. Do not weaken authentication, plugin trust checks, or session isolation for convenience.

## Commits & Guidance Maintenance

Use Conventional Commits, such as `feat(providers): add a provider` or `fix(auth-files): preserve disabled actions`. Keep changes focused. Pull requests should include a summary, linked issue when applicable, backend version/reproduction details for integration work, UI screenshots or notes when relevant, and verification results.

Maintain shared repository guidance in `AGENTS.md`. When updating it, synchronize the local `CLAUDE.md` to identical content if present; `CLAUDE.md` is currently ignored and untracked, so shared guidance must not depend on it. Keep guidance aligned with source and configuration rather than duplicating long implementation details.
