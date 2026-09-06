# API Key Entry On/Off Toggle Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a per-entry on/off (disabled) toggle to the Upstream Providers API key entries editor; disabled entries are persisted in PG but excluded from the rendered config.

**Architecture:** A new `Disabled bool` field flows config types → PG store (nullable-safe migration) → management handler → renderer. The renderer **skips** disabled entries when projecting a provider row into `config.yaml` (data stays in PG, so toggling back re-activates). The dashboard entries editor gets a per-row switch that mutates form state; commit happens on Save (no new endpoint).

**Tech Stack:** Go 1.26 (config/store/management/upstreamsync), React (Vite SPA), node:test.

**Design doc:** `docs/plans/2026-09-07-api-key-entry-toggle-design.md`
**Worktree:** `/home/bilfid/projects/nixllm/.worktrees/api-key-entry-toggle` (branch `feat/api-key-entry-toggle`)

---

## Task 1: Config types — `Disabled` on entry structs

**Files:**
- Modify: `internal/config/config_types.go` (`OpenAICompatibilityAPIKey` ~line 777, `ClaudeKey` — find `type ClaudeKey struct`)

**Step 1: Add field to `OpenAICompatibilityAPIKey`** (after the `Priority` field):

```go
	// Disabled excludes this entry from routing without deleting it. The
	// upstreamsync renderer skips disabled entries when projecting the
	// provider row into config.yaml, so the credential stays persisted and
	// re-activates when toggled back.
	Disabled bool `yaml:"disabled,omitempty" json:"disabled,omitempty"`
```

**Step 2: Add the same field to `ClaudeKey`** (next to its `Weight`/`Priority` fields):

```go
	// Disabled excludes this entry from routing without deleting it. Set
	// per-entry on the Claude fan-out; the renderer skips disabled items.
	Disabled bool `yaml:"disabled,omitempty" json:"disabled,omitempty"`
```

**Step 3: Verify compile**

Run: `go build ./internal/config/`
Expected: no output (success)

**Step 4: Commit**

```bash
git add internal/config/config_types.go
git commit -m "feat(config): Disabled field on OpenAI-compat entry and Claude key"
```

## Task 2: Store — struct field, migration, SQL wiring

**Files:**
- Modify: `internal/store/pg_upstream_providers.go` (`UpstreamProviderAPIKey` ~line 100; SELECT ~line 522; `syncAPIKeyEntriesTx` ~line 638)
- Modify: `internal/store/postgresstore.go` (migrations block ~line 745)
- Test: `internal/store/pg_upstream_providers_test.go`

**Step 1: Write the failing round-trip test** (append to `pg_upstream_providers_test.go`, modeled on `TestUpstreamProviderStoreAPIKeyEntryWeightRoundTrip` at line 321):

```go
func TestUpstreamProviderStoreAPIKeyEntryDisabledRoundTrip(t *testing.T) {
	pg := newTestPostgresStore(t, "upstream_entry_disabled")
	defer pg.Close()
	ensureMigrated(t, pg)

	src := NewUpstreamProviderStore(pg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// The migration must add a NOT NULL disabled column defaulting to false
	// so legacy rows survive the upgrade with unchanged routing behavior.
	colType, colNullable, colDefault := "", "", ""
	if err := pg.DB().QueryRowContext(ctx, `
		SELECT data_type, is_nullable, COALESCE(column_default, '')
		FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = $2 AND column_name = 'disabled'
	`, pg.cfg.Schema, pg.cfg.UpstreamProviderEntriesTable).Scan(&colType, &colNullable, &colDefault); err != nil {
		t.Fatalf("query disabled column: %v", err)
	}
	if colType != "boolean" {
		t.Fatalf("disabled column type = %q, want boolean", colType)
	}
	if colNullable != "NO" {
		t.Fatalf("disabled column nullable = %q, want NO (NOT NULL)", colNullable)
	}
	if colDefault != "false" {
		t.Fatalf("disabled column default = %q, want false", colDefault)
	}

	created, err := src.Create(ctx, UpstreamProvider{
		ProviderType: "openai-compatibility",
		Name:         "toggleable",
		BaseURL:      "https://api.example.test/v1",
		APIKeyEntries: []UpstreamProviderAPIKey{
			{APIKey: "toggle-secret-1", Name: "alpha"},
			{APIKey: "toggle-secret-2", Name: "beta", Disabled: true},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.APIKeyEntries[0].Disabled {
		t.Fatalf("Create returned Disabled=true for omitted entry, want false")
	}
	if !created.APIKeyEntries[1].Disabled {
		t.Fatalf("Create returned Disabled=false for entry with Disabled=true, want true")
	}

	firstID := created.APIKeyEntries[0].ID
	secondID := created.APIKeyEntries[1].ID

	loaded, err := src.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	for _, e := range loaded.APIKeyEntries {
		want := e.ID == secondID
		if e.Disabled != want {
			t.Fatalf("loaded entry %d Disabled = %v, want %v", e.ID, e.Disabled, want)
		}
	}

	// Toggle back: the dashboard's enable path must round-trip too.
	updated, err := src.Update(ctx, UpstreamProvider{
		ID:           created.ID,
		ProviderType: "openai-compatibility",
		Name:         "toggleable",
		BaseURL:      "https://api.example.test/v1",
		APIKeyEntries: []UpstreamProviderAPIKey{
			{ID: firstID, APIKey: "toggle-secret-1", Name: "alpha", Disabled: true},
			{ID: secondID, APIKey: "toggle-secret-2", Name: "beta"},
		},
	})
	if err != nil {
		t.Fatalf("Update with toggled disabled: %v", err)
	}
	for _, e := range updated.APIKeyEntries {
		want := e.ID == firstID
		if e.Disabled != want {
			t.Fatalf("updated entry %d Disabled = %v, want %v", e.ID, e.Disabled, want)
		}
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test -run TestUpstreamProviderStoreAPIKeyEntryDisabledRoundTrip ./internal/store/ -v`
Expected: FAIL (needs a Postgres test DB — see how existing store tests run in this repo; if they are skipped without a DB, follow the same skip behavior and verify the failure/skip mode matches `TestUpstreamProviderStoreAPIKeyEntryWeightRoundTrip`'s)

**Step 3: Add the struct field** to `UpstreamProviderAPIKey` (after `Priority`, ~line 123):

```go
	// Disabled excludes this entry from routing without deleting it. The
	// upstreamsync renderer skips disabled entries when projecting the row
	// into config.yaml. Stored NOT NULL DEFAULT FALSE so legacy rows survive
	// the column add with unchanged behavior.
	Disabled bool `json:"disabled,omitempty"`
```

**Step 4: Add the migration** in `postgresstore.go`, right after the entries `priority` migration block (~line 745):

```go
	// upstream_provider_api_key_entries disabled column. Per-entry on/off
	// toggle for the dashboard's entries editor; the upstreamsync renderer
	// skips disabled entries when rendering config.yaml. NOT NULL DEFAULT
	// FALSE so legacy rows keep their routing behavior after the upgrade
	// and no NULL-scan handling is needed.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS disabled BOOLEAN NOT NULL DEFAULT FALSE`, upstreamEntriesTable,
	)); err != nil {
		return fmt.Errorf("postgres store: migrate upstream provider entries disabled column: %w", err)
	}
```

**Step 5: Wire the SQL**:

- SELECT loop (~line 522): add `disabled` to the column list and scan into `e.Disabled`.
- `syncAPIKeyEntriesTx` INSERT (~line 711): add `disabled` column + `entry.Disabled` value.
- UPDATE (~line 721): add `disabled = $N` + arg.

**Step 6: Run test to verify it passes**

Run: `go test -run TestUpstreamProviderStoreAPIKeyEntryDisabledRoundTrip ./internal/store/ -v`
Expected: PASS

**Step 7: Run full store tests**

Run: `go test ./internal/store/`
Expected: PASS (all)

**Step 8: Commit**

```bash
git add internal/store/pg_upstream_providers.go internal/store/postgresstore.go internal/store/pg_upstream_providers_test.go
git commit -m "feat(store): per-entry disabled column with round-trip test"
```

## Task 3: Management handler — request field + wiring

**Files:**
- Modify: `internal/api/handlers/management/upstream_providers_types.go` (`upstreamProviderEntryReq` ~line 66)
- Modify: `internal/api/handlers/management/upstream_providers.go` (entry loop in `toUpstreamProvider` ~line 155)

**Step 1: Add field to `upstreamProviderEntryReq`** (after `Priority`):

```go
	// Disabled excludes this entry from routing without deleting it. The
	// renderer skips disabled entries when rendering config.yaml.
	Disabled bool `json:"disabled,omitempty"`
```

**Step 2: Wire in `toUpstreamProvider`** — add `Disabled: e.Disabled` to the `store.UpstreamProviderAPIKey` literal in the entries loop (booleans don't need the pointer copy the weight/priority paths use).

**Step 3: Verify compile + management tests**

Run: `go build ./internal/api/handlers/management/ && go test ./internal/api/handlers/management/`
Expected: no errors

**Step 4: Commit**

```bash
git add internal/api/handlers/management/upstream_providers_types.go internal/api/handlers/management/upstream_providers.go
git commit -m "feat(management): accept disabled flag on upstream provider entries"
```

## Task 4: Renderer — skip disabled entries

**Files:**
- Modify: `internal/upstreamsync/render.go` (Claude fan-out loop ~line 233; `openAICompatFromProviderWithPools` entries loop ~line 321)
- Test: `internal/upstreamsync/claude_multi_entry_test.go`

**Step 1: Write the failing render tests** (append to `claude_multi_entry_test.go`; `ptrInt` helper already exists there):

```go
func TestRenderSkipsDisabledEntries(t *testing.T) {
	provider := store.UpstreamProvider{
		ProviderType: TypeOpenAICompatibility,
		ID:           9,
		Name:         "compat",
		APIKeyEntries: []store.UpstreamProviderAPIKey{
			{ID: 31, APIKey: "compat-live-secret", Name: "live"},
			{ID: 32, APIKey: "compat-off-secret", Name: "off", Disabled: true},
		},
	}
	got := openAICompatFromProvider(provider)
	if len(got.APIKeyEntries) != 1 {
		t.Fatalf("rendered %d entries, want 1 (disabled entry skipped)", len(got.APIKeyEntries))
	}
	if got.APIKeyEntries[0].APIKey != "compat-live-secret" {
		t.Fatalf("wrong entry rendered: %q", got.APIKeyEntries[0].APIKey)
	}

	// Claude fan-out must skip the disabled entry too.
	prov := store.UpstreamProvider{
		ProviderType: TypeClaudeAPIKey,
		ID:           10,
		APIKeyEntries: []store.UpstreamProviderProviderAPIKey{
			{ID: 41, APIKey: "claude-live-secret", Name: "live"},
			{ID:  typo, ... },
		},
	}
	_ = prov
	_ = got
}
```

> **Note to implementer:** the sketch above shows the OpenAI-compat half complete and the Claude half as a placeholder — write the Claude half following `TestClaudeRenderFanOutPerEntry` (line 16) which calls the Claude render path and asserts on the resulting `[]config.ClaudeKey` slice: build the provider with one live + one `Disabled: true` entry, render, assert the disabled secret is absent from every rendered key.

**Step 2: Run tests to verify they fail**

Run: `go test -run TestRenderSkipsDisabledEntries ./internal/upstreamsync/ -v`
Expected: FAIL — disabled entry still rendered

**Step 3: Implement the skip** in `render.go`:

- Claude fan-out loop (~line 233): first line of the loop body: `if e.Disabled { continue }`
- OpenAI-compat entries loop (~line 321): same guard.

With a one-line comment each:

```go
		if e.Disabled {
			// Disabled entries stay persisted but never reach config.yaml;
			// toggling them back on re-renders them on the next reload.
			continue
		}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/upstreamsync/`
Expected: PASS (all)

**Step 5: Commit**

```bash
git add internal/upstreamsync/render.go internal/upstreamsync/claude_multi_entry_test.go
git commit -m "feat(upstreamsync): skip disabled entries when rendering config"
```

## Task 5: Frontend — toggle in EntriesEditor + form layer

**Files:**
- Modify: `web/dashboard/src/pages/upstream-provider-editor/EntriesEditor.jsx`
- Modify: `web/dashboard/src/pages/upstream-provider-editor/form.js` (`hydrateEntries` ~line 92, legacy synth ~line 194, `buildPayload` entries map ~line 295)
- Test: `web/dashboard/src/pages/upstream-provider-editor/editor.test.js`

**Step 1: Write the failing form-layer tests** (append to `editor.test.js`, following its `test('...')` + `assert` style):

```js
test('hydrateEntries: round-trips per-entry disabled flag', () => {
  const form = buildForm('openai-compatibility', {
    api_key_entries: [
      { id: 1, api_key: 'sk-live', name: 'live', disabled: false },
      { id: 2, api_key: 'sk-off', name: 'off', disabled: true },
    ],
  });
  assert.equal(form.api_key_entries[0].disabled, false);
  assert.equal(form.api_key_entries[1].disabled, true);
});

test('buildForm: legacy claude synthesized entry is not disabled', () => {
  const form = buildForm('claude-api-key', { api_key: 'sk-legacy' });
  assert.equal(form.api_key_entries[0].disabled, false);
});

test('buildPayload: emits disabled and keeps keyed disabled entries', () => {
  const form = buildForm('openai-compatibility', {
    api_key_entries: [
      { id: 1, api_key: 'sk-live', name: 'live' },
      { id: 2, api_key: 'sk-off', name: 'off', disabled: true },
    ],
  });
  // Simulate the operator toggling entry 2 off in the editor.
  form.api_key_entries[1].disabled = true;
  const payload = buildPayload(form, 'openai-compatibility');
  assert.equal(payload.api_key_entries.length, 2,
    'a disabled entry with a key is NOT filtered out');
  assert.equal(payload.api_key_entries[1].disabled, true);
  assert.equal(payload.api_key_entries[0].disabled, false);
});
```

**Step 2: Run tests to verify they fail**

Run (from `web/dashboard/`): `npm test 2>&1 | grep -A3 'disabled'`
Expected: FAIL — `disabled` is `undefined`/payload entry lacks the field

**Step 3: Implement `form.js` changes**:

- `hydrateEntries` return object: add `disabled: !!(e && e.disabled),`
- Legacy synth entry in `buildForm`: add `disabled: false,`
- `buildPayload` entries `.map`: add near the name handling:

```js
        entry.disabled = !!e.disabled;
```

**Step 4: Implement `EntriesEditor.jsx`**:

- In `add()`: include `disabled: false` in the new-row object.
- Per row, compute `const isOff = !!e.disabled;`
- Wrap the row group: `<div className={...list-editor__rowgroup${isOff ? ' list-editor__rowgroup--disabled' : ''}}>`
- Insert the toggle as the first control in `.list-editor__row`, before the identity input:

```jsx
<label className="entry-toggle" title={isOff ? 'Entry is disabled — excluded from routing' : 'Entry is active'} data-testid={`api-key-entry-disabled-${idx}`}>
  <input
    type="checkbox"
    checked={isOff}
    onChange={(ev) => update(idx, { disabled: ev.target.checked })}
    aria-label="Disable entry"
  />
  <span className="toggle-switch__slider" />
</label>
```

plus a small `disabled` badge next to the hint line when `isOff`:

```jsx
{isOff && <span className="badge badge--disabled" style={{ marginLeft: 6, fontSize: 10 }}>disabled</span>}
```

Note: semantics here are "checkbox = disabled" (mirrors the data field). If the reviewer prefers inverted ("on = active"), swap the `checked`/`onChange` inversion in one place.

**Step 5: Add the dimmed styling** to the dashboard CSS (find where `.list-editor__rowgroup` is defined, same file/section):

```css
.list-editor__rowgroup--disabled .list-editor__row > *:not(.entry-toggle) {
  opacity: 0.5;
}
.entry-toggle { display: inline-flex; align-items: center; cursor: pointer; }
```

(Reuse the existing `.toggle-switch__slider` visuals; adjust selector names to match how the stylesheet actually organizes list-editor rules.)

**Step 6: Add a render test** (append to `editor-render.test.jsx`, following its pattern):

```jsx
test('ProviderEditorForm renders per-entry disabled toggle checked for disabled entries', () => {
  const provider = {
    id: 5,
    provider_type: 'openai-compatibility',
    name: 'compat',
    api_key_entries: [
      { id: 1, api_key: 'FAKE-SECRET-ONE', name: 'live' },
      { id: 2, api_key: 'FAKE-SECRET-TWO', name: 'off', disabled: true },
    ],
  };
  const html = renderEditor(provider);
  assert.ok(html.includes('api-key-entry-disabled-0'), 'toggle renders for entry 0');
  assert.ok(html.includes('api-key-entry-disabled-1'), 'toggle renders for entry 1');
  // The disabled entry's toggle renders checked.
  assert.ok(/api-key-entry-disabled-1[^>]*>\s*<input[^>]*checked/.test(html) ||
            /checked[^>]*\/>\s*<span class="toggle-switch__slider"/.test(html),
    'disabled entry toggle renders checked');
});
```

**Step 7: Run the full dashboard suite**

Run (from `web/dashboard/`): `npm test`
Expected: all pass (203 baseline + new)

**Step 8: Commit**

```bash
git add web/dashboard/src/pages/upstream-provider-editor/EntriesEditor.jsx web/dashboard/src/pages/upstream-provider-editor/form.js web/dashboard/src/pages/upstream-provider-editor/editor.test.js web/dashboard/src/pages/upstream-provider-editor/editor-render.test.jsx
git commit -m "feat(dashboard): per-entry on/off toggle in API key entries editor"
```

## Task 6: Full verification + embed

**Step 1: gofmt**

Run: `gofmt -l .` (from repo root) — expected: empty output; fix any listed files with `gofmt -w`.

**Step 2: Full Go test suite**

Run: `go test ./...`
Expected: PASS

**Step 3: Build verify**

Run: `go build -o test-output ./cmd/server && rm test-output`
Expected: success (required by AGENTS.md after changes)

**Step 4: Dashboard build + embed**

Run: `make dash-embed`
Expected: SPA builds, dist copied into `internal/dashboardasset`, Go binary rebuilt

**Step 5: Commit (if dash-embed produced tracked changes)**

```bash
git status --short
git add -A && git commit -m "chore: rebuild dashboard embed for entry toggle" --only-with-changes 2>/dev/null || git commit -m "chore: rebuild dashboard embed for entry toggle" || true
```

**Step 6: Report**

Summarize: test counts, files touched, and that `make dash-embed` succeeded.

---

## Notes for the implementer

- The design decision "disabled entries stay in the payload and in PG" is intentional — do NOT add filtering of keyed disabled entries in `buildPayload`.
- The Claude "at least one entry" frontend validation must keep counting disabled entries as valid — no change needed, just don't "fix" it.
- Renderer skip is the single source of exclusion truth: no scheduler/conductor changes.
- Store tests need a Postgres test DB — check how `newTestPostgresStore` skips/passes without one and match that behavior; don't invent a new skip mechanism.
- CSS: inspect the existing `.list-editor` and `.toggle-switch` rules before adding new ones; prefer reusing `.toggle-switch` markup exactly as `ToggleRow` does.
