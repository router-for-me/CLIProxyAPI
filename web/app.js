const API_KEY = 'devin-test';
const MGMT_KEY = 'devin-test';
const API_BASE = '';

async function api(path, options = {}) {
  const headers = {
    'Authorization': `Bearer ${API_KEY}`,
    'Content-Type': 'application/json',
  };
  if (path.startsWith('/v0/management')) {
    headers['X-Management-Key'] = MGMT_KEY;
  }
  const res = await fetch(`${API_BASE}${path}`, {
    ...options,
    headers: { ...headers, ...(options.headers || {}) },
  });
  if (!res.ok) {
    const text = await res.text();
    throw new Error(`${res.status}: ${text}`);
  }
  return res.json();
}

function groupBy(arr, keyFn) {
  const groups = {};
  for (const item of arr) {
    const key = keyFn(item);
    groups[key] = groups[key] || [];
    groups[key].push(item);
  }
  return groups;
}

async function loadStatus() {
  const status = document.getElementById('status');
  try {
    const models = await api('/v1/models');
    const byProvider = groupBy(models.data, m => m.owned_by || 'unknown');
    const providers = Object.keys(byProvider).sort();
    const total = models.data.length;

    status.innerHTML = `
      <div class="card">
        <h2>Status</h2>
        <div class="status-grid">
          <div class="status-item">
            <div class="label">Providers</div>
            <div class="value ok">${providers.length}</div>
          </div>
          <div class="status-item">
            <div class="label">Models</div>
            <div class="value ok">${total}</div>
          </div>
          <div class="status-item">
            <div class="label">Endpoint</div>
            <div class="value">localhost:8317</div>
          </div>
        </div>
      </div>
    `;
    renderModels(models.data, byProvider, providers);
  } catch (err) {
    status.innerHTML = `<div class="card error">Failed to load status: ${escapeHtml(err.message)}</div>`;
  }
}

function renderModels(models, byProvider, providers) {
  const container = document.querySelector('.model-list');
  if (!container) return;
  const html = providers.map(provider => {
    const items = byProvider[provider].map(m => `
      <div class="model-item">
        <span class="id">${escapeHtml(m.id)}</span>
        <span class="provider">${escapeHtml(provider)}</span>
      </div>
    `).join('');
    return `
      <div class="model-group">
        <div class="model-group-header">${escapeHtml(provider)} (${byProvider[provider].length})</div>
        ${items}
      </div>
    `;
  }).join('');
  container.innerHTML = html;
  container.classList.remove('loading');
}

function escapeHtml(str) {
  if (str == null) return '';
  return String(str)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}

function toast(message) {
  const el = document.createElement('div');
  el.className = 'toast';
  el.textContent = message;
  document.body.appendChild(el);
  requestAnimationFrame(() => el.classList.add('show'));
  setTimeout(() => {
    el.classList.remove('show');
    setTimeout(() => el.remove(), 300);
  }, 3000);
}

async function runAuthExtractor() {
  const btn = document.getElementById('extract-auth');
  btn.disabled = true;
  btn.textContent = 'Detecting…';
  try {
    const res = await fetch('/v0/management/extract-auth', { method: 'POST' });
    const data = await res.json();
    if (!res.ok) throw new Error(data.message || 'unknown error');
    toast(`Detected: ${data.providers.join(', ') || 'none'}`);
    await loadStatus();
  } catch (err) {
    toast(`Error: ${err.message}`);
  } finally {
    btn.disabled = false;
    btn.textContent = 'Auto-detect local credentials';
  }
}

document.addEventListener('DOMContentLoaded', () => {
  loadStatus();
  const btn = document.getElementById('extract-auth');
  if (btn) btn.addEventListener('click', runAuthExtractor);
});
