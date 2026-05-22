package api

import (
	"bytes"
	"strings"
)

const kiroCreditScriptMarker = "data-cpa-auth-card-script"

const kiroCreditScript = `<script data-cpa-auth-card-script>
(() => {
  const API_BASE = "/v0/management";
  const AUTH_STORE = "cli-proxy-auth";
  const ENC_PREFIX = "enc::v1::";
  const STORE_KEY = "cli-proxy-api-webui::secure-storage";
  const CLASS_PREFIX = {
    card: ["AuthFilesPage-module__fileCard___", "AuthFilesPage_fileCard__", "fileCard___", "fileCard__"],
    type: ["AuthFilesPage-module__typeBadge___", "AuthFilesPage_typeBadge__", "typeBadge___", "typeBadge__"],
    name: ["AuthFilesPage-module__fileName___", "AuthFilesPage_fileName__", "fileName___", "fileName__"],
    meta: ["AuthFilesPage-module__cardMeta___", "AuthFilesPage_cardMeta__", "cardMeta___", "cardMeta__"],
    item: ["AuthFilesPage-module__metaItem___", "AuthFilesPage_metaItem__", "metaItem___", "metaItem__"],
    label: ["AuthFilesPage-module__metaLabel___", "AuthFilesPage_metaLabel__", "metaLabel___", "metaLabel__"],
    value: ["AuthFilesPage-module__metaValue___", "AuthFilesPage_metaValue__", "metaValue___", "metaValue__"]
  };
  const quotaState = new Map();
  let filesCache = null;
  let filesCacheAt = 0;
  let pauseUntil = 0;
  let capturedAuthHeader = "";

  function rememberAuth(value, url) {
    if (typeof value !== "string") return;
    if (!value.toLowerCase().startsWith("bearer ")) return;
    if (typeof url === "string" && url && !url.includes("/v0/management/")) return;
    capturedAuthHeader = value;
  }

  const originalFetch = window.fetch ? window.fetch.bind(window) : null;
  if (originalFetch) {
    window.fetch = function (input, init) {
      try {
        const url = typeof input === "string" ? input : (input && input.url) || "";
        if (init && init.headers) {
          if (init.headers instanceof Headers) {
            const auth = init.headers.get("Authorization") || init.headers.get("authorization");
            if (auth) rememberAuth(auth, url);
          } else if (Array.isArray(init.headers)) {
            for (const pair of init.headers) {
              if (Array.isArray(pair) && pair.length === 2 && String(pair[0]).toLowerCase() === "authorization") {
                rememberAuth(pair[1], url);
              }
            }
          } else if (typeof init.headers === "object") {
            for (const k of Object.keys(init.headers)) {
              if (k.toLowerCase() === "authorization") rememberAuth(init.headers[k], url);
            }
          }
        } else if (input && typeof input === "object" && input.headers && input.headers.get) {
          const auth = input.headers.get("Authorization") || input.headers.get("authorization");
          if (auth) rememberAuth(auth, url);
        }
      } catch {}
      return originalFetch(input, init);
    };
  }

  const originalSetHeader = XMLHttpRequest.prototype.setRequestHeader;
  XMLHttpRequest.prototype.setRequestHeader = function (name, value) {
    try {
      if (typeof name === "string" && name.toLowerCase() === "authorization") {
        rememberAuth(value, this._cpaUrl || "");
      }
    } catch {}
    return originalSetHeader.apply(this, arguments);
  };
  const originalOpen = XMLHttpRequest.prototype.open;
  XMLHttpRequest.prototype.open = function (method, url) {
    try { this._cpaUrl = typeof url === "string" ? url : ""; } catch {}
    return originalOpen.apply(this, arguments);
  };

  function prefixesFor(key) {
    const value = CLASS_PREFIX[key];
    if (!value) return [];
    return Array.isArray(value) ? value : [value];
  }

  function buildSelector(key) {
    return prefixesFor(key).map(p => '[class*="' + p + '"]').join(",");
  }

  function classNameWithPrefix(root, key) {
    const prefixes = prefixesFor(key);
    if (prefixes.length === 0) return "";
    const selector = buildSelector(key);
    const scoped = root && root.querySelector ? root.querySelector(selector) : null;
    const candidates = scoped ? [scoped] : Array.from(document.querySelectorAll(selector));
    for (const el of candidates) {
      for (const cls of Array.from(el.classList)) {
        if (prefixes.some(p => cls.startsWith(p))) return cls;
      }
    }
    return "";
  }

  function queryByClassPrefix(root, key) {
    const selector = buildSelector(key);
    return selector ? root.querySelector(selector) : null;
  }

  function elementsByClassPrefix(key) {
    const selector = buildSelector(key);
    return selector ? Array.from(document.querySelectorAll(selector)) : [];
  }

  function xorBytes(bytes, key) {
    const out = new Uint8Array(bytes.length);
    for (let i = 0; i < bytes.length; i += 1) out[i] = bytes[i] ^ key[i % key.length];
    return out;
  }

  function decodeStorage(value) {
    if (!value) return null;
    try {
      let raw = value;
      if (raw.startsWith(ENC_PREFIX)) {
        const binary = atob(raw.slice(ENC_PREFIX.length));
        const bytes = new Uint8Array(binary.length);
        for (let i = 0; i < binary.length; i += 1) bytes[i] = binary.charCodeAt(i);
        const key = new TextEncoder().encode(STORE_KEY + "|" + window.location.host + "|" + navigator.userAgent);
        raw = new TextDecoder().decode(xorBytes(bytes, key));
      }
      return JSON.parse(raw);
    } catch {
      return null;
    }
  }

  function readManagementKey() {
    const direct = decodeStorage(localStorage.getItem("managementKey"));
    if (typeof direct === "string" && direct.trim() !== "") return direct.trim();
    const stored = decodeStorage(localStorage.getItem(AUTH_STORE));
    const fromStore = stored && (stored.state && stored.state.managementKey || stored.managementKey);
    if (typeof fromStore === "string" && fromStore.trim() !== "") return fromStore.trim();
    return "";
  }

  function authHeaders() {
    if (capturedAuthHeader) return { Authorization: capturedAuthHeader };
    const key = readManagementKey();
    return key ? { Authorization: "Bearer " + key } : null;
  }

  function canCall() {
    if (Date.now() < pauseUntil) return false;
    if (!capturedAuthHeader && !readManagementKey()) return false;
    return true;
  }

  function noteAuthFailure(status) {
    if (status === 401 || status === 403) {
      pauseUntil = Date.now() + 60000;
    }
  }

  async function getKiroQuota(authIndex) {
    if (!canCall()) throw new Error("kiro-quota paused");
    const headers = authHeaders();
    if (!headers) throw new Error("kiro-quota no key");
    const res = await originalFetch(API_BASE + "/kiro-quota?auth_index=" + encodeURIComponent(authIndex), { headers });
    if (!res.ok) {
      noteAuthFailure(res.status);
      throw new Error("kiro-quota " + res.status);
    }
    return res.json();
  }

  async function getAuthFiles() {
    const now = Date.now();
    if (filesCache && now - filesCacheAt < 10000) return filesCache;
    if (!canCall()) throw new Error("auth-files paused");
    const headers = authHeaders();
    if (!headers) throw new Error("auth-files no key");
    const res = await originalFetch(API_BASE + "/auth-files", { headers });
    if (!res.ok) {
      noteAuthFailure(res.status);
      throw new Error("auth-files " + res.status);
    }
    const body = await res.json();
    filesCache = Array.isArray(body.files) ? body.files : [];
    filesCacheAt = now;
    return filesCache;
  }

  function formatCredit(value) {
    const n = Number(value);
    if (!Number.isFinite(n)) return "-";
    return Number.isInteger(n) ? String(n) : n.toFixed(4).replace(/0+$/, "").replace(/\.$/, "");
  }

  function upsertCredit(card, text, title, opts) {
    const meta = queryByClassPrefix(card, "meta");
    if (!meta) return;
    let item = meta.querySelector("[data-kiro-credit]");
    if (!item) {
      item = document.createElement("div");
      item.className = classNameWithPrefix(document, "item");
      item.dataset.kiroCredit = "true";
      const labelClass = classNameWithPrefix(document, "label");
      const valueClass = classNameWithPrefix(document, "value");
      const btnStyle = "background:transparent;border:none;cursor:pointer;padding:0 4px;font-size:14px;line-height:1;color:inherit;opacity:0.7;margin-left:4px;";
      item.innerHTML = '<span class="' + labelClass + '">Credit Used</span><span class="' + valueClass + '" data-kiro-credit-value></span><button type="button" data-kiro-refresh title="刷新" style="' + btnStyle + '">↻</button>';
      meta.appendChild(item);
    }
    const btn = item.querySelector("[data-kiro-refresh]");
    if (btn && opts) {
      if (opts.authIndex) btn.dataset.authIndex = opts.authIndex;
      if (opts.name) btn.dataset.fileName = opts.name;
      btn.disabled = !!opts.refreshing;
      btn.style.opacity = opts.refreshing ? "0.4" : "0.7";
    }
    item.title = title || text;
    const value = item.querySelector("[data-kiro-credit-value]") || queryByClassPrefix(item, "value");
    if (value) value.textContent = text;
  }

  async function refreshKiroQuota(card, authIndex, name) {
    const cached = quotaState.get(name);
    if (cached && cached.inflight) return;
    quotaState.set(name, { authIndex, status: "loading", text: "Refreshing...", title: "Refreshing Kiro credit usage", inflight: true });
    upsertCredit(card, "Refreshing...", "Refreshing Kiro credit usage", { authIndex, name, refreshing: true });
    try {
      const quota = await getKiroQuota(authIndex);
      const text = formatCredit(quota.credit_used) + " / " + formatCredit(quota.credit_total);
      const title = quota.subscription_title || text;
      quotaState.set(name, { authIndex, status: "success", text, title });
      upsertCredit(card, text, title, { authIndex, name });
    } catch {
      const text = "Unavailable";
      const title = "Kiro credit usage is unavailable";
      quotaState.set(name, { authIndex, status: "error", text, title });
      upsertCredit(card, text, title, { authIndex, name });
    }
  }

  async function hydrateKiroCredits() {
    if (!location.hash.includes("/auth-files")) return;
    const cards = elementsByClassPrefix("card");
    const kiroCards = cards.filter(card => queryByClassPrefix(card, "type")?.textContent?.trim().toLowerCase() === "kiro");
    if (kiroCards.length === 0) return;
    let files;
    try {
      files = await getAuthFiles();
    } catch {
      return;
    }
    for (const card of kiroCards) {
      const name = queryByClassPrefix(card, "name")?.textContent?.trim();
      if (!name) continue;
      const file = files.find(f => f && (f.name === name || f.id === name));
      const authIndex = file?.auth_index || file?.authIndex;
      if (!authIndex) continue;
      const cached = quotaState.get(name);
      if (cached?.authIndex === authIndex) {
        upsertCredit(card, cached.text, cached.title, { authIndex, name });
        if (cached.status === "loading") continue;
        if (cached.status === "success") continue;
      }
      quotaState.set(name, { authIndex, status: "loading", text: "Loading...", title: "Loading Kiro credit usage" });
      upsertCredit(card, "Loading...", "Loading Kiro credit usage", { authIndex, name });
      getKiroQuota(authIndex).then(quota => {
        const text = formatCredit(quota.credit_used) + " / " + formatCredit(quota.credit_total);
        const title = quota.subscription_title || text;
        quotaState.set(name, { authIndex, status: "success", text, title });
        upsertCredit(card, text, title, { authIndex, name });
      }).catch(() => {
        const text = "Unavailable";
        const title = "Kiro credit usage is unavailable";
        quotaState.set(name, { authIndex, status: "error", text, title });
        upsertCredit(card, text, title, { authIndex, name });
      });
    }
  }

  function hydrateAll() {
    if (!canCall()) return;
    hydrateKiroCredits();
  }

  document.addEventListener("click", (e) => {
    const btn = e.target.closest && e.target.closest("[data-kiro-refresh]");
    if (!btn) return;
    e.preventDefault();
    e.stopPropagation();
    const authIndex = btn.dataset.authIndex;
    const name = btn.dataset.fileName;
    const card = btn.closest(buildSelector("card"));
    if (!authIndex || !name || !card) return;
    refreshKiroQuota(card, authIndex, name);
  }, true);

  const observer = new MutationObserver(() => {
    window.clearTimeout(observer.timer);
    observer.timer = window.setTimeout(hydrateAll, 250);
  });
  observer.observe(document.documentElement, { childList: true, subtree: true });
  window.addEventListener("hashchange", hydrateAll);
  window.setInterval(hydrateAll, 15000);
})();
</script>`

func injectKiroCreditScript(html []byte) []byte {
	if len(html) == 0 || bytes.Contains(html, []byte(kiroCreditScriptMarker)) {
		return html
	}
	lower := strings.ToLower(string(html))
	idx := strings.LastIndex(lower, "</body>")
	if idx < 0 {
		return append(append([]byte{}, html...), []byte(kiroCreditScript)...)
	}
	out := make([]byte, 0, len(html)+len(kiroCreditScript)+1)
	out = append(out, html[:idx]...)
	out = append(out, '\n')
	out = append(out, kiroCreditScript...)
	out = append(out, html[idx:]...)
	return out
}
