// ConfigRevisionsPage — timeline of every accepted runtime_config save.
// Phase 4 of the PG-first control plane replaces the legacy RawConfigTab
// with a structured view backed by the /config-revisions endpoint. Each
// row shows the revision, the actor + reason, and the SHA-256 checksum
// the repository records for that save. Operators can scroll back to
// find a known-good revision; the Rollback action lives in the existing
// rollback handler (use the runtime-config page's rollback flow).
import React, { useEffect, useState } from 'react';
import { listConfigRevisions } from '../api/client.js';
import { Spinner, ErrorBanner } from '../components/Primitives.jsx';

export default function ConfigRevisionsPage() {
  const [rows, setRows] = useState(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(null);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    listConfigRevisions({ limit: 50 })
      .then((r) => { if (!cancelled) setRows(r.revisions ?? []); })
      .catch((e) => { if (!cancelled) setError(e); })
      .finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, []);

  if (loading) return <Spinner label="Loading revisions…" />;
  if (error) return <ErrorBanner error={error} />;
  if (!rows || rows.length === 0) {
    return <div className="muted" style={{ padding: 16 }}>No revisions recorded yet.</div>;
  }
  return (
    <div className="page">
      <h2>Config revisions</h2>
      <p className="muted">Most recent accepted runtime_config saves.</p>
      <table className="table">
        <thead>
          <tr>
            <th>Revision</th>
            <th>Reason</th>
            <th>Actor</th>
            <th>Created</th>
            <th>Checksum</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.revision}>
              <td className="mono">{r.revision}</td>
              <td>{r.reason}</td>
              <td>{r.actor}</td>
              <td>{r.created_at}</td>
              <td className="mono" style={{ fontSize: 11 }}>
                {r.checksum ? r.checksum.slice(0, 12) : '—'}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
