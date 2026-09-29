# Data Table Components

This package keeps a stable public API through `index.ts`; feature code should
continue importing from `@/components/data-table`.

- `core/`: TanStack table rendering primitives, headers, rows, pagination,
  loading, empty states, and pinned-column behavior.
- `layout/`: responsive page-level composition that combines toolbar, desktop
  table, mobile list, bulk actions, and pagination placement.
- `toolbar/`: filter/search/view-option controls and selection action toolbar.
- `static/`: lightweight table rendering for local/static arrays that do not
  need TanStack state.
- `hooks/`: table state and filter hooks.

Keep feature-specific columns, actions, and dialogs inside their feature
folders. Shared table code belongs here only when it is reusable across more
than one feature.
