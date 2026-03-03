(() => {
  "use strict";

  const ENHANCER_KEY = "__cpaOAuthAvailabilityEnhancer";
  if (window[ENHANCER_KEY]?.started) {
    return;
  }

  const WIDGET_ID = "cpa-oauth-availability-widget";
  const STYLE_ID = "cpa-oauth-availability-style";
  const SAMPLE_INTERVAL_MS = 10_000;
  const HISTORY_MAX_POINTS = 30;
  const MANAGEMENT_SEGMENT = "/v0/management";
  const STORAGE_PREFIX = "enc::v1::";
  const STORAGE_SALT = "cli-proxy-api-webui::secure-storage";
  const OAUTH_PROVIDER_FALLBACK = new Set([
    "gemini",
    "gemini-cli",
    "vertex",
    "aistudio",
    "antigravity",
    "claude",
    "codex",
    "qwen",
    "iflow",
    "kimi",
  ]);

  const state = {
    started: true,
    authHeader: "",
    apiBase: "",
    history: Object.create(null),
    timer: 0,
    renderSeq: 0,
  };
  window[ENHANCER_KEY] = state;

  function toStringSafe(value) {
    if (value === null || value === undefined) {
      return "";
    }
    return String(value);
  }

  function toBool(value) {
    if (typeof value === "boolean") {
      return value;
    }
    if (typeof value === "string") {
      const lower = value.trim().toLowerCase();
      if (lower === "true") {
        return true;
      }
      if (lower === "false") {
        return false;
      }
    }
    return Boolean(value);
  }

  function clampPercent(value) {
    const num = Number(value);
    if (!Number.isFinite(num)) {
      return 0;
    }
    if (num < 0) {
      return 0;
    }
    if (num > 100) {
      return 100;
    }
    return num;
  }

  function escapeHTML(value) {
    return toStringSafe(value)
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/\"/g, "&quot;")
      .replace(/'/g, "&#39;");
  }

  function normalizeProvider(raw) {
    const provider = toStringSafe(raw).trim().toLowerCase();
    if (!provider) {
      return "unknown";
    }
    if (provider === "anthropic") {
      return "claude";
    }
    return provider;
  }

  function providerLabel(provider) {
    const key = normalizeProvider(provider);
    if (key === "unknown") {
      return "unknown";
    }
    return key;
  }

  function isOAuthCredential(item) {
    const accountType = toStringSafe(item?.account_type)
      .trim()
      .toLowerCase();
    if (accountType) {
      if (accountType.includes("oauth")) {
        return true;
      }
      if (accountType.includes("api_key") || accountType.includes("apikey")) {
        return false;
      }
    }

    const authType = toStringSafe(item?.auth_type)
      .trim()
      .toLowerCase();
    if (authType) {
      if (authType.includes("oauth")) {
        return true;
      }
      if (authType.includes("api_key") || authType.includes("apikey")) {
        return false;
      }
    }

    const email = toStringSafe(item?.email).trim();
    if (email.includes("@")) {
      return true;
    }

    const account = toStringSafe(item?.account).trim();
    if (account.includes("@")) {
      return true;
    }

    const rawProvider = toStringSafe(item?.provider || item?.type)
      .trim()
      .toLowerCase();
    const provider = normalizeProvider(rawProvider);

    if (OAUTH_PROVIDER_FALLBACK.has(provider)) {
      return true;
    }

    const name = toStringSafe(item?.name).toLowerCase();
    const status = toStringSafe(item?.status).toLowerCase();
    const statusMessage = toStringSafe(item?.status_message).toLowerCase();
    return (
      rawProvider.includes("oauth") ||
      name.includes("oauth") ||
      status.includes("oauth") ||
      statusMessage.includes("oauth")
    );
  }

  function extractAuthFiles(payload) {
    if (Array.isArray(payload)) {
      return payload;
    }
    if (!payload || typeof payload !== "object") {
      return [];
    }

    if (Array.isArray(payload.files)) {
      return payload.files;
    }
    if (Array.isArray(payload.auth_files)) {
      return payload.auth_files;
    }

    if (payload.data && typeof payload.data === "object") {
      if (Array.isArray(payload.data.files)) {
        return payload.data.files;
      }
      if (Array.isArray(payload.data.auth_files)) {
        return payload.data.auth_files;
      }
    }

    if (payload.data && Array.isArray(payload.data)) {
      return payload.data;
    }
    return [];
  }

  function aggregateAvailability(authFiles) {
    const grouped = new Map();
    for (const item of authFiles) {
      if (!isOAuthCredential(item)) {
        continue;
      }
      const provider = normalizeProvider(item?.provider || item?.type);
      let acc = grouped.get(provider);
      if (!acc) {
        acc = {
          provider,
          total: 0,
          available: 0,
          unavailable: 0,
          disabled: 0,
          details: [],
        };
        grouped.set(provider, acc);
      }
      acc.total += 1;

      const disabled = toBool(item?.disabled);
      const unavailable = toBool(item?.unavailable);
      if (disabled) {
        acc.disabled += 1;
      } else if (unavailable) {
        acc.unavailable += 1;
      } else {
        acc.available += 1;
      }

      acc.details.push({
        provider,
        name: toStringSafe(item?.name),
        email: toStringSafe(item?.email),
        status: toStringSafe(item?.status),
        statusMessage: toStringSafe(item?.status_message),
        disabled,
        unavailable,
      });
    }

    const snapshots = Array.from(grouped.values())
      .map((item) => ({
        ...item,
        availablePct: item.total > 0 ? clampPercent((item.available / item.total) * 100) : 0,
      }))
      .sort((a, b) => a.provider.localeCompare(b.provider));

    snapshots.forEach((snapshot) => {
      snapshot.details.sort((a, b) => {
        const left = `${a.name} ${a.email}`.toLowerCase();
        const right = `${b.name} ${b.email}`.toLowerCase();
        return left.localeCompare(right);
      });
    });

    return snapshots;
  }

  function recordHistory(snapshots) {
    const seen = new Set();
    for (const snapshot of snapshots) {
      const key = snapshot.provider;
      seen.add(key);
      const previous = Array.isArray(state.history[key]) ? state.history[key] : [];
      const updated = previous.concat(snapshot.availablePct);
      if (updated.length > HISTORY_MAX_POINTS) {
        state.history[key] = updated.slice(updated.length - HISTORY_MAX_POINTS);
      } else {
        state.history[key] = updated;
      }
    }

    for (const key of Object.keys(state.history)) {
      if (!seen.has(key)) {
        delete state.history[key];
      }
    }
  }

  function buildTrendSeries(values, currentPct) {
    const series = Array.isArray(values)
      ? values
          .map((value) => Number(value))
          .filter((value) => Number.isFinite(value))
          .map((value) => clampPercent(value))
      : [];

    if (series.length === 0) {
      const current = clampPercent(currentPct);
      return [current, current];
    }
    if (series.length === 1) {
      return [series[0], series[0]];
    }
    return series;
  }

  function renderTrendLine(values) {
    if (!Array.isArray(values) || values.length === 0) {
      return `<span class="cpa-oauth-trend-placeholder">采样中...</span>`;
    }

    const width = 160;
    const height = 24;
    const padding = 2;
    const innerHeight = height - padding * 2;
    const step = values.length > 1 ? (width - padding * 2) / (values.length - 1) : 0;

    const points = values
      .map((value, index) => {
        const x = padding + index * step;
        const y = padding + ((100 - clampPercent(value)) / 100) * innerHeight;
        return `${x.toFixed(2)},${y.toFixed(2)}`;
      })
      .join(" ");

    const baseline = `${padding},${(height - padding).toFixed(2)} ${(width - padding).toFixed(2)},${(height - padding).toFixed(2)}`;

    return `
      <svg class="cpa-oauth-trend-svg" viewBox="0 0 ${width} ${height}" preserveAspectRatio="none" aria-hidden="true">
        <polyline class="cpa-oauth-trend-base" points="${baseline}"></polyline>
        <polyline class="cpa-oauth-trend-path" points="${points}"></polyline>
      </svg>
    `;
  }

  function buildBarRow(snapshot) {
    const pct = clampPercent(snapshot.availablePct);
    const provider = escapeHTML(providerLabel(snapshot.provider));
    const legend = `${snapshot.available}/${snapshot.total}`;
    const legendText = escapeHTML(`${legend}  (unavailable:${snapshot.unavailable}, disabled:${snapshot.disabled})`);

    let statusClass = "is-good";
    let statusText = "可用";
    if (snapshot.available === 0) {
      statusClass = "is-bad";
      statusText = "不可用";
    } else if (pct < 50) {
      statusClass = "is-warn";
      statusText = "可用率低";
    }

    return `
      <div class="cpa-oauth-row">
        <div class="cpa-oauth-row-head">
          <span class="cpa-oauth-provider">${provider}</span>
          <span class="cpa-oauth-pct">${pct.toFixed(0)}%</span>
        </div>
        <div class="cpa-oauth-bar-track">
          <div class="cpa-oauth-bar-fill ${statusClass}" style="width:${pct.toFixed(2)}%"></div>
        </div>
        <div class="cpa-oauth-legend">
          ${legendText}
          <span class="cpa-oauth-status-pill ${statusClass}">${statusText}</span>
        </div>
      </div>
    `;
  }

  function renderDetails(snapshots) {
    const detailRows = [];
    for (const snapshot of snapshots) {
      for (const item of snapshot.details) {
        const rawStatus = toStringSafe(item.status).trim().toLowerCase();
        const isActiveLike =
          rawStatus === "active" ||
          (!item.disabled && !item.unavailable && (rawStatus === "" || rawStatus === "available" || rawStatus === "ok"));
        if (isActiveLike) {
          continue
        }

        const provider = escapeHTML(providerLabel(item.provider));
        const name = escapeHTML(item.name || "-");
        const email = escapeHTML(item.email || "-");
        let stateText = "available";
        if (item.disabled) {
          stateText = "disabled";
        } else if (item.unavailable) {
          stateText = "unavailable";
        }
        let statusClass = "cpa-oauth-status-available";
        if (item.disabled) {
          statusClass = "cpa-oauth-status-disabled";
        } else if (item.unavailable) {
          statusClass = "cpa-oauth-status-unavailable";
        }
        const status = escapeHTML(item.status || stateText);
        const statusMessage = escapeHTML(item.statusMessage || "");
        detailRows.push(`
          <tr>
            <td>${provider}</td>
            <td>${name}</td>
            <td>${email}</td>
            <td><span class="${statusClass}">${status}</span></td>
            <td>${statusMessage || "-"}</td>
          </tr>
        `);
      }
    }

    if (detailRows.length === 0) {
      return `<div class="cpa-oauth-empty">暂无非 active 的 OAuth 凭证</div>`;
    }

    return `
      <div class="cpa-oauth-detail-wrap">
        <table class="cpa-oauth-table">
          <thead>
            <tr>
              <th>provider</th>
              <th>name</th>
              <th>email</th>
              <th>status</th>
              <th>status message</th>
            </tr>
          </thead>
          <tbody>
            ${detailRows.join("")}
          </tbody>
        </table>
      </div>
    `;
  }

  function renderContent(snapshots, errorText) {
    if (errorText) {
      return `
        <div class="cpa-oauth-title">OAuth Provider 可用性</div>
        <div class="cpa-oauth-error">${escapeHTML(errorText)}</div>
      `;
    }

    if (!Array.isArray(snapshots) || snapshots.length === 0) {
      return `
        <div class="cpa-oauth-title">OAuth Provider 可用性</div>
        <div class="cpa-oauth-hint">按 OAuth 凭证聚合，10 秒采样，展示最近 5 分钟趋势。</div>
        <div class="cpa-oauth-empty">暂无 OAuth 凭证可展示</div>
      `;
    }

    const bars = snapshots.map(buildBarRow).join("");
    const trends = snapshots
      .map((snapshot) => {
        const historyValues = state.history[snapshot.provider] || [];
        const trendSeries = buildTrendSeries(historyValues, snapshot.availablePct);
        const provider = escapeHTML(providerLabel(snapshot.provider));
        const pct = clampPercent(snapshot.availablePct).toFixed(0);
        const trendLine = renderTrendLine(trendSeries);
        return `
          <div class="cpa-oauth-trend-row">
            <span class="cpa-oauth-trend-provider">${provider}</span>
            <span class="cpa-oauth-trend-line">${trendLine}</span>
            <span class="cpa-oauth-trend-pct">${pct}%</span>
          </div>
        `;
      })
      .join("");

    return `
      <div class="cpa-oauth-title">OAuth Provider 可用性</div>
      <div class="cpa-oauth-hint">按 OAuth 凭证聚合，10 秒采样，展示最近 5 分钟趋势。</div>

      <div class="cpa-oauth-section-title">Provider 可用率</div>
      <div class="cpa-oauth-list">${bars}</div>

      <div class="cpa-oauth-section-title">可用率趋势（5m）</div>
      <div class="cpa-oauth-trend-list">${trends}</div>

      <div class="cpa-oauth-section-title">OAuth 凭证明细</div>
      ${renderDetails(snapshots)}
    `;
  }

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) {
      return;
    }
    const style = document.createElement("style");
    style.id = STYLE_ID;
    style.textContent = `
      #${WIDGET_ID} {
        margin: 14px 0 18px;
        padding: 16px;
        display: block;
        border: 1px solid var(--border-color, #d7dce5);
        border-radius: 12px;
        background: var(--bg-secondary, #f8fafc);
        color: var(--text-primary, #111827);
      }
      #${WIDGET_ID} .cpa-oauth-title {
        font-weight: 700;
        font-size: 16px;
        margin-bottom: 6px;
      }
      #${WIDGET_ID} .cpa-oauth-hint {
        color: var(--text-secondary, #6b7280);
        font-size: 12px;
        margin-bottom: 12px;
      }
      #${WIDGET_ID} .cpa-oauth-section-title {
        margin-top: 12px;
        margin-bottom: 8px;
        font-size: 13px;
        font-weight: 600;
        color: var(--text-primary, #111827);
      }
      #${WIDGET_ID} .cpa-oauth-row {
        margin-bottom: 10px;
      }
      #${WIDGET_ID} .cpa-oauth-row-head {
        display: flex;
        justify-content: space-between;
        align-items: center;
        margin-bottom: 4px;
        font-size: 13px;
      }
      #${WIDGET_ID} .cpa-oauth-provider {
        font-weight: 600;
      }
      #${WIDGET_ID} .cpa-oauth-bar-track {
        width: 100%;
        height: 8px;
        border-radius: 999px;
        background: var(--bg-tertiary, #e5e7eb);
        overflow: hidden;
      }
      #${WIDGET_ID} .cpa-oauth-bar-fill {
        height: 100%;
        border-radius: inherit;
        background: var(--primary-color, #6366f1);
      }
      #${WIDGET_ID} .cpa-oauth-bar-fill.is-good {
        background: #22c55e;
      }
      #${WIDGET_ID} .cpa-oauth-bar-fill.is-warn {
        background: #f59e0b;
      }
      #${WIDGET_ID} .cpa-oauth-bar-fill.is-bad {
        background: #ef4444;
      }
      #${WIDGET_ID} .cpa-oauth-legend {
        margin-top: 4px;
        font-size: 12px;
        color: var(--text-secondary, #6b7280);
        display: flex;
        align-items: center;
        gap: 8px;
        flex-wrap: wrap;
      }
      #${WIDGET_ID} .cpa-oauth-status-pill {
        border: 1px solid currentColor;
        border-radius: 999px;
        padding: 1px 8px;
        font-size: 11px;
        line-height: 1.5;
      }
      #${WIDGET_ID} .cpa-oauth-status-pill.is-good {
        color: #16a34a;
      }
      #${WIDGET_ID} .cpa-oauth-status-pill.is-warn {
        color: #d97706;
      }
      #${WIDGET_ID} .cpa-oauth-status-pill.is-bad {
        color: #dc2626;
      }
      #${WIDGET_ID} .cpa-oauth-trend-row {
        display: grid;
        grid-template-columns: 120px 1fr 48px;
        gap: 8px;
        align-items: center;
        margin-bottom: 4px;
        font-size: 12px;
      }
      #${WIDGET_ID} .cpa-oauth-trend-provider {
        color: var(--text-secondary, #6b7280);
      }
      #${WIDGET_ID} .cpa-oauth-trend-line {
        width: 100%;
        min-height: 24px;
        display: flex;
        align-items: center;
      }
      #${WIDGET_ID} .cpa-oauth-trend-svg {
        width: 100%;
        height: 24px;
        display: block;
      }
      #${WIDGET_ID} .cpa-oauth-trend-base {
        fill: none;
        stroke: color-mix(in srgb, var(--text-secondary, #6b7280) 30%, transparent);
        stroke-width: 1;
        stroke-dasharray: 3 3;
      }
      #${WIDGET_ID} .cpa-oauth-trend-path {
        fill: none;
        stroke: var(--primary-color, #6366f1);
        stroke-width: 2;
        stroke-linecap: round;
        stroke-linejoin: round;
      }
      #${WIDGET_ID} .cpa-oauth-trend-placeholder {
        color: var(--text-secondary, #6b7280);
        font-size: 12px;
      }
      #${WIDGET_ID} .cpa-oauth-trend-pct {
        text-align: right;
      }
      #${WIDGET_ID} .cpa-oauth-detail-wrap {
        overflow-x: auto;
      }
      #${WIDGET_ID} .cpa-oauth-table {
        width: 100%;
        border-collapse: collapse;
        font-size: 12px;
      }
      #${WIDGET_ID} .cpa-oauth-table th,
      #${WIDGET_ID} .cpa-oauth-table td {
        border-bottom: 1px solid var(--border-color, #e5e7eb);
        text-align: left;
        padding: 6px 8px;
        vertical-align: top;
      }
      #${WIDGET_ID} .cpa-oauth-table th {
        color: var(--text-secondary, #6b7280);
        font-weight: 600;
      }
      #${WIDGET_ID} .cpa-oauth-status-available {
        color: #16a34a;
        font-weight: 600;
      }
      #${WIDGET_ID} .cpa-oauth-status-unavailable {
        color: #d97706;
        font-weight: 600;
      }
      #${WIDGET_ID} .cpa-oauth-status-disabled {
        color: #dc2626;
        font-weight: 600;
      }
      #${WIDGET_ID} .cpa-oauth-empty {
        margin-top: 4px;
        color: var(--text-secondary, #6b7280);
        font-size: 12px;
      }
      #${WIDGET_ID} .cpa-oauth-error {
        color: #ef4444;
        font-size: 12px;
      }
    `;
    document.head.appendChild(style);
  }

  function findUsageHeading() {
    const headings = Array.from(document.querySelectorAll("h1"));
    for (const heading of headings) {
      const text = toStringSafe(heading.textContent).trim().toLowerCase();
      if (!text) {
        continue;
      }
      if (text.includes("usage") || text.includes("使用统计") || text.includes("стат")) {
        return heading;
      }
    }
    return null;
  }

  function findUsageContainer() {
    const heading = findUsageHeading();
    if (!heading) {
      return null;
    }

    let node = heading.parentElement;
    while (node && node !== document.body) {
      if (node.classList && node.classList.length > 0) {
        const classes = Array.from(node.classList);
        const matched = classes.some((name) =>
          name.toLowerCase().includes("usagepage-module__container") ||
          name.toLowerCase().includes("usagepage") ||
          name.toLowerCase().includes("container")
        );
        if (matched) {
          return node;
        }
      }
      node = node.parentElement;
    }

    return heading.closest("div");
  }

  function ensureWidgetHost() {
    const existing = document.getElementById(WIDGET_ID);
    if (existing) {
      return existing;
    }

    const usageContainer = findUsageContainer();
    if (!usageContainer) {
      return null;
    }

    const children = Array.from(usageContainer.children || []);
    const firstCard = children.find((child) => {
      const className = toStringSafe(child.className).toLowerCase();
      return className.includes("statsgrid") || className.includes("statcard") || className.includes("chartsgrid");
    });

    const host = document.createElement("section");
    host.id = WIDGET_ID;

    if (firstCard && firstCard.parentElement === usageContainer) {
      usageContainer.insertBefore(host, firstCard);
    } else if (usageContainer.lastElementChild) {
      usageContainer.insertBefore(host, usageContainer.lastElementChild);
    } else {
      usageContainer.appendChild(host);
    }
    return host;
  }

  function removeWidgetHost() {
    const host = document.getElementById(WIDGET_ID);
    if (host && host.parentElement) {
      host.parentElement.removeChild(host);
    }
  }

  function isUsageRoute() {
    const hash = toStringSafe(window.location.hash).toLowerCase();
    const path = toStringSafe(window.location.pathname).toLowerCase();
    const heading = findUsageHeading();
    if (heading) {
      return true;
    }
    return hash.includes("/usage") || path.endsWith("/usage") || path.includes("/usage");
  }

  function decodeMaybeEncrypted(raw) {
    if (typeof raw !== "string") {
      return "";
    }
    if (!raw.startsWith(STORAGE_PREFIX)) {
      return raw;
    }
    try {
      const payload = raw.slice(STORAGE_PREFIX.length);
      const binary = atob(payload);
      const input = new Uint8Array(binary.length);
      for (let i = 0; i < binary.length; i += 1) {
        input[i] = binary.charCodeAt(i);
      }
      const keySource = `${STORAGE_SALT}|${window.location.host}|${navigator.userAgent}`;
      const key = new TextEncoder().encode(keySource);
      const output = new Uint8Array(input.length);
      for (let i = 0; i < input.length; i += 1) {
        output[i] = input[i] ^ key[i % key.length];
      }
      return new TextDecoder().decode(output);
    } catch (_err) {
      return raw;
    }
  }

  function readStoredValue(key) {
    try {
      const raw = localStorage.getItem(key);
      if (raw === null) {
        return null;
      }
      const decoded = decodeMaybeEncrypted(raw);
      try {
        return JSON.parse(decoded);
      } catch (_err) {
        return decoded;
      }
    } catch (_err) {
      return null;
    }
  }

  function normalizeBase(value) {
    const text = toStringSafe(value).trim();
    if (!text) {
      return "";
    }
    return text.replace(/\/+$/, "");
  }

  function recoverSessionFromStorage() {
    const persisted = readStoredValue("cli-proxy-auth");
    if (persisted && typeof persisted === "object") {
      const session = persisted.state && typeof persisted.state === "object" ? persisted.state : persisted;
      if (!state.apiBase && typeof session.apiBase === "string") {
        state.apiBase = normalizeBase(session.apiBase);
      }
      if (!state.authHeader && typeof session.managementKey === "string" && session.managementKey.trim()) {
        state.authHeader = `Bearer ${session.managementKey.trim()}`;
      }
    }

    const legacyApiBase = readStoredValue("apiBase") || readStoredValue("apiUrl");
    if (!state.apiBase && typeof legacyApiBase === "string") {
      state.apiBase = normalizeBase(legacyApiBase);
    }

    const legacyKey = readStoredValue("managementKey");
    if (!state.authHeader && typeof legacyKey === "string" && legacyKey.trim()) {
      state.authHeader = `Bearer ${legacyKey.trim()}`;
    }
  }

  function inferBaseFromURL(rawURL) {
    try {
      const url = new URL(rawURL, window.location.href);
      const idx = url.pathname.indexOf(MANAGEMENT_SEGMENT);
      if (idx < 0) {
        return "";
      }
      const prefixPath = url.pathname.slice(0, idx);
      return normalizeBase(`${url.origin}${prefixPath}`);
    } catch (_err) {
      return "";
    }
  }

  function captureAuthHeader(value) {
    const text = toStringSafe(value).trim();
    if (!text) {
      return;
    }
    if (!/^Bearer\s+/i.test(text)) {
      return;
    }
    state.authHeader = text;
  }

  function inspectHeaders(headers) {
    if (!headers) {
      return;
    }
    if (headers instanceof Headers) {
      captureAuthHeader(headers.get("Authorization") || headers.get("authorization") || "");
      return;
    }
    if (Array.isArray(headers)) {
      for (const pair of headers) {
        if (!Array.isArray(pair) || pair.length < 2) {
          continue;
        }
        if (toStringSafe(pair[0]).toLowerCase() === "authorization") {
          captureAuthHeader(pair[1]);
        }
      }
      return;
    }
    if (typeof headers === "object") {
      for (const key of Object.keys(headers)) {
        if (toStringSafe(key).toLowerCase() === "authorization") {
          captureAuthHeader(headers[key]);
        }
      }
    }
  }

  function installRequestHooks() {
    const xhrProto = window.XMLHttpRequest && window.XMLHttpRequest.prototype;
    if (xhrProto && !xhrProto.__cpaOAuthPatched) {
      const openOriginal = xhrProto.open;
      const setHeaderOriginal = xhrProto.setRequestHeader;

      xhrProto.open = function patchedOpen(method, url, ...rest) {
        this.__cpaRequestURL = url;
        const inferredBase = inferBaseFromURL(url);
        if (inferredBase) {
          state.apiBase = inferredBase;
        }
        return openOriginal.call(this, method, url, ...rest);
      };

      xhrProto.setRequestHeader = function patchedSetRequestHeader(name, value) {
        if (toStringSafe(name).toLowerCase() === "authorization") {
          captureAuthHeader(value);
        }
        return setHeaderOriginal.call(this, name, value);
      };

      Object.defineProperty(xhrProto, "__cpaOAuthPatched", {
        value: true,
        configurable: false,
        enumerable: false,
        writable: false,
      });
    }

    if (typeof window.fetch === "function" && !window.fetch.__cpaOAuthPatched) {
      const originalFetch = window.fetch.bind(window);
      const patchedFetch = function patchedFetch(input, init) {
        try {
          const requestURL = typeof input === "string" ? input : input?.url;
          const inferredBase = inferBaseFromURL(requestURL);
          if (inferredBase) {
            state.apiBase = inferredBase;
          }
          inspectHeaders(input?.headers);
          inspectHeaders(init?.headers);
        } catch (_err) {
          // ignore
        }
        return originalFetch(input, init);
      };
      Object.defineProperty(patchedFetch, "__cpaOAuthPatched", {
        value: true,
        configurable: false,
        enumerable: false,
        writable: false,
      });
      window.fetch = patchedFetch;
    }
  }

  async function fetchAuthFiles() {
    recoverSessionFromStorage();

    const base = state.apiBase || normalizeBase(window.location.origin);
    const endpoint = `${base}${MANAGEMENT_SEGMENT}/auth-files`;
    const headers = {
      "X-Local-Password": ""
    };
    if (state.authHeader) {
      headers.Authorization = state.authHeader;
      headers["X-Local-Password"] = state.authHeader.replace(/^Bearer\s+/i, "");
    }

    const response = await fetch(endpoint, {
      method: "GET",
      cache: "no-store",
      headers,
    });

    if (!response.ok) {
      throw new Error(`auth-files 请求失败 (${response.status})`);
    }

    const payload = await response.json();
    return extractAuthFiles(payload);
  }

  async function refreshWidget() {
    if (!isUsageRoute()) {
      return;
    }

    ensureStyle();
    const host = ensureWidgetHost();
    if (!host) {
      return;
    }

    const currentSeq = ++state.renderSeq;
    host.innerHTML = `<div class="cpa-oauth-title">OAuth Provider 可用性</div><div class="cpa-oauth-hint">加载中...</div>`;

    try {
      const authFiles = await fetchAuthFiles();
      const snapshots = aggregateAvailability(authFiles);
      recordHistory(snapshots);
      if (currentSeq !== state.renderSeq) {
        return;
      }
      host.innerHTML = renderContent(snapshots, "");
    } catch (error) {
      if (currentSeq !== state.renderSeq) {
        return;
      }
      host.innerHTML = renderContent([], toStringSafe(error?.message || error || "加载失败"));
    }
  }

  function startSampling() {
    if (state.timer) {
      return;
    }
    void refreshWidget();
    state.timer = window.setInterval(() => {
      void refreshWidget();
    }, SAMPLE_INTERVAL_MS);
  }

  function stopSampling() {
    if (state.timer) {
      window.clearInterval(state.timer);
      state.timer = 0;
    }
    removeWidgetHost();
  }

  function handleRouteChange() {
    if (isUsageRoute()) {
      startSampling();
      void refreshWidget();
    } else {
      stopSampling();
    }
  }

  function installRouteHooks() {
    window.addEventListener("hashchange", handleRouteChange);
    window.addEventListener("popstate", handleRouteChange);
    window.addEventListener("focus", handleRouteChange);
    document.addEventListener("visibilitychange", () => {
      if (document.visibilityState === "visible") {
        handleRouteChange();
      }
    });

    const pushState = history.pushState;
    history.pushState = function patchedPushState(...args) {
      const result = pushState.apply(this, args);
      window.setTimeout(handleRouteChange, 0);
      return result;
    };

    const replaceState = history.replaceState;
    history.replaceState = function patchedReplaceState(...args) {
      const result = replaceState.apply(this, args);
      window.setTimeout(handleRouteChange, 0);
      return result;
    };

    const observer = new MutationObserver(() => {
      if (!isUsageRoute()) {
        return;
      }
      if (!document.getElementById(WIDGET_ID)) {
        void refreshWidget();
      }
    });

    observer.observe(document.body, {
      childList: true,
      subtree: true,
    });
  }

  installRequestHooks();
  installRouteHooks();
  handleRouteChange();
})();
