package api

import (
	"bytes"
	"strings"
)

const kiroCreditScriptMarker = "data-cpa-kiro-credit-script"

const kiroCreditScript = `<script data-cpa-kiro-credit-script>
(() => {
  const API_BASE = "/v0/management";
  const AUTH_STORE = "cli-proxy-auth";
  const ENC_PREFIX = "enc::v1::";
  const STORE_KEY = "cli-proxy-api-webui::secure-storage";
  const CLASS_PREFIX = {
    card: "AuthFilesPage-module__fileCard___",
    type: "AuthFilesPage-module__typeBadge___",
    name: "AuthFilesPage-module__fileName___",
    meta: "AuthFilesPage-module__cardMeta___",
    item: "AuthFilesPage-module__metaItem___",
    label: "AuthFilesPage-module__metaLabel___",
    value: "AuthFilesPage-module__metaValue___"
  };
  const quotaState = new Map();
  let filesCache = null;
  let filesCacheAt = 0;

  function classNameWithPrefix(root, key) {
    const prefix = CLASS_PREFIX[key];
    if (!prefix) return "";
    const scoped = root?.querySelector?.('[class*="' + prefix + '"]');
    if (scoped) {
      const found = Array.from(scoped.classList).find(cls => cls.startsWith(prefix));
      if (found) return found;
    }
    const global = document.querySelector('[class*="' + prefix + '"]');
    return global ? Array.from(global.classList).find(cls => cls.startsWith(prefix)) || "" : "";
  }

  function queryByClassPrefix(root, key) {
    const prefix = CLASS_PREFIX[key];
    return prefix ? root.querySelector('[class*="' + prefix + '"]') : null;
  }

  function elementsByClassPrefix(key) {
    const prefix = CLASS_PREFIX[key];
    return prefix ? Array.from(document.querySelectorAll('[class*="' + prefix + '"]')) : [];
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

  function authHeaders() {
    const stored = decodeStorage(localStorage.getItem(AUTH_STORE));
    const key = stored?.state?.managementKey || stored?.managementKey || "";
    return key ? { Authorization: "Bearer " + key } : {};
  }

  async function getAuthFiles() {
    const now = Date.now();
    if (filesCache && now - filesCacheAt < 10000) return filesCache;
    const res = await fetch(API_BASE + "/auth-files", { headers: authHeaders() });
    if (!res.ok) throw new Error("auth-files " + res.status);
    const body = await res.json();
    filesCache = Array.isArray(body.files) ? body.files : [];
    filesCacheAt = now;
    return filesCache;
  }

  async function getKiroQuota(authIndex) {
    const res = await fetch(API_BASE + "/kiro-quota?auth_index=" + encodeURIComponent(authIndex), { headers: authHeaders() });
    if (!res.ok) throw new Error("kiro-quota " + res.status);
    return res.json();
  }

  function formatCredit(value) {
    const n = Number(value);
    if (!Number.isFinite(n)) return "-";
    return Number.isInteger(n) ? String(n) : n.toFixed(4).replace(/0+$/, "").replace(/\.$/, "");
  }

  function upsertCredit(card, text, title) {
    const meta = queryByClassPrefix(card, "meta");
    if (!meta) return;
    let item = meta.querySelector("[data-kiro-credit]");
    if (!item) {
      item = document.createElement("div");
      item.className = classNameWithPrefix(document, "item");
      item.dataset.kiroCredit = "true";
      const labelClass = classNameWithPrefix(document, "label");
      const valueClass = classNameWithPrefix(document, "value");
      item.innerHTML = '<span class="' + labelClass + '">Credit Used</span><span class="' + valueClass + '"></span>';
      meta.appendChild(item);
    }
    item.title = title || text;
    const value = queryByClassPrefix(item, "value") || item.querySelector("span:last-child");
    if (value) value.textContent = text;
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
        upsertCredit(card, cached.text, cached.title);
        if (cached.status === "loading") continue;
        if (cached.status === "success") continue;
      }
      quotaState.set(name, { authIndex, status: "loading", text: "Loading...", title: "Loading Kiro credit usage" });
      upsertCredit(card, "Loading...", "Loading Kiro credit usage");
      getKiroQuota(authIndex).then(quota => {
        const text = formatCredit(quota.credit_used) + " / " + formatCredit(quota.credit_total);
        const title = quota.subscription_title || text;
        quotaState.set(name, { authIndex, status: "success", text, title });
        upsertCredit(card, text, title);
      }).catch(() => {
        const text = "Unavailable";
        const title = "Kiro credit usage is unavailable";
        quotaState.set(name, { authIndex, status: "error", text, title });
        upsertCredit(card, text, title);
      });
    }
  }

  const observer = new MutationObserver(() => {
    window.clearTimeout(observer.timer);
    observer.timer = window.setTimeout(hydrateKiroCredits, 250);
  });
  observer.observe(document.documentElement, { childList: true, subtree: true });
  window.addEventListener("hashchange", hydrateKiroCredits);
  window.setInterval(hydrateKiroCredits, 5000);
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
