(function () {
  "use strict";

  const API = Object.freeze({
    profiles: "/v0/management/api-key-profiles",
    usage: "/v0/management/client-key-usage",
    settings: "/v0/management/usage-billing-settings",
    csv: "/v0/management/usage/export.csv"
  });
  const SESSION_SECRET_KEY = "cliproxy:pulse:management-secret";
  const THEME_KEY = "cliproxy:pulse:theme";
  const SVG_NS = "http" + "://www.w3.org/2000/svg";
  const PAGE_SIZE = 25;
  const RATE_FIELDS = ["input-per-million", "output-per-million", "cache-read-per-million", "cache-write-per-million"];

  const state = {
    managementSecret: "",
    serverVersion: "",
    profiles: [],
    report: null,
    baseReport: null,
    settings: null,
    billingDraft: null,
    billingBaseRevision: "",
    billingDirty: false,
    filters: {
      from: "",
      to: "",
      key: "",
      provider: "",
      model: "",
      tier: ""
    },
    chartMode: "attempts",
    chartCurrency: "",
    keySearch: "",
    keyStatus: "all",
    keySort: "requests",
    keyPage: 1,
    editingProfile: null,
    currentView: "overview",
    lastUpdatedAt: null
  };

  const dom = {};
  let refreshController = null;
  let refreshSequence = 0;
  let filterTimer = 0;
  let resizeTimer = 0;
  let confirmResolver = null;

  function byId(id) {
    return document.getElementById(id);
  }

  function all(selector, root) {
    return Array.from((root || document).querySelectorAll(selector));
  }

  function element(tag, className, text) {
    const node = document.createElement(tag);
    if (className) {
      node.className = className;
    }
    if (text !== undefined && text !== null) {
      node.textContent = String(text);
    }
    return node;
  }

  function svgElement(tag, attributes) {
    const node = document.createElementNS(SVG_NS, tag);
    Object.keys(attributes || {}).forEach(function (name) {
      node.setAttribute(name, String(attributes[name]));
    });
    return node;
  }

  function safeStorage(storage, action, key, value) {
    try {
      if (action === "get") {
        return storage.getItem(key) || "";
      }
      if (action === "set") {
        storage.setItem(key, value);
      } else if (action === "remove") {
        storage.removeItem(key);
      }
    } catch (_error) {
      return "";
    }
    return "";
  }

  function clone(value) {
    return JSON.parse(JSON.stringify(value));
  }

  function init() {
    cacheDOM();
    initTheme();
    renderAuthSecurityState();
    setDatePreset(30, false);
    bindEvents();

    const remembered = safeStorage(window.sessionStorage, "get", SESSION_SECRET_KEY);
    if (remembered) {
      dom.secret.value = remembered;
      dom.rememberSecret.checked = true;
      authenticate(remembered, true);
    }
  }

  function cacheDOM() {
    [
      "auth-view", "app-view", "auth-form", "management-secret", "remember-secret", "toggle-secret", "connect-button", "auth-error",
      "insecure-warning", "insecure-confirm-row", "insecure-confirm", "sign-out", "compact-sign-out", "compact-export",
      "page-kicker", "page-title", "last-updated", "refresh-data", "keys-refresh", "theme-toggle", "export-csv",
      "notice-stack", "filter-from", "filter-to", "filter-key", "filter-provider", "filter-model", "filter-tier", "clear-filters",
      "metric-cost", "metric-cost-note", "metric-tokens", "metric-token-note", "metric-requests", "metric-request-note",
      "metric-success", "metric-success-note", "metric-latency", "metric-latency-note", "trend-chart", "chart-wrap", "chart-tooltip",
      "chart-empty", "chart-data-table", "chart-currency", "currency-costs", "unpriced-tokens", "unpriced-note",
      "key-ranking-body", "key-ranking-empty", "model-breakdown", "model-empty", "model-count", "key-total", "key-search",
      "key-status-filter", "key-sort", "keys-table-body", "keys-card-list", "keys-empty", "pagination-summary", "page-prev", "page-next",
      "billing-dirty", "billing-enabled", "retention-days", "pricing-currency", "pricing-version", "add-pricing-rule", "pricing-rules-body",
      "pricing-empty", "billing-form-error", "settings-revision", "reset-billing", "save-billing", "profile-dialog", "profile-form",
      "profile-preview-alias", "profile-preview-key", "profile-preview-status", "profile-alias", "profile-disabled", "profile-id", "alias-count",
      "profile-form-error", "save-profile", "confirm-dialog", "confirm-title", "confirm-message", "confirm-cancel", "confirm-accept", "toast-region"
    ].forEach(function (id) {
      const key = id.replace(/-([a-z])/g, function (_match, letter) { return letter.toUpperCase(); });
      dom[key] = byId(id);
    });
    dom.secret = dom.managementSecret;
  }

  function bindEvents() {
    dom.authForm.addEventListener("submit", function (event) {
      event.preventDefault();
      authenticate(dom.secret.value, dom.rememberSecret.checked);
    });

    dom.toggleSecret.addEventListener("click", function () {
      const reveal = dom.secret.type === "password";
      dom.secret.type = reveal ? "text" : "password";
      dom.toggleSecret.textContent = reveal ? "隐藏" : "显示";
      dom.toggleSecret.setAttribute("aria-label", reveal ? "隐藏管理密码" : "显示管理密码");
    });

    dom.signOut.addEventListener("click", requestSignOut);
    dom.compactSignOut.addEventListener("click", function () {
      closeCompactMenu();
      requestSignOut();
    });
    dom.compactExport.addEventListener("click", function () {
      closeCompactMenu();
      exportCSV();
    });
    const compactMenu = dom.compactSignOut.closest("details");
    compactMenu.addEventListener("toggle", function () {
      compactMenu.querySelector("summary").setAttribute("aria-expanded", compactMenu.open ? "true" : "false");
    });
    dom.refreshData.addEventListener("click", function () { refreshAll({ includeSettings: true }); });
    dom.keysRefresh.addEventListener("click", function () { refreshAll({ includeSettings: true }); });
    dom.exportCsv.addEventListener("click", exportCSV);
    dom.themeToggle.addEventListener("click", toggleTheme);

    all("[data-view]").forEach(function (button) {
      button.addEventListener("click", function () { setView(button.dataset.view); });
    });
    all("[data-go-view]").forEach(function (button) {
      button.addEventListener("click", function () { setView(button.dataset.goView); });
    });

    all("[data-days]").forEach(function (button) {
      button.addEventListener("click", function () {
        setDatePreset(button.dataset.days, true);
      });
    });

    [dom.filterFrom, dom.filterTo].forEach(function (input) {
      input.addEventListener("change", function () {
        all("[data-days]").forEach(function (button) {
          button.classList.remove("active");
          button.setAttribute("aria-pressed", "false");
        });
        syncFilters();
        scheduleRefresh();
      });
    });
    [dom.filterKey, dom.filterProvider, dom.filterModel, dom.filterTier].forEach(function (select) {
      select.addEventListener("change", function () {
        syncFilters();
        if (select === dom.filterProvider) {
          populateDimensionFilters();
          syncFilters();
        }
        scheduleRefresh();
      });
    });
    dom.clearFilters.addEventListener("click", function () { setDatePreset(30, true); });

    all("[data-chart-mode]").forEach(function (button) {
      button.addEventListener("click", function () {
        state.chartMode = button.dataset.chartMode;
        all("[data-chart-mode]").forEach(function (item) {
          const active = item === button;
          item.classList.toggle("active", active);
          item.setAttribute("aria-pressed", active ? "true" : "false");
        });
        renderChart();
      });
    });
    dom.chartCurrency.addEventListener("change", function () {
      state.chartCurrency = dom.chartCurrency.value;
      renderChart();
    });

    dom.keySearch.addEventListener("input", function () {
      state.keySearch = dom.keySearch.value.trim().toLocaleLowerCase();
      state.keyPage = 1;
      renderKeys();
    });
    dom.keyStatusFilter.addEventListener("change", function () {
      state.keyStatus = dom.keyStatusFilter.value;
      state.keyPage = 1;
      renderKeys();
    });
    dom.keySort.addEventListener("change", function () {
      state.keySort = dom.keySort.value;
      state.keyPage = 1;
      renderKeys();
    });
    dom.pagePrev.addEventListener("click", function () {
      if (state.keyPage > 1) {
        state.keyPage -= 1;
        renderKeys();
      }
    });
    dom.pageNext.addEventListener("click", function () {
      state.keyPage += 1;
      renderKeys();
    });

    [dom.billingEnabled, dom.retentionDays, dom.pricingCurrency, dom.pricingVersion].forEach(function (input) {
      input.addEventListener("input", function () {
        syncBillingDraftFromForm();
        markBillingDirty(true);
      });
      input.addEventListener("change", function () {
        syncBillingDraftFromForm();
        markBillingDirty(true);
      });
    });
    dom.addPricingRule.addEventListener("click", addPricingRule);
    dom.resetBilling.addEventListener("click", resetBillingDraft);
    dom.saveBilling.addEventListener("click", saveBillingSettings);

    all("[data-close-dialog]").forEach(function (button) {
      button.addEventListener("click", function () { dom.profileDialog.close(); });
    });
    dom.profileAlias.addEventListener("input", function () {
      dom.aliasCount.textContent = Array.from(dom.profileAlias.value).length + " / 128";
      dom.profilePreviewAlias.textContent = dom.profileAlias.value.trim() || "未命名 Key";
    });
    dom.profileDisabled.addEventListener("change", updateProfilePreviewStatus);
    dom.profileForm.addEventListener("submit", saveProfile);
    dom.confirmCancel.addEventListener("click", function () { resolveConfirmation(false); });
    dom.confirmAccept.addEventListener("click", function () { resolveConfirmation(true); });
    dom.confirmDialog.addEventListener("cancel", function (event) {
      event.preventDefault();
      resolveConfirmation(false);
    });

    window.addEventListener("resize", function () {
      window.clearTimeout(resizeTimer);
      resizeTimer = window.setTimeout(renderChart, 120);
    });
    window.addEventListener("beforeunload", function (event) {
      if (!state.billingDirty) {
        return;
      }
      event.preventDefault();
      event.returnValue = "";
    });
  }

  function initTheme() {
    let theme = safeStorage(window.localStorage, "get", THEME_KEY);
    if (theme !== "light" && theme !== "dark") {
      theme = window.matchMedia && window.matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark";
    }
    applyTheme(theme);
  }

  function applyTheme(theme) {
    document.documentElement.setAttribute("data-theme", theme);
    dom.themeToggle.textContent = theme === "dark" ? "☀" : "◐";
    dom.themeToggle.setAttribute("aria-label", theme === "dark" ? "切换到浅色主题" : "切换到深色主题");
    dom.themeToggle.title = theme === "dark" ? "切换到浅色主题" : "切换到深色主题";
  }

  function toggleTheme() {
    const next = document.documentElement.getAttribute("data-theme") === "dark" ? "light" : "dark";
    applyTheme(next);
    safeStorage(window.localStorage, "set", THEME_KEY, next);
    renderChart();
  }

  function isLoopbackHost(hostname) {
    const value = String(hostname || "").toLocaleLowerCase();
    return value === "127.0.0.1" || value === "localhost" || value === "::1" || value === "[::1]";
  }

  function isInsecureRemoteContext() {
    return window.location.protocol === "http:" && !isLoopbackHost(window.location.hostname);
  }

  function renderAuthSecurityState() {
    const insecure = isInsecureRemoteContext();
    dom.insecureWarning.hidden = !insecure;
    dom.insecureConfirmRow.hidden = !insecure;
    if (!insecure) {
      dom.insecureConfirm.checked = false;
    }
  }

  async function authenticate(rawSecret, remember) {
    const secret = String(rawSecret || "").trim();
    setAuthError("");
    if (!secret) {
      setAuthError("请输入管理密码。");
      dom.secret.focus();
      return;
    }
    if (isInsecureRemoteContext() && !dom.insecureConfirm.checked) {
      setAuthError("远程 HTTP 会明文传输管理密码。请改用 HTTPS，或勾选风险确认后继续。");
      dom.insecureConfirm.focus();
      return;
    }

    setConnectBusy(true);
    state.managementSecret = secret;
    try {
      const settings = await apiRequest(API.settings, { method: "GET" });
      setSettings(settings);
      if (remember) {
        safeStorage(window.sessionStorage, "set", SESSION_SECRET_KEY, secret);
      } else {
        safeStorage(window.sessionStorage, "remove", SESSION_SECRET_KEY);
      }
      dom.authView.hidden = true;
      dom.appView.hidden = false;
      await refreshAll({ includeSettings: false, initial: true });
    } catch (error) {
      state.managementSecret = "";
      safeStorage(window.sessionStorage, "remove", SESSION_SECRET_KEY);
      setAuthError(apiErrorMessage(error));
      dom.secret.focus();
    } finally {
      setConnectBusy(false);
    }
  }

  function setConnectBusy(busy) {
    dom.connectButton.disabled = busy;
    dom.connectButton.setAttribute("aria-busy", busy ? "true" : "false");
    const label = dom.connectButton.querySelector("span");
    if (label) {
      label.textContent = busy ? "正在验证…" : "连接管理服务";
    }
  }

  function setAuthError(message) {
    dom.authError.textContent = message || "";
    dom.authError.hidden = !message;
  }

  function signOut(message) {
    if (refreshController) {
      refreshController.abort();
      refreshController = null;
    }
    state.managementSecret = "";
    state.profiles = [];
    state.report = null;
    state.baseReport = null;
    state.settings = null;
    state.billingDraft = null;
    state.billingBaseRevision = "";
    state.billingDirty = false;
    safeStorage(window.sessionStorage, "remove", SESSION_SECRET_KEY);
    dom.appView.hidden = true;
    dom.authView.hidden = false;
    dom.secret.value = "";
    dom.secret.type = "password";
    dom.toggleSecret.textContent = "显示";
    setAuthError(message || "");
    dom.secret.focus();
  }

  async function requestSignOut() {
    if (state.billingDirty) {
      const confirmed = await confirmAction("放弃未保存的计费修改？", "断开连接会丢失当前计费草稿。", "放弃并断开");
      if (!confirmed) {
        return;
      }
    }
    signOut("");
  }

  function closeCompactMenu() {
    const menu = dom.compactSignOut.closest("details");
    if (menu) {
      menu.open = false;
    }
  }

  async function apiRequest(path, options) {
    if (!state.managementSecret) {
      throw new Error("尚未连接管理服务");
    }
    const requestOptions = Object.assign({ method: "GET", cache: "no-store", credentials: "same-origin" }, options || {});
    const headers = new Headers(requestOptions.headers || {});
    headers.set("Authorization", "Bearer " + state.managementSecret);
    headers.set("Accept", "application/json");
    if (requestOptions.body !== undefined && !headers.has("Content-Type")) {
      headers.set("Content-Type", "application/json");
    }
    requestOptions.headers = headers;

    const response = await window.fetch(path, requestOptions);
    const version = response.headers.get("X-CPA-VERSION");
    if (version) {
      state.serverVersion = version;
    }
    const contentType = response.headers.get("Content-Type") || "";
    let payload = null;
    if (contentType.includes("application/json")) {
      try {
        payload = await response.json();
      } catch (_error) {
        payload = null;
      }
    } else {
      payload = await response.text();
    }
    if (!response.ok) {
      const error = new Error(extractError(payload, response.status));
      error.status = response.status;
      error.payload = payload;
      throw error;
    }
    return payload;
  }

  function extractError(payload, status) {
    if (payload && typeof payload === "object") {
      return payload.message || payload.error || ("请求失败（" + status + "）");
    }
    if (typeof payload === "string" && payload.trim()) {
      return payload.trim();
    }
    return "请求失败（" + status + "）";
  }

  function apiErrorMessage(error) {
    if (!error) {
      return "未知错误";
    }
    if (error.name === "AbortError") {
      return "请求已取消";
    }
    if (error.status === 401) {
      return "管理密码无效，请检查后重试。";
    }
    if (error.status === 403) {
      return "访问被拒绝：" + error.message;
    }
    if (error.status === 404) {
      return "管理接口不可用，请确认新版服务已启动。";
    }
    return error.message || String(error);
  }

  async function refreshAll(options) {
    const settings = Object.assign({ includeSettings: true, initial: false }, options || {});
    const sequence = ++refreshSequence;
    if (refreshController) {
      refreshController.abort();
    }
    const controller = new AbortController();
    refreshController = controller;
    setRefreshBusy(true);

    try {
      if (settings.includeSettings) {
        const latestSettings = await apiRequest(API.settings, { method: "GET", signal: controller.signal });
        if (sequence !== refreshSequence) {
          return;
        }
        setSettings(latestSettings, !state.billingDirty);
      }

      syncFilters();
      if (state.filters.from && state.filters.to && state.filters.from > state.filters.to) {
        throw new Error("开始日期不能晚于结束日期。");
      }

      const baseQuery = buildUsageQuery(false);
      const filteredQuery = buildUsageQuery(true);
      const basePromise = apiRequest(API.usage + baseQuery, { method: "GET", signal: controller.signal });
      const reportPromise = filteredQuery === baseQuery ? basePromise : apiRequest(API.usage + filteredQuery, { method: "GET", signal: controller.signal });
      const results = await Promise.all([
        apiRequest(API.profiles, { method: "GET", signal: controller.signal }),
        basePromise,
        reportPromise
      ]);
      if (sequence !== refreshSequence) {
        return;
      }

      state.profiles = Array.isArray(results[0]["api-key-profiles"]) ? results[0]["api-key-profiles"] : [];
      state.baseReport = normalizeReport(results[1]);
      state.report = normalizeReport(results[2]);
      state.lastUpdatedAt = new Date();
      populateDimensionFilters();
      renderAll();
      if (!settings.initial) {
        toast("数据已刷新");
      }
    } catch (error) {
      controller.abort();
      if (error.name === "AbortError") {
        return;
      }
      if (error.status === 401 || error.status === 403) {
        signOut(apiErrorMessage(error));
        return;
      }
      toast(apiErrorMessage(error), true);
      renderTransientError(apiErrorMessage(error));
    } finally {
      if (sequence === refreshSequence) {
        setRefreshBusy(false);
        refreshController = null;
      }
    }
  }

  function setRefreshBusy(busy) {
    [dom.refreshData, dom.keysRefresh].forEach(function (button) {
      if (!button) {
        return;
      }
      button.disabled = busy;
      button.setAttribute("aria-busy", busy ? "true" : "false");
    });
    dom.refreshData.classList.toggle("loading", busy);
  }

  function normalizeReport(report) {
    const value = report && typeof report === "object" ? report : {};
    value.keys = Array.isArray(value.keys) ? value.keys : [];
    value.daily = Array.isArray(value.daily) ? value.daily : [];
    value.models = Array.isArray(value.models) ? value.models : [];
    value.currencies = Array.isArray(value.currencies) ? value.currencies : [];
    value.summary = value.summary || {};
    value.overflow = value.overflow || {};
    return value;
  }

  function renderTransientError(message) {
    if (!dom.noticeStack) {
      return;
    }
    const notice = createNotice("danger", "刷新失败", message);
    dom.noticeStack.prepend(notice);
  }

  function scheduleRefresh() {
    window.clearTimeout(filterTimer);
    filterTimer = window.setTimeout(function () { refreshAll({ includeSettings: false }); }, 180);
  }

  function setDatePreset(days, refresh) {
    all("[data-days]").forEach(function (button) {
      const active = String(button.dataset.days) === String(days);
      button.classList.toggle("active", active);
      button.setAttribute("aria-pressed", active ? "true" : "false");
    });
    if (days === "all") {
      dom.filterFrom.value = "";
      dom.filterTo.value = "";
    } else {
      const count = Number(days);
      const today = new Date();
      dom.filterTo.value = utcDateString(today);
      const start = new Date(today.getTime());
      start.setUTCDate(start.getUTCDate() - Math.max(0, count - 1));
      dom.filterFrom.value = utcDateString(start);
    }
    dom.filterKey.value = "";
    dom.filterProvider.value = "";
    dom.filterModel.value = "";
    dom.filterTier.value = "";
    syncFilters();
    if (refresh && state.managementSecret) {
      scheduleRefresh();
    }
  }

  function utcDateString(date) {
    return new Date(Date.UTC(date.getUTCFullYear(), date.getUTCMonth(), date.getUTCDate())).toISOString().slice(0, 10);
  }

  function syncFilters() {
    state.filters.from = dom.filterFrom.value;
    state.filters.to = dom.filterTo.value;
    state.filters.key = dom.filterKey.value;
    state.filters.provider = dom.filterProvider.value;
    state.filters.model = dom.filterModel.value;
    state.filters.tier = dom.filterTier.value;
  }

  function buildUsageQuery(includeDimensions) {
    const params = new URLSearchParams();
    if (state.filters.from) {
      params.set("from", state.filters.from);
    }
    if (state.filters.to) {
      params.set("to", state.filters.to);
    }
    if (includeDimensions) {
      if (state.filters.key) {
        params.set("key_id", state.filters.key);
      }
      if (state.filters.provider) {
        params.set("provider", state.filters.provider);
      }
      if (state.filters.model) {
        params.set("model", state.filters.model);
      }
      if (state.filters.tier) {
        params.set("service_tier", state.filters.tier);
      }
    }
    const query = params.toString();
    return query ? "?" + query : "";
  }

  function populateDimensionFilters() {
    const base = state.baseReport || normalizeReport({});
    const currentKey = state.filters.key;
    const keyOptions = new Map();
    state.profiles.forEach(function (profile) {
      const label = (profile.alias || "未命名 Key") + " · " + (profile.masked_key || profile.id);
      keyOptions.set(profile.id, label);
    });
    base.keys.forEach(function (key) {
      if (!keyOptions.has(key.key_id)) {
        keyOptions.set(key.key_id, (key.alias || "历史 Key") + " · " + (key.masked_key || key.key_id));
      }
    });
    replaceSelectOptions(dom.filterKey, "全部 Key", Array.from(keyOptions.entries()).map(function (entry) {
      return { value: entry[0], label: entry[1] };
    }), currentKey);

    const providers = uniqueSorted(base.models.map(function (item) { return item.provider; }));
    replaceSelectOptions(dom.filterProvider, "全部 Provider", providers.map(optionFromString), state.filters.provider);

    const modelRows = state.filters.provider ? base.models.filter(function (item) {
      return String(item.provider || "").toLocaleLowerCase() === state.filters.provider.toLocaleLowerCase();
    }) : base.models;
    const models = uniqueSorted(modelRows.map(function (item) { return item.model; }));
    replaceSelectOptions(dom.filterModel, "全部模型", models.map(optionFromString), state.filters.model);

    const tiers = uniqueSorted(modelRows.map(function (item) { return item.service_tier; }));
    replaceSelectOptions(dom.filterTier, "全部 Tier", tiers.map(optionFromString), state.filters.tier);
    syncFilters();
  }

  function optionFromString(value) {
    return { value: value, label: value || "default" };
  }

  function uniqueSorted(values) {
    return Array.from(new Set(values.filter(function (value) { return value !== null && value !== undefined && String(value).trim() !== ""; }).map(String))).sort(function (a, b) {
      return a.localeCompare(b, "zh-CN");
    });
  }

  function replaceSelectOptions(select, emptyLabel, options, selectedValue) {
    const fragment = document.createDocumentFragment();
    const empty = element("option", "", emptyLabel);
    empty.value = "";
    fragment.appendChild(empty);
    options.forEach(function (item) {
      const option = element("option", "", item.label);
      option.value = item.value;
      fragment.appendChild(option);
    });
    select.replaceChildren(fragment);
    select.value = selectedValue || "";
    if (selectedValue && select.value !== selectedValue) {
      const preserved = element("option", "", selectedValue);
      preserved.value = selectedValue;
      select.appendChild(preserved);
      select.value = selectedValue;
    }
  }

  function renderAll() {
    renderNotices();
    renderOverview();
    renderKeys();
    const versionText = state.serverVersion ? " · 服务 " + state.serverVersion : "";
    dom.lastUpdated.textContent = state.lastUpdatedAt ? "更新于 " + formatDateTime(state.lastUpdatedAt) + versionText : "等待首次同步";
  }

  function renderNotices() {
    dom.noticeStack.replaceChildren();
    const report = state.report;
    if (!report) {
      return;
    }
    if (isInsecureRemoteContext()) {
      dom.noticeStack.appendChild(createNotice("warning", "远程连接未加密", "当前管理密码通过明文网络传输。请改用 HTTPS 或仅在可信局域网中访问。"));
    }
    if (!report.enabled) {
      dom.noticeStack.appendChild(createNotice("warning", "用量统计尚未启用", "新请求不会进入报表。可前往计费规则页面开启收集。", "去开启", function () { setView("billing"); }));
    }
    if (report.persistence_error) {
      dom.noticeStack.appendChild(createNotice("danger", "统计持久化异常", String(report.persistence_error)));
    }
    const summary = report.summary || {};
    if (number(summary.unpriced_attempts) > 0) {
      dom.noticeStack.appendChild(createNotice("warning", "存在未定价请求", formatInteger(summary.unpriced_attempts) + " 次请求没有完整匹配价格规则。", "检查规则", function () { setView("billing"); }));
    }
    if (number(summary.overflow_attempts) > 0 || number((report.overflow || {}).attempts) > 0) {
      dom.noticeStack.appendChild(createNotice("info", "部分高基数记录已合并", "排行榜可能不包含所有维度，但汇总指标仍包含这些请求。"));
    }
    if ((report.currencies || []).length > 1) {
      dom.noticeStack.appendChild(createNotice("info", "检测到多币种历史数据", "不同币种分别展示，系统不会将其直接相加。"));
    }
  }

  function createNotice(kind, title, message, actionLabel, action) {
    const notice = element("div", "notice " + kind);
    const copy = element("div");
    copy.appendChild(element("strong", "", title));
    copy.appendChild(element("span", "", message));
    notice.appendChild(copy);
    if (actionLabel && action) {
      const button = element("button", "text-button", actionLabel);
      button.type = "button";
      button.addEventListener("click", action);
      notice.appendChild(button);
    }
    return notice;
  }

  function renderOverview() {
    const report = state.report || normalizeReport({});
    const summary = report.summary || {};
    const attempts = number(summary.attempts);
    const success = number(summary.success);
    const tokenTotal = number((summary.tokens || {}).total_tokens);
    const costs = costMap(summary, report.currency);
    const currencies = Object.keys(costs).sort();

    if (currencies.length === 1) {
      dom.metricCost.textContent = formatMoney(currencies[0], costs[currencies[0]]);
      dom.metricCostNote.textContent = "单币种预估 · 微单位精度";
    } else if (currencies.length > 1) {
      dom.metricCost.textContent = currencies.length + " 种币种";
      dom.metricCostNote.textContent = "请在费用构成中分别查看";
    } else {
      dom.metricCost.textContent = formatMoney(report.currency || settingsCurrency(), 0);
      dom.metricCostNote.textContent = state.settings && state.settings.pricing.rules.length ? "当前筛选暂无费用" : "尚未配置价格规则";
    }
    dom.metricTokens.textContent = formatCompact(tokenTotal);
    dom.metricTokenNote.textContent = "输入 " + formatCompact(number((summary.tokens || {}).input_tokens)) + " · 输出 " + formatCompact(number((summary.tokens || {}).output_tokens));
    dom.metricRequests.textContent = formatCompact(attempts);
    dom.metricRequestNote.textContent = formatInteger(success) + " 成功 · " + formatInteger(number(summary.failed)) + " 失败 · 上游失败尝试 " + formatInteger(number(summary.upstream_failed_attempts));
    dom.metricSuccess.textContent = attempts > 0 ? formatPercent(success / attempts) : "—";
    dom.metricSuccessNote.textContent = attempts > 0 ? "基于 " + formatInteger(attempts) + " 次最终请求" : "等待请求数据";
    dom.metricLatency.textContent = number(summary.average_latency_ms) > 0 ? formatDuration(summary.average_latency_ms) : "—";
    dom.metricLatencyNote.textContent = "首 Token " + (number(summary.average_ttft_ms) > 0 ? formatDuration(summary.average_ttft_ms) : "—");

    renderCurrencyCosts(costs, report.currency);
    dom.unpricedTokens.textContent = formatCompact(number(summary.unpriced_tokens)) + " Token";
    dom.unpricedNote.textContent = number(summary.unpriced_attempts) > 0 ? formatInteger(summary.unpriced_attempts) + " 次尝试未完整计价" : "尚未检测到未定价请求";
    renderChart();
    renderKeyRanking();
    renderModelBreakdown();
  }

  function renderCurrencyCosts(costs, fallbackCurrency) {
    dom.currencyCosts.replaceChildren();
    let entries = Object.keys(costs).sort().map(function (currency) { return [currency, costs[currency]]; });
    if (!entries.length) {
      entries = [[fallbackCurrency || settingsCurrency(), 0]];
    }
    const max = Math.max.apply(null, entries.map(function (entry) { return number(entry[1]); }).concat([1]));
    entries.forEach(function (entry) {
      const row = element("div", "currency-row");
      row.appendChild(element("span", "currency-code", entry[0]));
      const progress = element("progress", "currency-progress");
      progress.max = max;
      progress.value = number(entry[1]);
      progress.setAttribute("aria-label", entry[0] + " 费用相对当前最高值");
      row.appendChild(progress);
      row.appendChild(element("strong", "", formatMoney(entry[0], entry[1])));
      dom.currencyCosts.appendChild(row);
    });
  }

  function renderChart() {
    if (!dom.trendChart || !state.report) {
      return;
    }
    const daily = state.report.daily.slice().sort(function (a, b) { return String(a.day).localeCompare(String(b.day)); });
    const currencies = reportCurrencies(state.report);
    if (!state.chartCurrency || !currencies.includes(state.chartCurrency)) {
      state.chartCurrency = currencies[0] || state.report.currency || settingsCurrency();
    }
    replaceSelectOptions(dom.chartCurrency, "选择币种", currencies.map(optionFromString), state.chartCurrency);
    dom.chartCurrency.hidden = state.chartMode !== "cost";

    const points = daily.map(function (entry) {
      const summary = entry.summary || {};
      let value = number(summary.attempts);
      if (state.chartMode === "tokens") {
        value = number((summary.tokens || {}).total_tokens);
      } else if (state.chartMode === "cost") {
        value = number(costMap(summary, state.report.currency)[state.chartCurrency]);
      }
      return { day: entry.day, value: value };
    });

    dom.trendChart.replaceChildren();
    const tableBody = dom.chartDataTable.querySelector("tbody");
    tableBody.replaceChildren();
    dom.chartEmpty.hidden = points.length > 0;
    dom.trendChart.hidden = points.length === 0;
    if (!points.length) {
      return;
    }

    const width = Math.max(320, Math.round(dom.chartWrap.clientWidth || 900));
    const height = 320;
    dom.trendChart.setAttribute("viewBox", "0 0 " + width + " " + height);
    const padding = { left: width < 520 ? 42 : 56, right: 18, top: 18, bottom: 42 };
    const plotWidth = width - padding.left - padding.right;
    const plotHeight = height - padding.top - padding.bottom;
    const maxValue = Math.max.apply(null, points.map(function (point) { return point.value; }).concat([1]));

    for (let index = 0; index <= 4; index += 1) {
      const ratio = index / 4;
      const y = padding.top + plotHeight * ratio;
      dom.trendChart.appendChild(svgElement("line", { x1: padding.left, y1: y, x2: width - padding.right, y2: y, class: "chart-grid-line" }));
      const label = svgElement("text", { x: padding.left - 8, y: y + 4, "text-anchor": "end", class: "chart-axis-label" });
      label.textContent = formatChartValue(maxValue * (1 - ratio));
      dom.trendChart.appendChild(label);
    }

    const coordinates = points.map(function (point, index) {
      const x = points.length === 1 ? padding.left + plotWidth / 2 : padding.left + (plotWidth * index / (points.length - 1));
      const y = padding.top + plotHeight - (point.value / maxValue) * plotHeight;
      return { x: x, y: y, day: point.day, value: point.value };
    });
    const linePath = coordinates.map(function (point, index) {
      return (index === 0 ? "M" : "L") + point.x.toFixed(2) + " " + point.y.toFixed(2);
    }).join(" ");
    const areaPath = "M" + coordinates[0].x.toFixed(2) + " " + (padding.top + plotHeight) + " " + linePath.slice(1) + " L" + coordinates[coordinates.length - 1].x.toFixed(2) + " " + (padding.top + plotHeight) + " Z";
    dom.trendChart.appendChild(svgElement("path", { d: areaPath, class: "chart-area" }));
    dom.trendChart.appendChild(svgElement("path", { d: linePath, class: "chart-line" }));

    const labelEvery = Math.max(1, Math.ceil(points.length / (width < 520 ? 4 : 6)));
    coordinates.forEach(function (point, index) {
      const visiblePoint = svgElement("circle", { cx: point.x, cy: point.y, r: 4, class: "chart-point" });
      dom.trendChart.appendChild(visiblePoint);
      const hit = svgElement("circle", { cx: point.x, cy: point.y, r: 14, class: "chart-hit", tabindex: 0, role: "img" });
      const accessibleValue = formatChartPoint(point.value);
      hit.setAttribute("aria-label", point.day + "，" + accessibleValue);
      const title = svgElement("title");
      title.textContent = point.day + " · " + accessibleValue;
      hit.appendChild(title);
      dom.trendChart.appendChild(hit);

      if (index % labelEvery === 0 || index === points.length - 1) {
        const label = svgElement("text", { x: point.x, y: height - 14, "text-anchor": index === 0 ? "start" : (index === points.length - 1 ? "end" : "middle"), class: "chart-axis-label" });
        label.textContent = shortDay(point.day);
        dom.trendChart.appendChild(label);
      }

      const row = element("tr");
      row.appendChild(element("td", "", point.day));
      row.appendChild(element("td", "", accessibleValue));
      tableBody.appendChild(row);
    });
    dom.trendChart.setAttribute("aria-label", "从 " + points[0].day + " 到 " + points[points.length - 1].day + " 的" + chartModeLabel() + "趋势");
  }

  function chartModeLabel() {
    if (state.chartMode === "tokens") {
      return "Token";
    }
    if (state.chartMode === "cost") {
      return state.chartCurrency + " 预估费用";
    }
    return "请求次数";
  }

  function formatChartPoint(value) {
    if (state.chartMode === "cost") {
      return formatMoney(state.chartCurrency, value);
    }
    return formatInteger(value) + (state.chartMode === "tokens" ? " Token" : " 次请求");
  }

  function renderKeyRanking() {
    dom.keyRankingBody.replaceChildren();
    const keys = (state.report.keys || []).slice().sort(function (a, b) {
      return number((b.summary || {}).attempts) - number((a.summary || {}).attempts);
    }).slice(0, 8);
    dom.keyRankingEmpty.hidden = keys.length > 0;
    keys.forEach(function (key) {
      const summary = key.summary || {};
      const row = element("tr");
      const nameCell = element("td");
      nameCell.appendChild(element("span", "table-primary", key.alias || "未命名 Key"));
      nameCell.appendChild(element("code", "table-subline", key.masked_key || key.key_id));
      row.appendChild(nameCell);
      row.appendChild(element("td", "", formatInteger(summary.attempts)));
      row.appendChild(element("td", "", formatCompact(number((summary.tokens || {}).total_tokens))));
      row.appendChild(element("td", "", number(summary.attempts) > 0 ? formatPercent(number(summary.success) / number(summary.attempts)) : "—"));
      row.appendChild(element("td", "", formatSummaryCost(summary, state.report.currency)));
      dom.keyRankingBody.appendChild(row);
    });
  }

  function renderModelBreakdown() {
    dom.modelBreakdown.replaceChildren();
    const models = (state.report.models || []).slice().sort(function (a, b) {
      return number((b.summary || {}).attempts) - number((a.summary || {}).attempts);
    }).slice(0, 8);
    dom.modelCount.textContent = (state.report.models || []).length + " 个模型";
    dom.modelEmpty.hidden = models.length > 0;
    const max = Math.max.apply(null, models.map(function (item) { return number((item.summary || {}).attempts); }).concat([1]));
    models.forEach(function (model) {
      const summary = model.summary || {};
      const row = element("div", "model-row");
      row.appendChild(element("span", "model-name", model.model || "unknown"));
      row.appendChild(element("span", "model-provider", (model.provider || "unknown") + " · " + (model.service_tier || "default")));
      row.appendChild(element("span", "model-value", formatInteger(summary.attempts) + " 次"));
      const progress = element("progress", "model-progress");
      progress.max = max;
      progress.value = number(summary.attempts);
      progress.setAttribute("aria-label", (model.model || "unknown") + " 请求数相对当前最高值");
      row.appendChild(progress);
      dom.modelBreakdown.appendChild(row);
    });
  }

  function mergedKeys() {
    const usageByID = new Map();
    (state.baseReport && state.baseReport.keys || []).forEach(function (usage) { usageByID.set(usage.key_id, usage); });
    const merged = state.profiles.map(function (profile) {
      const usage = usageByID.get(profile.id);
      usageByID.delete(profile.id);
      return {
        profile: profile,
        id: profile.id,
        alias: profile.alias || (usage && usage.alias) || "",
        maskedKey: profile.masked_key || (usage && usage.masked_key) || "",
        summary: usage && usage.summary || {},
        historical: false
      };
    });
    usageByID.forEach(function (usage, id) {
      merged.push({ profile: null, id: id, alias: usage.alias || "", maskedKey: usage.masked_key || "", summary: usage.summary || {}, historical: true });
    });
    return merged;
  }

  function renderKeys() {
    syncCostSortAvailability();
    const rows = mergedKeys().filter(keyMatchesFilters).sort(compareKeys);
    const pageCount = Math.max(1, Math.ceil(rows.length / PAGE_SIZE));
    state.keyPage = Math.min(Math.max(1, state.keyPage), pageCount);
    const start = (state.keyPage - 1) * PAGE_SIZE;
    const pageRows = rows.slice(start, start + PAGE_SIZE);

    dom.keyTotal.textContent = state.profiles.length + " 个 Key";
    dom.keysTableBody.replaceChildren();
    dom.keysCardList.replaceChildren();
    pageRows.forEach(function (item) {
      dom.keysTableBody.appendChild(createKeyTableRow(item));
      dom.keysCardList.appendChild(createKeyCard(item));
    });
    dom.keysEmpty.hidden = rows.length > 0;
    dom.paginationSummary.textContent = rows.length ? (start + 1) + "–" + Math.min(start + PAGE_SIZE, rows.length) + " / " + rows.length : "0–0 / 0";
    dom.pagePrev.disabled = state.keyPage <= 1;
    dom.pageNext.disabled = state.keyPage >= pageCount;
  }

  function syncCostSortAvailability() {
    const option = dom.keySort.querySelector('option[value="cost"]');
    if (!option) {
      return;
    }
    const multipleCurrencies = reportCurrencies(state.baseReport).length > 1;
    option.disabled = multipleCurrencies;
    option.textContent = multipleCurrencies ? "费用最高（多币种不可用）" : "费用最高";
    if (multipleCurrencies && state.keySort === "cost") {
      state.keySort = "requests";
      dom.keySort.value = "requests";
    }
  }

  function keyMatchesFilters(item) {
    const profile = item.profile;
    const searchable = [item.alias, item.id, item.maskedKey].join(" ").toLocaleLowerCase();
    if (state.keySearch && !searchable.includes(state.keySearch)) {
      return false;
    }
    if (state.keyStatus === "enabled" && (!profile || profile.disabled || !profile.effective)) {
      return false;
    }
    if (state.keyStatus === "disabled" && (!profile || !profile.disabled)) {
      return false;
    }
    if (state.keyStatus === "issue" && (!profile || (profile.effective && !profile.issue))) {
      return false;
    }
    return true;
  }

  function compareKeys(left, right) {
    if (state.keySort === "alias") {
      return (left.alias || left.id).localeCompare(right.alias || right.id, "zh-CN");
    }
    if (state.keySort === "tokens") {
      return number((right.summary.tokens || {}).total_tokens) - number((left.summary.tokens || {}).total_tokens);
    }
    if (state.keySort === "cost") {
      return totalKnownCost(right.summary) - totalKnownCost(left.summary);
    }
    if (state.keySort === "recent") {
      return timestamp(right.summary.last_used_at) - timestamp(left.summary.last_used_at);
    }
    return number(right.summary.attempts) - number(left.summary.attempts);
  }

  function createKeyTableRow(item) {
    const row = element("tr");
    const identity = element("td");
    identity.appendChild(element("span", "table-primary", item.alias || (item.historical ? "历史 Key" : "未命名 Key")));
    identity.appendChild(element("code", "table-subline", (item.maskedKey || "无脱敏信息") + " · " + item.id));
    row.appendChild(identity);
    const statusCell = element("td");
    statusCell.appendChild(keyStatusChip(item));
    row.appendChild(statusCell);
    row.appendChild(element("td", "", formatInteger(item.summary.attempts)));
    row.appendChild(element("td", "", formatCompact(number((item.summary.tokens || {}).total_tokens))));
    row.appendChild(element("td", "", formatSummaryCost(item.summary, state.baseReport && state.baseReport.currency)));
    row.appendChild(element("td", "", item.summary.last_used_at ? formatDateTime(item.summary.last_used_at) : "从未"));
    const action = element("td");
    if (item.profile) {
      const button = element("button", "edit-key-button", "编辑");
      button.type = "button";
      button.addEventListener("click", function () { openProfileEditor(item.profile); });
      action.appendChild(button);
    } else {
      action.appendChild(element("span", "muted", "只读"));
    }
    row.appendChild(action);
    return row;
  }

  function createKeyCard(item) {
    const card = element("article", "key-mobile-card");
    const header = element("header");
    const title = element("div");
    title.appendChild(element("strong", "", item.alias || (item.historical ? "历史 Key" : "未命名 Key")));
    title.appendChild(element("code", "table-subline", item.maskedKey || item.id));
    header.appendChild(title);
    header.appendChild(keyStatusChip(item));
    card.appendChild(header);
    const list = element("dl");
    appendDefinition(list, "请求", formatInteger(item.summary.attempts));
    appendDefinition(list, "Token", formatCompact(number((item.summary.tokens || {}).total_tokens)));
    appendDefinition(list, "预估费用", formatSummaryCost(item.summary, state.baseReport && state.baseReport.currency));
    appendDefinition(list, "最近使用", item.summary.last_used_at ? formatDateTime(item.summary.last_used_at) : "从未");
    card.appendChild(list);
    const footer = element("footer");
    footer.appendChild(element("code", "table-subline", item.id));
    if (item.profile) {
      const button = element("button", "edit-key-button", "编辑");
      button.type = "button";
      button.addEventListener("click", function () { openProfileEditor(item.profile); });
      footer.appendChild(button);
    }
    card.appendChild(footer);
    return card;
  }

  function appendDefinition(list, term, description) {
    const wrapper = element("div");
    wrapper.appendChild(element("dt", "", term));
    wrapper.appendChild(element("dd", "", description));
    list.appendChild(wrapper);
  }

  function keyStatusChip(item) {
    if (item.historical) {
      return element("span", "status-chip", "历史记录");
    }
    if (!item.profile.effective || item.profile.issue) {
      return element("span", "status-chip issue", item.profile.issue === "duplicate" ? "重复 Key" : "配置异常");
    }
    if (item.profile.disabled) {
      return element("span", "status-chip disabled", "已停用");
    }
    return element("span", "status-chip success", "已启用");
  }

  function openProfileEditor(profile) {
    state.editingProfile = clone(profile);
    dom.profileAlias.value = profile.alias || "";
    dom.profileDisabled.checked = Boolean(profile.disabled);
    dom.profileId.value = profile.id || "";
    dom.profilePreviewAlias.textContent = profile.alias || "未命名 Key";
    dom.profilePreviewKey.textContent = profile.masked_key || "无脱敏信息";
    dom.aliasCount.textContent = Array.from(dom.profileAlias.value).length + " / 128";
    dom.profileFormError.textContent = "";
    dom.profileFormError.hidden = true;
    updateProfilePreviewStatus();
    dom.profileDialog.showModal();
    window.setTimeout(function () { dom.profileAlias.focus(); }, 0);
  }

  function updateProfilePreviewStatus() {
    const disabled = dom.profileDisabled.checked;
    dom.profilePreviewStatus.textContent = disabled ? "将停用" : "已启用";
    dom.profilePreviewStatus.className = "status-chip " + (disabled ? "disabled" : "success");
  }

  async function saveProfile(event) {
    event.preventDefault();
    const profile = state.editingProfile;
    if (!profile) {
      return;
    }
    const alias = dom.profileAlias.value.trim();
    const id = dom.profileId.value.trim();
    const disabled = dom.profileDisabled.checked;
    const idPattern = /^[A-Za-z0-9][A-Za-z0-9._-]*$/;
    if (Array.from(alias).length > 128) {
      setProfileError("别名不能超过 128 个字符。");
      return;
    }
    if (!id || id.length > 64 || !idPattern.test(id)) {
      setProfileError("稳定 ID 必须为 1–64 位，并仅包含字母、数字、点、下划线或连字符。");
      return;
    }
    if (!profile.disabled && disabled) {
      const confirmed = await confirmAction("停用此 Key？", "停用后，此 Key 的新请求会被拒绝；历史统计仍会保留。", "确认停用");
      if (!confirmed) {
        return;
      }
    }

    setProfileError("");
    dom.saveProfile.disabled = true;
    try {
      await apiRequest(API.profiles, {
        method: "PATCH",
        body: JSON.stringify({ index: profile.index, expected_id: profile.id, id: id, alias: alias, disabled: disabled })
      });
      dom.profileDialog.close();
      toast("Key 档案已更新");
      await refreshAll({ includeSettings: false });
    } catch (error) {
      if (error.status === 401 || error.status === 403) {
        dom.profileDialog.close();
        signOut(apiErrorMessage(error));
        return;
      }
      setProfileError(error.status === 409 ? "配置已被其他操作修改。请关闭窗口、刷新列表后重新应用。" : apiErrorMessage(error));
    } finally {
      dom.saveProfile.disabled = false;
    }
  }

  function setProfileError(message) {
    dom.profileFormError.textContent = message || "";
    dom.profileFormError.hidden = !message;
  }

  function confirmAction(title, message, acceptLabel) {
    if (confirmResolver) {
      confirmResolver(false);
    }
    dom.confirmTitle.textContent = title;
    dom.confirmMessage.textContent = message;
    dom.confirmAccept.textContent = acceptLabel || "确认";
    dom.confirmDialog.showModal();
    return new Promise(function (resolve) {
      confirmResolver = resolve;
    });
  }

  function resolveConfirmation(value) {
    if (dom.confirmDialog.open) {
      dom.confirmDialog.close();
    }
    if (confirmResolver) {
      const resolver = confirmResolver;
      confirmResolver = null;
      resolver(Boolean(value));
    }
  }

  function setSettings(settings, replaceDraft) {
    state.settings = normalizeSettings(settings);
    if (replaceDraft !== false || !state.billingDraft) {
      state.billingDraft = clone(state.settings);
      state.billingBaseRevision = state.settings.revision;
      markBillingDirty(false);
      renderBillingSettings();
    }
  }

  function normalizeSettings(settings) {
    const value = settings && typeof settings === "object" ? settings : {};
    value.enabled = Boolean(value.enabled);
    value.retention_days = number(value.retention_days) || 90;
    value.pricing = value.pricing || {};
    value.pricing.currency = value.pricing.currency || "USD";
    value.pricing.version = value.pricing.version || "default";
    value.pricing.rules = Array.isArray(value.pricing.rules) ? value.pricing.rules : [];
    value.limits = value.limits || {};
    value.revision = value.revision || "";
    return value;
  }

  function renderBillingSettings() {
    const draft = state.billingDraft;
    if (!draft) {
      return;
    }
    dom.billingEnabled.checked = Boolean(draft.enabled);
    dom.retentionDays.value = draft.retention_days;
    dom.pricingCurrency.value = draft.pricing.currency;
    dom.pricingVersion.value = draft.pricing.version;
    dom.settingsRevision.textContent = "版本 " + shortRevision(state.settings && state.settings.revision);
    renderPricingRules();
  }

  function syncBillingDraftFromForm() {
    if (!state.billingDraft) {
      return;
    }
    state.billingDraft.enabled = dom.billingEnabled.checked;
    state.billingDraft.retention_days = Number(dom.retentionDays.value);
    state.billingDraft.pricing.currency = dom.pricingCurrency.value.trim().toUpperCase();
    state.billingDraft.pricing.version = dom.pricingVersion.value.trim();
  }

  function markBillingDirty(dirty) {
    state.billingDirty = Boolean(dirty);
    dom.billingDirty.hidden = !state.billingDirty;
    dom.saveBilling.disabled = !state.billingDirty;
    dom.resetBilling.disabled = !state.billingDirty;
  }

  function addPricingRule() {
    if (!state.billingDraft) {
      return;
    }
    const maxRules = number((state.billingDraft.limits || {}).max_rules) || 500;
    if (state.billingDraft.pricing.rules.length >= maxRules) {
      setBillingError("价格规则最多允许 " + maxRules + " 条。");
      return;
    }
    state.billingDraft.pricing.rules.push({
      provider: "*",
      model: "*",
      "service-tier": "*",
      "input-per-million": "",
      "output-per-million": "",
      "cache-read-per-million": "",
      "cache-write-per-million": ""
    });
    markBillingDirty(true);
    renderPricingRules();
    const addedRow = dom.pricingRulesBody.lastElementChild;
    const firstInput = addedRow && addedRow.querySelector("input");
    if (firstInput) {
      firstInput.focus();
    }
  }

  function renderPricingRules() {
    dom.pricingRulesBody.replaceChildren();
    const rules = state.billingDraft ? state.billingDraft.pricing.rules : [];
    dom.pricingEmpty.hidden = rules.length > 0;
    rules.forEach(function (rule, index) {
      const cacheInputRate = String(rule["cache-read-per-million"] || rule["cache-write-per-million"] || "").trim();
      rule["cache-read-per-million"] = cacheInputRate;
      rule["cache-write-per-million"] = cacheInputRate;
      const row = element("tr");
      [
        { field: "provider", label: "Provider", placeholder: "openai" },
        { field: "model", label: "模型", placeholder: "gpt-*" },
        { field: "service-tier", label: "Tier", placeholder: "*" },
        { field: "input-per-million", label: "输入单价", placeholder: "0.00" },
        { field: "cache-read-per-million", label: "缓存输入单价", placeholder: "0.00" },
        { field: "output-per-million", label: "输出单价", placeholder: "0.00" }
      ].forEach(function (definition) {
        const cell = element("td");
        const input = element("input");
        input.type = "text";
        input.value = rule[definition.field] || "";
        input.placeholder = definition.placeholder;
        input.setAttribute("aria-label", "规则 " + (index + 1) + " " + definition.label);
        if (RATE_FIELDS.includes(definition.field)) {
          input.inputMode = "decimal";
          input.spellcheck = false;
        }
        input.addEventListener("input", function () {
          state.billingDraft.pricing.rules[index][definition.field] = input.value.trim();
          if (definition.field === "cache-read-per-million") {
            state.billingDraft.pricing.rules[index]["cache-write-per-million"] = input.value.trim();
          }
          setBillingError("");
          markBillingDirty(true);
        });
        cell.appendChild(input);
        row.appendChild(cell);
      });

      const actions = element("td");
      const group = element("div", "rule-actions");
      const up = element("button", "rule-action", "↑");
      up.type = "button";
      up.title = "上移规则";
      up.setAttribute("aria-label", "上移规则 " + (index + 1));
      up.disabled = index === 0;
      up.addEventListener("click", function () { movePricingRule(index, -1); });
      const down = element("button", "rule-action", "↓");
      down.type = "button";
      down.title = "下移规则";
      down.setAttribute("aria-label", "下移规则 " + (index + 1));
      down.disabled = index === rules.length - 1;
      down.addEventListener("click", function () { movePricingRule(index, 1); });
      const remove = element("button", "rule-action delete", "×");
      remove.type = "button";
      remove.title = "删除规则";
      remove.setAttribute("aria-label", "删除规则 " + (index + 1));
      remove.addEventListener("click", function () {
        state.billingDraft.pricing.rules.splice(index, 1);
        markBillingDirty(true);
        renderPricingRules();
      });
      group.append(up, down, remove);
      actions.appendChild(group);
      row.appendChild(actions);
      dom.pricingRulesBody.appendChild(row);
    });
  }

  function movePricingRule(index, direction) {
    const rules = state.billingDraft.pricing.rules;
    const target = index + direction;
    if (target < 0 || target >= rules.length) {
      return;
    }
    const temporary = rules[index];
    rules[index] = rules[target];
    rules[target] = temporary;
    markBillingDirty(true);
    renderPricingRules();
    const movedRow = dom.pricingRulesBody.children[target];
    const movedInput = movedRow && movedRow.querySelector("input");
    if (movedInput) {
      movedInput.focus();
    }
  }

  function resetBillingDraft() {
    if (!state.settings) {
      return;
    }
    state.billingDraft = clone(state.settings);
    state.billingBaseRevision = state.settings.revision;
    setBillingError("");
    markBillingDirty(false);
    renderBillingSettings();
  }

  function validateBillingDraft() {
    syncBillingDraftFromForm();
    const draft = state.billingDraft;
    const limits = draft.limits || {};
    const minDays = number(limits.min_retention_days) || 1;
    const maxDays = number(limits.max_retention_days) || 3650;
    if (!Number.isInteger(draft.retention_days) || draft.retention_days < minDays || draft.retention_days > maxDays) {
      return "保留天数必须是 " + minDays + "–" + maxDays + " 之间的整数。";
    }
    if (!draft.pricing.currency || draft.pricing.currency.length > (number(limits.max_currency_length) || 16)) {
      return "请输入有效币种，长度不能超过 " + (number(limits.max_currency_length) || 16) + "。";
    }
    if (!draft.pricing.version || draft.pricing.version.length > (number(limits.max_version_length) || 96)) {
      return "请输入有效规则版本。";
    }
    const decimal = /^[0-9]+(?:\.[0-9]+)?$/;
    for (let index = 0; index < draft.pricing.rules.length; index += 1) {
      const rule = draft.pricing.rules[index];
      const numberLabel = "第 " + (index + 1) + " 条规则";
      rule["cache-write-per-million"] = String(rule["cache-read-per-million"] || "").trim();
      if (!(rule.provider || "").trim() || !(rule.model || "").trim()) {
        return numberLabel + "必须填写 Provider 和模型匹配式。";
      }
      const rates = RATE_FIELDS.map(function (field) { return String(rule[field] || "").trim(); });
      if (!rates.some(Boolean)) {
        return numberLabel + "至少需要填写一个单价。";
      }
      const invalidRate = rates.find(function (rate) { return rate && !decimal.test(rate); });
      if (invalidRate) {
        return numberLabel + "包含无效单价；请使用非负十进制数字。";
      }
    }
    return "";
  }

  async function saveBillingSettings() {
    const validationError = validateBillingDraft();
    setBillingError(validationError);
    if (validationError) {
      return;
    }
    dom.saveBilling.disabled = true;
    dom.saveBilling.setAttribute("aria-busy", "true");
    try {
      const draft = state.billingDraft;
      await apiRequest(API.settings, {
        method: "PUT",
        body: JSON.stringify({
          enabled: draft.enabled,
          retention_days: draft.retention_days,
          pricing: {
            currency: draft.pricing.currency,
            version: draft.pricing.version,
            rules: draft.pricing.rules
          },
          expected_revision: state.billingBaseRevision
        })
      });
      const latest = await apiRequest(API.settings, { method: "GET" });
      setSettings(latest, true);
      toast("计费设置已保存，历史费用与新请求已统一按新规则计算");
      await refreshAll({ includeSettings: false });
    } catch (error) {
      if (error.status === 401 || error.status === 403) {
        signOut(apiErrorMessage(error));
        return;
      }
      if (error.status === 409) {
        try {
          const latest = normalizeSettings(await apiRequest(API.settings, { method: "GET" }));
          state.settings = latest;
          dom.settingsRevision.textContent = "最新 " + shortRevision(latest.revision) + " · 草稿基于 " + shortRevision(state.billingBaseRevision);
          setBillingError("设置已被其他标签页修改，本次未保存，以避免覆盖对方修改。请先撤销草稿载入最新版本，再重新编辑。");
        } catch (refreshError) {
          setBillingError("设置已发生冲突，且无法载入最新版本：" + apiErrorMessage(refreshError));
        }
      } else {
        setBillingError(apiErrorMessage(error));
      }
    } finally {
      dom.saveBilling.removeAttribute("aria-busy");
      dom.saveBilling.disabled = !state.billingDirty;
    }
  }

  function setBillingError(message) {
    dom.billingFormError.textContent = message || "";
    dom.billingFormError.hidden = !message;
  }

  async function exportCSV() {
    if (!state.managementSecret) {
      return;
    }
    dom.exportCsv.disabled = true;
    dom.compactExport.disabled = true;
    dom.exportCsv.setAttribute("aria-busy", "true");
    dom.compactExport.setAttribute("aria-busy", "true");
    try {
      const response = await window.fetch(API.csv + buildUsageQuery(true), {
        method: "GET",
        cache: "no-store",
        credentials: "same-origin",
        headers: { Authorization: "Bearer " + state.managementSecret, Accept: "text/csv" }
      });
      if (!response.ok) {
        let payload = null;
        try { payload = await response.json(); } catch (_error) { payload = null; }
        const error = new Error(extractError(payload, response.status));
        error.status = response.status;
        throw error;
      }
      const blob = await response.blob();
      const url = URL.createObjectURL(blob);
      const anchor = element("a");
      anchor.href = url;
      anchor.download = "cliproxy-usage-" + utcDateString(new Date()) + ".csv";
      anchor.hidden = true;
      document.body.appendChild(anchor);
      anchor.click();
      anchor.remove();
      URL.revokeObjectURL(url);
      toast("CSV 已按当前筛选导出");
    } catch (error) {
      if (error.status === 401 || error.status === 403) {
        signOut(apiErrorMessage(error));
        return;
      }
      toast(apiErrorMessage(error), true);
    } finally {
      dom.exportCsv.disabled = false;
      dom.compactExport.disabled = false;
      dom.exportCsv.removeAttribute("aria-busy");
      dom.compactExport.removeAttribute("aria-busy");
    }
  }

  function setView(view) {
    if (!view || view === state.currentView) {
      return;
    }
    state.currentView = view;
    all("[data-view-panel]").forEach(function (panel) { panel.hidden = panel.dataset.viewPanel !== view; });
    all("[data-view]").forEach(function (button) {
      const active = button.dataset.view === view;
      button.classList.toggle("active", active);
      if (active) {
        button.setAttribute("aria-current", "page");
      } else {
        button.removeAttribute("aria-current");
      }
    });
    const headings = {
      overview: ["实时成本视图", "用量总览"],
      keys: ["身份与访问", "API Keys"],
      billing: ["价格引擎", "计费规则"]
    };
    dom.pageKicker.textContent = headings[view][0];
    dom.pageTitle.textContent = headings[view][1];
    if (view === "billing" && state.billingDraft) {
      renderBillingSettings();
    }
    window.scrollTo({ top: 0, behavior: window.matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth" });
  }

  function toast(message, isError) {
    const item = element("div", "toast" + (isError ? " error" : ""), message);
    dom.toastRegion.appendChild(item);
    window.setTimeout(function () { item.remove(); }, isError ? 6000 : 3200);
  }

  function number(value) {
    const parsed = Number(value);
    return Number.isFinite(parsed) ? parsed : 0;
  }

  function timestamp(value) {
    const parsed = Date.parse(value || "");
    return Number.isFinite(parsed) ? parsed : 0;
  }

  function formatInteger(value) {
    return new Intl.NumberFormat("zh-CN", { maximumFractionDigits: 0 }).format(number(value));
  }

  function formatCompact(value) {
    const amount = number(value);
    if (Math.abs(amount) < 10000) {
      return formatInteger(amount);
    }
    try {
      return new Intl.NumberFormat("zh-CN", { notation: "compact", maximumFractionDigits: 2 }).format(amount);
    } catch (_error) {
      return formatInteger(amount);
    }
  }

  function formatChartValue(value) {
    if (state.chartMode === "cost") {
      return (number(value) / 1000000).toLocaleString("zh-CN", { maximumFractionDigits: 3 });
    }
    return formatCompact(value);
  }

  function formatPercent(ratio) {
    return new Intl.NumberFormat("zh-CN", { style: "percent", maximumFractionDigits: 1 }).format(Math.max(0, Math.min(1, number(ratio))));
  }

  function formatDuration(milliseconds) {
    const value = number(milliseconds);
    if (value < 1000) {
      return formatInteger(value) + " ms";
    }
    return (value / 1000).toLocaleString("zh-CN", { maximumFractionDigits: 2 }) + " s";
  }

  function formatMoney(currency, micros) {
    const code = String(currency || "USD").toUpperCase();
    const amount = number(micros) / 1000000;
    return code + " " + amount.toLocaleString("zh-CN", { minimumFractionDigits: amount === 0 ? 2 : 2, maximumFractionDigits: 6 });
  }

  function formatDateTime(value) {
    const date = value instanceof Date ? value : new Date(value);
    if (!Number.isFinite(date.getTime())) {
      return "—";
    }
    return new Intl.DateTimeFormat("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false }).format(date);
  }

  function shortDay(day) {
    const parts = String(day || "").split("-");
    return parts.length === 3 ? parts[1] + "/" + parts[2] : String(day || "");
  }

  function costMap(summary, fallbackCurrency) {
    const source = summary && summary.estimated_cost_micros_by_currency;
    const result = {};
    if (source && typeof source === "object") {
      Object.keys(source).forEach(function (currency) {
        result[String(currency).toUpperCase()] = number(source[currency]);
      });
    }
    if (!Object.keys(result).length && number(summary && summary.estimated_cost_micros) !== 0) {
      result[String(fallbackCurrency || "UNKNOWN").toUpperCase()] = number(summary.estimated_cost_micros);
    }
    return result;
  }

  function totalKnownCost(summary) {
    const costs = costMap(summary, state.baseReport && state.baseReport.currency);
    const currencies = Object.keys(costs);
    return currencies.length === 1 ? number(costs[currencies[0]]) : 0;
  }

  function formatSummaryCost(summary, fallbackCurrency) {
    const costs = costMap(summary, fallbackCurrency);
    const currencies = Object.keys(costs);
    if (currencies.length === 1) {
      return formatMoney(currencies[0], costs[currencies[0]]);
    }
    if (currencies.length > 1) {
      return currencies.length + " 种币种";
    }
    return "—";
  }

  function reportCurrencies(report) {
    const set = new Set((report && report.currencies || []).map(function (currency) { return String(currency).toUpperCase(); }));
    Object.keys(costMap(report && report.summary || {}, report && report.currency)).forEach(function (currency) { set.add(currency); });
    return Array.from(set).filter(Boolean).sort();
  }

  function settingsCurrency() {
    return state.settings && state.settings.pricing && state.settings.pricing.currency || "USD";
  }

  function shortRevision(revision) {
    const value = String(revision || "—");
    return value.length > 12 ? value.slice(0, 12) : value;
  }

  init();
}());
