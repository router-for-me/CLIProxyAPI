import { getModels, extractAuth, chatCompletion, type ModelInfo } from './api.js';

export class App {
  private root: HTMLElement;
  private models: ModelInfo[] = [];
  private providers: Set<string> = new Set();

  constructor(root: HTMLElement) {
    this.root = root;
    this.render();
    this.load();
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
            <button class="btn primary" id="extract-btn">Auto-detect local credentials</button>
            <button class="btn" id="test-btn">Test devin/glm-5.2</button>
          </section>

          <section class="models">
            <div class="section-title">Models</div>
            <div class="provider-grid" id="provider-grid"></div>
          </section>

          <section class="test-output" id="test-output" style="display:none">
            <div class="section-title">Test response</div>
            <pre id="test-result"></pre>
          </section>
        </main>

        <footer class="footer">
          <span>Dashboard for CLIProxyAPI</span>
        </footer>
      </div>
    `;

    this.root.querySelector('#extract-btn')?.addEventListener('click', () => this.handleExtract());
    this.root.querySelector('#test-btn')?.addEventListener('click', () => this.handleTest());
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

  private renderProviders() {
    const grid = document.getElementById('provider-grid');
    if (!grid) return;

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

  private async handleExtract() {
    const btn = document.getElementById('extract-btn') as HTMLButtonElement;
    if (!btn) return;
    btn.disabled = true;
    btn.textContent = 'Detecting…';
    try {
      const data = await extractAuth();
      alert(`Detected credentials: ${data.providers.join(', ') || 'none'}`);
      await this.load();
    } catch (err) {
      this.showError(err instanceof Error ? err.message : String(err));
    } finally {
      btn.disabled = false;
      btn.textContent = 'Auto-detect local credentials';
    }
  }

  private async handleTest() {
    const output = document.getElementById('test-output');
    const result = document.getElementById('test-result');
    if (!output || !result) return;
    output.style.display = 'block';
    result.textContent = 'Loading…';
    try {
      const data = await chatCompletion({
        model: 'devin/glm-5.2',
        messages: [{ role: 'user', content: 'say hello' }],
        stream: false,
        max_tokens: 64,
      });
      result.textContent = JSON.stringify(data, null, 2);
    } catch (err) {
      result.textContent = `Error: ${err instanceof Error ? err.message : String(err)}`;
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
