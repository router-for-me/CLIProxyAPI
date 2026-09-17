// ConfigImportsPage — read-only audit of every /config_imports row.
// Phase 4 of the PG-first control plane surfaces the CLI import audit
// trail alongside the revisions history so operators can correlate
// "revision N came from this CLI invocation".
import React, { useEffect, useState } from 'react';
import { listConfigImports } from '../api/client.js';
import { Spinner, ErrorBanner } from '../components/Primitives.jsx';
import { StatusBadge } from '../components/Primitives.jsx';

export default function ConfigImportsPage() {
  const [rows, setRows] = useState(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(null);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    listConfigImports({ limit: 50 })
      .then((r) => { if (!cancelled) setRows(r.imports ?? []); })
      .catch((e) => { if (!cancelled) setError(e); })
      .finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, []);

  if (loading) return <Spinner label="Loading imports…" />;
  if (error) return <ErrorBanner error={error} />;
  if (!rows || rows.length === 0) {
    return <div className="muted" style={{ padding: 16 }}>No imports recorded yet.</div>;
  }
  return (
    <div className="page">
      <h2>Config imports</h2>
      <p className="muted">Audit trail for CLI and boot-time imports.</p>
      <table className="table">
        <thead>
          <tr>
            <th>ID</th>
            <th>Mode</th>
            <th>Status</th>
            <th>Revision</th>
            <th>Actor</th>
            <th>Created</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.id}>
              <td className="mono">{r.id}</td>
              <td>{r.mode}</td>
              <td><StatusBadge status={r.status} /></td>
              <td className="mono">{r.revision ?? '—'}</td>
              <td>{r.actor}</td>
              <td>{r.created_at}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
