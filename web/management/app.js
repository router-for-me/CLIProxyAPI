(function () {
  "use strict";

  const API = Object.freeze({
    profiles: "/v0/management/api-key-profiles",
    apiKeys: "/v0/management/api-keys",
    resetClientKeyUsage: "/v0/management/client-key-usage/reset",
    usage: "/v0/management/client-key-usage",
    settings: "/v0/management/usage-billing-settings",
    csv: "/v0/management/usage/export.csv",
    authFiles: "/v0/management/auth-files",
    authFileModels: "/v0/management/auth-files/models",
    resetQuota: "/v0/management/reset-quota",
    apiCall: "/v0/management/api-call",
    authStatus: "/v0/management/get-auth-status",
    logs: "/v0/management/logs",
    errorLogs: "/v0/management/request-error-logs",
    plugins: "/v0/management/plugins",
    config: "/v0/management/config",
    configYAML: "/v0/management/config.yaml"
  });
  const SESSION_SECRET_KEY = "cliproxy:pulse:management-secret";
  const THEME_KEY = "cliproxy:pulse:theme";
  const SVG_NS = "http" + "://www.w3.org/2000/svg";
  const PAGE_SIZE = 25;
  const KEY_DETAIL_REFRESH_INTERVAL = 30000;
  const RATE_FIELDS = ["input-per-million", "output-per-million", "cache-read-per-million", "cache-write-per-million"];
  const BILLING_CUSTOM_VALUE = "__custom__";
  const QUOTA_PROVIDER_LABELS = Object.freeze({
    codex: "OpenAI · Codex",
    claude: "Anthropic · Claude",
    antigravity: "Google · Antigravity",
    kimi: "Moonshot · Kimi",
    xai: "xAI · Grok"
  });
  const QUOTA_PROVIDER_ORDER = Object.freeze(["codex", "claude", "antigravity", "kimi", "xai"]);
  const BILLING_PROVIDER_LABELS = Object.freeze({
    codex: "OpenAI · Codex",
    openai: "OpenAI API",
    claude: "Anthropic · Claude",
    anthropic: "Anthropic · Claude",
    gemini: "Google · Gemini",
    "gemini-cli": "Google · Gemini CLI",
    antigravity: "Google · Antigravity",
    xai: "xAI · Grok",
    kimi: "Moonshot · Kimi"
  });
  const BILLING_TIERS = Object.freeze([
    { value: "*", label: "全部层级" },
    { value: "default", label: "默认" },
    { value: "auto", label: "自动" },
    { value: "priority", label: "优先" },
    { value: "flex", label: "Flex" },
    { value: "batch", label: "Batch" }
  ]);

  const state = {
    managementSecret: "",
    serverVersion: "",
    profiles: [],
    report: null,
    baseReport: null,
    billingCatalogReport: null,
    billingProviderCatalog: [],
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
    keyUsageResettingAll: false,
    keyDetail: null,
    keyDetailLoading: false,
    keyDetailRequest: 0,
    editingProfile: null,
    authFiles: [],
    credentialSearch: "",
    credentialProvider: "all",
    credentialStatus: "all",
    quotaSearch: "",
    quotaProvider: "all",
    quotaResults: {},
    quotaBusy: {},
    quotaRefreshingAll: false,
    quotaWindowOffset: 0,
    quotaWindowMode: "week",
    quotaEpoch: 0,
    oauthProvider: "",
    oauthSessionState: "",
    logs: [],
    errorLogFiles: [],
    logSearch: "",
    logLevel: "all",
    logCursor: "",
    logLiveError: false,
    plugins: [],
    runtimeConfig: null,
    configYAML: "",
    yamlDirty: false,
    loadedViews: {},
    currentView: "overview",
    lastUpdatedAt: null
  };

  const dom = {};
  const billingCustomModes = new WeakMap();
  let refreshController = null;
  let refreshSequence = 0;
  let filterTimer = 0;
  let resizeTimer = 0;
  let confirmResolver = null;
  let oauthPollTimer = 0;
  let logsLiveTimer = 0;
  let viewEnterTimer = 0;
  let keyDetailRefreshTimer = 0;
  let keyDetailController = null;
  let motionFrame = 0;
  let motionPointerX = 0;
  let motionPointerY = 0;
  let motionSurface = null;
  let motionPointerGlow = null;

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
    initMotionExperience();
    window.setInterval(function () {
      if (!document.hidden && state.currentView === "quota") {
        refreshQuotaCountdowns();
      }
    }, 1000);

    const remembered = safeStorage(window.sessionStorage, "get", SESSION_SECRET_KEY);
    if (remembered) {
      dom.secret.value = remembered;
      dom.rememberSecret.checked = true;
      authenticate(remembered, true);
    }
  }

  function initMotionExperience() {
    const root = document.documentElement;
    const reduceMotion = window.matchMedia("(prefers-reduced-motion: reduce)");
    const finePointer = window.matchMedia("(hover: hover) and (pointer: fine)");
    root.classList.add("motion-ready");
    if (reduceMotion.matches || !finePointer.matches) {
      return;
    }

    motionPointerGlow = element("div", "magic-pointer-glow");
    motionPointerGlow.setAttribute("aria-hidden", "true");
    motionPointerGlow.appendChild(element("span", "magic-pointer-core"));
    document.body.appendChild(motionPointerGlow);

    document.addEventListener("pointermove", handleMotionPointerMove, { passive: true });
    document.addEventListener("pointerover", handleMotionPointerOver, { passive: true });
    document.addEventListener("pointerout", handleMotionPointerOut, { passive: true });
    document.addEventListener("pointerdown", handleMotionPointerDown, { passive: true });
    window.addEventListener("blur", clearMotionSurface);
    document.addEventListener("visibilitychange", function () {
      root.classList.toggle("motion-paused", document.hidden);
    });
    document.addEventListener("mouseleave", function () {
      root.classList.remove("pointer-present");
      clearMotionSurface();
    });
  }

  function handleMotionPointerMove(event) {
    if (event.pointerType && event.pointerType !== "mouse" && event.pointerType !== "pen") {
      return;
    }
    motionPointerX = event.clientX;
    motionPointerY = event.clientY;
    document.documentElement.classList.add("pointer-present");
    if (!motionFrame) {
      motionFrame = window.requestAnimationFrame(renderMotionPointer);
    }
  }

  function renderMotionPointer() {
    motionFrame = 0;
    if (motionPointerGlow) {
      motionPointerGlow.style.transform = "translate3d(" + (motionPointerX - 190) + "px," + (motionPointerY - 190) + "px,0)";
    }
    if (!motionSurface || !motionSurface.isConnected) {
      return;
    }
    const bounds = motionSurface.getBoundingClientRect();
    if (!bounds.width || !bounds.height) {
      return;
    }
    const x = Math.max(0, Math.min(100, (motionPointerX - bounds.left) / bounds.width * 100));
    const y = Math.max(0, Math.min(100, (motionPointerY - bounds.top) / bounds.height * 100));
    motionSurface.style.setProperty("--motion-x", x.toFixed(2) + "%");
    motionSurface.style.setProperty("--motion-y", y.toFixed(2) + "%");
  }

  function handleMotionPointerOver(event) {
    const target = event.target instanceof Element ? event.target : null;
    const surface = target && target.closest(".metric-card, .panel, .filter-bar, .list-toolbar, .settings-strip, .save-bar, .quota-card, .credential-card, .pricing-rule-card");
    if (!surface || surface === motionSurface) {
      return;
    }
    clearMotionSurface();
    motionSurface = surface;
    motionSurface.classList.add("motion-surface", "is-pointer-lit");
    if (!motionSurface.querySelector(":scope > .motion-glow")) {
      const glow = element("span", "motion-glow");
      glow.setAttribute("aria-hidden", "true");
      motionSurface.appendChild(glow);
    }
  }

  function handleMotionPointerOut(event) {
    if (!motionSurface) {
      return;
    }
    const next = event.relatedTarget instanceof Node ? event.relatedTarget : null;
    if (next && motionSurface.contains(next)) {
      return;
    }
    clearMotionSurface();
  }

  function clearMotionSurface() {
    if (!motionSurface) {
      return;
    }
    motionSurface.classList.remove("is-pointer-lit");
    motionSurface = null;
  }

  function handleMotionPointerDown(event) {
    const target = event.target instanceof Element ? event.target : null;
    const control = target && target.closest(".button, .icon-button, .nav-item, .preset-button, .segment button, .provider-connect");
    if (!control || control.disabled) {
      return;
    }
    const bounds = control.getBoundingClientRect();
    const ripple = element("span", "magic-ripple");
    const size = Math.max(bounds.width, bounds.height) * 1.45;
    ripple.style.width = size + "px";
    ripple.style.height = size + "px";
    ripple.style.left = (event.clientX - bounds.left - size / 2) + "px";
    ripple.style.top = (event.clientY - bounds.top - size / 2) + "px";
    ripple.setAttribute("aria-hidden", "true");
    control.classList.add("motion-ripple-host");
    control.appendChild(ripple);
    let rippleRemoved = false;
    const removeRipple = function () {
      if (rippleRemoved) {
        return;
      }
      rippleRemoved = true;
      ripple.remove();
    };
    ripple.addEventListener("animationend", removeRipple, { once: true });
    window.setTimeout(removeRipple, 760);
    if (control.matches(".button.primary, .nav-item.active")) {
      createMagicSparks(event.clientX, event.clientY);
    }
  }

  function createMagicSparks(x, y) {
    const layer = element("span", "magic-spark-burst");
    layer.setAttribute("aria-hidden", "true");
    layer.style.left = x + "px";
    layer.style.top = y + "px";
    for (let index = 0; index < 6; index += 1) {
      const spark = element("i");
      const angle = Math.PI * 2 * index / 6 + Math.PI / 12;
      const distance = 22 + index % 2 * 9;
      spark.style.setProperty("--spark-x", Math.cos(angle) * distance + "px");
      spark.style.setProperty("--spark-y", Math.sin(angle) * distance + "px");
      spark.style.setProperty("--spark-delay", index * 18 + "ms");
      layer.appendChild(spark);
    }
    document.body.appendChild(layer);
    window.setTimeout(function () { layer.remove(); }, 760);
  }

  function cacheDOM() {
    [
      "auth-view", "app-view", "auth-form", "management-secret", "remember-secret", "toggle-secret", "connect-button", "auth-error",
      "insecure-warning", "insecure-confirm-row", "insecure-confirm", "sign-out", "compact-sign-out", "compact-export",
      "page-kicker", "page-title", "last-updated", "refresh-data", "key-create", "key-reset-usage", "keys-refresh", "theme-toggle", "export-csv",
      "notice-stack", "filter-from", "filter-to", "filter-key", "filter-provider", "filter-model", "filter-tier", "clear-filters",
      "metric-cost", "metric-cost-note", "metric-tokens", "metric-token-note", "metric-requests", "metric-request-note",
      "metric-success", "metric-success-note", "metric-latency", "metric-latency-note", "trend-chart", "chart-wrap", "chart-tooltip",
      "chart-empty", "chart-data-table", "chart-currency", "currency-costs", "unpriced-tokens", "unpriced-note",
      "live-throughput-chart", "live-throughput-success", "live-throughput-failed", "live-throughput-window",
      "key-ranking-body", "key-ranking-empty", "model-breakdown", "model-empty", "model-count", "key-total", "key-search",
      "key-status-filter", "key-sort", "keys-table-body", "keys-card-list", "keys-empty", "pagination-summary", "page-prev", "page-next",
      "key-detail-panel", "key-detail-title", "key-detail-subtitle", "key-detail-status", "key-detail-close", "key-detail-stats",
      "key-detail-throughput", "key-detail-throughput-chart", "key-detail-throughput-success", "key-detail-throughput-failed", "key-detail-throughput-window", "key-detail-throughput-updated", "key-detail-throughput-empty", "key-detail-throughput-empty-copy",
      "key-detail-model-count", "key-detail-models", "key-detail-models-empty", "key-detail-daily", "key-detail-daily-empty",
      "billing-dirty", "billing-enabled", "retention-days", "pricing-currency", "pricing-version", "add-pricing-rule", "pricing-catalog-status", "pricing-rule-count", "pricing-rules-body",
      "pricing-empty", "billing-form-error", "settings-revision", "reset-billing", "save-billing", "profile-dialog", "profile-form",
      "profile-preview-alias", "profile-preview-key", "profile-preview-status", "profile-alias", "profile-id", "alias-count",
      "key-create-dialog", "key-create-form", "key-create-value", "key-create-alias", "key-create-id", "key-create-form-error", "save-key-create", "key-create-toggle-visibility", "key-create-regenerate", "key-create-copy",
      "key-created-dialog", "key-created-value", "key-created-copy",
      "credential-total", "credentials-refresh", "oauth-session", "oauth-callback-panel", "oauth-callback-provider", "oauth-callback-value",
      "oauth-callback-submit", "oauth-callback-status", "auth-upload-input", "auth-upload-status", "credential-search",
      "credential-provider", "credential-status", "credential-table-body", "credential-card-list", "credential-empty", "logs-live",
      "quota-total", "quota-ready-count", "quota-issue-count", "quota-refresh-all", "quota-provider-tabs", "quota-search",
      "quota-card-list", "quota-window-panel", "quota-window-days", "quota-window-rows", "quota-window-range", "quota-window-today", "quota-empty",
      "logs-refresh", "logs-clear", "log-line-count", "log-search", "log-level", "logs-tail", "log-console", "logs-empty",
      "error-file-count", "error-file-list", "error-files-empty", "settings-state",
      "settings-refresh", "runtime-settings-form", "runtime-routing", "runtime-retry", "runtime-retry-interval", "runtime-proxy",
      "runtime-debug", "runtime-file-log", "runtime-request-log", "runtime-ws-auth", "runtime-model-prefix", "runtime-settings-save",
      "yaml-reload", "yaml-save", "yaml-editor", "yaml-status", "yaml-lines", "command-open", "command-dialog", "command-search",
      "command-list", "profile-form-error", "save-profile", "confirm-dialog", "confirm-title", "confirm-message", "confirm-cancel",
      "confirm-accept", "toast-region"
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
    dom.refreshData.addEventListener("click", refreshCurrentView);
    dom.keyCreate.addEventListener("click", openKeyCreateDialog);
    dom.keyResetUsage.addEventListener("click", resetAllClientKeyUsage);
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
    dom.keyDetailClose.addEventListener("click", closeKeyDetails);

    dom.billingEnabled.addEventListener("change", function () {
      syncBillingDraftFromForm();
      markBillingDirty(true);
    });
    [dom.retentionDays, dom.pricingCurrency, dom.pricingVersion].forEach(function (input) {
      input.addEventListener("change", function () {
        syncBillingDraftFromForm();
        markBillingDirty(true);
        if (input === dom.pricingCurrency) {
          renderPricingRules();
        }
      });
    });
    dom.addPricingRule.addEventListener("click", addPricingRule);
    dom.resetBilling.addEventListener("click", resetBillingDraft);
    dom.saveBilling.addEventListener("click", saveBillingSettings);

    dom.credentialsRefresh.addEventListener("click", function () { loadCredentials(true); });
    dom.credentialSearch.addEventListener("input", function () {
      state.credentialSearch = dom.credentialSearch.value.trim().toLocaleLowerCase();
      renderCredentials();
    });
    dom.credentialProvider.addEventListener("change", function () {
      state.credentialProvider = dom.credentialProvider.value;
      renderCredentials();
    });
    dom.credentialStatus.addEventListener("change", function () {
      state.credentialStatus = dom.credentialStatus.value;
      renderCredentials();
    });
    if (dom.quotaRefreshAll) {
      dom.quotaRefreshAll.addEventListener("click", refreshAllQuotas);
    }
    if (dom.quotaSearch) {
      dom.quotaSearch.addEventListener("input", function () {
        state.quotaSearch = dom.quotaSearch.value.trim().toLocaleLowerCase();
        renderQuotaCredentials();
      });
    }
    all("[data-quota-provider]", dom.quotaProviderTabs || document).forEach(function (button) {
      button.addEventListener("click", function () {
        state.quotaProvider = button.dataset.quotaProvider || "all";
        renderQuotaCredentials();
      });
    });
    all("[data-quota-window-shift]").forEach(function (button) {
      button.addEventListener("click", function () {
        state.quotaWindowOffset += number(button.dataset.quotaWindowShift);
        renderQuotaCredentials();
      });
    });
    if (dom.quotaWindowToday) {
      dom.quotaWindowToday.addEventListener("click", function () {
        state.quotaWindowOffset = 0;
        renderQuotaCredentials();
      });
    }
    all("[data-quota-window-mode]").forEach(function (button) {
      button.addEventListener("click", function () {
        state.quotaWindowMode = button.dataset.quotaWindowMode || "week";
        all("[data-quota-window-mode]").forEach(function (item) {
          const active = item === button;
          item.classList.toggle("active", active);
          item.setAttribute("aria-pressed", active ? "true" : "false");
        });
        renderQuotaCredentials();
      });
    });
    dom.authUploadInput.addEventListener("change", uploadCredentialFiles);
    dom.oauthCallbackSubmit.addEventListener("click", submitOAuthCallback);
    all("[data-oauth-endpoint]").forEach(function (button) {
      button.addEventListener("click", function () { startOAuth(button); });
    });

    dom.logsRefresh.addEventListener("click", function () { loadLogs(true); });
    dom.logsClear.addEventListener("click", clearLogs);
    dom.logsLive.addEventListener("change", syncLiveLogs);
    dom.logSearch.addEventListener("input", function () {
      state.logSearch = dom.logSearch.value.trim().toLocaleLowerCase();
      renderLogs();
    });
    dom.logLevel.addEventListener("change", function () {
      state.logLevel = dom.logLevel.value;
      renderLogs();
    });
    dom.logsTail.addEventListener("click", scrollLogsToBottom);

    dom.settingsRefresh.addEventListener("click", requestRuntimeSettingsReload);
    dom.runtimeSettingsForm.addEventListener("submit", saveRuntimeSettings);
    dom.yamlReload.addEventListener("click", resetYAMLEditor);
    dom.yamlSave.addEventListener("click", saveYAMLConfig);
    dom.yamlEditor.addEventListener("input", function () {
      state.yamlDirty = dom.yamlEditor.value !== state.configYAML;
      updateYAMLEditorStatus();
    });

    dom.commandOpen.addEventListener("click", openCommandPalette);
    dom.commandSearch.addEventListener("input", filterCommandPalette);
    dom.commandSearch.addEventListener("keydown", handleCommandKeys);
    all("[data-command-view]").forEach(function (button) {
      button.addEventListener("click", function () {
        dom.commandDialog.close();
        setView(button.dataset.commandView);
      });
    });
    document.addEventListener("keydown", function (event) {
      if ((event.ctrlKey || event.metaKey) && event.key.toLocaleLowerCase() === "k") {
        event.preventDefault();
        openCommandPalette();
      }
    });
    window.addEventListener("hashchange", function () {
      const view = viewFromHash();
      if (view) {
        setView(view, false);
      }
    });
    document.addEventListener("visibilitychange", function () {
      if (document.hidden) {
        cancelKeyDetailRefresh();
      } else if (state.currentView === "keys" && state.keyDetail) {
        refreshKeyDetailUsage({ showLoading: false, recentOnly: true });
      }
    });

    all("[data-close-dialog]").forEach(function (button) {
      button.addEventListener("click", function () {
        const dialog = button.closest("dialog");
        if (dialog) {
          dialog.close();
        }
      });
    });
    dom.profileAlias.addEventListener("input", function () {
      dom.aliasCount.textContent = Array.from(dom.profileAlias.value).length + " / 128";
      dom.profilePreviewAlias.textContent = dom.profileAlias.value.trim() || "未命名 Key";
    });
    dom.profileForm.addEventListener("submit", saveProfile);
    dom.keyCreateForm.addEventListener("submit", createKey);
    dom.keyCreateRegenerate.addEventListener("click", generateKeyCreateValue);
    dom.keyCreateToggleVisibility.addEventListener("click", toggleKeyCreateVisibility);
    dom.keyCreateCopy.addEventListener("click", copyKeyCreateValue);
    dom.keyCreatedCopy.addEventListener("click", copyCreatedKeyValue);
    dom.keyCreateDialog.addEventListener("close", function () {
      dom.keyCreateForm.reset();
      setKeyCreateError("");
    });
    dom.keyCreatedDialog.addEventListener("close", function () {
      dom.keyCreatedValue.value = "";
    });
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
      if (!state.billingDirty && !state.yamlDirty) {
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

  function toggleTheme(event) {
    const next = document.documentElement.getAttribute("data-theme") === "dark" ? "light" : "dark";
    const bounds = dom.themeToggle.getBoundingClientRect();
    const themeX = event && event.clientX ? event.clientX : bounds.left + bounds.width / 2;
    const themeY = event && event.clientY ? event.clientY : bounds.top + bounds.height / 2;
    document.documentElement.style.setProperty("--theme-x", themeX + "px");
    document.documentElement.style.setProperty("--theme-y", themeY + "px");
    const apply = function () {
      applyTheme(next);
      safeStorage(window.localStorage, "set", THEME_KEY, next);
      renderChart();
    };
    const reduceMotion = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    if (document.startViewTransition && !reduceMotion) {
      const root = document.documentElement;
      root.classList.add("theme-transitioning");
      const transition = document.startViewTransition(apply);
      const cleanup = function () { root.classList.remove("theme-transitioning"); };
      transition.finished.then(cleanup, cleanup);
    } else {
      apply();
    }
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
      dom.appView.classList.add("is-initial-loading");
      await refreshAll({ includeSettings: false, initial: true });
      dom.appView.classList.remove("is-initial-loading");
      const requestedView = viewFromHash();
      if (requestedView && requestedView !== state.currentView) {
        setView(requestedView, false);
      } else {
        markViewEntering(state.currentView);
        loadViewData(state.currentView, false);
      }
    } catch (error) {
      state.managementSecret = "";
      safeStorage(window.sessionStorage, "remove", SESSION_SECRET_KEY);
      setAuthError(apiErrorMessage(error));
      dom.secret.focus();
    } finally {
      dom.appView.classList.remove("is-initial-loading");
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
    window.clearTimeout(oauthPollTimer);
    window.clearTimeout(logsLiveTimer);
    cancelKeyDetailRefresh();
    state.managementSecret = "";
    state.profiles = [];
    state.report = null;
    state.baseReport = null;
    state.billingCatalogReport = null;
    state.billingProviderCatalog = [];
    state.settings = null;
    state.billingDraft = null;
    state.billingBaseRevision = "";
    state.billingDirty = false;
    state.yamlDirty = false;
    state.oauthProvider = "";
    state.oauthSessionState = "";
    state.quotaSearch = "";
    state.quotaProvider = "all";
    state.quotaResults = {};
    state.quotaBusy = {};
    state.quotaRefreshingAll = false;
    state.quotaWindowOffset = 0;
    state.quotaWindowMode = "week";
    state.keyUsageResettingAll = false;
    state.keyDetail = null;
    state.keyDetailLoading = false;
    state.quotaEpoch += 1;
    state.logCursor = "";
    state.logLiveError = false;
    state.plugins = [];
    state.loadedViews = {};
    if (dom.quotaSearch) {
      dom.quotaSearch.value = "";
    }
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
    if (state.billingDirty || state.yamlDirty) {
      const confirmed = await confirmAction("放弃未保存的修改？", "断开连接会丢失当前计费或 YAML 草稿。", "放弃并断开");
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
    const formDataBody = typeof FormData !== "undefined" && requestOptions.body instanceof FormData;
    if (requestOptions.body !== undefined && !formDataBody && !headers.has("Content-Type")) {
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
      if (state.currentView === "keys" && state.keyDetail) {
        refreshKeyDetailUsage({ showLoading: false, recentOnly: false });
      }
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
    dom.appView.classList.toggle("is-refreshing", Boolean(busy));
    [dom.refreshData, dom.keysRefresh].forEach(function (button) {
      if (!button) {
        return;
      }
      button.disabled = busy;
      button.setAttribute("aria-busy", busy ? "true" : "false");
    });
    setControlBusy(dom.keyResetUsage, busy || state.keyUsageResettingAll);
    dom.refreshData.classList.toggle("loading", busy);
  }

  function normalizeReport(report) {
    const value = report && typeof report === "object" ? report : {};
    value.keys = Array.isArray(value.keys) ? value.keys : [];
    value.daily = Array.isArray(value.daily) ? value.daily : [];
    value.models = Array.isArray(value.models) ? value.models : [];
    value.recent = Array.isArray(value.recent) ? value.recent : [];
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
      dom.filterTo.value = localDateString(today);
      const start = new Date(today.getTime());
      start.setDate(start.getDate() - Math.max(0, count - 1));
      dom.filterFrom.value = localDateString(start);
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

  function localDateString(date) {
    const value = date instanceof Date ? date : new Date(date);
    return [
      value.getFullYear(),
      String(value.getMonth() + 1).padStart(2, "0"),
      String(value.getDate()).padStart(2, "0"),
    ].join("-");
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
    renderKeyDetails();
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
    const previousMetricValues = all(".metric-value").map(function (node) { return node.textContent; });
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
    renderLiveThroughput();
    renderChart();
    renderKeyRanking();
    renderModelBreakdown();
    animateMetricValues(previousMetricValues);
  }

  function animateMetricValues(previousValues) {
    if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) {
      return;
    }
    all(".metric-value").forEach(function (node, index) {
      node.classList.remove("is-updated");
      if (previousValues[index] === node.textContent || previousValues[index] === "—") {
        return;
      }
      node.style.setProperty("--metric-delay", (index * 34) + "ms");
      window.requestAnimationFrame(function () { node.classList.add("is-updated"); });
    });
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

  function renderLiveThroughput() {
    if (!dom.liveThroughputChart) {
      return;
    }
    renderThroughput(state.report || normalizeReport({}), {
      chart: dom.liveThroughputChart,
      success: dom.liveThroughputSuccess,
      failed: dom.liveThroughputFailed,
      window: dom.liveThroughputWindow
    });
  }

  function renderThroughput(report, nodes, options) {
    if (!nodes || !nodes.chart) {
      return;
    }
    const settings = Object.assign({ contextLabel: "实时窗口", maximumPlotHeight: 300 }, options || {});
    const bucketMinutes = 10;
    const bucketMilliseconds = bucketMinutes * 60 * 1000;
    const requestedWindow = number(report.recent_window_minutes) || 200;
    const bucketCount = Math.max(1, Math.round(requestedWindow / bucketMinutes));
    const now = new Date(Math.floor(Date.now() / bucketMilliseconds) * bucketMilliseconds);
    const values = new Map();
    (report.recent || []).forEach(function (entry) {
      const start = new Date(entry.start_at);
      if (Number.isNaN(start.getTime())) {
        return;
      }
      values.set(start.getTime(), entry.summary || {});
    });

    const points = [];
    for (let index = bucketCount - 1; index >= 0; index -= 1) {
      const start = new Date(now.getTime() - index * bucketMilliseconds);
      const summary = values.get(start.getTime()) || {};
      points.push({ start: start, summary: summary, success: number(summary.success), failed: number(summary.failed) });
    }
    const success = points.reduce(function (total, point) { return total + point.success; }, 0);
    const failed = points.reduce(function (total, point) { return total + point.failed; }, 0);
    const max = Math.max.apply(null, points.map(function (point) { return point.success + point.failed; }).concat([1]));
    Array.from(nodes.chart.classList).forEach(function (className) {
      if (className.startsWith("live-plot-height-")) {
        nodes.chart.classList.remove(className);
      }
    });
    nodes.chart.classList.add("live-plot-height-" + liveThroughputPlotHeight(max, settings.maximumPlotHeight));
    const focusedBucket = nodes.chart.contains(document.activeElement) && document.activeElement.dataset
      ? document.activeElement.dataset.bucketStart || ""
      : "";
    const fragment = document.createDocumentFragment();
    points.forEach(function (point, index) {
      const total = point.success + point.failed;
      const startLabel = formatTenMinuteTime(point.start);
      const end = new Date(point.start.getTime() + bucketMilliseconds);
      const label = startLabel + "–" + formatTenMinuteTime(end);
      const bar = element("div", "live-throughput-bar");
      bar.tabIndex = 0;
      bar.dataset.bucketStart = String(point.start.getTime());
      bar.setAttribute("role", "listitem");
      bar.setAttribute("aria-label", label + "：成功 " + formatInteger(point.success) + "，失败 " + formatInteger(point.failed));
      bar.title = label + "\n成功 " + formatInteger(point.success) + " · 失败 " + formatInteger(point.failed) + " · 共 " + formatInteger(total);
      const stack = element("span", "live-throughput-stack");
      const fillClasses = liveThroughputFillClasses(point.success, point.failed, max);
      stack.appendChild(element("span", "live-throughput-success-bar " + fillClasses.success + (point.success > 0 ? " live-has-value" : "") + (point.failed > 0 ? "" : " live-top-segment")));
      stack.appendChild(element("span", "live-throughput-failure-bar " + fillClasses.failed + (point.failed > 0 ? " live-has-value live-top-segment" : "")));
      bar.appendChild(stack);
      if (index === 0 || index === Math.floor((points.length - 1) / 2) || index === points.length - 1) {
        bar.appendChild(element("span", "live-throughput-label", startLabel));
      }
      fragment.appendChild(bar);
    });
    nodes.chart.replaceChildren(fragment);
    if (focusedBucket) {
      const replacement = Array.from(nodes.chart.children).find(function (bar) { return bar.dataset.bucketStart === focusedBucket; });
      if (replacement) {
        replacement.focus({ preventScroll: true });
      }
    }
    nodes.chart.setAttribute("aria-label", settings.contextLabel + "：成功 " + formatInteger(success) + "，失败 " + formatInteger(failed));
    if (nodes.success) {
      nodes.success.textContent = formatInteger(success);
    }
    if (nodes.failed) {
      nodes.failed.textContent = formatInteger(failed);
    }
    const summaryNode = nodes.success && nodes.success.closest(".live-throughput-summary");
    if (summaryNode) {
      summaryNode.setAttribute("aria-label", settings.contextLabel + "汇总：成功 " + formatInteger(success) + "，失败 " + formatInteger(failed));
    }
    const hours = Math.floor(requestedWindow / 60);
    const minutes = requestedWindow % 60;
    const windowLabel = "最近 " + hours + " 小时" + (minutes ? " " + minutes + " 分" : "");
    if (nodes.window) {
      nodes.window.textContent = windowLabel;
    }
    const hasUsage = success + failed > 0;
    if (nodes.empty) {
      nodes.empty.hidden = hasUsage;
      nodes.chart.hidden = !hasUsage;
    } else {
      nodes.chart.hidden = false;
    }
    return { success: success, failed: failed, windowLabel: windowLabel };
  }

  function liveThroughputPlotHeight(maxValue, maximumValue) {
    const minimum = 120;
    const maximum = Math.max(minimum, number(maximumValue) || 300);
    const scaled = 80 + Math.sqrt(Math.max(0, number(maxValue))) * 12;
    return Math.max(minimum, Math.min(maximum, Math.round(scaled / 20) * 20));
  }

  function liveThroughputFillClasses(successValue, failedValue, maxValue) {
    let successStep = successValue > 0 ? Math.max(5, quotaScaleStep(number(successValue) / maxValue * 100)) : 0;
    let failedStep = failedValue > 0 ? Math.max(5, quotaScaleStep(number(failedValue) / maxValue * 100)) : 0;
    while (successStep + failedStep > 100) {
      if (successStep >= failedStep && successStep > 5) {
        successStep -= 5;
      } else if (failedStep > 5) {
        failedStep -= 5;
      } else {
        break;
      }
    }
    return {
      success: "live-fill-" + successStep,
      failed: "live-fill-" + failedStep
    };
  }

  function formatTenMinuteTime(value) {
    const date = new Date(value);
    return String(date.getHours()).padStart(2, "0") + ":" + String(date.getMinutes()).padStart(2, "0");
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

    const defs = svgElement("defs");
    const gradient = svgElement("linearGradient", { id: "trend-area-gradient", x1: "0", y1: "0", x2: "0", y2: "1" });
    gradient.appendChild(svgElement("stop", { offset: "0%", class: "chart-gradient-start" }));
    gradient.appendChild(svgElement("stop", { offset: "100%", class: "chart-gradient-end" }));
    defs.appendChild(gradient);
    dom.trendChart.appendChild(defs);

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
      visiblePoint.style.setProperty("--point-delay", (220 + Math.min(index, 12) * 24) + "ms");
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
    return state.profiles.map(function (profile) {
      const usage = usageByID.get(profile.id);
      return {
        profile: profile,
        id: profile.id,
        alias: profile.alias || (usage && usage.alias) || "",
        maskedKey: profile.masked_key || (usage && usage.masked_key) || "",
        summary: usage && usage.summary || {},
        historical: false
      };
    });
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
    setControlBusy(dom.keyResetUsage, state.keyUsageResettingAll);
  }

  async function resetAllClientKeyUsage() {
    if (state.keyUsageResettingAll) {
      return;
    }
    const confirmed = await confirmAction(
      "重置所有客户端 Key 的使用额度？",
      "这会永久清零全部客户端 Key 的本地请求数、Token、费用和历史用量统计。Key 本身、别名、启用状态及上游服务商额度都不会改变。",
      "确认清零用量"
    );
    if (!confirmed) {
      return;
    }

    state.keyUsageResettingAll = true;
    setControlBusy(dom.keyResetUsage, true);
    try {
      const result = await apiRequest(API.resetClientKeyUsage, { method: "POST", body: "{}" });
      const resetCount = number(result && result.reset_count);
      closeKeyDetails();
      await refreshAll({ includeSettings: false, initial: true });
      toast(resetCount ? "已清零 " + resetCount + " 个有使用记录的客户端 Key" : "当前没有需要清零的客户端 Key 用量");
    } catch (error) {
      if (error.status === 401 || error.status === 403) {
        signOut(apiErrorMessage(error));
        return;
      }
      toast(apiErrorMessage(error), true);
    } finally {
      state.keyUsageResettingAll = false;
      setControlBusy(dom.keyResetUsage, false);
    }
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
    identity.appendChild(createKeyCreatedAt(item.profile, true));
    row.appendChild(identity);
    const statusCell = element("td");
    statusCell.appendChild(keyStatusChip(item));
    row.appendChild(statusCell);
    row.appendChild(element("td", "", formatInteger(item.summary.attempts)));
    row.appendChild(element("td", "", formatCompact(number((item.summary.tokens || {}).total_tokens))));
    row.appendChild(element("td", "", formatSummaryCost(item.summary, state.baseReport && state.baseReport.currency)));
    row.appendChild(element("td", "", item.summary.last_used_at ? formatDateTime(item.summary.last_used_at) : "从未"));
    const action = element("td");
    action.appendChild(createKeyActions(item));
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
    appendDefinition(list, "创建时间", createKeyCreatedAt(item.profile, false));
    appendDefinition(list, "最近使用", item.summary.last_used_at ? formatDateTime(item.summary.last_used_at) : "从未");
    card.appendChild(list);
    const footer = element("footer");
    footer.appendChild(element("code", "table-subline", item.id));
    footer.appendChild(createKeyActions(item));
    card.appendChild(footer);
    return card;
  }

  function createKeyActions(item) {
    const actions = element("div", "key-row-actions");
    const detailsButton = element("button", "key-details-button", "详情");
    detailsButton.type = "button";
    detailsButton.addEventListener("click", function () { openKeyDetails(item); });
    actions.appendChild(detailsButton);

    const profile = item.profile;
    if (!profile) {
      actions.appendChild(element("span", "muted", "历史"));
      return actions;
    }
    const editButton = element("button", "edit-key-button", "编辑");
    editButton.type = "button";
    editButton.addEventListener("click", function () { openProfileEditor(profile); });
    actions.appendChild(editButton);

    if (profile.effective && !profile.issue) {
      const statusSwitch = element("label", "key-status-switch");
      const toggle = document.createElement("input");
      toggle.type = "checkbox";
      toggle.role = "switch";
      toggle.checked = !profile.disabled;
      toggle.setAttribute("aria-label", profile.disabled ? "启用此 Key" : "停用此 Key");
      toggle.title = profile.disabled ? "启用此 Key" : "停用此 Key";
      const track = element("span", "key-status-switch-track");
      statusSwitch.appendChild(toggle);
      statusSwitch.appendChild(track);
      toggle.addEventListener("change", function () { toggleKeyProfile(profile, toggle); });
      actions.appendChild(statusSwitch);
    }

    const deleteButton = element("button", "delete-key-button", "删除");
    deleteButton.type = "button";
    deleteButton.addEventListener("click", function () { deleteKeyProfile(profile); });
    actions.appendChild(deleteButton);
    return actions;
  }

  function buildKeyDetailQuery(keyID, includeDates) {
    const params = new URLSearchParams();
    if (includeDates !== false && state.filters.from) {
      params.set("from", state.filters.from);
    }
    if (includeDates !== false && state.filters.to) {
      params.set("to", state.filters.to);
    }
    params.set("key_id", keyID);
    if (state.filters.provider) {
      params.set("provider", state.filters.provider);
    }
    if (state.filters.model) {
      params.set("model", state.filters.model);
    }
    if (state.filters.tier) {
      params.set("service_tier", state.filters.tier);
    }
    return "?" + params.toString();
  }

  async function openKeyDetails(item) {
    if (!item || !item.id) {
      return;
    }
    cancelKeyDetailRefresh();
    state.keyDetail = {
      item: {
        id: item.id,
        alias: item.alias || "",
        maskedKey: item.maskedKey || "",
        historical: Boolean(item.historical),
        profile: item.profile ? { disabled: Boolean(item.profile.disabled), effective: Boolean(item.profile.effective), issue: item.profile.issue || "", created_at: item.profile.created_at || "" } : null
      },
      report: null,
      recentReport: null
    };
    state.keyDetailLoading = true;
    renderKeyDetails();
    dom.keyDetailPanel.scrollIntoView({ behavior: window.matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth", block: "nearest" });
    await refreshKeyDetailUsage({ showLoading: true });
  }

  function clearKeyDetailRefresh() {
    window.clearTimeout(keyDetailRefreshTimer);
    keyDetailRefreshTimer = 0;
  }

  function cancelKeyDetailRefresh() {
    clearKeyDetailRefresh();
    if (keyDetailController) {
      keyDetailController.abort();
      keyDetailController = null;
    }
    state.keyDetailRequest += 1;
    state.keyDetailLoading = false;
  }

  function scheduleKeyDetailRefresh() {
    clearKeyDetailRefresh();
    if (!state.managementSecret || document.hidden || state.currentView !== "keys" || !state.keyDetail || state.keyDetail.item.historical) {
      return;
    }
    keyDetailRefreshTimer = window.setTimeout(function () {
      refreshKeyDetailUsage({ showLoading: false, recentOnly: true });
    }, KEY_DETAIL_REFRESH_INTERVAL);
  }

  async function refreshKeyDetailUsage(options) {
    const settings = Object.assign({ showLoading: false, recentOnly: false }, options || {});
    const detail = state.keyDetail;
    if (!detail || !detail.item || !detail.item.id) {
      return;
    }
    clearKeyDetailRefresh();
    if (keyDetailController) {
      keyDetailController.abort();
    }
    const controller = new AbortController();
    keyDetailController = controller;
    const itemID = detail.item.id;
    const request = ++state.keyDetailRequest;
    if (settings.showLoading && !detail.report) {
      state.keyDetailLoading = true;
      delete detail.error;
      renderKeyDetails();
    }
    try {
      const recentPath = API.usage + buildKeyDetailQuery(itemID, false);
      let report = detail.report;
      let recentReport = null;
      if (settings.recentOnly && report) {
        recentReport = await apiRequest(recentPath, { method: "GET", signal: controller.signal });
      } else {
        const reportPath = API.usage + buildKeyDetailQuery(itemID, true);
        const reportPromise = apiRequest(reportPath, { method: "GET", signal: controller.signal });
        const recentPromise = reportPath === recentPath
          ? reportPromise
          : apiRequest(recentPath, { method: "GET", signal: controller.signal });
        const results = await Promise.all([reportPromise, recentPromise]);
        report = results[0];
        recentReport = results[1];
      }
      if (request !== state.keyDetailRequest || !state.keyDetail || state.keyDetail.item.id !== itemID) {
        return;
      }
      if (!settings.recentOnly || !detail.report) {
        detail.report = normalizeReport(report);
      }
      detail.recentReport = normalizeReport(recentReport);
      detail.refreshedAt = new Date();
      detail.refreshError = "";
      delete detail.error;
    } catch (error) {
      if (request !== state.keyDetailRequest || !state.keyDetail || state.keyDetail.item.id !== itemID) {
        return;
      }
      if (error.name === "AbortError") {
        return;
      }
      if (error.status === 401 || error.status === 403) {
        signOut(apiErrorMessage(error));
        return;
      }
      if (!detail.report) {
        detail.error = apiErrorMessage(error);
      } else {
        detail.refreshError = apiErrorMessage(error);
      }
    } finally {
      if (request === state.keyDetailRequest && state.keyDetail && state.keyDetail.item.id === itemID) {
        if (keyDetailController === controller) {
          keyDetailController = null;
        }
        state.keyDetailLoading = false;
        renderKeyDetails();
        scheduleKeyDetailRefresh();
      }
    }
  }

  function closeKeyDetails() {
    cancelKeyDetailRefresh();
    state.keyDetail = null;
    renderKeyDetails();
  }

  function renderKeyDetails() {
    if (!dom.keyDetailPanel) {
      return;
    }
    const detail = state.keyDetail;
    dom.keyDetailPanel.hidden = !detail;
    if (!detail) {
      if (dom.keyDetailThroughput) {
        dom.keyDetailThroughput.hidden = true;
      }
      return;
    }
    const item = detail.item;
    dom.keyDetailTitle.textContent = (item.alias || (item.historical ? "历史 Key" : "未命名 Key")) + " · 使用详情";
    dom.keyDetailSubtitle.replaceChildren();
    dom.keyDetailSubtitle.append(
      document.createTextNode((item.maskedKey || "脱敏信息不可用") + " · " + item.id + " · "),
      createKeyCreatedAt(item.profile, true)
    );
    dom.keyDetailStatus.replaceWith(keyStatusChip({ profile: item.profile, historical: item.historical }));
    dom.keyDetailStatus = dom.keyDetailPanel.querySelector(".status-chip");
    dom.keyDetailStats.replaceChildren();
    dom.keyDetailModels.replaceChildren();
    dom.keyDetailDaily.replaceChildren();
    dom.keyDetailModelsEmpty.hidden = true;
    dom.keyDetailDailyEmpty.hidden = true;
    dom.keyDetailModelCount.textContent = "";
    dom.keyDetailThroughput.hidden = true;

    if (state.keyDetailLoading) {
      dom.keyDetailStats.appendChild(createKeyDetailLoading("正在读取这个 Key 的历史用量…"));
      return;
    }
    if (detail.error) {
      dom.keyDetailStats.appendChild(createKeyDetailLoading(detail.error, true));
      return;
    }
    const report = detail.report || normalizeReport({});
    const summary = report.summary || {};
    const attempts = number(summary.attempts);
    const detailTokens = reportTokenBreakdown(report);
    const statValues = [
      ["请求", formatInteger(attempts), formatInteger(number(summary.success)) + " 成功 · " + formatInteger(number(summary.failed)) + " 失败 · 上游失败尝试 " + formatInteger(number(summary.upstream_failed_attempts))],
      ["成功率", attempts ? formatPercent(number(summary.success) / attempts) : "—", attempts ? "按最终请求计算" : "等待调用"],
      ["总 Token", formatCompact(number((summary.tokens || {}).total_tokens)), "按计费口径拆分", detailTokens],
      ["预估费用", formatSummaryCost(summary, report.currency), summary.unpriced_attempts ? formatInteger(number(summary.unpriced_attempts)) + " 次未完整计价" : "按规则估算"],
      ["平均延迟", attempts && number(summary.average_latency_ms) ? formatDuration(number(summary.average_latency_ms)) : "—", attempts && summary.last_used_at ? "最近 " + formatDateTime(summary.last_used_at) : "从未使用"]
    ];
    statValues.forEach(function (entry) {
      const card = element("article", "key-detail-stat" + (entry[3] ? " is-token-summary" : ""));
      card.appendChild(element("span", "key-detail-stat-label", entry[0]));
      card.appendChild(element("strong", "key-detail-stat-value", entry[1]));
      card.appendChild(element("small", "key-detail-stat-note", entry[2]));
      if (entry[3]) {
        const split = element("div", "key-detail-token-split");
        [
          ["输入", entry[3].input, "token-input"],
          ["缓存输入", entry[3].cacheInput, "token-cache"],
          ["输出", entry[3].output, "token-output"]
        ].forEach(function (tokenEntry) {
          const part = element("span", "key-detail-token-part " + tokenEntry[2]);
          part.appendChild(element("small", "", tokenEntry[0]));
          part.appendChild(element("strong", "", formatCompact(tokenEntry[1])));
          split.appendChild(part);
        });
        card.appendChild(split);
      }
      dom.keyDetailStats.appendChild(card);
    });

    renderKeyDetailThroughput(detail.recentReport || report, detail);

    const models = (report.models || []).slice().sort(function (left, right) {
      return number((right.summary || {}).attempts) - number((left.summary || {}).attempts);
    });
    dom.keyDetailModelCount.textContent = models.length + " 个模型";
    dom.keyDetailModelsEmpty.hidden = models.length > 0;
    models.forEach(function (model) {
      const modelSummary = model.summary || {};
      const modelTokens = tokenBreakdown((modelSummary.tokens || {}), model.provider);
      const row = element("tr");
      const name = element("td");
      name.appendChild(element("span", "table-primary", model.model || "unknown"));
      name.appendChild(element("span", "table-subline", (model.provider || "unknown") + " · " + (model.service_tier || "default")));
      row.appendChild(name);
      row.appendChild(element("td", "", formatInteger(number(modelSummary.attempts))));
      row.appendChild(element("td", "token-column token-input", formatCompact(modelTokens.input)));
      row.appendChild(element("td", "token-column token-cache", formatCompact(modelTokens.cacheInput)));
      row.appendChild(element("td", "token-column token-output", formatCompact(modelTokens.output)));
      row.appendChild(element("td", "", number(modelSummary.attempts) ? formatPercent(number(modelSummary.success) / number(modelSummary.attempts)) : "—"));
      row.appendChild(element("td", "", formatSummaryCost(modelSummary, report.currency)));
      dom.keyDetailModels.appendChild(row);
    });

    const daily = (report.daily || []).slice().sort(function (left, right) { return String(left.day).localeCompare(String(right.day)); });
    const dailyMax = Math.max.apply(null, daily.map(function (entry) { return number((entry.summary || {}).attempts); }).concat([1]));
    dom.keyDetailDailyEmpty.hidden = daily.length > 0;
    daily.forEach(function (entry) {
      const daySummary = entry.summary || {};
      const attemptsForDay = number(daySummary.attempts);
      const row = element("div", "key-detail-day-row");
      row.title = entry.day + "\n" + formatInteger(attemptsForDay) + " 次请求 · " + formatInteger(number(daySummary.success)) + " 成功 · " + formatInteger(number(daySummary.failed)) + " 失败";
      row.appendChild(element("time", "key-detail-day-label", entry.day.slice(5)));
      const rail = element("span", "key-detail-day-rail");
      const fill = element("span", "key-detail-day-fill");
      fill.style.setProperty("--key-day-width", ((attemptsForDay / dailyMax) * 100).toFixed(2) + "%");
      rail.appendChild(fill);
      row.appendChild(rail);
      row.appendChild(element("strong", "key-detail-day-value", formatInteger(attemptsForDay)));
      dom.keyDetailDaily.appendChild(row);
    });
  }

  function renderKeyDetailThroughput(report, detail) {
    if (!dom.keyDetailThroughput || !dom.keyDetailThroughputChart) {
      return;
    }
    dom.keyDetailThroughput.hidden = false;
    dom.keyDetailThroughputEmptyCopy.textContent = detail.item.historical
      ? "这个历史 Key 不会再产生新调用；这里只展示服务本次运行期间仍保留的实时窗口。"
      : "新的成功或失败请求会按 10 分钟自动汇总到这里。";
    renderThroughput(report, {
      chart: dom.keyDetailThroughputChart,
      success: dom.keyDetailThroughputSuccess,
      failed: dom.keyDetailThroughputFailed,
      window: dom.keyDetailThroughputWindow,
      empty: dom.keyDetailThroughputEmpty
    }, {
      contextLabel: "当前 Key 实时窗口",
      maximumPlotHeight: 220
    });
    const refreshError = detail.refreshError || "";
    dom.keyDetailThroughputUpdated.classList.toggle("is-error", Boolean(refreshError));
    if (refreshError) {
      dom.keyDetailThroughputUpdated.textContent = "自动刷新失败，将在 30 秒后重试";
      dom.keyDetailThroughputUpdated.title = refreshError;
    } else if (detail.item.historical) {
      dom.keyDetailThroughputUpdated.textContent = "历史 Key · 实时窗口不会继续产生新调用";
      dom.keyDetailThroughputUpdated.removeAttribute("title");
    } else {
      const refreshedAt = detail.refreshedAt ? formatDateTime(detail.refreshedAt) : "刚刚";
      dom.keyDetailThroughputUpdated.textContent = "更新于 " + refreshedAt + " · 每 30 秒刷新 · 重启后重新统计";
      dom.keyDetailThroughputUpdated.removeAttribute("title");
    }
  }

  function tokenBreakdown(tokens, provider) {
    const value = tokens && typeof tokens === "object" ? tokens : {};
    const normalizedProvider = String(provider || "").trim().toLowerCase();
    let input = number(value.input_tokens);
    let output = number(value.output_tokens);
    let cacheRead = number(value.cache_read_tokens);
    const cacheWrite = number(value.cache_creation_tokens);
    if (!cacheRead) {
      cacheRead = number(value.cached_tokens);
    }
    if (normalizedProvider === "gemini" || normalizedProvider === "aistudio" || normalizedProvider === "antigravity" || normalizedProvider === "vertex") {
      output += number(value.reasoning_tokens);
    }
    if (normalizedProvider !== "claude") {
      input = Math.max(0, input - cacheRead);
    }
    return {
      input: input,
      cacheInput: cacheRead + cacheWrite,
      output: output
    };
  }

  function reportTokenBreakdown(report) {
    const models = report && Array.isArray(report.models) ? report.models : [];
    if (!models.length) {
      return tokenBreakdown((((report || {}).summary || {}).tokens || {}), state.filters.provider);
    }
    return models.reduce(function (totals, model) {
      const current = tokenBreakdown((((model || {}).summary || {}).tokens || {}), model && model.provider);
      totals.input += current.input;
      totals.cacheInput += current.cacheInput;
      totals.output += current.output;
      return totals;
    }, { input: 0, cacheInput: 0, output: 0 });
  }

  function createKeyDetailLoading(message, isError) {
    const node = element("div", "key-detail-message" + (isError ? " is-error" : ""));
    node.appendChild(element("strong", "", isError ? "读取失败" : "正在加载"));
    node.appendChild(element("span", "", message));
    return node;
  }

  function appendDefinition(list, term, description) {
    const wrapper = element("div");
    const value = element("dd");
    wrapper.appendChild(element("dt", "", term));
    if (description && description.nodeType) {
      value.appendChild(description);
    } else {
      value.textContent = description;
    }
    wrapper.appendChild(value);
    list.appendChild(wrapper);
  }

  function createKeyCreatedAt(profile, includeLabel) {
    const node = element("time", "key-created-at");
    const value = profile && profile.created_at;
    const createdAt = value ? new Date(value) : null;
    if (createdAt && Number.isFinite(createdAt.getTime())) {
      const formatted = formatFullDateTime(createdAt);
      node.dateTime = createdAt.toISOString();
      node.textContent = (includeLabel ? "创建于 " : "") + formatted;
      node.title = "创建时间：" + formatted;
      return node;
    }
    node.classList.add("is-unknown");
    node.textContent = "创建时间未记录";
    node.title = "此 Key 创建于升级前，旧版本未保存创建时间";
    return node;
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
    dom.profileId.value = profile.id || "";
    dom.profilePreviewAlias.textContent = profile.alias || "未命名 Key";
    dom.profilePreviewKey.textContent = profile.masked_key || "无脱敏信息";
    dom.aliasCount.textContent = Array.from(dom.profileAlias.value).length + " / 128";
    dom.profileFormError.textContent = "";
    dom.profileFormError.hidden = true;
    updateProfilePreviewStatus(Boolean(profile.disabled));
    dom.profileDialog.showModal();
    window.setTimeout(function () { dom.profileAlias.focus(); }, 0);
  }

  function openKeyCreateDialog() {
    dom.keyCreateForm.reset();
    setKeyCreateError("");
    generateKeyCreateValue();
    dom.keyCreateDialog.showModal();
    window.setTimeout(function () { dom.keyCreateValue.focus(); }, 0);
  }

  function showCreatedKeyDialog(key) {
    dom.keyCreatedValue.value = key;
    dom.keyCreatedDialog.showModal();
    window.setTimeout(function () { dom.keyCreatedValue.select(); }, 0);
  }

  function updateProfilePreviewStatus(disabled) {
    dom.profilePreviewStatus.textContent = disabled ? "已停用" : "已启用";
    dom.profilePreviewStatus.className = "status-chip " + (disabled ? "disabled" : "success");
  }

  function generateRandomClientKey() {
    if (!window.crypto || typeof window.crypto.getRandomValues !== "function") {
      throw new Error("当前浏览器不支持安全随机 Key 生成，请手动输入 Key。");
    }
    const bytes = new Uint8Array(32);
    window.crypto.getRandomValues(bytes);
    let binary = "";
    bytes.forEach(function (value) { binary += String.fromCharCode(value); });
    return "sk-" + window.btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  }

  function generateKeyCreateValue() {
    try {
      dom.keyCreateValue.value = generateRandomClientKey();
      dom.keyCreateValue.type = "password";
      dom.keyCreateToggleVisibility.textContent = "显示";
      dom.keyCreateToggleVisibility.setAttribute("aria-pressed", "false");
      setKeyCreateError("");
    } catch (error) {
      dom.keyCreateValue.value = "";
      setKeyCreateError(error.message);
    }
  }

  function toggleKeyCreateVisibility() {
    const reveal = dom.keyCreateValue.type === "password";
    dom.keyCreateValue.type = reveal ? "text" : "password";
    dom.keyCreateToggleVisibility.textContent = reveal ? "隐藏" : "显示";
    dom.keyCreateToggleVisibility.setAttribute("aria-pressed", reveal ? "true" : "false");
  }

  async function copyKeyCreateValue() {
    const key = dom.keyCreateValue.value;
    if (!key) {
      return;
    }
    try {
      await copyTextToClipboard(key);
      toast("Key 已复制；保存后将不再显示完整内容");
    } catch (_error) {
      setKeyCreateError("无法自动复制，请点击“显示”后手动复制。");
    }
  }

  async function copyCreatedKeyValue() {
    const key = dom.keyCreatedValue.value;
    if (!key) {
      return;
    }
    try {
      await copyTextToClipboard(key);
      toast("Key 已复制，请妥善保存");
    } catch (_error) {
      dom.keyCreatedValue.focus();
      dom.keyCreatedValue.select();
      toast("无法自动复制，已选中 Key，可按 Ctrl + C 复制。", true);
    }
  }

  async function copyTextToClipboard(value) {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(value);
      return;
    }
    const helper = document.createElement("textarea");
    helper.value = value;
    helper.setAttribute("readonly", "");
    helper.style.position = "fixed";
    helper.style.opacity = "0";
    document.body.appendChild(helper);
    try {
      helper.select();
      if (!document.execCommand("copy")) {
        throw new Error("复制失败");
      }
    } finally {
      helper.remove();
    }
  }

  async function saveProfile(event) {
    event.preventDefault();
    const profile = state.editingProfile;
    if (!profile) {
      return;
    }
    const alias = dom.profileAlias.value.trim();
    const id = dom.profileId.value.trim();
    const idPattern = /^[A-Za-z0-9][A-Za-z0-9._-]*$/;
    if (Array.from(alias).length > 128) {
      setProfileError("别名不能超过 128 个字符。");
      return;
    }
    if (!id || id.length > 64 || !idPattern.test(id)) {
      setProfileError("稳定 ID 必须为 1–64 位，并仅包含字母、数字、点、下划线或连字符。");
      return;
    }
    setProfileError("");
    dom.saveProfile.disabled = true;
    try {
      await apiRequest(API.profiles, {
        method: "PATCH",
        body: JSON.stringify({ index: profile.index, expected_id: profile.id, id: id, alias: alias })
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

  async function createKey(event) {
    event.preventDefault();
    const key = dom.keyCreateValue.value.trim();
    const alias = dom.keyCreateAlias.value.trim();
    const id = dom.keyCreateId.value.trim();
    const idPattern = /^[A-Za-z0-9][A-Za-z0-9._-]*$/;
    if (!key) {
      setKeyCreateError("请输入客户端 Key。");
      dom.keyCreateValue.focus();
      return;
    }
    if (Array.from(alias).length > 128) {
      setKeyCreateError("别名不能超过 128 个字符。");
      return;
    }
    if (id && (id.length > 64 || !idPattern.test(id))) {
      setKeyCreateError("稳定 ID 必须为 1–64 位，且仅包含字母、数字、点、下划线或连字符。");
      return;
    }

    setKeyCreateError("");
    dom.saveKeyCreate.disabled = true;
    let submitted = false;
    try {
      const profileCount = state.profiles.length;
      submitted = true;
      await apiRequest(API.apiKeys, {
        method: "PATCH",
        body: JSON.stringify({ new: key })
      });

      if (alias || id) {
        const profileResponse = await apiRequest(API.profiles, { method: "GET" });
        const profiles = Array.isArray(profileResponse["api-key-profiles"]) ? profileResponse["api-key-profiles"] : [];
        const profile = profiles.find(function (item) { return item.index === profileCount; });
        if (!profile) {
          throw new Error("Key 已添加，但未找到其档案。请刷新列表后补充别名。");
        }
        const metadata = { index: profile.index, expected_id: profile.id };
        if (alias) {
          metadata.alias = alias;
        }
        if (id) {
          metadata.id = id;
        }
        await apiRequest(API.profiles, {
          method: "PATCH",
          body: JSON.stringify(metadata)
        });
      }
      dom.keyCreateDialog.close();
      showCreatedKeyDialog(key);
      toast("新 Key 已添加");
      await refreshAll({ includeSettings: false });
    } catch (error) {
      if (error.status === 401 || error.status === 403) {
        dom.keyCreateDialog.close();
        signOut(apiErrorMessage(error));
        return;
      }
      setKeyCreateError(apiErrorMessage(error));
    } finally {
      if (submitted) {
        dom.keyCreateValue.value = "";
      }
      dom.saveKeyCreate.disabled = false;
    }
  }

  async function toggleKeyProfile(profile, toggle) {
    const disabled = !toggle.checked;
    if (disabled) {
      const confirmed = await confirmAction("停用此 Key？", "停用后，此 Key 的新请求会被拒绝；历史统计仍会保留。", "确认停用");
      if (!confirmed) {
        toggle.checked = !profile.disabled;
        return;
      }
    }
    toggle.disabled = true;
    try {
      await apiRequest(API.profiles, {
        method: "PATCH",
        body: JSON.stringify({ index: profile.index, expected_id: profile.id, disabled: disabled })
      });
      toast(disabled ? "Key 已停用" : "Key 已启用");
      await refreshAll({ includeSettings: false });
    } catch (error) {
      if (error.status === 401 || error.status === 403) {
        signOut(apiErrorMessage(error));
        return;
      }
      toggle.checked = !profile.disabled;
      toast(error.status === 409 ? "配置已变化，请刷新列表后重试。" : apiErrorMessage(error), true);
    } finally {
      toggle.disabled = false;
    }
  }

  async function deleteKeyProfile(profile) {
    const label = profile.alias || profile.id || "此 Key";
    const confirmed = await confirmAction("删除 “" + label + "”？", "删除后该 Key 将立即失效，且无法恢复。历史用量只会保留在总览与审计数据中，不会继续显示在客户端 Key 列表。", "确认删除");
    if (!confirmed) {
      return;
    }
    try {
      const query = "?index=" + encodeURIComponent(String(profile.index)) + "&expected_id=" + encodeURIComponent(profile.id || "");
      await apiRequest(API.apiKeys + query, { method: "DELETE" });
      closeKeyDetails();
      toast("Key 已删除");
      await refreshAll({ includeSettings: false });
    } catch (error) {
      if (error.status === 401 || error.status === 403) {
        signOut(apiErrorMessage(error));
        return;
      }
      toast(error.status === 409 ? "配置已变化，请刷新列表后重试。" : apiErrorMessage(error), true);
    }
  }

  function setProfileError(message) {
    dom.profileFormError.textContent = message || "";
    dom.profileFormError.hidden = !message;
  }

  function setKeyCreateError(message) {
    dom.keyCreateFormError.textContent = message || "";
    dom.keyCreateFormError.hidden = !message;
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
    renderBillingSelects(draft);
    dom.billingEnabled.checked = Boolean(draft.enabled);
    dom.retentionDays.value = draft.retention_days;
    dom.pricingCurrency.value = draft.pricing.currency;
    dom.pricingVersion.value = draft.pricing.version;
    dom.settingsRevision.textContent = "版本 " + shortRevision(state.settings && state.settings.revision);
    renderPricingRules();
  }

  function renderBillingSelects(draft) {
    const limits = draft.limits || {};
    const minDays = number(limits.min_retention_days) || 1;
    const maxDays = number(limits.max_retention_days) || 3650;
    const retentionOptions = [7, 30, 60, 90, 180, 365, 730, 3650].filter(function (days) {
      return days >= minDays && days <= maxDays;
    }).map(function (days) {
      const labels = { 365: "1 年", 730: "2 年", 3650: "10 年" };
      return { value: String(days), label: labels[days] || days + " 天" };
    });
    setBillingSelectOptions(dom.retentionDays, retentionOptions, String(draft.retention_days), draft.retention_days + " 天 · 当前设置");

    const currencyOptions = ["USD", "CNY", "EUR", "JPY", "GBP", "HKD", "KRW"].map(function (currency) {
      const labels = { USD: "美元", CNY: "人民币", EUR: "欧元", JPY: "日元", GBP: "英镑", HKD: "港币", KRW: "韩元" };
      return { value: currency, label: currency + " · " + labels[currency] };
    });
    setBillingSelectOptions(dom.pricingCurrency, currencyOptions, draft.pricing.currency, draft.pricing.currency + " · 当前设置");

    const now = new Date();
    const monthVersion = "rates-" + now.getUTCFullYear() + "-" + String(now.getUTCMonth() + 1).padStart(2, "0");
    setBillingSelectOptions(dom.pricingVersion, [
      { value: "default", label: "默认版本" },
      { value: monthVersion, label: "按月版本 · " + monthVersion }
    ], draft.pricing.version, draft.pricing.version + " · 当前版本");
  }

  function setBillingSelectOptions(select, options, selectedValue, selectedLabel) {
    const selected = String(selectedValue || "");
    const fragment = document.createDocumentFragment();
    const seen = new Set();
    options.forEach(function (item) {
      const value = String(item.value);
      if (seen.has(value.toLocaleLowerCase())) {
        return;
      }
      seen.add(value.toLocaleLowerCase());
      const option = element("option", "", item.label);
      option.value = value;
      fragment.appendChild(option);
    });
    if (selected && !seen.has(selected.toLocaleLowerCase())) {
      const current = element("option", "", selectedLabel || selected);
      current.value = selected;
      fragment.appendChild(current);
    }
    select.replaceChildren(fragment);
    select.value = selected;
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
    const rules = all(".pricing-rule-card", dom.pricingRulesBody);
    const addedCard = rules.length && rules[rules.length - 1];
    if (addedCard && !window.matchMedia("(prefers-reduced-motion: reduce)").matches) {
      addedCard.classList.add("is-new");
      window.setTimeout(function () { addedCard.classList.remove("is-new"); }, 760);
    }
    const firstField = addedCard && addedCard.querySelector("[data-rule-field]");
    if (firstField) {
      firstField.focus();
    }
  }

  function renderPricingRules() {
    dom.pricingRulesBody.replaceChildren();
    const rules = state.billingDraft ? state.billingDraft.pricing.rules : [];
    dom.pricingEmpty.hidden = rules.length > 0;
    dom.pricingRuleCount.textContent = rules.length + " 条规则";
    renderRateSuggestions(rules);
    rules.forEach(function (rule, index) {
      const cacheInputRate = String(rule["cache-read-per-million"] || rule["cache-write-per-million"] || "").trim();
      rule["cache-read-per-million"] = cacheInputRate;
      rule["cache-write-per-million"] = cacheInputRate;
      const card = element("article", "pricing-rule-card");
      card.dataset.ruleIndex = String(index);
      card.style.setProperty("--rule-delay", (Math.min(index, 8) * 34) + "ms");
      const cardHeader = element("header", "pricing-rule-header");
      const identity = element("div", "pricing-rule-identity");
      identity.appendChild(element("span", "pricing-rule-index", String(index + 1).padStart(2, "0")));
      const identityCopy = element("span");
      identityCopy.appendChild(element("strong", "", "计费规则 " + (index + 1)));
      const summary = element("small", "", billingRuleSummary(rule));
      identityCopy.appendChild(summary);
      identity.appendChild(identityCopy);

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
      cardHeader.append(identity, group);
      card.appendChild(cardHeader);

      const matchSection = element("section", "pricing-rule-section");
      matchSection.appendChild(element("p", "pricing-rule-section-title", "匹配范围"));
      const matchGrid = element("div", "pricing-match-grid");
      const providerField = createBillingMatchField(rule, index, "provider", "服务商", billingProviderOptions(rule.provider), function (source) {
        summary.textContent = billingRuleSummary(rule);
        if (source === "input") {
          return;
        }
        rule.model = "*";
        rule["service-tier"] = "*";
        setBillingCustomMode(rule, "model", false);
        setBillingCustomMode(rule, "service-tier", false);
        renderPricingRules();
        focusBillingRuleField(index, source === "custom" ? "provider" : "model", source === "custom");
      });
      const modelField = createBillingMatchField(rule, index, "model", "模型", billingModelOptions(rule.provider, rule.model), function (source) {
        summary.textContent = billingRuleSummary(rule);
        if (source === "input") {
          return;
        }
        rule["service-tier"] = "*";
        setBillingCustomMode(rule, "service-tier", false);
        renderPricingRules();
        focusBillingRuleField(index, source === "custom" ? "model" : "service-tier", source === "custom");
      });
      const tierField = createBillingMatchField(rule, index, "service-tier", "服务层级", billingTierOptions(rule.provider, rule.model, rule["service-tier"]), function (source) {
        summary.textContent = billingRuleSummary(rule);
        if (source === "custom") {
          renderPricingRules();
          focusBillingRuleField(index, "service-tier", true);
        }
      });
      matchGrid.append(providerField.label, modelField.label, tierField.label);
      matchSection.appendChild(matchGrid);
      card.appendChild(matchSection);

      const rateSection = element("section", "pricing-rule-section pricing-rate-section");
      const rateTitle = element("div", "pricing-rate-heading");
      rateTitle.appendChild(element("p", "pricing-rule-section-title", "单价设置"));
      rateTitle.appendChild(element("span", "", "每 100 万 Token"));
      rateSection.appendChild(rateTitle);
      const rateGrid = element("div", "pricing-rate-grid");
      [
        { field: "input-per-million", label: "输入" },
        { field: "cache-read-per-million", label: "缓存输入" },
        { field: "output-per-million", label: "输出" }
      ].forEach(function (definition) {
        rateGrid.appendChild(createBillingRateField(rule, index, definition));
      });
      rateSection.appendChild(rateGrid);
      const note = element("p", "pricing-rule-note");
      note.append(element("span", "", "缓存读取与缓存写入统一按缓存输入计价"), element("span", "", "留空表示未定价 · 0.00 表示免费"));
      rateSection.appendChild(note);
      card.appendChild(rateSection);

      if (index < rules.length - 1 && [rule.provider, rule.model, rule["service-tier"]].every(function (value) { return String(value || "*").trim() === "*"; })) {
        const warning = element("div", "pricing-rule-warning", "这条全局规则会优先匹配，后面的规则将无法生效。请将它移到最后。");
        card.appendChild(warning);
      }
      dom.pricingRulesBody.appendChild(card);
    });
  }

  function createBillingMatchField(rule, index, field, labelText, options, onValueChange) {
    const wrapper = element("div", "pricing-field pricing-match-field");
    const fieldID = "billing-rule-" + index + "-" + field.replace(/[^a-z0-9]+/gi, "-");
    const customID = fieldID + "-custom";
    const hintID = customID + "-hint";
    const visibleLabel = element("label", "pricing-field-label", labelText);
    visibleLabel.htmlFor = fieldID;
    const shell = element("span", "billing-select-shell");
    const select = element("select", "billing-select");
    select.id = fieldID;
    select.dataset.ruleField = field;
    select.setAttribute("aria-controls", customID);
    select.setAttribute("aria-label", "规则 " + (index + 1) + " " + labelText);

    const current = billingRuleValue(rule[field]);
    const customMode = isBillingCustomMode(rule, field, current);
    const fragment = document.createDocumentFragment();
    options.forEach(function (item) {
      const option = element("option");
      option.value = item.value;
      option.textContent = item.label || item.value;
      fragment.appendChild(option);
    });
    const customOption = element("option");
    customOption.value = BILLING_CUSTOM_VALUE;
    customOption.textContent = "自定义匹配式…";
    fragment.appendChild(customOption);
    select.appendChild(fragment);
    select.value = customMode ? BILLING_CUSTOM_VALUE : current;
    if (!select.value) {
      select.value = "*";
    }

    const customRegion = element("span", "billing-custom-match");
    customRegion.id = customID;
    customRegion.hidden = !customMode;
    const customLabel = element("label", "sr-only", "规则 " + (index + 1) + " " + labelText + "自定义匹配式");
    const customInputID = customID + "-input";
    customLabel.htmlFor = customInputID;
    const customInput = element("input", "billing-custom-input");
    customInput.id = customInputID;
    customInput.type = "text";
    customInput.value = customMode ? String(rule[field] == null ? "" : rule[field]) : "";
    customInput.placeholder = field === "provider" ? "例如 codex 或 open*" : field === "model" ? "例如 gpt-*" : "例如 priority 或 *";
    customInput.autocomplete = "off";
    customInput.spellcheck = false;
    customInput.maxLength = number(((state.billingDraft || {}).limits || {}).max_pattern_length) || 256;
    customInput.dataset.ruleCustomField = field;
    customInput.setAttribute("aria-describedby", hintID);
    const hint = element("small", "billing-custom-hint", "支持 * 匹配任意字符，? 匹配单个字符");
    hint.id = hintID;
    customRegion.append(customLabel, customInput, hint);

    select.addEventListener("change", function () {
      const selectingCustom = select.value === BILLING_CUSTOM_VALUE;
      setBillingCustomMode(rule, field, selectingCustom);
      rule[field] = selectingCustom ? "" : select.value;
      setBillingError("");
      markBillingDirty(true);
      if (onValueChange) {
        onValueChange(selectingCustom ? "custom" : "select");
      }
    });
    customInput.addEventListener("input", function () {
      rule[field] = customInput.value.trim();
      setBillingError("");
      markBillingDirty(true);
      if (onValueChange) {
        onValueChange("input");
      }
    });

    shell.appendChild(select);
    wrapper.append(visibleLabel, shell, customRegion);
    return { label: wrapper, input: select, select: select, customInput: customInput };
  }

  function createBillingRateField(rule, index, definition) {
    const label = element("label", "pricing-field pricing-rate-field");
    label.appendChild(element("span", "pricing-field-label", definition.label));
    const shell = element("span", "pricing-rate-input");
    shell.appendChild(element("span", "pricing-currency-prefix", String(state.billingDraft.pricing.currency || "USD").toUpperCase()));
    const input = element("input");
    input.type = "number";
    input.min = "0";
    input.step = "0.000001";
    input.inputMode = "decimal";
    input.spellcheck = false;
    input.placeholder = "0.00";
    input.value = rule[definition.field] == null ? "" : String(rule[definition.field]);
    input.dataset.ruleField = definition.field;
    input.setAttribute("list", "billing-rate-suggestions");
    input.setAttribute("aria-label", "规则 " + (index + 1) + " " + definition.label + "单价");
    input.addEventListener("input", function () {
      rule[definition.field] = input.value.trim();
      if (definition.field === "cache-read-per-million") {
        rule["cache-write-per-million"] = rule[definition.field];
      }
      setBillingError("");
      markBillingDirty(true);
    });
    shell.appendChild(input);
    label.appendChild(shell);
    return label;
  }

  function renderRateSuggestions(rules) {
    const list = byId("billing-rate-suggestions");
    if (!list) {
      return;
    }
    const values = new Set(["0.00"]);
    rules.forEach(function (rule) {
      RATE_FIELDS.forEach(function (field) {
        const value = String(rule[field] == null ? "" : rule[field]).trim();
        if (value) {
          values.add(value);
        }
      });
    });
    const fragment = document.createDocumentFragment();
    Array.from(values).forEach(function (value) {
      const option = element("option");
      option.value = value;
      option.label = value === "0.00" ? "免费" : "已使用价格";
      fragment.appendChild(option);
    });
    list.replaceChildren(fragment);
  }

  function billingProviderOptions(current) {
    const options = [];
    const seen = new Set();
    addBillingOption(options, seen, "*", "全部服务商");
    state.billingProviderCatalog.forEach(function (entry) {
      const count = number(entry.credentials);
      const suffix = count > 0 ? " · " + count + " 个凭据" : "";
      addBillingOption(options, seen, entry.provider, billingProviderLabel(entry.provider) + suffix);
    });
    billingCatalogRows().forEach(function (row) {
      addBillingOption(options, seen, row.provider, billingProviderLabel(row.provider) + " · 历史使用");
    });
    billingDraftRules().forEach(function (rule) {
      if (!isBillingGlobPattern(rule.provider)) {
        addBillingOption(options, seen, rule.provider, billingProviderLabel(rule.provider) + " · 已保存");
      }
    });
    if (!isBillingGlobPattern(current)) {
      addBillingOption(options, seen, current, billingProviderLabel(current) + " · 当前不可用");
    }
    return options;
  }

  function billingModelOptions(provider, current) {
    const options = [];
    const seen = new Set();
    addBillingOption(options, seen, "*", "全部模型");
    state.billingProviderCatalog.forEach(function (entry) {
      if (!billingPatternMatches(provider, entry.provider)) {
        return;
      }
      (entry.models || []).forEach(function (model) {
        const modelID = String(model.id || "").trim();
        const displayName = String(model.display_name || "").trim();
        const label = displayName && !sameText(displayName, modelID) ? displayName + " · " + modelID : modelID;
        addBillingOption(options, seen, modelID, label);
      });
    });
    billingCatalogRows().forEach(function (row) {
      if (billingPatternMatches(provider, row.provider)) {
        addBillingOption(options, seen, row.model, String(row.model || "") + " · 历史使用");
      }
    });
    billingDraftRules().forEach(function (rule) {
      if (billingPatternMatches(provider, rule.provider) && !isBillingGlobPattern(rule.model)) {
        addBillingOption(options, seen, rule.model, String(rule.model || "") + " · 已保存");
      }
    });
    if (!isBillingGlobPattern(current)) {
      addBillingOption(options, seen, current, String(current || "") + " · 当前不可用");
    }
    return options;
  }

  function billingTierOptions(provider, model, current) {
    const options = [];
    const seen = new Set();
    BILLING_TIERS.forEach(function (item) { addBillingOption(options, seen, item.value, item.label); });
    billingCatalogRows().forEach(function (row) {
      if (billingPatternMatches(provider, row.provider) && billingPatternMatches(model, row.model)) {
        addBillingOption(options, seen, row.service_tier, String(row.service_tier || "") + " · 历史使用");
      }
    });
    billingDraftRules().forEach(function (rule) {
      if (billingPatternMatches(provider, rule.provider) && billingPatternMatches(model, rule.model) && !isBillingGlobPattern(rule["service-tier"])) {
        addBillingOption(options, seen, rule["service-tier"], String(rule["service-tier"] || "") + " · 已保存");
      }
    });
    if (!isBillingGlobPattern(current)) {
      addBillingOption(options, seen, current, String(current || "") + " · 当前不可用");
    }
    return options;
  }

  function billingCatalogRows() {
    const report = state.billingCatalogReport || state.baseReport || {};
    return Array.isArray(report.models) ? report.models : [];
  }

  function addBillingOption(options, seen, value, label) {
    const text = String(value == null ? "" : value).trim();
    const key = text.toLocaleLowerCase();
    if (!text || seen.has(key) || (isBillingGlobPattern(text) && text !== "*")) {
      return;
    }
    seen.add(key);
    options.push({ value: text, label: label || text });
  }

  function billingDraftRules() {
    return state.billingDraft && state.billingDraft.pricing && Array.isArray(state.billingDraft.pricing.rules)
      ? state.billingDraft.pricing.rules
      : [];
  }

  function billingProviderLabel(provider) {
    const value = String(provider || "").trim();
    return BILLING_PROVIDER_LABELS[value.toLocaleLowerCase()] || value;
  }

  function billingRuleValue(value) {
    const text = String(value == null ? "" : value).trim();
    return text || "*";
  }

  function isBillingGlobPattern(value) {
    const text = String(value == null ? "" : value).trim();
    return text !== "*" && /[?*]/.test(text);
  }

  function isBillingCustomMode(rule, field, current) {
    const fields = billingCustomModes.get(rule);
    return Boolean(fields && fields.has(field)) || isBillingGlobPattern(current);
  }

  function setBillingCustomMode(rule, field, enabled) {
    let fields = billingCustomModes.get(rule);
    if (!fields && enabled) {
      fields = new Set();
      billingCustomModes.set(rule, fields);
    }
    if (!fields) {
      return;
    }
    if (enabled) {
      fields.add(field);
    } else {
      fields.delete(field);
    }
    if (!fields.size) {
      billingCustomModes.delete(rule);
    }
  }

  function billingPatternMatches(pattern, value) {
    const expected = String(pattern == null ? "" : pattern).trim().toLocaleLowerCase();
    const actual = String(value == null ? "" : value).trim().toLocaleLowerCase();
    if (!expected || expected === "*") {
      return true;
    }
    if (!isBillingGlobPattern(expected)) {
      return expected === actual;
    }
    let patternIndex = 0;
    let valueIndex = 0;
    let starIndex = -1;
    let starValueIndex = 0;
    while (valueIndex < actual.length) {
      if (patternIndex < expected.length && (expected[patternIndex] === "?" || expected[patternIndex] === actual[valueIndex])) {
        patternIndex += 1;
        valueIndex += 1;
      } else if (patternIndex < expected.length && expected[patternIndex] === "*") {
        starIndex = patternIndex;
        patternIndex += 1;
        starValueIndex = valueIndex;
      } else if (starIndex >= 0) {
        patternIndex = starIndex + 1;
        starValueIndex += 1;
        valueIndex = starValueIndex;
      } else {
        return false;
      }
    }
    while (patternIndex < expected.length && expected[patternIndex] === "*") {
      patternIndex += 1;
    }
    return patternIndex === expected.length;
  }

  function sameText(left, right) {
    return String(left || "").trim().toLocaleLowerCase() === String(right || "").trim().toLocaleLowerCase();
  }

  function billingRuleSummary(rule) {
    return [rule.provider || "*", rule.model || "*", rule["service-tier"] || "*"].join("  /  ");
  }

  function focusBillingRuleField(index, field, custom) {
    window.requestAnimationFrame(function () {
      const card = dom.pricingRulesBody.querySelector('[data-rule-index="' + index + '"]');
      if (!card) {
        return;
      }
      const selector = custom
        ? '[data-rule-custom-field="' + field + '"]'
        : '[data-rule-field="' + field + '"]';
      const control = card.querySelector(selector);
      if (control) {
        control.focus();
      }
    });
  }

  function captureBillingRuleFocus() {
    const active = document.activeElement;
    if (!active || !dom.pricingRulesBody.contains(active)) {
      return null;
    }
    const card = active.closest("[data-rule-index]");
    const field = active.dataset.ruleCustomField || active.dataset.ruleField;
    if (!card || !field) {
      return null;
    }
    return {
      index: number(card.dataset.ruleIndex),
      field: field,
      custom: Boolean(active.dataset.ruleCustomField),
      selectionStart: typeof active.selectionStart === "number" ? active.selectionStart : null,
      selectionEnd: typeof active.selectionEnd === "number" ? active.selectionEnd : null
    };
  }

  function restoreBillingRuleFocus(focus) {
    if (!focus) {
      return;
    }
    window.requestAnimationFrame(function () {
      const card = dom.pricingRulesBody.querySelector('[data-rule-index="' + focus.index + '"]');
      if (!card) {
        return;
      }
      const selector = focus.custom
        ? '[data-rule-custom-field="' + focus.field + '"]'
        : '[data-rule-field="' + focus.field + '"]';
      const control = card.querySelector(selector);
      if (!control) {
        return;
      }
      control.focus();
      if (focus.custom && focus.selectionStart !== null && typeof control.setSelectionRange === "function") {
        control.setSelectionRange(focus.selectionStart, focus.selectionEnd);
      }
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
    const movedInput = movedRow && movedRow.querySelector("[data-rule-field]");
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
    const maxPatternLength = number(limits.max_pattern_length) || 256;
    const maxRateLength = number(limits.max_rate_length) || 64;
    for (let index = 0; index < draft.pricing.rules.length; index += 1) {
      const rule = draft.pricing.rules[index];
      const numberLabel = "第 " + (index + 1) + " 条规则";
      rule["cache-write-per-million"] = String(rule["cache-read-per-million"] || "").trim();
      if (!(rule.provider || "").trim() || !(rule.model || "").trim()) {
        return numberLabel + "必须填写 Provider 和模型匹配式。";
      }
      const patternTooLong = [rule.provider, rule.model, rule["service-tier"]].some(function (pattern) {
        return String(pattern || "").length > maxPatternLength;
      });
      if (patternTooLong) {
        return numberLabel + "的匹配内容不能超过 " + maxPatternLength + " 个字符。";
      }
      const rates = RATE_FIELDS.map(function (field) { return String(rule[field] || "").trim(); });
      if (!rates.some(Boolean)) {
        return numberLabel + "至少需要填写一个单价。";
      }
      const invalidRate = rates.find(function (rate) { return rate && (rate.length > maxRateLength || !decimal.test(rate)); });
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

  async function loadBillingCatalog(force) {
    if (state.loadedViews.billingCatalog && !force) {
      const cachedFocus = captureBillingRuleFocus();
      renderPricingRules();
      restoreBillingRuleFocus(cachedFocus);
      return;
    }
    setBillingCatalogStatus("正在读取服务商…", false, true);
    const focus = captureBillingRuleFocus();
    try {
      const initialResults = await Promise.allSettled([
        apiRequest(API.usage, { method: "GET" }),
        apiRequest(API.authFiles, { method: "GET" })
      ]);
      const usageResult = initialResults[0];
      const authResult = initialResults[1];
      const authenticationError = [usageResult, authResult].find(function (result) {
        return result.status === "rejected" && result.reason && (result.reason.status === 401 || result.reason.status === 403);
      });
      if (authenticationError) {
        signOut(apiErrorMessage(authenticationError.reason));
        return;
      }

      if (usageResult.status === "fulfilled") {
        state.billingCatalogReport = normalizeReport(usageResult.value);
      }

      let modelFailures = 0;
      if (authResult.status === "fulfilled") {
        const payload = authResult.value && typeof authResult.value === "object" ? authResult.value : {};
        const files = Array.isArray(payload.files) ? payload.files : [];
        const catalog = prepareBillingProviderCatalog(files);
        const modelResults = await Promise.allSettled(catalog.jobs.map(function (job) {
          return apiRequest(API.authFileModels + "?name=" + encodeURIComponent(job.reference), { method: "GET" });
        }));
        const modelAuthenticationError = modelResults.find(function (result) {
          return result.status === "rejected" && result.reason && (result.reason.status === 401 || result.reason.status === 403);
        });
        if (modelAuthenticationError) {
          signOut(apiErrorMessage(modelAuthenticationError.reason));
          return;
        }
        modelResults.forEach(function (result, index) {
          const job = catalog.jobs[index];
          if (result.status !== "fulfilled") {
            modelFailures += 1;
            return;
          }
          const response = result.value && typeof result.value === "object" ? result.value : {};
          const models = Array.isArray(response.models) ? response.models : [];
          models.forEach(function (model) {
            addBillingCatalogModel(job.entry, model);
          });
        });
        state.billingProviderCatalog = finalizeBillingProviderCatalog(catalog.entries);
      }

      state.loadedViews.billingCatalog = true;
      renderPricingRules();
      restoreBillingRuleFocus(focus);

      const providerCount = state.billingProviderCatalog.length;
      const catalogFailed = authResult.status !== "fulfilled";
      const usageFailed = usageResult.status !== "fulfilled";
      if (catalogFailed) {
        setBillingCatalogStatus("服务商读取失败 · 可使用历史或自定义规则", true, false);
      } else if (!providerCount) {
        setBillingCatalogStatus("未发现凭据 · 可使用自定义规则", false, false);
      } else if (modelFailures || usageFailed) {
        const detail = modelFailures ? "部分模型未载入" : "历史候选未载入";
        setBillingCatalogStatus(providerCount + " 个服务商 · " + detail, true, false);
      } else {
        setBillingCatalogStatus(providerCount + " 个服务商 · 模型已同步", false, false);
      }

      if (catalogFailed && usageFailed) {
        toast("服务商与模型目录载入失败，仍可继续编辑当前规则。", true);
      }
    } catch (error) {
      if (error.status === 401 || error.status === 403) {
        signOut(apiErrorMessage(error));
        return;
      }
      state.loadedViews.billingCatalog = true;
      renderPricingRules();
      restoreBillingRuleFocus(focus);
      setBillingCatalogStatus("目录载入失败 · 可使用自定义规则", true, false);
      toast("计费下拉选项载入失败，将继续使用当前规则。", true);
    } finally {
      if (dom.pricingCatalogStatus) {
        dom.pricingCatalogStatus.removeAttribute("aria-busy");
      }
    }
  }

  function prepareBillingProviderCatalog(files) {
    const byProvider = new Map();
    const jobs = [];
    files.forEach(function (file) {
      if (!file || typeof file !== "object") {
        return;
      }
      const provider = String(file.provider || file.type || "").trim();
      const reference = String(file.id || file.name || "").trim();
      if (!provider) {
        return;
      }
      const key = provider.toLocaleLowerCase();
      let entry = byProvider.get(key);
      if (!entry) {
        entry = { provider: provider, credentials: 0, models: [], modelKeys: new Set() };
        byProvider.set(key, entry);
      }
      entry.credentials += 1;
      if (reference) {
        jobs.push({ entry: entry, reference: reference });
      }
    });
    return { entries: Array.from(byProvider.values()), jobs: jobs };
  }

  function addBillingCatalogModel(entry, model) {
    if (!entry || !model || typeof model !== "object") {
      return;
    }
    const id = String(model.id || "").trim();
    const key = id.toLocaleLowerCase();
    if (!id || entry.modelKeys.has(key)) {
      return;
    }
    entry.modelKeys.add(key);
    entry.models.push({
      id: id,
      display_name: String(model.display_name || "").trim()
    });
  }

  function finalizeBillingProviderCatalog(entries) {
    entries.forEach(function (entry) {
      delete entry.modelKeys;
      entry.models.sort(function (left, right) {
        return left.id.localeCompare(right.id, "zh-CN", { sensitivity: "base" });
      });
    });
    return entries.sort(function (left, right) {
      return billingProviderLabel(left.provider).localeCompare(billingProviderLabel(right.provider), "zh-CN", { sensitivity: "base" });
    });
  }

  function setBillingCatalogStatus(message, issue, busy) {
    if (!dom.pricingCatalogStatus) {
      return;
    }
    dom.pricingCatalogStatus.textContent = message;
    dom.pricingCatalogStatus.classList.toggle("issue", Boolean(issue));
    dom.pricingCatalogStatus.classList.toggle("soft", !issue);
    if (busy) {
      dom.pricingCatalogStatus.setAttribute("aria-busy", "true");
    } else {
      dom.pricingCatalogStatus.removeAttribute("aria-busy");
    }
  }

  async function loadCredentials(force) {
    if (state.loadedViews.credentials && !force) {
      renderCredentials();
      return;
    }
    setControlBusy(dom.credentialsRefresh, true);
    try {
      const pluginRequest = state.loadedViews.pluginCatalog
        ? Promise.resolve(null)
        : apiRequest(API.plugins, { method: "GET" });
      const results = await Promise.allSettled([
        apiRequest(API.authFiles, { method: "GET" }),
        pluginRequest
      ]);
      if (results[0].status !== "fulfilled") {
        throw results[0].reason;
      }
      const payload = results[0].value;
      state.authFiles = Array.isArray(payload && payload.files) ? payload.files : [];
      reconcileQuotaResults();
      if (results[1].status === "fulfilled" && results[1].value) {
        applyPluginPayload(results[1].value);
        state.loadedViews.pluginCatalog = true;
      }
      state.loadedViews.credentials = true;
      populateCredentialProviders();
      renderPluginOAuthProviders();
      renderCredentials();
    } catch (error) {
      handleViewError(error, "凭据载入失败");
    } finally {
      setControlBusy(dom.credentialsRefresh, false);
    }
  }

  function populateCredentialProviders() {
    const providers = uniqueSorted(state.authFiles.map(function (item) { return item.provider || item.type; }));
    const fragment = document.createDocumentFragment();
    const allOption = element("option", "", "全部 Provider");
    allOption.value = "all";
    fragment.appendChild(allOption);
    providers.forEach(function (provider) {
      const option = element("option", "", provider);
      option.value = provider;
      fragment.appendChild(option);
    });
    dom.credentialProvider.replaceChildren(fragment);
    dom.credentialProvider.value = providers.includes(state.credentialProvider) ? state.credentialProvider : "all";
    state.credentialProvider = dom.credentialProvider.value;
  }

  function credentialState(item) {
    const status = String(item.status || "unknown").toLocaleLowerCase();
    if (item.disabled || status === "disabled") {
      return "disabled";
    }
    if (["pending", "refreshing"].includes(status)) {
      return "waiting";
    }
    if (item.unavailable || ["error", "failed", "expired", "unknown"].includes(status)) {
      return "issue";
    }
    return status === "active" ? "active" : "issue";
  }

  function credentialMatches(item) {
    const searchable = [item.label, item.email, item.account, item.name, item.id, item.provider, item.type, item.note].join(" ").toLocaleLowerCase();
    if (state.credentialSearch && !searchable.includes(state.credentialSearch)) {
      return false;
    }
    const provider = String(item.provider || item.type || "unknown");
    if (state.credentialProvider !== "all" && provider !== state.credentialProvider) {
      return false;
    }
    return state.credentialStatus === "all" || credentialState(item) === state.credentialStatus;
  }

  function renderCredentials() {
    const rows = state.authFiles.filter(credentialMatches);
    dom.credentialTotal.textContent = state.authFiles.length + " 个凭据";
    dom.credentialTableBody.replaceChildren();
    dom.credentialCardList.replaceChildren();
    rows.forEach(function (item) {
      dom.credentialTableBody.appendChild(createCredentialRow(item));
      dom.credentialCardList.appendChild(createCredentialCard(item));
    });
    dom.credentialEmpty.hidden = rows.length > 0;
  }

  function credentialDisplayName(item) {
    return item.label || item.email || item.account || item.name || item.id || "未命名凭据";
  }

  function credentialSubtitle(item) {
    const parts = [];
    if (item.email && item.email !== credentialDisplayName(item)) {
      parts.push(item.email);
    }
    parts.push(item.name || item.id || "runtime");
    return parts.filter(Boolean).join(" · ");
  }

  function createCredentialIdentity(item) {
    const wrapper = element("div", "credential-identity");
    const provider = String(item.provider || item.type || "?");
    wrapper.appendChild(element("span", "credential-avatar", provider.slice(0, 1).toUpperCase()));
    const copy = element("span");
    copy.appendChild(element("strong", "", credentialDisplayName(item)));
    copy.appendChild(element("small", "", credentialSubtitle(item)));
    wrapper.appendChild(copy);
    return wrapper;
  }

  function credentialStatusChip(item) {
    const status = credentialState(item);
    const labels = { active: "可用", disabled: "已停用", waiting: "等待授权", issue: "需要处理" };
    const tone = status === "active" ? "success" : status === "issue" ? "danger" : status === "waiting" ? "waiting" : "";
    return element("span", "status-chip " + tone, labels[status]);
  }

  function credentialActions(item) {
    const actions = element("div", "row-actions");
    if (item.runtime_only) {
      actions.appendChild(element("span", "status-chip soft", "运行时凭据"));
      return actions;
    }
    const disabled = credentialState(item) === "disabled";
    const toggle = element("button", "mini-button", disabled ? "启用" : "停用");
    toggle.type = "button";
    toggle.addEventListener("click", function () { toggleCredential(item, !disabled); });
    actions.appendChild(toggle);
    if (item.source !== "memory") {
      const remove = element("button", "mini-button danger", "删除");
      remove.type = "button";
      remove.addEventListener("click", function () { deleteCredential(item); });
      actions.appendChild(remove);
    }
    return actions;
  }

  function createCredentialRow(item) {
    const row = element("tr");
    const identity = element("td");
    identity.appendChild(createCredentialIdentity(item));
    row.appendChild(identity);
    row.appendChild(element("td", "", item.provider || item.type || "unknown"));
    const status = element("td");
    status.appendChild(credentialStatusChip(item));
    row.appendChild(status);
    row.appendChild(element("td", "", formatInteger(number(item.success) + number(item.failed))));
    row.appendChild(element("td", "", item.last_refresh ? formatDateTime(item.last_refresh) : "—"));
    const actionCell = element("td");
    actionCell.appendChild(credentialActions(item));
    row.appendChild(actionCell);
    return row;
  }

  function createCredentialCard(item) {
    const card = element("article", "credential-card");
    card.appendChild(createCredentialIdentity(item));
    const meta = element("div", "field-meta");
    meta.appendChild(credentialStatusChip(item));
    meta.appendChild(element("span", "", (item.provider || item.type || "unknown") + " · " + formatInteger(number(item.success) + number(item.failed)) + " 次请求"));
    card.appendChild(meta);
    card.appendChild(credentialActions(item));
    return card;
  }

  function quotaProvider(item) {
    const raw = String(item && (item.provider || item.type) || "").trim().toLocaleLowerCase().replace(/[_\s]+/g, "-");
    if (raw === "anthropic") {
      return "claude";
    }
    if (raw === "x-ai" || raw === "grok") {
      return "xai";
    }
    return QUOTA_PROVIDER_ORDER.includes(raw) ? raw : "";
  }

  function quotaAuthIndex(item) {
    return String(item && (item.auth_index || item.authIndex || item.AuthIndex) || "").trim();
  }

  function quotaCredentialKey(item) {
    return quotaAuthIndex(item) || String(item && (item.id || item.name) || "unknown");
  }

  function quotaMembership(item, result, provider) {
    const claims = quotaObject(item && item.id_token);
    const rawPlan = quotaValue(claims, ["plan_type", "planType"])
      || quotaValue(item, ["plan_type", "planType"]);
    const providerLabel = QUOTA_PROVIDER_LABELS[provider] || provider || "账户";
    let plan = String(result && result.plan || "").trim();
    if (!plan || plan === providerLabel) {
      plan = rawPlan ? quotaPlanLabel(rawPlan, providerLabel) : "";
    }
    const renewalAt = quotaValue(claims, [
      "chatgpt_subscription_active_until", "chatgptSubscriptionActiveUntil", "subscription_active_until", "subscriptionActiveUntil"
    ]) || quotaValue(item, ["subscription_active_until", "subscriptionActiveUntil"]);
    return {
      plan: plan || "套餐待查询",
      known: Boolean(plan),
      renewalAt: renewalAt
    };
  }

  function quotaMembershipTone(plan) {
    const normalized = String(plan || "").toLocaleLowerCase();
    if (normalized.includes("max") || normalized.includes("ultra")) {
      return "elite";
    }
    if (normalized.includes("pro") || normalized.includes("plus")) {
      return "premium";
    }
    if (normalized.includes("team") || normalized.includes("business")) {
      return "team";
    }
    return "standard";
  }

  function quotaTone(value) {
    if (value === null) {
      return "unknown";
    }
    if (value <= 15) {
      return "danger";
    }
    if (value <= 40) {
      return "warning";
    }
    if (value <= 70) {
      return "steady";
    }
    return "success";
  }

  function quotaScaleStep(value) {
    const parsed = Number(value);
    if (!Number.isFinite(parsed)) {
      return 0;
    }
    return Math.max(0, Math.min(100, Math.round(parsed / 5) * 5));
  }

  function quotaScaleClass(prefix, value) {
    return prefix + "-" + quotaScaleStep(value);
  }

  function quotaCredentials() {
    return state.authFiles.filter(function (item) {
      return Boolean(quotaProvider(item)) && !item.disabled && String(item.status || "").toLocaleLowerCase() !== "disabled";
    }).sort(function (left, right) {
      const providerOrder = QUOTA_PROVIDER_ORDER.indexOf(quotaProvider(left)) - QUOTA_PROVIDER_ORDER.indexOf(quotaProvider(right));
      return providerOrder || credentialDisplayName(left).localeCompare(credentialDisplayName(right), "zh-CN");
    });
  }

  function reconcileQuotaResults() {
    const validKeys = new Set(quotaCredentials().map(quotaCredentialKey));
    Object.keys(state.quotaResults).forEach(function (key) {
      if (!validKeys.has(key)) {
        delete state.quotaResults[key];
      }
    });
    Object.keys(state.quotaBusy).forEach(function (key) {
      if (!validKeys.has(key)) {
        delete state.quotaBusy[key];
      }
    });
  }

  async function loadQuotaCredentials(force) {
    if ((state.loadedViews.quota || state.loadedViews.credentials) && !force) {
      state.loadedViews.quota = true;
      reconcileQuotaResults();
      renderQuotaCredentials();
      return;
    }
    setControlBusy(dom.quotaRefreshAll, true);
    try {
      const payload = await apiRequest(API.authFiles, { method: "GET" });
      state.authFiles = Array.isArray(payload && payload.files) ? payload.files : [];
      state.loadedViews.quota = true;
      reconcileQuotaResults();
      renderQuotaCredentials();
    } catch (error) {
      handleViewError(error, "凭证额度载入失败");
    } finally {
      setControlBusy(dom.quotaRefreshAll, state.quotaRefreshingAll);
    }
  }

  function quotaCredentialMatches(item) {
    const provider = quotaProvider(item);
    if (state.quotaProvider !== "all" && provider !== state.quotaProvider) {
      return false;
    }
    const searchable = [
      credentialDisplayName(item), credentialSubtitle(item), item.email, item.account, item.name, item.id,
      item.project_id, QUOTA_PROVIDER_LABELS[provider]
    ].join(" ").toLocaleLowerCase();
    return !state.quotaSearch || searchable.includes(state.quotaSearch);
  }

  function renderQuotaCredentials() {
    if (!dom.quotaCardList) {
      return;
    }
    const credentials = quotaCredentials();
    const visible = credentials.filter(quotaCredentialMatches);
    const results = credentials.map(function (item) { return state.quotaResults[quotaCredentialKey(item)]; }).filter(Boolean);
    const readyCount = results.filter(function (result) { return result.status === "success"; }).length;
    const issueCount = results.filter(function (result) { return result.status === "error"; }).length;

    if (dom.quotaTotal) {
      dom.quotaTotal.textContent = formatInteger(credentials.length);
    }
    if (dom.quotaReadyCount) {
      dom.quotaReadyCount.textContent = formatInteger(readyCount);
    }
    if (dom.quotaIssueCount) {
      dom.quotaIssueCount.textContent = formatInteger(issueCount);
    }
    all("[data-quota-provider]", dom.quotaProviderTabs || document).forEach(function (button) {
      const active = (button.dataset.quotaProvider || "all") === state.quotaProvider;
      button.classList.toggle("active", active);
      button.setAttribute("aria-pressed", active ? "true" : "false");
    });
    setControlBusy(dom.quotaRefreshAll, state.quotaRefreshingAll);
    dom.quotaCardList.replaceChildren();
    visible.forEach(function (item) {
      dom.quotaCardList.appendChild(createQuotaCard(item));
    });
    renderQuotaWindows(visible);
    if (dom.quotaEmpty) {
      dom.quotaEmpty.hidden = visible.length > 0;
    }
  }

  function createQuotaCard(item) {
    const provider = quotaProvider(item);
    const key = quotaCredentialKey(item);
    const result = state.quotaResults[key];
    const busy = Boolean(state.quotaBusy[key]);
    const knownQuotaValues = result && result.status === "success" ? (result.meters || []).map(function (meter) {
      return quotaPercent(meter.remainingPercent);
    }).filter(function (value) { return value !== null; }) : [];
    const cardTone = quotaTone(knownQuotaValues.length ? Math.min.apply(null, knownQuotaValues) : null);
    const card = element("article", "quota-card " + provider + " quota-health-" + cardTone + (busy ? " loading is-loading" : result && result.status === "error" ? " has-error" : ""));
    card.dataset.provider = provider;
    card.setAttribute("aria-busy", busy ? "true" : "false");

    const header = element("header", "quota-card-header");
    const identity = element("div", "quota-identity");
    identity.appendChild(element("span", "quota-avatar " + provider, provider.slice(0, 1).toUpperCase()));
    const copy = element("div", "quota-card-copy");
    copy.appendChild(element("strong", "quota-file-name", item.name || credentialDisplayName(item)));
    copy.appendChild(element("small", "quota-card-meta", (QUOTA_PROVIDER_LABELS[provider] || provider) + " · " + credentialSubtitle(item)));
    copy.appendChild(element("small", "quota-account-meta", credentialSubtitle(item)));
    const membership = quotaMembership(item, result, provider);
    const membershipRow = element("div", "quota-membership-row");
    membershipRow.appendChild(element("span", "quota-membership-label", "套餐"));
    membershipRow.appendChild(element("span", "quota-membership-badge " + quotaMembershipTone(membership.plan) + (membership.known ? "" : " is-pending"), membership.plan));
    const renewalAt = quotaTimestamp(membership.renewalAt);
    if (renewalAt) {
      membershipRow.appendChild(element("span", "quota-membership-renewal", "续期 " + formatDateTime(renewalAt)));
      const renewalCountdown = element("span", "quota-inline-countdown");
      renewalCountdown.dataset.quotaCountdownAt = String(renewalAt);
      renewalCountdown.dataset.quotaCountdownFormat = "relative";
      membershipRow.appendChild(renewalCountdown);
    }
    copy.appendChild(membershipRow);
    identity.appendChild(copy);
    header.appendChild(identity);

    const actions = element("div", "quota-card-actions");
    let stateLabel = "等待查询";
    let stateTone = "soft";
    if (busy) {
      stateLabel = "查询中";
      stateTone = "waiting";
    } else if (result && result.status === "success") {
      stateLabel = "已更新";
      stateTone = "success";
    } else if (result && result.status === "error") {
      stateLabel = "查询失败";
      stateTone = "danger";
    }
    actions.appendChild(element("span", "status-chip " + stateTone, stateLabel));
    const refresh = element("button", "mini-button secondary", result ? "刷新" : "查询额度");
    refresh.type = "button";
    refresh.disabled = busy || state.quotaRefreshingAll;
    refresh.setAttribute("aria-label", (result ? "刷新" : "查询") + credentialDisplayName(item) + "的凭证额度");
    refresh.addEventListener("click", function () { queryQuotaCredential(item); });
    const authIndex = quotaAuthIndex(item);
    const reset = element("button", "mini-button secondary quota-reset-button", "重置");
    reset.type = "button";
    reset.disabled = busy || state.quotaRefreshingAll || !authIndex;
    reset.title = authIndex
      ? "清除该凭证在代理服务中的本地限流与冷却状态"
      : "当前凭证缺少 auth_index，无法重置";
    reset.setAttribute("aria-label", "重置" + credentialDisplayName(item) + "的本地限流状态");
    reset.addEventListener("click", function () { resetQuotaCredential(item); });
    if (result) {
      actions.appendChild(reset);
    }
    actions.appendChild(refresh);
    card.appendChild(header);

    const content = element("div", "quota-state");
    if (busy) {
      content.appendChild(element("div", "quota-card-message", "正在通过当前凭据向服务商查询额度，请稍候…"));
    } else if (!result) {
      const idleMessage = credentialState(item) === "issue"
        ? "此凭据当前需要处理；仍可尝试查询，若失败请重新登录。"
        : "额度不会自动请求。点击“查询额度”后才会连接对应服务商。";
      content.appendChild(element("div", "quota-card-message", idleMessage));
    } else if (result.status === "error") {
      content.appendChild(element("div", "quota-card-message error", result.message));
    } else {
      const meterList = element("div", "quota-meter-list");
      (result.meters || []).forEach(function (meter) { meterList.appendChild(createQuotaMeter(meter)); });
      if (meterList.childElementCount) {
        content.appendChild(meterList);
      } else {
        content.appendChild(element("div", "quota-card-message", "服务商已响应，但没有返回可展示的额度窗口。"));
      }
      const resetNote = (result.notes || []).find(function (note) {
        return note && typeof note === "object" && note.type === "rate-limit-reset-credit";
      });
      if (resetNote) {
        content.appendChild(createQuotaResetSection(resetNote));
      }
      (result.notes || []).forEach(function (note) {
        if (note && typeof note === "object" && note.type === "rate-limit-reset-credit") {
          return;
        }
        content.appendChild(element("div", "quota-card-message soft", note));
      });
    }
    card.appendChild(content);

    const footer = element("footer", "quota-card-footer");
    footer.appendChild(element("span", "status-chip soft", result && result.plan ? result.plan : QUOTA_PROVIDER_LABELS[provider]));
    footer.appendChild(element("span", "", result && result.updatedAt ? "更新于 " + formatDateTime(result.updatedAt) : "尚未查询"));
    footer.appendChild(actions);
    card.appendChild(footer);
    refreshQuotaCountdowns(card);
    return card;
  }

  function createQuotaResetSection(note) {
    const wrapper = element("section", "quota-reset-section");
    const heading = element("div", "quota-reset-heading");
    heading.appendChild(element("strong", "", "主动重置次数"));
    heading.appendChild(element("span", "quota-reset-count", formatInteger(note.credits) + " 次"));
    wrapper.appendChild(heading);
    wrapper.appendChild(element("p", "quota-reset-caption", "主动重置过期时间（GMT+8）"));

    const row = element("div", "quota-reset-row");
    row.appendChild(element("span", "quota-reset-index", note.credits > 0 ? "第 1 次" : "暂无可用次数"));
    const expiryAt = quotaTimestamp(note.expiresAt);
    row.appendChild(element("span", "quota-reset-expiry", note.expiryText || "到期时间未返回"));
    if (expiryAt) {
      const countdown = element("span", "quota-inline-countdown");
      countdown.dataset.quotaCountdownAt = String(expiryAt);
      countdown.dataset.quotaCountdownFormat = "relative";
      row.appendChild(countdown);
    }
    wrapper.appendChild(row);
    return wrapper;
  }

  function createQuotaResetCreditNote(note) {
    const wrapper = element("div", "quota-card-message soft quota-reset-credit-note");
    wrapper.style.display = "flex";
    wrapper.style.flexWrap = "wrap";
    wrapper.style.alignItems = "center";
    wrapper.style.justifyContent = "center";
    wrapper.appendChild(element("span", "quota-reset-credit-label", "可用速率限制重置额度：" + formatInteger(note.credits) + " 次"));
    wrapper.appendChild(element("span", "quota-reset-credit-separator", "·"));
    wrapper.appendChild(element("span", "quota-reset-credit-expiry", note.expiryText || "到期时间未返回"));
    const expiresAt = quotaTimestamp(note.expiresAt);
    if (expiresAt) {
      const countdown = element("span", "quota-reset-credit-countdown");
      countdown.style.flex = "0 0 100%";
      countdown.style.maxWidth = "100%";
      countdown.style.textAlign = "center";
      countdown.dataset.quotaCountdownAt = String(expiresAt);
      wrapper.appendChild(countdown);
    }
    refreshQuotaCountdowns(wrapper);
    return wrapper;
  }

  function refreshQuotaCountdowns(root) {
    all("[data-quota-countdown-at]", root || document).forEach(function (node) {
      const expiresAt = Number(node.dataset.quotaCountdownAt);
      if (!Number.isFinite(expiresAt) || expiresAt <= 0) {
        node.textContent = "";
        return;
      }
      const remaining = expiresAt - Date.now();
      node.classList.remove("is-warning", "is-danger", "is-expired");
      if (remaining <= 0) {
        node.textContent = "已到期";
        node.classList.add("is-expired");
        return;
      }
      node.textContent = node.dataset.quotaCountdownFormat === "relative"
        ? formatQuotaRelative(remaining)
        : "倒计时 " + formatQuotaCountdown(remaining);
      if (remaining <= 60 * 60 * 1000) {
        node.classList.add("is-danger");
      } else if (remaining <= 24 * 60 * 60 * 1000) {
        node.classList.add("is-warning");
      }
    });
  }

  function createQuotaMeter(meter) {
    const value = quotaPercent(meter.remainingPercent);
    const meterTone = quotaTone(value);
    const wrapper = element("div", "quota-meter tone-" + meterTone);
    wrapper.dataset.tone = meterTone;
    const head = element("div", "quota-meter-head");
    head.appendChild(element("strong", "", meter.label || "额度"));
    head.appendChild(element("span", "", value === null ? "额度已返回" : "剩余 " + formatQuotaPercent(value)));
    wrapper.appendChild(head);

    const progress = element("div", "quota-progress");
    progress.setAttribute("role", "progressbar");
    progress.setAttribute("aria-label", meter.label || "剩余额度");
    if (value !== null) {
      progress.setAttribute("aria-valuemin", "0");
      progress.setAttribute("aria-valuemax", "100");
      progress.setAttribute("aria-valuenow", String(value));
    }
    const fill = element("span", "quota-progress-fill " + quotaScaleClass("quota-width", value));
    progress.appendChild(fill);
    wrapper.appendChild(progress);

    const foot = element("div", "quota-meter-foot");
    foot.appendChild(element("span", "", meter.detail || ""));
    const resetAt = quotaTimestamp(meter.resetAt);
    const reset = element("span", "quota-meter-reset", quotaResetText(meter.resetAt));
    if (resetAt) {
      const countdown = element("span", "quota-inline-countdown");
      countdown.dataset.quotaCountdownAt = String(resetAt);
      countdown.dataset.quotaCountdownFormat = "relative";
      reset.appendChild(document.createTextNode(" · "));
      reset.appendChild(countdown);
    }
    foot.appendChild(reset);
    wrapper.appendChild(foot);
    return wrapper;
  }

  function renderQuotaWindows(credentials) {
    if (!dom.quotaWindowPanel || !dom.quotaWindowDays || !dom.quotaWindowRows) {
      return;
    }
    const now = Date.now();
    const day = 24 * 60 * 60 * 1000;
    const start = quotaWindowDayStart(now + state.quotaWindowOffset * 14 * day);
    const end = start + 14 * day;
    const rows = [];

    all("[data-quota-window-mode]").forEach(function (button) {
      const active = (button.dataset.quotaWindowMode || "week") === state.quotaWindowMode;
      button.classList.toggle("active", active);
      button.setAttribute("aria-pressed", active ? "true" : "false");
    });

    dom.quotaWindowDays.replaceChildren();
    for (let dayIndex = 0; dayIndex < 14; dayIndex += 1) {
      const date = new Date(start + dayIndex * day);
      const dayCell = element("span", "quota-window-day");
      dayCell.appendChild(element("small", "quota-window-weekday", quotaWindowWeekday(date)));
      dayCell.appendChild(element("strong", "", quotaWindowDayLabel(date)));
      dom.quotaWindowDays.appendChild(dayCell);
    }

    credentials.forEach(function (item) {
      const result = state.quotaResults[quotaCredentialKey(item)];
      if (!result || result.status !== "success") {
        return;
      }
      const meters = (result.meters || []).map(function (meter) {
        return Object.assign({}, meter, { resetMs: quotaTimestamp(meter.resetAt) });
      }).filter(function (meter) {
        return meter.resetMs > now;
      });
      if (!meters.length) {
        return;
      }
      const resetNote = (result.notes || []).find(function (note) {
        return note && typeof note === "object" && note.type === "rate-limit-reset-credit";
      });
      rows.push({
        item: item,
        meters: meters.slice(0, 3),
        resetExpiryMs: resetNote ? quotaTimestamp(resetNote.expiresAt) : null
      });
    });

    dom.quotaWindowRows.replaceChildren();
    rows.forEach(function (row) {
      dom.quotaWindowRows.appendChild(createQuotaWindowRow(row, start, end, now));
    });
    dom.quotaWindowPanel.hidden = rows.length === 0;
    if (rows.length && dom.quotaWindowRange) {
      const rangeLabel = quotaWindowDayLabel(new Date(start)) + " – " + quotaWindowDayLabel(new Date(end - day));
      dom.quotaWindowRange.textContent = rangeLabel + (state.quotaWindowOffset === 0 ? " · 当前" : " · 参考窗口");
    }
  }

  function quotaWindowDayStart(timestamp) {
    const date = new Date(timestamp);
    date.setHours(0, 0, 0, 0);
    return date.getTime();
  }

  function quotaWindowDayLabel(date) {
    return String(date.getMonth() + 1).padStart(2, "0") + "/" + String(date.getDate()).padStart(2, "0");
  }

  function quotaWindowWeekday(date) {
    return ["日", "一", "二", "三", "四", "五", "六"][date.getDay()];
  }

  function createQuotaWindowRow(entry, start, end, now) {
    const row = element("article", "quota-window-row");
    const identity = element("div", "quota-window-identity");
    identity.appendChild(element("strong", "", entry.item.name || credentialDisplayName(entry.item)));
    const details = element("div", "quota-window-meter-tags");
    entry.meters.forEach(function (meter) {
      const value = quotaPercent(meter.remainingPercent);
      const tag = element("span", "quota-window-meter-tag tone-" + quotaTone(value));
      tag.textContent = (meter.label || "额度") + (value === null ? "" : " " + formatQuotaPercent(value));
      details.appendChild(tag);
    });
    identity.appendChild(details);
    row.appendChild(identity);

    const rail = element("div", "quota-window-rail quota-window-lines-" + Math.max(1, Math.min(3, entry.meters.length)));
    rail.appendChild(element("span", "quota-window-grid"));
    const currentPosition = quotaWindowPosition(now, start, end);
    if (Number.isFinite(entry.resetExpiryMs) && entry.resetExpiryMs > 0) {
      const resetPosition = quotaWindowPosition(entry.resetExpiryMs, start, end);
      const resetLine = element("span", "quota-window-reset " + quotaScaleClass("quota-position", resetPosition));
      resetLine.title = "主动重置过期 · " + formatDateTime(entry.resetExpiryMs);
      rail.appendChild(resetLine);
    } else {
      const current = element("span", "quota-window-now " + quotaScaleClass("quota-position", currentPosition));
      current.title = "当前查询时刻";
      rail.appendChild(current);
    }

    entry.meters.forEach(function (meter, index) {
      const resetPosition = quotaWindowPosition(meter.resetMs, start, end);
      const bar = element("span", "quota-window-bar " + quotaWindowTone(meter) + " quota-window-line-" + Math.min(index, 2)
        + " " + quotaScaleClass("quota-position", currentPosition)
        + " " + quotaScaleClass("quota-width", Math.max(1, resetPosition - currentPosition)));
      const value = quotaPercent(meter.remainingPercent);
      bar.title = (meter.label || "额度") + (value === null ? "" : " · 剩余 " + formatQuotaPercent(value)) + " · " + quotaResetText(meter.resetAt);
      rail.appendChild(bar);
    });
    row.appendChild(rail);
    return row;
  }

  function quotaWindowPosition(value, start, end) {
    if (!Number.isFinite(value) || end <= start) {
      return 0;
    }
    return Math.max(0, Math.min(100, (value - start) / (end - start) * 100));
  }

  function quotaWindowTone(meter) {
    return quotaTone(quotaPercent(meter.remainingPercent));
  }

  function quotaPercent(value) {
    if (value === null || value === undefined || value === "") {
      return null;
    }
    const parsed = Number(value);
    if (!Number.isFinite(parsed)) {
      return null;
    }
    return Math.max(0, Math.min(100, parsed));
  }

  function formatQuotaPercent(value) {
    return number(value).toLocaleString("zh-CN", { maximumFractionDigits: 1 }) + "%";
  }

  function quotaTimestamp(value) {
    if (value === null || value === undefined || value === "") {
      return 0;
    }
    if (typeof value === "number" || /^\d+(\.\d+)?$/.test(String(value).trim())) {
      const numeric = Number(value);
      return Number.isFinite(numeric) ? (numeric < 100000000000 ? numeric * 1000 : numeric) : 0;
    }
    const parsed = Date.parse(String(value).replace(/(\.\d{6})\d+/, "$1"));
    return Number.isFinite(parsed) ? parsed : 0;
  }

  function quotaResetText(value) {
    const resetAt = quotaTimestamp(value);
    if (!resetAt) {
      return "重置时间未返回";
    }
    return (resetAt <= Date.now() ? "预计已重置 · " : "重置于 ") + formatDateTime(resetAt);
  }

  function quotaResetCreditIsAvailable(credit) {
    const status = String(quotaValue(credit, ["status", "state"]) || "available").trim().toLocaleLowerCase();
    return status === "available" || status === "active";
  }

  function quotaResetCreditExpiryText(creditPayload, usageCreditValue) {
    const payload = quotaObject(creditPayload);
    const details = Array.isArray(payload.credits) ? payload.credits.filter(quotaResetCreditIsAvailable) : [];
    const expirations = [];
    let hasNonExpiring = false;
    let hasUnknownExpiry = false;
    details.forEach(function (credit) {
      const rawExpiry = quotaValue(credit, [
        "expires_at", "expiresAt", "expiry", "expiration_at", "expirationAt"
      ]);
      if (rawExpiry === undefined || rawExpiry === null || rawExpiry === "") {
        hasNonExpiring = true;
        return;
      }
      const expiry = quotaTimestamp(rawExpiry);
      if (expiry) {
        expirations.push(expiry);
      } else {
        hasUnknownExpiry = true;
      }
    });
    if (expirations.length) {
      expirations.sort(function (left, right) { return left - right; });
      const text = (expirations.length > 1 ? "最近到期于 " : "到期于 ") + formatDateTime(expirations[0]);
      return hasNonExpiring ? text + " · 另有永久额度" : text;
    }
    if (hasNonExpiring && !hasUnknownExpiry) {
      return "无固定到期时间";
    }

    const sources = [payload, quotaObject(usageCreditValue)];
    const expiryNames = ["expires_at", "expiresAt", "expiry", "expiration_at", "expirationAt", "reset_at", "resetAt"];
    const relativeExpiryNames = [
      "expires_in_seconds", "expiresInSeconds", "expires_after_seconds", "expiresAfterSeconds",
      "reset_after_seconds", "resetAfterSeconds", "ttl_seconds", "ttlSeconds"
    ];
    for (const source of sources) {
      const absolute = quotaTimestamp(quotaValue(source, expiryNames));
      if (absolute) {
        return "到期于 " + formatDateTime(absolute);
      }
      const relative = quotaNumber(source, relativeExpiryNames);
      if (relative !== null) {
        return "到期于 " + formatDateTime(Date.now() + relative * 1000);
      }
    }
    return "到期时间未返回";
  }

  function quotaResetCreditExpiryTimestamp(creditPayload, usageCreditValue) {
    const payload = quotaObject(creditPayload);
    const details = Array.isArray(payload.credits) ? payload.credits.filter(quotaResetCreditIsAvailable) : [];
    const expirations = [];
    details.forEach(function (credit) {
      const rawExpiry = quotaValue(credit, [
        "expires_at", "expiresAt", "expiry", "expiration_at", "expirationAt"
      ]);
      const expiry = quotaTimestamp(rawExpiry);
      if (expiry) {
        expirations.push(expiry);
      }
    });
    if (expirations.length) {
      return Math.min.apply(null, expirations);
    }

    const sources = [payload, quotaObject(usageCreditValue)];
    const expiryNames = ["expires_at", "expiresAt", "expiry", "expiration_at", "expirationAt", "reset_at", "resetAt"];
    const relativeExpiryNames = [
      "expires_in_seconds", "expiresInSeconds", "expires_after_seconds", "expiresAfterSeconds",
      "reset_after_seconds", "resetAfterSeconds", "ttl_seconds", "ttlSeconds"
    ];
    for (const source of sources) {
      const absolute = quotaTimestamp(quotaValue(source, expiryNames));
      if (absolute) {
        return absolute;
      }
      const relative = quotaNumber(source, relativeExpiryNames);
      if (relative !== null) {
        return Date.now() + relative * 1000;
      }
    }
    return 0;
  }

  function quotaValue(object, names) {
    if (!object || typeof object !== "object") {
      return undefined;
    }
    for (const name of names) {
      if (Object.prototype.hasOwnProperty.call(object, name) && object[name] !== null && object[name] !== undefined) {
        return object[name];
      }
    }
    return undefined;
  }

  function quotaNumber(object, names) {
    const value = quotaValue(object, names);
    if (value === null || value === undefined || value === "") {
      return null;
    }
    const parsed = Number(value);
    return Number.isFinite(parsed) ? parsed : null;
  }

  function quotaWrappedNumber(object, names) {
    const value = quotaValue(object, names);
    if (value && typeof value === "object" && !Array.isArray(value)) {
      return quotaNumber(value, ["val", "value", "amount"]);
    }
    if (value === null || value === undefined || value === "") {
      return null;
    }
    const parsed = Number(value);
    return Number.isFinite(parsed) ? parsed : null;
  }

  function quotaObject(value) {
    return value && typeof value === "object" && !Array.isArray(value) ? value : {};
  }

  function quotaBody(payload) {
    const value = payload && Object.prototype.hasOwnProperty.call(payload, "body") ? payload.body : payload;
    if (value && typeof value === "object") {
      return value;
    }
    const source = String(value || "").trim();
    if (!source) {
      return {};
    }
    try {
      return JSON.parse(source);
    } catch (_error) {
      const error = new Error("服务商返回了无法解析的数据。");
      error.code = "invalid-payload";
      throw error;
    }
  }

  function quotaUpstreamError(status) {
    const error = new Error("服务商请求失败（" + status + "）");
    error.status = status;
    error.quotaUpstream = true;
    return error;
  }

  async function quotaAPICall(item, method, url, header, data) {
    const authIndex = quotaAuthIndex(item);
    if (!authIndex) {
      const missing = new Error("当前凭据缺少 auth_index，无法安全查询额度。");
      missing.code = "missing-auth-index";
      throw missing;
    }
    const request = { auth_index: authIndex, method: method, url: url, header: header || {} };
    if (data !== undefined) {
      request.data = data;
    }
    const payload = await apiRequest(API.apiCall, { method: "POST", body: JSON.stringify(request) });
    const status = number(payload && (payload.status_code || payload.statusCode || payload.StatusCode));
    if (status < 200 || status >= 300) {
      throw quotaUpstreamError(status || 502);
    }
    return quotaBody(payload);
  }

  function quotaErrorMessage(error) {
    if (error && error.code === "missing-auth-index") {
      return error.message;
    }
    if (error && error.code === "missing-project-id") {
      return "当前 Antigravity 凭据缺少项目 ID，请重新登录后再查询。";
    }
    if (error && error.code === "invalid-payload") {
      return error.message;
    }
    if (error && (error.status === 401 || error.status === 403)) {
      return "凭据已过期或无权查询额度，请重新登录该服务商。";
    }
    if (error && error.status === 404) {
      return "当前服务版本或凭据不支持此额度接口，请更新服务后重试。";
    }
    if (error && error.status === 429) {
      return "服务商限制了查询频率，请稍后再试。";
    }
    if (error && error.status >= 500) {
      return "服务商额度接口暂时不可用，请稍后再试。";
    }
    return "额度查询失败，请检查网络与凭据状态后重试。";
  }

  async function queryQuotaCredential(item) {
    const key = quotaCredentialKey(item);
    if (state.quotaBusy[key]) {
      return state.quotaResults[key] || null;
    }
    const epoch = state.quotaEpoch;
    state.quotaBusy[key] = true;
    renderQuotaCredentials();
    try {
      const result = await fetchCredentialQuota(item);
      if (epoch !== state.quotaEpoch) {
        return null;
      }
      state.quotaResults[key] = Object.assign({}, result, { status: "success", updatedAt: new Date() });
      return state.quotaResults[key];
    } catch (error) {
      if (epoch !== state.quotaEpoch) {
        return null;
      }
      if (!error.quotaUpstream && (error.status === 401 || error.status === 403)) {
        signOut(apiErrorMessage(error));
        return null;
      }
      state.quotaResults[key] = { status: "error", message: quotaErrorMessage(error), meters: [], notes: [], updatedAt: new Date() };
      return state.quotaResults[key];
    } finally {
      if (epoch === state.quotaEpoch) {
        delete state.quotaBusy[key];
        renderQuotaCredentials();
      }
    }
  }

  async function resetQuotaCredential(item) {
    const authIndex = quotaAuthIndex(item);
    const key = quotaCredentialKey(item);
    if (!authIndex || state.quotaBusy[key] || state.quotaRefreshingAll) {
      return;
    }
    const name = credentialDisplayName(item);
    const confirmed = await confirmAction(
      "重置“" + name + "”的限流状态？",
      "这会清除该凭证在代理服务中的本地限流、冷却与重试状态，不会删除凭证，也不会修改服务商账户的真实额度。",
      "确认重置"
    );
    if (!confirmed) {
      return;
    }

    state.quotaBusy[key] = true;
    renderQuotaCredentials();
    try {
      await apiRequest(API.resetQuota, {
        method: "POST",
        body: JSON.stringify({ auth_index: authIndex })
      });
      delete state.quotaResults[key];
      toast("已重置“" + name + "”的本地限流状态，可点击刷新重新查询额度");
    } catch (error) {
      if (error.status === 401 || error.status === 403) {
        signOut(apiErrorMessage(error));
        return;
      }
      toast(apiErrorMessage(error), true);
    } finally {
      delete state.quotaBusy[key];
      renderQuotaCredentials();
    }
  }

  async function refreshAllQuotas() {
    if (state.quotaRefreshingAll) {
      return;
    }
    if (!state.loadedViews.quota && !state.loadedViews.credentials) {
      await loadQuotaCredentials(false);
    }
    const credentials = quotaCredentials().filter(quotaCredentialMatches);
    if (!credentials.length) {
      toast("当前筛选没有可查询额度的 OAuth 凭据", true);
      renderQuotaCredentials();
      return;
    }
    const epoch = state.quotaEpoch;
    state.quotaRefreshingAll = true;
    renderQuotaCredentials();
    let cursor = 0;
    const worker = async function () {
      while (cursor < credentials.length && epoch === state.quotaEpoch) {
        const item = credentials[cursor];
        cursor += 1;
        await queryQuotaCredential(item);
      }
    };
    try {
      const workers = [];
      for (let index = 0; index < Math.min(2, credentials.length); index += 1) {
        workers.push(worker());
      }
      await Promise.all(workers);
      if (epoch === state.quotaEpoch) {
        const failed = credentials.filter(function (item) {
          const result = state.quotaResults[quotaCredentialKey(item)];
          return !result || result.status !== "success";
        }).length;
        toast(failed ? "额度查询完成，" + failed + " 个凭据需要处理" : "全部凭据额度已更新", Boolean(failed));
      }
    } finally {
      if (epoch === state.quotaEpoch) {
        state.quotaRefreshingAll = false;
        renderQuotaCredentials();
      }
    }
  }

  function fetchCredentialQuota(item) {
    const provider = quotaProvider(item);
    if (provider === "antigravity") {
      return fetchAntigravityQuota(item);
    }
    if (provider === "claude") {
      return fetchClaudeQuota(item);
    }
    if (provider === "codex") {
      return fetchCodexQuota(item);
    }
    if (provider === "kimi") {
      return fetchKimiQuota(item);
    }
    if (provider === "xai") {
      return fetchXAIQuota(item);
    }
    const error = new Error("不支持的服务商");
    error.status = 404;
    error.quotaUpstream = true;
    throw error;
  }

  async function fetchAntigravityQuota(item) {
    const projectID = String(item.project_id || item.projectId || "").trim();
    if (!projectID) {
      const missing = new Error("缺少项目 ID");
      missing.code = "missing-project-id";
      throw missing;
    }
    const endpoints = [
      "https://daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary",
      "https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:retrieveUserQuotaSummary",
      "https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary"
    ];
    const headers = {
      Authorization: "Bearer $TOKEN$",
      "Content-Type": "application/json",
      "User-Agent": "antigravity/cli/1.0.13 (aidev_client; os_type=darwin; arch=arm64)"
    };
    let payload = null;
    let lastError = null;
    for (const endpoint of endpoints) {
      try {
        payload = await quotaAPICall(item, "POST", endpoint, headers, JSON.stringify({ project: projectID }));
        break;
      } catch (error) {
        lastError = error;
        if (error.status === 401 || error.status === 403 || error.status === 429) {
          throw error;
        }
      }
    }
    if (!payload) {
      throw lastError || quotaUpstreamError(502);
    }
    const source = quotaObject(payload.data || payload);
    const groups = Array.isArray(source.groups) ? source.groups : Array.isArray(source.quotaGroups) ? source.quotaGroups : [];
    const meters = [];
    groups.forEach(function (group, groupIndex) {
      const buckets = Array.isArray(group.buckets) ? group.buckets : [];
      buckets.forEach(function (bucket, bucketIndex) {
        const fraction = quotaNumber(bucket, ["remainingFraction", "remaining_fraction"]);
        const remaining = fraction === null ? quotaNumber(bucket, ["remainingPercent", "remaining_percent"]) : fraction <= 1 ? fraction * 100 : fraction;
        const groupName = String(group.displayName || group.name || "额度组 " + (groupIndex + 1));
        const bucketName = String(bucket.displayName || bucket.name || bucket.bucketId || "窗口 " + (bucketIndex + 1));
        meters.push({
          label: groupName === bucketName ? groupName : groupName + " · " + bucketName,
          remainingPercent: remaining,
          resetAt: quotaValue(bucket, ["resetTime", "reset_time", "resetAt", "reset_at"]),
          detail: String(bucket.description || bucket.window || group.description || "")
        });
      });
    });
    if (!meters.length && Array.isArray(source.buckets)) {
      source.buckets.forEach(function (bucket, index) {
        const fraction = quotaNumber(bucket, ["remainingFraction", "remaining_fraction"]);
        meters.push({
          label: String(bucket.displayName || bucket.name || "额度窗口 " + (index + 1)),
          remainingPercent: fraction === null ? quotaNumber(bucket, ["remainingPercent", "remaining_percent"]) : fraction <= 1 ? fraction * 100 : fraction,
          resetAt: quotaValue(bucket, ["resetTime", "reset_time", "resetAt", "reset_at"]),
          detail: String(bucket.description || bucket.window || "")
        });
      });
    }
    let plan = quotaValue(source, ["planType", "plan_type", "plan", "tier"]);
    if (!plan) {
      try {
        const subscription = await quotaAPICall(item, "POST", "https://daily-cloudcode-pa.googleapis.com/v1internal:loadCodeAssist", headers, JSON.stringify({ metadata: { ideType: "ANTIGRAVITY" } }));
        const subscriptionSource = quotaObject(subscription.data || subscription);
        plan = quotaValue(subscriptionSource, ["planType", "plan_type", "plan", "tier", "subscriptionTier", "subscription_tier"]);
        if (!plan) {
          const currentTier = quotaObject(subscriptionSource.currentTier || subscriptionSource.current_tier);
          plan = quotaValue(currentTier, ["displayName", "display_name", "name", "id"]);
        }
      } catch (_error) {
        if (!_error.quotaUpstream && (_error.status === 401 || _error.status === 403)) {
          throw _error;
        }
        plan = null;
      }
    }
    return {
      plan: quotaPlanLabel(plan, "Antigravity"),
      meters: meters,
      notes: []
    };
  }

  async function fetchClaudeQuota(item) {
    const headers = {
      Authorization: "Bearer $TOKEN$",
      "Content-Type": "application/json",
      "anthropic-beta": "oauth-2025-04-20"
    };
    const results = await Promise.allSettled([
      quotaAPICall(item, "GET", "https://api.anthropic.com/api/oauth/usage", headers),
      quotaAPICall(item, "GET", "https://api.anthropic.com/api/oauth/profile", headers)
    ]);
    if (results[0].status !== "fulfilled") {
      throw results[0].reason;
    }
    throwOptionalManagementAuthError(results[1]);
    const usage = quotaObject(results[0].value.usage || results[0].value);
    const definitions = [
      ["five_hour", "5 小时用量"], ["seven_day", "7 天用量"], ["seven_day_oauth_apps", "OAuth 应用 7 天用量"],
      ["seven_day_opus", "Opus 7 天用量"], ["seven_day_sonnet", "Sonnet 7 天用量"],
      ["seven_day_cowork", "Cowork 7 天用量"], ["iguana_necktie", "额外 7 天用量"]
    ];
    const meters = [];
    definitions.forEach(function (definition) {
      const windowData = quotaObject(usage[definition[0]]);
      if (!Object.keys(windowData).length) {
        return;
      }
      const used = quotaNumber(windowData, ["utilization", "used_percent", "usedPercent"]);
      meters.push({
        label: definition[1],
        remainingPercent: used === null ? null : 100 - used,
        resetAt: quotaValue(windowData, ["resets_at", "resetsAt", "reset_at", "resetAt"]),
        detail: used === null ? "" : "已用 " + formatQuotaPercent(used)
      });
    });
    const profile = results[1].status === "fulfilled" ? quotaObject(results[1].value) : {};
    const account = quotaObject(profile.account || profile);
    let plan = quotaValue(account, ["plan", "plan_type", "planType", "subscription_type"]);
    if (!plan && account.has_claude_max) {
      plan = "Max";
    } else if (!plan && account.has_claude_pro) {
      plan = "Pro";
    } else if (!plan && (account.organization || account.team)) {
      plan = "Team";
    }
    return { plan: quotaPlanLabel(plan, "Claude"), meters: meters, notes: [] };
  }

  async function fetchCodexQuota(item) {
    const headers = {
      Authorization: "Bearer $TOKEN$",
      "Content-Type": "application/json",
      "User-Agent": "codex_cli_rs/0.76.0 (Debian 13.0.0; x86_64) WindowsTerminal"
    };
    const tokenClaims = quotaObject(item.id_token);
    const accountID = String(item.chatgpt_account_id || item.account_id || tokenClaims.chatgpt_account_id || tokenClaims.chatgptAccountId || "").trim();
    if (accountID) {
      headers["Chatgpt-Account-Id"] = accountID;
    }
    const results = await Promise.allSettled([
      quotaAPICall(item, "GET", "https://chatgpt.com/backend-api/wham/usage", headers),
      quotaAPICall(item, "GET", "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits", Object.assign({}, headers, {
        Accept: "application/json", "OpenAI-Beta": "codex-1", Originator: "Codex Desktop"
      }))
    ]);
    if (results[0].status !== "fulfilled") {
      throw results[0].reason;
    }
    throwOptionalManagementAuthError(results[1]);
    const usage = quotaObject(results[0].value);
    const meters = [];
    appendCodexRateLimit(meters, usage.rate_limit || usage.rateLimit, "Codex", quotaValue(usage, ["plan_type", "planType"]));
    appendCodexRateLimit(meters, usage.code_review_rate_limit || usage.codeReviewRateLimit, "代码审查", quotaValue(usage, ["plan_type", "planType"]));
    let additional = usage.additional_rate_limits || usage.additionalRateLimits || [];
    if (!Array.isArray(additional) && additional && typeof additional === "object") {
      additional = Object.keys(additional).map(function (name) {
        return Object.assign({ name: name }, quotaObject(additional[name]));
      });
    }
    additional.forEach(function (entry, index) {
      const label = String(entry.limit_name || entry.limitName || entry.name || "额外额度 " + (index + 1));
      appendCodexRateLimit(meters, entry.rate_limit || entry.rateLimit || entry, label, quotaValue(usage, ["plan_type", "planType"]));
    });
    const notes = [];
    const creditPayload = results[1].status === "fulfilled" ? quotaObject(results[1].value) : {};
    const usageCreditValue = quotaValue(usage, ["rate_limit_reset_credits", "rateLimitResetCredits"]);
    const creditExpiryAt = quotaResetCreditExpiryTimestamp(creditPayload, usageCreditValue);
    let credits = quotaNumber(creditPayload, ["applicable_available_count", "applicableAvailableCount", "available_count", "availableCount", "remaining", "count"]);
    if (credits === null && Array.isArray(creditPayload.credits)) {
      credits = creditPayload.credits.filter(quotaResetCreditIsAvailable).length;
    }
    if (credits === null) {
      if (usageCreditValue && typeof usageCreditValue === "object") {
        credits = quotaNumber(usageCreditValue, ["applicable_available_count", "applicableAvailableCount", "available_count", "availableCount", "remaining", "count"]);
      } else if (usageCreditValue !== undefined && Number.isFinite(Number(usageCreditValue))) {
        credits = Number(usageCreditValue);
      }
    }
    if (credits !== null) {
      notes.push({
        type: "rate-limit-reset-credit",
        credits: credits,
        expiresAt: creditExpiryAt,
        expiryText: quotaResetCreditExpiryText(creditPayload, usageCreditValue)
      });
    }
    const plan = quotaValue(usage, ["plan_type", "planType"]) || tokenClaims.plan_type || tokenClaims.planType;
    return { plan: quotaPlanLabel(plan, "Codex"), meters: meters, notes: notes };
  }

  function appendCodexRateLimit(meters, value, prefix, plan) {
    const rateLimit = quotaObject(value);
    if (!Object.keys(rateLimit).length) {
      return;
    }
    const primaryWindow = rateLimit.primary_window || rateLimit.primaryWindow;
    const secondaryWindow = rateLimit.secondary_window || rateLimit.secondaryWindow;
    const isTeamPlan = String(plan || "").toLocaleLowerCase().includes("team");
    const windows = [
      // The provider may change which window is primary. Prefer its duration
      // instead of assuming that every primary window is five hours.
      [primaryWindow, codexRateWindowLabel(primaryWindow, "周限额")],
      [secondaryWindow, codexRateWindowLabel(secondaryWindow, isTeamPlan ? "月限额" : "周限额")]
    ];
    windows.forEach(function (definition) {
      const windowData = quotaObject(definition[0]);
      if (!Object.keys(windowData).length) {
        return;
      }
      const used = quotaNumber(windowData, ["used_percent", "usedPercent", "utilization"]);
      const resetAt = quotaValue(windowData, ["reset_at", "resetAt"]);
      const resetAfter = quotaNumber(windowData, ["reset_after_seconds", "resetAfterSeconds"]);
      meters.push({
        label: prefix + " · " + definition[1],
        remainingPercent: used === null ? null : 100 - used,
        resetAt: resetAt || (resetAfter === null ? null : Date.now() + resetAfter * 1000),
        detail: used === null ? "" : "已用 " + formatQuotaPercent(used)
      });
    });
  }

  function codexRateWindowLabel(windowData, fallback) {
    const source = quotaObject(windowData);
    const seconds = quotaNumber(source, [
      "limit_window_seconds", "limitWindowSeconds", "window_seconds", "windowSeconds",
      "duration_seconds", "durationSeconds"
    ]);
    if (seconds !== null && seconds > 0) {
      const hour = 60 * 60;
      const day = 24 * hour;
      if (seconds >= 6 * day && seconds <= 8 * day) {
        return "周限额";
      }
      if (seconds >= 25 * day && seconds <= 32 * day) {
        return "月限额";
      }
      if (seconds >= 4 * hour && seconds <= 6 * hour) {
        return "5 小时";
      }
      return formatQuotaWindow(seconds, "seconds");
    }
    return fallback;
  }

  async function fetchKimiQuota(item) {
    const payload = await quotaAPICall(item, "GET", "https://api.kimi.com/coding/v1/usages", { Authorization: "Bearer $TOKEN$" });
    const source = quotaObject(payload.data || payload);
    let limits = source.limits || source.usages || source.usage || [];
    if (!Array.isArray(limits) && limits && typeof limits === "object") {
      limits = Object.keys(limits).map(function (name) {
        return Object.assign({ name: name }, quotaObject(limits[name]));
      });
    }
    const meters = [];
    (Array.isArray(limits) ? limits : []).forEach(function (limit, index) {
      const rawLimit = quotaObject(limit);
      const detail = Object.keys(quotaObject(rawLimit.detail)).length ? quotaObject(rawLimit.detail) : rawLimit;
      const windowData = quotaObject(rawLimit.window);
      const total = quotaNumber(detail, ["limit", "total", "quota", "max"]);
      const used = quotaNumber(detail, ["used", "usage", "consumed"]);
      const remaining = quotaNumber(detail, ["remaining", "left", "available"]);
      let remainingPercent = quotaNumber(detail, ["remaining_percent", "remainingPercent"]);
      if (remainingPercent === null && total !== null && total > 0) {
        remainingPercent = remaining !== null ? remaining / total * 100 : used !== null ? 100 - used / total * 100 : null;
      }
      const details = [];
      if (remaining !== null && total !== null) {
        details.push("剩余 " + formatQuotaAmount(remaining) + " / " + formatQuotaAmount(total));
      } else if (used !== null && total !== null) {
        details.push("已用 " + formatQuotaAmount(used) + " / " + formatQuotaAmount(total));
      }
      let resetAt = quotaValue(detail, ["reset_at", "resetAt", "reset_time", "resetTime", "resets_at", "resetsAt", "expires_at", "expiresAt"]);
      const resetIn = quotaNumber(detail, ["reset_in", "resetIn", "ttl", "reset_after_seconds", "resetAfterSeconds"]);
      if (!resetAt && resetIn !== null) {
        resetAt = Date.now() + resetIn * 1000;
      }
      const duration = quotaNumber(windowData, ["duration"]);
      const durationLabel = duration === null ? "" : " · " + formatQuotaWindow(duration, quotaValue(windowData, ["timeUnit", "time_unit"]));
      meters.push({
        label: String(rawLimit.display_name || rawLimit.displayName || rawLimit.name || rawLimit.title || rawLimit.scope || detail.name || detail.title || "额度窗口 " + (index + 1)) + durationLabel,
        remainingPercent: remainingPercent,
        resetAt: resetAt,
        detail: details.join(" · ")
      });
    });
    if (source.usage && typeof source.usage === "object" && !Array.isArray(source.usage)) {
      const summary = quotaObject(source.usage);
      const total = quotaNumber(summary, ["limit", "total", "quota", "max"]);
      const used = quotaNumber(summary, ["used", "usage", "consumed"]);
      const remaining = quotaNumber(summary, ["remaining", "left", "available"]);
      if (total !== null || used !== null) {
        let resetAt = quotaValue(summary, ["reset_at", "resetAt", "reset_time", "resetTime"]);
        const resetIn = quotaNumber(summary, ["reset_in", "resetIn", "ttl", "reset_after_seconds", "resetAfterSeconds"]);
        if (!resetAt && resetIn !== null) {
          resetAt = Date.now() + resetIn * 1000;
        }
        meters.push({
          label: String(summary.name || summary.title || "每周额度"),
          remainingPercent: total !== null && total > 0 ? (remaining !== null ? remaining / total * 100 : 100 - number(used) / total * 100) : null,
          resetAt: resetAt,
          detail: total !== null ? "已用 " + formatQuotaAmount(used) + " / " + formatQuotaAmount(total) : ""
        });
      }
    }
    return {
      plan: quotaPlanLabel(quotaValue(source, ["plan", "plan_type", "planType", "tier"]), "Kimi"),
      meters: meters,
      notes: []
    };
  }

  function formatQuotaWindow(duration, unit) {
    const normalized = String(unit || "minutes").toLocaleLowerCase().replace(/^time_unit_/, "");
    const suffixes = { second: "秒", seconds: "秒", minute: "分钟", minutes: "分钟", hour: "小时", hours: "小时", day: "天", days: "天", week: "周", weeks: "周" };
    return formatQuotaAmount(duration) + " " + (suffixes[normalized] || "分钟");
  }

  async function fetchXAIQuota(item) {
    const headers = {
      Authorization: "Bearer $TOKEN$",
      "x-xai-token-auth": "xai-grok-cli",
      "x-grok-client-version": "0.2.91",
      accept: "*/*",
      "user-agent": "grok-pager/0.2.91 grok-shell/0.2.91 (macos; aarch64)"
    };
    const userID = String(item.user_id || item.userId || item.sub || "").trim();
    if (userID) {
      headers["x-userid"] = userID;
    }
    const results = await Promise.allSettled([
      quotaAPICall(item, "GET", "https://cli-chat-proxy.grok.com/v1/billing?format=credits", headers),
      quotaAPICall(item, "GET", "https://cli-chat-proxy.grok.com/v1/billing", headers)
    ]);
    const sources = results.filter(function (result) { return result.status === "fulfilled"; }).map(function (result) {
      return quotaObject(result.value.config || result.value.data || result.value);
    });
    if (!sources.length) {
      const managementAuthError = results.map(function (result) { return result.reason; }).find(function (error) {
        return error && !error.quotaUpstream && (error.status === 401 || error.status === 403);
      });
      throw managementAuthError || results[0].reason || results[1].reason || quotaUpstreamError(502);
    }
    const source = Object.assign({}, sources.length > 1 ? sources[1] : {}, sources[0]);
    const period = quotaObject(source.currentPeriod || source.current_period);
    const meters = [];
    const monthlyLimit = quotaWrappedNumber(source, ["monthlyLimit", "monthly_limit"]);
    const monthlyUsed = quotaWrappedNumber(source, ["used", "usage", "creditsUsed", "credits_used"]);
    let creditUsage = quotaNumber(period, ["creditUsagePercent", "credit_usage_percent", "usagePercent", "usage_percent"]);
    if (creditUsage === null) {
      creditUsage = quotaNumber(source, ["creditUsagePercent", "credit_usage_percent", "usagePercent", "usage_percent"]);
    }
    const periodType = String(period.type || source.periodType || source.period_type || "").toLocaleLowerCase();
    if (creditUsage !== null) {
      meters.push({
        label: periodType.includes("month") ? "月度积分额度" : "每周积分额度",
        remainingPercent: 100 - creditUsage,
        resetAt: quotaValue(period, ["end", "endAt", "end_at", "periodEnd", "period_end"]),
        detail: "已用 " + formatQuotaPercent(creditUsage)
      });
    }
    if (monthlyLimit !== null && monthlyLimit > 0) {
      meters.push({
        label: "月度计费额度",
        remainingPercent: 100 - number(monthlyUsed) / monthlyLimit * 100,
        resetAt: quotaValue(source, ["billingPeriodEnd", "billing_period_end"]),
        detail: "已用 " + formatQuotaAmount(monthlyUsed) + " / " + formatQuotaAmount(monthlyLimit)
      });
    }
    const onDemandCap = quotaWrappedNumber(source, ["onDemandCap", "on_demand_cap"]);
    const onDemandUsed = quotaWrappedNumber(source, ["onDemandUsed", "on_demand_used"]);
    if (onDemandCap !== null && onDemandCap > 0) {
      meters.push({
        label: "按需额度",
        remainingPercent: 100 - number(onDemandUsed) / onDemandCap * 100,
        resetAt: quotaValue(source, ["billingPeriodEnd", "billing_period_end"]) || quotaValue(period, ["end", "endAt", "end_at", "periodEnd", "period_end"]),
        detail: "已用 " + formatQuotaAmount(onDemandUsed) + " / " + formatQuotaAmount(onDemandCap)
      });
    }
    const productUsage = source.productUsage || source.product_usage || period.productUsage || period.product_usage;
    const products = Array.isArray(productUsage) ? productUsage : productUsage && typeof productUsage === "object"
      ? Object.keys(productUsage).map(function (name) { return Object.assign({ name: name }, quotaObject(productUsage[name])); }) : [];
    products.forEach(function (product, index) {
      const total = quotaNumber(product, ["limit", "total", "monthlyLimit", "monthly_limit"]);
      const used = quotaNumber(product, ["used", "usage", "consumed"]);
      const remaining = quotaNumber(product, ["remaining", "available"]);
      let percent = quotaNumber(product, ["remainingPercent", "remaining_percent"]);
      const usedPercent = quotaNumber(product, ["usagePercent", "usage_percent", "usedPercent", "used_percent"]);
      if (percent === null && usedPercent !== null) {
        percent = 100 - usedPercent;
      }
      if (percent === null && total !== null && total > 0) {
        percent = remaining !== null ? remaining / total * 100 : used !== null ? 100 - used / total * 100 : null;
      }
      meters.push({
        label: String(product.displayName || product.display_name || product.name || "产品额度 " + (index + 1)),
        remainingPercent: percent,
        resetAt: quotaValue(product, ["resetAt", "reset_at", "periodEnd", "period_end"]) || quotaValue(period, ["end", "endAt", "end_at"]),
        detail: used !== null && total !== null ? "已用 " + formatQuotaAmount(used) + " / " + formatQuotaAmount(total) : ""
      });
    });
    return {
      plan: quotaPlanLabel(quotaValue(source, ["plan", "planType", "plan_type", "tier", "subscription"]), "Grok"),
      meters: meters,
      notes: []
    };
  }

  function quotaPlanLabel(value, fallback) {
    if (value && typeof value === "object") {
      value = quotaValue(value, ["displayName", "display_name", "name", "plan", "type", "tier"]);
    }
    const text = String(value || "").trim();
    if (!text) {
      return fallback;
    }
    if (String(fallback || "").toLocaleLowerCase().includes("codex")) {
      const codexPlan = text.toLocaleLowerCase().replace(/[-_\s]+/g, "");
      const codexLabels = {
        free: "Free",
        plus: "Plus",
        team: "Team",
        pro: "Pro 20x",
        prolite: "Pro 5x"
      };
      if (codexLabels[codexPlan]) {
        return codexLabels[codexPlan];
      }
    }
    return text.split(/[-_\s]+/).map(function (part) {
      return part ? part.charAt(0).toLocaleUpperCase() + part.slice(1) : "";
    }).filter(Boolean).join(" ");
  }

  function throwOptionalManagementAuthError(result) {
    if (result && result.status === "rejected" && result.reason && !result.reason.quotaUpstream
      && (result.reason.status === 401 || result.reason.status === 403)) {
      throw result.reason;
    }
  }

  function formatQuotaAmount(value) {
    return number(value).toLocaleString("zh-CN", { maximumFractionDigits: 2 });
  }

  async function toggleCredential(item, disabled) {
    if (disabled) {
      const accepted = await confirmAction("停用此上游凭据？", "新的请求将不再选择此凭据，直到重新启用。", "确认停用");
      if (!accepted) {
        return;
      }
    }
    try {
      await apiRequest(API.authFiles + "/status", { method: "PATCH", body: JSON.stringify({ name: item.id || item.name, disabled: disabled }) });
      toast(disabled ? "凭据已停用" : "凭据已启用");
      await loadCredentials(true);
    } catch (error) {
      handleViewError(error, "凭据状态更新失败");
    }
  }

  async function deleteCredential(item) {
    const accepted = await confirmAction("删除此认证文件？", "该操作会从服务器删除凭据文件，无法在页面内撤销。", "确认删除");
    if (!accepted) {
      return;
    }
    try {
      const name = encodeURIComponent(item.name || item.id || "");
      await apiRequest(API.authFiles + "?name=" + name, { method: "DELETE" });
      toast("认证文件已删除");
      await loadCredentials(true);
    } catch (error) {
      handleViewError(error, "删除认证文件失败");
    }
  }

  async function uploadCredentialFiles() {
    const files = Array.from(dom.authUploadInput.files || []);
    if (!files.length) {
      return;
    }
    const form = new FormData();
    files.forEach(function (file) { form.append("files", file, file.name); });
    dom.authUploadStatus.textContent = "正在上传 " + files.length + " 个文件…";
    try {
      const payload = await apiRequest(API.authFiles, { method: "POST", body: form });
      const hasUploadedCount = payload && Object.prototype.hasOwnProperty.call(payload, "uploaded");
      const uploaded = hasUploadedCount ? number(payload.uploaded) : files.length;
      const failed = Array.isArray(payload && payload.failed) ? payload.failed : [];
      dom.authUploadStatus.textContent = failed.length
        ? "成功 " + uploaded + " 个，失败 " + failed.length + " 个"
        : "已上传 " + uploaded + " 个文件";
      dom.authUploadInput.value = "";
      if (failed.length) {
        toast("部分文件导入失败：" + failed.map(function (item) { return item.name || "未知文件"; }).join("、"), true);
      } else {
        toast("认证文件已导入");
      }
      await loadCredentials(true);
    } catch (error) {
      dom.authUploadStatus.textContent = apiErrorMessage(error);
      toast("上传失败：" + apiErrorMessage(error), true);
    }
  }

  async function startOAuth(button) {
    const provider = button.dataset.oauthProvider || "OAuth";
    const endpoint = button.dataset.oauthEndpoint;
    const popup = window.open("about:blank", "_blank");
    if (popup) {
      popup.opener = null;
    }
    setControlBusy(button, true);
    state.oauthProvider = provider;
    state.oauthSessionState = "";
    dom.oauthSession.hidden = false;
    dom.oauthCallbackPanel.hidden = true;
    dom.oauthCallbackStatus.textContent = "";
    dom.oauthCallbackValue.value = "";
    dom.oauthSession.textContent = "正在创建 " + provider + " 授权会话…";
    try {
      const payload = await apiRequest(endpoint + "?is_webui=true", { method: "GET" });
      if (!payload || !payload.url || !payload.state) {
        throw new Error("授权接口没有返回有效链接");
      }
      state.oauthSessionState = payload.state;
      dom.oauthCallbackProvider.textContent = provider;
      dom.oauthCallbackPanel.hidden = provider === "kimi";
      if (popup) {
        popup.location.href = payload.url;
      } else {
        window.open(payload.url, "_blank", "noopener,noreferrer");
      }
      dom.oauthSession.textContent = "已打开 " + provider + " 登录页，正在等待授权完成…";
      pollOAuth(payload.state, provider, Date.now() + 5 * 60 * 1000);
    } catch (error) {
      if (popup) {
        popup.close();
      }
      dom.oauthSession.textContent = "授权启动失败：" + apiErrorMessage(error);
      toast(apiErrorMessage(error), true);
    } finally {
      setControlBusy(button, false);
    }
  }

  async function submitOAuthCallback() {
    const value = dom.oauthCallbackValue.value.trim();
    if (!state.oauthSessionState || !value) {
      dom.oauthCallbackStatus.textContent = "请先发起授权，再粘贴回调 URL 或授权 code。";
      return;
    }
    const looksLikeURL = /^https?:\/\//i.test(value);
    const body = { provider: state.oauthProvider, state: state.oauthSessionState };
    if (looksLikeURL) {
      body.redirect_url = value;
    } else {
      body.code = value;
    }
    setControlBusy(dom.oauthCallbackSubmit, true);
    dom.oauthCallbackStatus.textContent = "正在提交回调…";
    try {
      await apiRequest("/v0/management/oauth-callback", { method: "POST", body: JSON.stringify(body) });
      dom.oauthCallbackStatus.textContent = "回调已提交，正在等待服务器完成登录。";
      pollOAuth(state.oauthSessionState, state.oauthProvider, Date.now() + 5 * 60 * 1000);
    } catch (error) {
      dom.oauthCallbackStatus.textContent = "提交失败：" + apiErrorMessage(error);
    } finally {
      setControlBusy(dom.oauthCallbackSubmit, false);
    }
  }

  function renderPluginOAuthProviders() {
    const grid = document.querySelector(".provider-connect-grid");
    if (!grid) {
      return;
    }
    all("[data-plugin-oauth]", grid).forEach(function (item) { item.remove(); });
    const nativeProviders = new Set(["codex", "openai", "anthropic", "claude", "antigravity", "kimi", "xai"]);
    state.plugins.filter(function (plugin) {
      const provider = String(plugin.oauth_provider || "").trim().toLocaleLowerCase();
      const enabled = plugin.effective_enabled !== false && plugin.enabled !== false;
      return enabled && plugin.supports_oauth && provider && !nativeProviders.has(provider);
    }).forEach(function (plugin) {
      const provider = String(plugin.oauth_provider).trim();
      const button = element("button", "provider-connect");
      button.type = "button";
      button.dataset.pluginOauth = "true";
      button.dataset.oauthProvider = provider;
      button.dataset.oauthEndpoint = "/v0/management/" + encodeURIComponent(provider) + "-auth-url";
      button.appendChild(element("span", "provider-orb", pluginName(plugin).slice(0, 1).toUpperCase()));
      const copy = element("span");
      copy.appendChild(element("strong", "", pluginName(plugin)));
      copy.appendChild(element("small", "", "扩展 OAuth"));
      button.appendChild(copy);
      button.appendChild(element("span", "", "↗"));
      button.addEventListener("click", function () { startOAuth(button); });
      grid.appendChild(button);
    });
  }

  async function pollOAuth(sessionState, provider, deadline) {
    window.clearTimeout(oauthPollTimer);
    if (Date.now() > deadline) {
      dom.oauthSession.textContent = provider + " 授权等待超时，请重新发起。";
      return;
    }
    try {
      const payload = await apiRequest(API.authStatus + "?state=" + encodeURIComponent(sessionState), { method: "GET" });
      if (payload && payload.status === "ok") {
        dom.oauthSession.textContent = provider + " 已连接。";
        dom.oauthCallbackPanel.hidden = true;
        toast(provider + " 授权成功");
        await loadCredentials(true);
        return;
      }
      if (payload && payload.status === "error") {
        dom.oauthSession.textContent = provider + " 授权失败：" + (payload.error || "未知错误");
        return;
      }
    } catch (error) {
      dom.oauthSession.textContent = "授权状态检查失败，正在重试…";
    }
    oauthPollTimer = window.setTimeout(function () { pollOAuth(sessionState, provider, deadline); }, 1800);
  }

  async function loadLogs(force, silent, incremental) {
    if (state.loadedViews.logs && !force) {
      renderLogs();
      return;
    }
    if (!silent) {
      setControlBusy(dom.logsRefresh, true);
    }
    try {
      const useCursor = Boolean(incremental && state.logCursor);
      const logQuery = new URLSearchParams({ limit: "500" });
      if (useCursor) {
        logQuery.set("cursor", state.logCursor);
      }
      const results = await Promise.allSettled([
        apiRequest(API.logs + "?" + logQuery.toString(), { method: "GET" }),
        incremental ? Promise.resolve({ files: state.errorLogFiles }) : apiRequest(API.errorLogs, { method: "GET" })
      ]);
      if (results[0].status === "fulfilled") {
        const payload = results[0].value || {};
        const incoming = Array.isArray(payload.lines) ? payload.lines : [];
        if (useCursor && !payload["cursor-reset"]) {
          state.logs = state.logs.concat(incoming).slice(-2000);
        } else {
          state.logs = incoming;
        }
        state.logCursor = String(payload["next-cursor"] || "");
        state.logLiveError = false;
      } else if (!silent) {
        state.logs = [];
        state.logCursor = "";
        state.logLiveError = true;
        const message = apiErrorMessage(results[0].reason);
        toast(message.toLocaleLowerCase().includes("logging to file disabled")
          ? "文件日志尚未开启，可在系统设置中启用。"
          : "运行日志不可用：" + message, true);
      } else {
        state.logLiveError = true;
      }
      if (results[1].status === "fulfilled") {
        state.errorLogFiles = Array.isArray(results[1].value && results[1].value.files) ? results[1].value.files : [];
      } else {
        state.errorLogFiles = [];
      }
      state.loadedViews.logs = true;
      renderLogs();
      renderErrorLogFiles();
      if (dom.logsLive.checked) {
        scrollLogsToBottom();
      }
    } finally {
      if (!silent) {
        setControlBusy(dom.logsRefresh, false);
      }
    }
  }

  function detectLogLevel(line) {
    const value = String(line || "").toLocaleLowerCase();
    if (value.includes("[error") || value.includes(" error ") || value.includes("\"level\":\"error\"")) {
      return "error";
    }
    if (value.includes("[warn") || value.includes(" warning ") || value.includes("\"level\":\"warn\"")) {
      return "warn";
    }
    if (value.includes("[debug") || value.includes(" debug ") || value.includes("\"level\":\"debug\"")) {
      return "debug";
    }
    return "info";
  }

  function renderLogs() {
    const fragment = document.createDocumentFragment();
    const visible = state.logs.filter(function (line) {
      const level = detectLogLevel(line);
      return (state.logLevel === "all" || state.logLevel === level) && (!state.logSearch || String(line).toLocaleLowerCase().includes(state.logSearch));
    });
    visible.forEach(function (line) {
      const level = detectLogLevel(line);
      const row = element("div", "log-line " + level);
      row.appendChild(element("span", "log-level", level));
      row.appendChild(element("span", "", line));
      fragment.appendChild(row);
    });
    dom.logConsole.replaceChildren(fragment);
    dom.logLineCount.textContent = visible.length + " 行" + (state.logLiveError ? " · 刷新暂停" : "");
    dom.logLineCount.classList.toggle("danger", state.logLiveError);
    dom.logsEmpty.hidden = visible.length > 0;
    dom.logConsole.hidden = visible.length === 0;
  }

  function renderErrorLogFiles() {
    dom.errorFileList.replaceChildren();
    state.errorLogFiles.forEach(function (file) {
      const row = element("div", "file-row");
      row.appendChild(element("span", "provider-orb", "!"));
      const copy = element("div");
      copy.appendChild(element("strong", "", file.name || "error.log"));
      copy.appendChild(element("small", "", formatBytes(file.size) + (file.modified ? " · " + formatDateTime(number(file.modified) * 1000) : "")));
      row.appendChild(copy);
      const download = element("button", "mini-button", "下载");
      download.type = "button";
      download.addEventListener("click", function () {
        downloadManagementFile(API.errorLogs + "/" + encodeURIComponent(file.name), file.name);
      });
      row.appendChild(download);
      dom.errorFileList.appendChild(row);
    });
    dom.errorFileCount.textContent = state.errorLogFiles.length + " 个文件";
    dom.errorFilesEmpty.hidden = state.errorLogFiles.length > 0;
  }

  function scrollLogsToBottom() {
    dom.logConsole.scrollTop = dom.logConsole.scrollHeight;
  }

  function syncLiveLogs() {
    window.clearTimeout(logsLiveTimer);
    if (!dom.logsLive.checked || state.currentView !== "logs") {
      return;
    }
    logsLiveTimer = window.setTimeout(async function () {
      await loadLogs(true, true, true);
      syncLiveLogs();
    }, 2600);
  }

  async function clearLogs() {
    const accepted = await confirmAction("清空服务器日志？", "当前主日志会被截断，轮转日志会被删除。该操作无法撤销。", "确认清空");
    if (!accepted) {
      return;
    }
    setControlBusy(dom.logsClear, true);
    try {
      await apiRequest(API.logs, { method: "DELETE" });
      toast("日志已清空");
      state.loadedViews.logs = false;
      await loadLogs(true);
    } catch (error) {
      handleViewError(error, "清空日志失败");
    } finally {
      setControlBusy(dom.logsClear, false);
    }
  }

  async function downloadManagementFile(path, filename) {
    try {
      const response = await window.fetch(path, {
        method: "GET",
        cache: "no-store",
        credentials: "same-origin",
        headers: { Authorization: "Bearer " + state.managementSecret }
      });
      if (!response.ok) {
        throw new Error("下载失败（" + response.status + "）");
      }
      const blob = await response.blob();
      const url = URL.createObjectURL(blob);
      const anchor = element("a");
      anchor.href = url;
      anchor.download = filename || "download";
      anchor.hidden = true;
      document.body.appendChild(anchor);
      anchor.click();
      anchor.remove();
      URL.revokeObjectURL(url);
    } catch (error) {
      toast(apiErrorMessage(error), true);
    }
  }

  function applyPluginPayload(payload) {
    state.plugins = Array.isArray(payload && payload.plugins) ? payload.plugins : [];
  }

  function pluginName(plugin) {
    return plugin.name || plugin.title || (plugin.metadata && (plugin.metadata.name || plugin.metadata.title)) || plugin.id || "未命名扩展";
  }

  async function loadRuntimeSettings(force) {
    if (state.loadedViews.settings && !force) {
      renderRuntimeSettings();
      return;
    }
    setControlBusy(dom.settingsRefresh, true);
    dom.settingsState.textContent = "正在同步…";
    try {
      const results = await Promise.all([
        apiRequest(API.config, { method: "GET" }),
        apiRequest(API.configYAML, { method: "GET" })
      ]);
      state.runtimeConfig = results[0] || {};
      state.configYAML = String(results[1] || "");
      state.yamlDirty = false;
      state.loadedViews.settings = true;
      renderRuntimeSettings();
      dom.settingsState.textContent = "已同步";
    } catch (error) {
      dom.settingsState.textContent = "同步失败";
      handleViewError(error, "系统设置载入失败");
    } finally {
      setControlBusy(dom.settingsRefresh, false);
    }
  }

  function configField(object, key, fallback) {
    if (object && Object.prototype.hasOwnProperty.call(object, key)) {
      return object[key];
    }
    return fallback;
  }

  function renderRuntimeSettings() {
    const config = state.runtimeConfig || {};
    const routing = config.routing || {};
    all("option[data-runtime-value]", dom.runtimeRouting).forEach(function (option) { option.remove(); });
    const routingValue = String(configField(routing, "strategy", "round-robin") || "round-robin");
    if (!["round-robin", "fill-first"].includes(routingValue)) {
      const currentOption = element("option", "", "当前值：" + routingValue);
      currentOption.value = routingValue;
      currentOption.dataset.runtimeValue = "true";
      dom.runtimeRouting.appendChild(currentOption);
    }
    dom.runtimeRouting.value = routingValue;
    dom.runtimeRetry.value = number(configField(config, "request-retry", 0));
    dom.runtimeRetryInterval.value = number(configField(config, "max-retry-interval", 0));
    dom.runtimeProxy.value = String(configField(config, "proxy-url", "") || "");
    dom.runtimeDebug.checked = Boolean(configField(config, "debug", false));
    dom.runtimeFileLog.checked = Boolean(configField(config, "logging-to-file", false));
    dom.runtimeRequestLog.checked = Boolean(configField(config, "request-log", false));
    dom.runtimeWsAuth.checked = Boolean(configField(config, "ws-auth", false));
    dom.runtimeModelPrefix.checked = Boolean(configField(config, "force-model-prefix", false));
    dom.yamlEditor.value = state.configYAML;
    updateYAMLEditorStatus();
  }

  async function requestRuntimeSettingsReload() {
    if (state.yamlDirty) {
      const accepted = await confirmAction("放弃 YAML 修改？", "重新载入会丢失尚未保存的 YAML 草稿。", "放弃并载入");
      if (!accepted) {
        return;
      }
    }
    state.loadedViews.settings = false;
    await loadRuntimeSettings(true);
  }

  async function saveRuntimeSettings(event) {
    event.preventDefault();
    if (state.yamlDirty) {
      toast("请先保存或放弃 YAML 草稿，再修改常用设置。", true);
      dom.yamlEditor.focus();
      return;
    }
    if (!dom.runtimeSettingsForm.reportValidity()) {
      return;
    }
    const retryCount = Number(dom.runtimeRetry.value);
    const retryInterval = Number(dom.runtimeRetryInterval.value);
    if (!Number.isInteger(retryCount) || retryCount < 0 || !Number.isInteger(retryInterval) || retryInterval < 0) {
      toast("重试次数和间隔必须是大于或等于 0 的整数。", true);
      return;
    }
    const config = state.runtimeConfig || {};
    const routing = config.routing || {};
    const updates = [
      { path: "/v0/management/routing/strategy", value: dom.runtimeRouting.value, current: configField(routing, "strategy", "round-robin") },
      { path: "/v0/management/request-retry", value: retryCount, current: number(configField(config, "request-retry", 0)) },
      { path: "/v0/management/max-retry-interval", value: retryInterval, current: number(configField(config, "max-retry-interval", 0)) },
      { path: "/v0/management/proxy-url", value: dom.runtimeProxy.value.trim(), current: String(configField(config, "proxy-url", "") || "") },
      { path: "/v0/management/debug", value: dom.runtimeDebug.checked, current: Boolean(configField(config, "debug", false)) },
      { path: "/v0/management/logging-to-file", value: dom.runtimeFileLog.checked, current: Boolean(configField(config, "logging-to-file", false)) },
      { path: "/v0/management/request-log", value: dom.runtimeRequestLog.checked, current: Boolean(configField(config, "request-log", false)) },
      { path: "/v0/management/ws-auth", value: dom.runtimeWsAuth.checked, current: Boolean(configField(config, "ws-auth", false)) },
      { path: "/v0/management/force-model-prefix", value: dom.runtimeModelPrefix.checked, current: Boolean(configField(config, "force-model-prefix", false)) }
    ].filter(function (entry) { return entry.value !== entry.current; });
    if (!updates.length) {
      toast("设置没有变化");
      return;
    }
    setControlBusy(dom.runtimeSettingsSave, true);
    dom.settingsState.textContent = "正在保存…";
    let savedCount = 0;
    try {
      for (const update of updates) {
        await apiRequest(update.path, { method: "PUT", body: JSON.stringify({ value: update.value }) });
        savedCount += 1;
      }
      toast("常用设置已保存");
      state.loadedViews.settings = false;
      state.loadedViews.logs = false;
      await loadRuntimeSettings(true);
    } catch (error) {
      dom.settingsState.textContent = savedCount ? "已保存 " + savedCount + " 项，正在重新同步" : "设置未保存";
      toast(savedCount ? "已保存前 " + savedCount + " 项；后续设置失败，正在同步实际状态。" : "设置保存失败：" + apiErrorMessage(error), true);
      state.loadedViews.settings = false;
      try {
        await loadRuntimeSettings(true);
      } catch (_reloadError) {
        handleViewError(error, "设置保存失败");
      }
    } finally {
      setControlBusy(dom.runtimeSettingsSave, false);
    }
  }

  async function resetYAMLEditor() {
    if (state.yamlDirty) {
      const accepted = await confirmAction("放弃 YAML 修改？", "编辑器会恢复为服务器最近一次载入的内容。", "放弃修改");
      if (!accepted) {
        return;
      }
    }
    dom.yamlEditor.value = state.configYAML;
    state.yamlDirty = false;
    updateYAMLEditorStatus();
  }

  async function saveYAMLConfig() {
    if (!state.yamlDirty) {
      toast("YAML 没有变化");
      return;
    }
    const accepted = await confirmAction("验证并保存完整配置？", "服务器会先校验 YAML。保存成功后相关运行设置会热重载。", "验证并保存");
    if (!accepted) {
      return;
    }
    setControlBusy(dom.yamlSave, true);
    dom.yamlStatus.textContent = "正在校验…";
    try {
      await apiRequest(API.configYAML, {
        method: "PUT",
        headers: { "Content-Type": "application/yaml; charset=utf-8" },
        body: dom.yamlEditor.value
      });
      state.configYAML = dom.yamlEditor.value;
      state.yamlDirty = false;
      state.loadedViews.settings = false;
      toast("配置已通过校验并保存");
      await loadRuntimeSettings(true);
    } catch (error) {
      dom.yamlStatus.textContent = "校验失败：" + apiErrorMessage(error);
      toast(apiErrorMessage(error), true);
    } finally {
      setControlBusy(dom.yamlSave, false);
      updateYAMLEditorStatus();
    }
  }

  function updateYAMLEditorStatus() {
    const lines = dom.yamlEditor.value ? dom.yamlEditor.value.split(/\r?\n/).length : 0;
    dom.yamlLines.textContent = lines + " 行";
    dom.yamlStatus.textContent = state.yamlDirty ? "有未保存修改" : "与服务器一致";
    dom.settingsState.textContent = state.yamlDirty ? "有未保存修改" : "已同步";
  }

  function openCommandPalette() {
    dom.commandSearch.value = "";
    filterCommandPalette();
    if (!dom.commandDialog.open) {
      dom.commandDialog.showModal();
    }
    window.setTimeout(function () { dom.commandSearch.focus(); }, 0);
  }

  function visibleCommandItems() {
    return all("[data-command-view], .command-list > a", dom.commandList).filter(function (item) { return !item.hidden; });
  }

  function filterCommandPalette() {
    const query = dom.commandSearch.value.trim().toLocaleLowerCase();
    const items = all("[data-command-view], .command-list > a", dom.commandList);
    items.forEach(function (item) {
      item.hidden = Boolean(query && !item.textContent.toLocaleLowerCase().includes(query));
      item.classList.remove("active");
    });
    const first = visibleCommandItems()[0];
    if (first) {
      first.classList.add("active");
    }
  }

  function handleCommandKeys(event) {
    if (event.key === "Escape") {
      event.preventDefault();
      dom.commandDialog.close();
      dom.commandOpen.focus();
      return;
    }
    const items = visibleCommandItems();
    if (!items.length) {
      return;
    }
    const current = items.findIndex(function (item) { return item.classList.contains("active"); });
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      const direction = event.key === "ArrowDown" ? 1 : -1;
      const next = (Math.max(0, current) + direction + items.length) % items.length;
      items.forEach(function (item) { item.classList.remove("active"); });
      items[next].classList.add("active");
      items[next].scrollIntoView({ block: "nearest" });
    } else if (event.key === "Enter") {
      event.preventDefault();
      const active = items[Math.max(0, current)];
      if (active) {
        active.click();
      }
    }
  }

  function setControlBusy(control, busy) {
    if (!control) {
      return;
    }
    control.disabled = Boolean(busy);
    if (busy) {
      control.setAttribute("aria-busy", "true");
    } else {
      control.removeAttribute("aria-busy");
    }
  }

  function handleViewError(error, prefix) {
    if (error && (error.status === 401 || error.status === 403)) {
      signOut(apiErrorMessage(error));
      return;
    }
    toast((prefix ? prefix + "：" : "") + apiErrorMessage(error), true);
  }

  function loadViewData(view, force) {
    if (!state.managementSecret) {
      return;
    }
    if (view === "credentials") {
      loadCredentials(force);
    } else if (view === "quota") {
      loadQuotaCredentials(force);
    } else if (view === "billing") {
      loadBillingCatalog(force);
    } else if (view === "logs") {
      loadLogs(force);
      syncLiveLogs();
    } else if (view === "settings") {
      loadRuntimeSettings(force);
    }
  }

  function refreshCurrentView() {
    if (state.currentView === "quota") {
      refreshAllQuotas();
      return;
    }
    if (state.currentView === "overview" || state.currentView === "keys" || state.currentView === "billing") {
      refreshAll({ includeSettings: true });
      if (state.currentView === "billing") {
        loadBillingCatalog(true);
      }
      return;
    }
    loadViewData(state.currentView, true);
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
      anchor.download = "cliproxy-usage-" + localDateString(new Date()) + ".csv";
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

  function markViewEntering(view) {
    window.clearTimeout(viewEnterTimer);
    all(".view").forEach(function (panel) { panel.classList.remove("is-entering"); });
    if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) {
      return;
    }
    const panel = document.querySelector('[data-view-panel="' + view + '"]');
    if (!panel) {
      return;
    }
    void panel.offsetWidth;
    panel.classList.add("is-entering");
    viewEnterTimer = window.setTimeout(function () { panel.classList.remove("is-entering"); }, 900);
  }

  function setView(view, updateHash) {
    const headings = {
      overview: ["指挥中心", "总览"],
      keys: ["身份与访问", "客户端 Key"],
      credentials: ["身份与访问", "上游凭据"],
      quota: ["用量与运营", "配额管理"],
      logs: ["可观测性", "请求与日志"],
      settings: ["系统控制", "系统设置"],
      billing: ["用量与运营", "计费规则"]
    };
    if (!view || !headings[view]) {
      return;
    }
    if (view === state.currentView) {
      loadViewData(view, false);
      return;
    }
    window.clearTimeout(logsLiveTimer);
    const applyView = function () {
      state.currentView = view;
      if (view !== "keys") {
        cancelKeyDetailRefresh();
      } else if (state.keyDetail && !document.hidden) {
        refreshKeyDetailUsage({ showLoading: false, recentOnly: true });
      }
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
      dom.pageKicker.textContent = headings[view][0];
      dom.pageTitle.textContent = headings[view][1];
      dom.exportCsv.hidden = view !== "overview";
      dom.compactExport.hidden = view !== "overview";
      if (view === "billing" && state.billingDraft) {
        renderBillingSettings();
      }
      if (!document.documentElement.classList.contains("view-transitioning")) {
        markViewEntering(view);
      }
    };
    const reduceMotion = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    if (document.startViewTransition && !reduceMotion) {
      const root = document.documentElement;
      root.classList.add("view-transitioning");
      const transition = document.startViewTransition(applyView);
      const cleanup = function () { root.classList.remove("view-transitioning"); };
      transition.finished.then(cleanup, cleanup);
    } else {
      applyView();
    }
    if (updateHash !== false) {
      window.history.replaceState(null, "", "#" + view);
    }
    loadViewData(view, false);
    window.scrollTo({ top: 0, behavior: window.matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth" });
  }

  function viewFromHash() {
    const view = window.location.hash.replace(/^#\/?/, "").trim();
    return ["overview", "keys", "credentials", "quota", "logs", "settings", "billing"].includes(view) ? view : "";
  }

  function toast(message, isError) {
    const item = element("div", "toast" + (isError ? " error" : ""), message);
    dom.toastRegion.appendChild(item);
    window.setTimeout(function () {
      let removed = false;
      const remove = function () {
        if (removed) {
          return;
        }
        removed = true;
        item.remove();
      };
      item.classList.add("is-leaving");
      item.addEventListener("animationend", remove, { once: true });
      window.setTimeout(remove, 260);
    }, isError ? 6000 : 3200);
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

  function formatBytes(value) {
    const bytes = Math.max(0, number(value));
    if (bytes < 1024) {
      return formatInteger(bytes) + " B";
    }
    const units = ["KB", "MB", "GB", "TB"];
    let amount = bytes / 1024;
    let unitIndex = 0;
    while (amount >= 1024 && unitIndex < units.length - 1) {
      amount /= 1024;
      unitIndex += 1;
    }
    return amount.toLocaleString("zh-CN", { maximumFractionDigits: amount >= 10 ? 1 : 2 }) + " " + units[unitIndex];
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

  function formatQuotaCountdown(milliseconds) {
    const totalSeconds = Math.max(0, Math.floor(number(milliseconds) / 1000));
    const days = Math.floor(totalSeconds / 86400);
    const hours = Math.floor((totalSeconds % 86400) / 3600);
    const minutes = Math.floor((totalSeconds % 3600) / 60);
    const seconds = totalSeconds % 60;
    const pad = function (value) { return String(value).padStart(2, "0"); };
    return (days ? days + "天 " : "") + pad(hours) + ":" + pad(minutes) + ":" + pad(seconds);
  }

  function formatQuotaRelative(milliseconds) {
    const totalSeconds = Math.max(0, Math.floor(number(milliseconds) / 1000));
    const days = Math.floor(totalSeconds / 86400);
    if (days > 0) {
      return days + "天后";
    }
    const hours = Math.floor(totalSeconds / 3600);
    if (hours > 0) {
      return hours + "小时后";
    }
    const minutes = Math.max(1, Math.floor(totalSeconds / 60));
    return minutes + "分钟后";
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

  function formatFullDateTime(value) {
    const date = value instanceof Date ? value : new Date(value);
    if (!Number.isFinite(date.getTime())) {
      return "—";
    }
    return new Intl.DateTimeFormat("zh-CN", { year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false }).format(date);
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
