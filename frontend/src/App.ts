import { getModels, extractAuth, type ModelInfo } from './api.js';

export class App {
  private root: HTMLElement;
  private models: ModelInfo[] = [];
  private providers: Set<string> = new Set();
  private detectedProviders: string[] = [];

  constructor(root: HTMLElement) {
    this.root = root;
    this.render();
    this.bootstrap();
  }

  private render() {
    this.root.innerHTML = `
      <div class="app">
        <header class="header">
          <div class="logo">CLIProxyAPI</div>
          <div class="tagline">One endpoint for all your coding agents</div>
        </header>

        <main class="main">
          <section class="hero">
            <div class="hero-card">
              <div class="hero-label">Endpoint</div>
              <code class="hero-value">http://localhost:8317</code>
            </div>
            <div class="hero-card">
              <div class="hero-label">Providers</div>
              <div class="hero-value" id="provider-count">—</div>
            </div>
            <div class="hero-card">
              <div class="hero-label">Models</div>
              <div class="hero-value" id="model-count">—</div>
            </div>
          </section>

          <section class="actions">
            <button class="btn primary" id="extract-btn" disabled>Scanning local subscriptions…</button>
          </section>

          <section class="detected" id="detected-section" style="display:none">
            <div class="section-title">Detected subscriptions</div>
            <div class="detected-list" id="detected-list"></div>
          </section>

          <section class="models">
            <div class="section-title">Models</div>
            <div class="provider-grid" id="provider-grid">Loading…</div>
          </section>
        </main>

        <footer class="footer">
          <span>Dashboard for CLIProxyAPI</span>
        </footer>
      </div>
    `;

    this.root.querySelector('#extract-btn')?.addEventListener('click', () => this.handleExtract(true));
  }

  private async bootstrap() {
    await this.handleExtract(false);
    await this.load();
  }

  private async load() {
    try {
      const data = await getModels();
      this.models = data.data || [];
      this.providers = new Set(this.models.map((m) => m.owned_by || 'unknown'));
      this.updateStats();
      this.renderProviders();
    } catch (err) {
      this.showError(err instanceof Error ? err.message : String(err));
    }
  }

  private updateStats() {
    const providerCount = document.getElementById('provider-count');
    const modelCount = document.getElementById('model-count');
    if (providerCount) providerCount.textContent = String(this.providers.size);
    if (modelCount) modelCount.textContent = String(this.models.length);
  }

  private renderDetected() {
    const section = document.getElementById('detected-section');
    const list = document.getElementById('detected-list');
    if (!section || !list) return;

    if (this.detectedProviders.length === 0) {
      section.style.display = 'none';
      return;
    }

    section.style.display = 'block';
    list.innerHTML = this.detectedProviders
      .map((p) => `<span class="detected-chip">${this.escape(p)}</span>`)
      .join('');
  }

  private renderProviders() {
    const grid = document.getElementById('provider-grid');
    if (!grid) return;

    if (this.models.length === 0) {
      grid.innerHTML = '<div class="empty">No models found. Run credential scan.</div>';
      return;
    }

    const byProvider: Record<string, ModelInfo[]> = {};
    for (const m of this.models) {
      const p = m.owned_by || 'unknown';
      byProvider[p] = byProvider[p] || [];
      byProvider[p].push(m);
    }

    grid.innerHTML = Object.entries(byProvider)
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([provider, models]) => `
        <div class="provider-card">
          <div class="provider-header">
            <span class="provider-name">${this.escape(provider)}</span>
            <span class="provider-badge">${models.length}</span>
          </div>
          <div class="model-list">
            ${models.map((m) => `
              <div class="model-row">
                <span class="model-id">${this.escape(m.id)}</span>
              </div>
            `).join('')}
          </div>
        </div>
      `).join('');
  }

  private async handleExtract(isManual: boolean) {
    const btn = document.getElementById('extract-btn') as HTMLButtonElement;
    if (btn) {
      btn.disabled = true;
      btn.textContent = 'Scanning local subscriptions…';
    }
    try {
      const data = await extractAuth();
      this.detectedProviders = data.providers || [];
      this.renderDetected();
      if (isManual) {
        await this.load();
      }
    } catch (err) {
      this.showError(err instanceof Error ? err.message : String(err));
    } finally {
      if (btn) {
        btn.disabled = false;
        btn.textContent = 'Rescan local subscriptions';
      }
    }
  }

  private showError(message: string) {
    const grid = document.getElementById('provider-grid');
    if (grid) grid.innerHTML = `<div class="error">${this.escape(message)}</div>`;
  }

  private escape(str: string) {
    return str
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;');
  }
}
