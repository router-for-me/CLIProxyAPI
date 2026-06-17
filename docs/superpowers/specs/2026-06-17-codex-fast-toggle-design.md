# Codex Fast Toggle Design

## Context

The local CLIProxyAPI runtime already supports a `codex-service-tier` request-normalizer plugin that can mark outbound Codex requests with `service_tier=priority` when a boolean `fast` flag is enabled. The current implementation only applies this behavior to `gpt-5.5`, while the user wants a single shared toggle that also covers `gpt-5.4`.

The runtime also already exposes generic plugin management endpoints under `/v0/management/plugins` and `/v0/management/plugins/:id/config`. The desired operator experience is to turn the shared fast mode on or off directly from the management UI instead of hand-editing `config.yaml`.

## Goal

Provide one shared `fast` switch for both `gpt-5.4` and `gpt-5.5`, and make that switch controllable from the existing plugin management UI path.

## Scope

In scope:
- Extend `codex-service-tier.fast` so it applies to both `gpt-5.4` and `gpt-5.5`.
- Reuse the existing dynamic plugin configuration model.
- Verify whether the current management UI already renders editable plugin config fields; if not, add the smallest UI support needed for this boolean field.
- Confirm runtime behavior for both models after toggling.

Out of scope:
- Introducing per-model independent fast switches.
- Designing a general service-tier policy engine.
- Changing non-Codex models or non-plugin config flows.

## Current State

### Backend/plugin side
- `examples/plugin/codex-service-tier/go/main.go` defines a plugin with a single boolean config field `fast`.
- When `fast` is enabled, the plugin adds `service_tier: "priority"` to outbound Codex request bodies.
- The current `shouldSetPriorityServiceTier(...)` function only returns true for `req.Model == "gpt-5.5"`.

### Management/API side
- The server already exposes plugin management routes:
  - `GET /v0/management/plugins`
  - `PATCH /v0/management/plugins/:id/enabled`
  - `PUT /v0/management/plugins/:id/config`
  - `PATCH /v0/management/plugins/:id/config`
- Plugin metadata already carries `ConfigFields`, which is the correct reusable place for UI-driven editing.

### Runtime/config side
- The runtime supports plugin reconfiguration via watcher-driven config reload and plugin `MethodPluginReconfigure` calls.
- The current local config uses:
  ```yaml
  plugins:
    enabled: true
    dir: "/CLIProxyAPI/plugins"
    configs:
      codex-service-tier:
        enabled: true
        priority: 1
        fast: true
  ```

## Recommended Approach

### 1. Keep one shared boolean toggle
Do not introduce separate `gpt54_fast` / `gpt55_fast` flags. Keep the existing boolean shape:

```yaml
plugins:
  configs:
    codex-service-tier:
      fast: true
```

This keeps the operator workflow simple and matches the current plugin contract.

### 2. Expand the plugin match logic to both models
Update the plugin’s request-normalization gate so `fast: true` applies to:
- `gpt-5.4`
- `gpt-5.5`

The plugin should still require:
- `req.ToFormat == "codex"`
- `fastEnabled == true`

This preserves the current behavior boundary and avoids unintended impact on non-Codex traffic.

### 3. Reuse the existing plugin management UI/API path
Do not create a special-purpose “Codex Fast” backend route. First verify whether the current management UI already renders plugin config fields from backend metadata. If it does, no UI-specific backend changes are needed.

If the UI does not yet expose an editable control for plugin boolean config fields, add only the minimum UI support necessary to:
- show the `codex-service-tier` plugin entry,
- show the `fast` boolean field,
- submit updates back through the existing plugin config update endpoint.

### 4. Preserve hot-toggle behavior
The toggle should continue to work through config reload / plugin reconfigure without rebuilding the plugin binary each time. Rebuild is only needed when the plugin code itself changes.

## Detailed Design

### Backend/plugin logic
Change the plugin’s model filter from a single-model check to a two-model allowlist. The behavior remains:
- if plugin fast is off: request body unchanged,
- if `ToFormat != codex`: request body unchanged,
- if model not in the allowlist: request body unchanged,
- otherwise: inject `service_tier = priority`.

This is intentionally conservative: it keeps the plugin as a narrow request normalizer rather than a general policy engine.

### Management UI behavior
The UI should present `codex-service-tier.fast` as a direct editable switch so the user can turn it on/off without editing YAML manually. The expected UX is:
1. Open management UI.
2. Open plugin list / plugin config section.
3. Find `codex-service-tier`.
4. Toggle `fast` on/off.
5. Save or apply.

If the current UI already supports generic plugin config editing, the implementation is only validation plus smoke testing. If not, the smallest acceptable enhancement is boolean field rendering and update wiring for this plugin config path only through the existing generic plugin endpoints.

### Config persistence
The source of truth remains `config.yaml`. UI edits should persist through the existing management config update path rather than temporary in-memory overrides.

## Risks and Constraints

### Risk: documentation drift
Some repository docs still describe the plugin as applying to `gpt-5.4` only, while current plugin code targets `gpt-5.5`. The implementation must update any directly user-facing docs touched by this change so the behavior description matches reality.

### Risk: UI may already support this
If the management UI already renders plugin config controls correctly, adding bespoke UI would be unnecessary duplication. Verify first, then only add what is missing.

### Risk: false confidence from config-only checks
A successful config update is not enough. Verification must include live requests for both `gpt-5.4` and `gpt-5.5` after toggling.

## Verification Plan

### Code-level verification
- Confirm plugin metadata still advertises `fast` as a boolean config field.
- Confirm plugin request-normalization logic now covers both `gpt-5.4` and `gpt-5.5`.

### Runtime verification
1. Start the local primary container with the plugin mounted and enabled.
2. With `fast: true`, send one `gpt-5.4` request and one `gpt-5.5` request through the live runtime.
3. Confirm both models still return successful responses.
4. Toggle `fast` off through the management path.
5. Repeat one `gpt-5.4` request and one `gpt-5.5` request and confirm the runtime still behaves normally.
6. If observable logging/metadata exists for `service_tier`, use it to confirm the toggle changes the outbound request shaping.

### UI verification
- Confirm the plugin appears in the management UI.
- Confirm `fast` is editable through the UI rather than requiring manual YAML edits.
- Confirm the UI-driven change persists in the underlying config.

## Files Likely Involved

Core implementation:
- `examples/plugin/codex-service-tier/go/main.go`

Possible docs alignment:
- `examples/plugin/codex-service-tier/README.md`
- `examples/plugin/README.md`
- `examples/plugin/README_CN.md`

Possible management UI/API support (only if current generic rendering is insufficient):
- plugin management handlers under `internal/api/handlers/management/`
- management UI asset files provided through the existing control panel path

## Recommendation

Implement this as a narrow extension of the existing plugin contract: keep one shared `fast` boolean, expand the model match to both `gpt-5.4` and `gpt-5.5`, and rely on the existing plugin management flow for runtime toggling. Only add UI code if generic plugin config editing is not already sufficient.