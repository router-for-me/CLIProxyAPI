import React, { useMemo, useState } from 'react';
import {
  sections, API_BASE, AUTH_NOTE, COMMON_HEADERS_NOTE, ERROR_ENVELOPE,
} from '../api/developerDocs.js';
import CopyButton from '../components/CopyButton.jsx';

// DeveloperPage renders the full management REST API documentation: an intro
// (base URL, auth, common headers, error envelope, pagination, timestamp
// format), a sticky anchor nav, and per-endpoint cards grouped by resource.
export default function DeveloperPage() {
  const [activeSection, setActiveSection] = useState(sections[0]?.id || '');

  // Flatten endpoints for the search box. Searching narrows the rendered set.
  const [query, setQuery] = useState('');
  const q = query.trim().toLowerCase();

  const visibleSections = useMemo(() => {
    if (!q) return sections;
    return sections
      .map((s) => ({
        ...s,
        endpoints: s.endpoints.filter(
          (e) =>
            e.path.toLowerCase().includes(q) ||
            e.method.toLowerCase().includes(q) ||
            e.summary.toLowerCase().includes(q) ||
            s.title.toLowerCase().includes(q),
        ),
      }))
      .filter((s) => s.endpoints.length > 0);
  }, [q]);

  return (
    <>
      <div className="main__header devdocs__header">
        <div>
          <h1 className="main__title">Developer</h1>
          <div className="main__subtitle">
            Full REST API reference for the NixLLM management surface. Every
            endpoint includes filters, example payloads, curl, and responses
            with status codes + messages.
          </div>
        </div>
        <input
          className="search-input devdocs__search"
          type="text"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Search endpoints…"
          aria-label="Search endpoints"
        />
      </div>

      <div className="devdocs__layout">
        <nav className="devdocs__nav">
          <a
            href="#intro"
            className={`devdocs__nav-link ${activeSection === 'intro' ? 'active' : ''}`}
            onClick={() => setActiveSection('intro')}
          >
            Overview
          </a>
          {sections.map((s) => (
            <a
              key={s.id}
              href={`#${s.id}`}
              className={`devdocs__nav-link ${activeSection === s.id ? 'active' : ''}`}
              onClick={() => setActiveSection(s.id)}
            >
              {s.title}
            </a>
          ))}
        </nav>

        <div className="devdocs__content">
          <IntroCard />

          {visibleSections.length === 0 && (
            <div className="empty-state">
              <div className="empty-state__title">No endpoints match “{query}”</div>
              <div className="dim">Try a different search term.</div>
            </div>
          )}

          {visibleSections.map((s) => (
            <section key={s.id} id={s.id} className="devdocs__section">
              <h2 className="devdocs__section-title">{s.title}</h2>
              <p className="devdocs__section-desc">{s.description}</p>
              {s.endpoints.map((e) => (
                <EndpointCard key={`${e.method}-${e.path}`} endpoint={e} />
              ))}
            </section>
          ))}
        </div>
      </div>
    </>
  );
}

function IntroCard() {
  return (
    <section id="intro" className="devdocs__section">
      <h2 className="devdocs__section-title">Overview</h2>

      <div className="card" style={{ marginBottom: 16 }}>
        <h3 className="card__title">Base URL &amp; Authentication</h3>
        <div className="form__row">
          <div className="form__label">Base URL</div>
          <code className="copyable mono">{API_BASE}</code>
        </div>
        <div className="form__row">
          <div className="form__label">Authentication</div>
          <p className="muted">{AUTH_NOTE}</p>
        </div>
        <div className="form__row">
          <div className="form__label">Response headers</div>
          <p className="muted">{COMMON_HEADERS_NOTE}</p>
        </div>
      </div>

      <div className="card" style={{ marginBottom: 16 }}>
        <h3 className="card__title">Error Envelope</h3>
        <p className="muted">
          All non-2xx responses use a uniform envelope shape:
        </p>
        <pre className="copyable mono">{ERROR_ENVELOPE.shape}</pre>
        <table className="table table--compact" style={{ marginTop: 12 }}>
          <thead>
            <tr><th>type</th><th>HTTP</th><th>Meaning</th></tr>
          </thead>
          <tbody>
            {ERROR_ENVELOPE.types.map((t) => (
              <tr key={t.type}>
                <td className="mono">{t.type}</td>
                <td className="mono">{t.status}</td>
                <td className="dim">{t.note}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <div className="card" style={{ marginBottom: 16 }}>
        <h3 className="card__title">Conventions</h3>
        <ul className="list-bare">
          <li><strong>Pagination:</strong> <code>page</code> (1-indexed) + <code>page_size</code> (default 25, max 200). List responses include <code>total</code> and <code>total_pages</code>.</li>
          <li><strong>Timestamps:</strong> RFC3339 in UTC (e.g. <code>2026-07-22T10:00:00Z</code>).</li>
          <li><strong>Partial updates:</strong> PATCH bodies use pointer-typed fields; omit a field to leave it unchanged, pass an explicit empty value to clear it.</li>
          <li><strong>Plaintext secrets:</strong> Management tokens and API keys return their plaintext secret <strong>once</strong> at create/regenerate time. Only the SHA-256 hash is persisted.</li>
          <li><strong>Masking:</strong> The <code>key_hash</code> field is never serialized (<code>json:"-"</code>); only <code>key_prefix</code> is exposed for display.</li>
        </ul>
      </div>
    </section>
  );
}

const METHOD_COLORS = {
  GET: 'devdocs__method--get',
  POST: 'devdocs__method--post',
  PATCH: 'devdocs__method--patch',
  PUT: 'devdocs__method--put',
  DELETE: 'devdocs__method--delete',
};

function EndpointCard({ endpoint }) {
  const curl = endpoint.exampleCurl.replace(/{API_BASE}/g, API_BASE);
  return (
    <div className="card devdocs__endpoint" id={`${endpoint.method}-${endpoint.path}`.replace(/[/:]/g, '-')}>
      <div className="devdocs__endpoint-header">
        <span className={`devdocs__method ${METHOD_COLORS[endpoint.method] || ''}`}>{endpoint.method}</span>
        <code className="devdocs__path mono">{API_BASE}{endpoint.path}</code>
      </div>
      <p className="devdocs__summary">{endpoint.summary}</p>

      {endpoint.params && endpoint.params.length > 0 && (
        <div className="devdocs__params">
          <div className="form__label">Parameters</div>
          <table className="table table--compact">
            <thead>
              <tr><th>Name</th><th>In</th><th>Type</th><th>Required</th><th>Default</th><th>Description</th></tr>
            </thead>
            <tbody>
              {endpoint.params.map((p) => (
                <tr key={p.name}>
                  <td className="mono">{p.name}</td>
                  <td className="dim">{p.in}</td>
                  <td className="mono">{p.type}</td>
                  <td>{p.required ? <span className="badge badge--active">yes</span> : <span className="dim">no</span>}</td>
                  <td className="mono dim">{p.default || '—'}</td>
                  <td className="dim">{p.description}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {endpoint.examplePayload && (
        <CodeBlock title="Request payload" code={endpoint.examplePayload} />
      )}

      <CodeBlock title="Example request (curl)" code={curl} />

      {endpoint.responses && endpoint.responses.length > 0 && (
        <div className="devdocs__responses">
          <div className="form__label">Responses</div>
          {endpoint.responses.map((r) => (
            <ResponseBlock key={r.status} response={r} />
          ))}
        </div>
      )}
    </div>
  );
}

function ResponseBlock({ response }) {
  const isError = response.status >= 400;
  return (
    <div className="devdocs__response">
      <div className="devdocs__response-header">
        <span className={`devdocs__status ${isError ? 'devdocs__status--error' : 'devdocs__status--ok'}`}>{response.status}</span>
        <span className="dim">{response.label}</span>
      </div>
      {response.body && (
        <CodeBlock code={response.body} hideTitle />
      )}
    </div>
  );
}

function CodeBlock({ title, code, hideTitle }) {
  return (
    <div className="devdocs__codeblock">
      {!hideTitle && title && <div className="devdocs__codeblock-title">{title}</div>}
      <div className="row" style={{ gap: 8, alignItems: 'flex-start' }}>
        <pre className="copyable mono devdocs__code" style={{ flex: 1 }}>{code}</pre>
        <CopyButton value={code} label="Copy" small />
      </div>
    </div>
  );
}
