import React, { useState } from 'react';
import { useParams, Link } from 'react-router-dom';
import ModelGroupForm, { formToGroup } from '../components/ModelGroupForm.jsx';
import RoutingSummaryCard from '../components/RoutingSummaryCard.jsx';
import { Modal, Spinner, ErrorBanner } from '../components/Primitives.jsx';
import { useAsync } from '../hooks/useAsync.js';
import { useToast } from '../components/Toast.jsx';
import {
  getModelGroup,
  updateModelGroup,
  attachModelGroup,
  detachModelGroup,
  listAPIKeys,
} from '../api/client.js';

export default function ModelGroupDetailPage() {
  const { id } = useParams();
  const toast = useToast();
  const { data, error, loading, reload } = useAsync(() => getModelGroup(id), [id]);
  const [form, setForm] = useState(null);
  const [saving, setSaving] = useState(false);
  const [showAttach, setShowAttach] = useState(false);

  const group = data?.group;
  const attachments = data?.attachments || [];

  async function handleSave() {
    if (!group) return;
    const payload = formToGroup(form);
    setSaving(true);
    try {
      await updateModelGroup(group.id, payload);
      toast.success('Group updated');
      reload();
    } catch (err) {
      toast.error(err?.payload?.message || err?.message || 'Failed to update group');
    } finally {
      setSaving(false);
    }
  }

  async function handleDetach(apiKeyId) {
    setSaving(true);
    try {
      await detachModelGroup(group.id, apiKeyId);
      toast.success('Detached');
      reload();
    } catch (err) {
      toast.error(err?.payload?.message || err?.message || 'Failed to detach');
    } finally {
      setSaving(false);
    }
  }

  if (loading) return <Spinner label="Loading group…" />;
  if (error) return <ErrorBanner error={error} onRetry={reload} />;
  if (!group) return <div className="card">Group not found.</div>;

  return (
    <>
      <div className="main__header">
        <div>
          <div className="dim"><Link to="/model-groups">← Model Groups</Link></div>
          <h1 className="main__title">{group.name}</h1>
          <div className="main__subtitle">
            Model group template. When attached to an API-key policy the
            group's allowed/blocked lists and per-model routes become the
            source of truth for that key's model access. Model groups attach
            ONLY to API-key policies — Internal Users are not attachable.
          </div>
        </div>
        <div className="row gap-sm">
          <button onClick={() => { reload(); toast.info('Reloaded'); }}>Refresh</button>
          <button className="primary" onClick={handleSave} disabled={saving}>
            {saving ? 'Saving…' : 'Save changes'}
          </button>
        </div>
      </div>

      <div className="card">
        <h2 className="card__title">Group</h2>
        <ModelGroupForm initial={group} onChange={setForm} />
      </div>

      <RoutingSummaryCard routes={group.model_routes} />

      <div className="card">
        <div className="card__header row" style={{ justifyContent: 'space-between', alignItems: 'center' }}>
          <h2 className="card__title">Attachments</h2>
          <button onClick={() => setShowAttach(true)}>+ Attach to API Key</button>
        </div>
        {attachments.length === 0 ? (
          <div className="muted">Not attached to any API-key policy. Attach to make this group the source of truth for a key's model access (and routes).</div>
        ) : (
          <table className="table">
            <thead>
              <tr><th>API Key</th><th>Owner</th><th>ID</th><th aria-label="Actions" /></tr>
            </thead>
            <tbody>
              {attachments.map((a, i) => (
                <tr key={`${a.entity_id}-${i}`}>
                  <td>
                    <Link to={`/api-keys/${encodeURIComponent(a.entity_id)}`}>{a.entity_label || a.entity_id}</Link>
                  </td>
                  <td>
                    {a.entity_user_id ? (
                      <Link
                        to={`/internal-users/${encodeURIComponent(a.entity_user_id)}`}
                        className="mono"
                        title={a.entity_user_id}
                      >
                        {a.entity_user_alias
                          ? `${a.entity_user_alias}${a.entity_user_email ? ` <${a.entity_user_email}>` : ''}`
                          : a.entity_user_id}
                      </Link>
                    ) : (
                      <span className="dim">unassigned</span>
                    )}
                  </td>
                  <td className="mono dim">{a.entity_id}</td>
                  <td>
                    <button
                      className="row-actions__btn row-actions__btn--danger"
                      onClick={() => handleDetach(a.entity_id)}
                      disabled={saving}
                    >Detach</button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {showAttach && (
        <AttachModal
          groupID={group.id}
          onClose={() => setShowAttach(null)}
          onChanged={() => { setShowAttach(null); reload(); }}
        />
      )}
    </>
  );
}

function AttachModal({ groupID, onClose, onChanged }) {
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const [search, setSearch] = useState('');
  // Fetch the candidate API keys once. Only 'active' keys are offered since
  // attaching to a disabled/revoked key has no enforcement effect.
  const { data, loading, error } = useAsync(
    () => listAPIKeys({ page: 1, pageSize: 200, status: 'active' }),
    [],
  );
  const rows = data?.api_keys || [];
  const filtered = rows.filter((r) => {
    const q = search.trim().toLowerCase();
    if (!q) return true;
    const hay = [r.name, r.key_prefix, r.user_alias, r.id].filter(Boolean).join(' ').toLowerCase();
    return hay.includes(q);
  });

  async function handleAttach(id) {
    setBusy(true);
    try {
      await attachModelGroup(groupID, id);
      toast.success('Attached');
      onChanged();
    } catch (err) {
      toast.error(err?.payload?.message || err?.message || 'Failed to attach');
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      title="Attach to API Key"
      onClose={onClose}
      size="md"
    >
      <input
        className="search-input"
        style={{ width: '100%', marginBottom: 12 }}
        type="text"
        value={search}
        onChange={(e) => setSearch(e.target.value)}
        placeholder="Search name, prefix, owner…"
      />
      {loading && <Spinner label="Loading…" />}
      {error && <ErrorBanner error={error} />}
      {!loading && filtered.length === 0 && (
        <div className="muted">No candidates.</div>
      )}
      {!loading && filtered.length > 0 && (
        <div style={{ maxHeight: 360, overflow: 'auto' }}>
          <table className="table">
            <tbody>
              {filtered.map((r) => (
                <tr key={r.id}>
                  <td>
                    {r.name}
                    <div className="dim mono" style={{ marginTop: 2 }}>{r.id}</div>
                  </td>
                  <td style={{ textAlign: 'right' }}>
                    <button
                      className="primary"
                      onClick={() => handleAttach(r.id)}
                      disabled={busy}
                    >Attach</button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Modal>
  );
}
