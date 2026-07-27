import React, { useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import ModelGroupForm, { formToGroup } from '../components/ModelGroupForm.jsx';
import { Modal, Spinner, ErrorBanner, EmptyState } from '../components/Primitives.jsx';
import Pager from '../components/Pager.jsx';
import { useAsync } from '../hooks/useAsync.js';
import { useToast } from '../components/Toast.jsx';
import { listModelGroups, createModelGroup, deleteModelGroup } from '../api/client.js';

const DEFAULT_PAGE_SIZE = 25;

export default function ModelGroupsPage() {
  const toast = useToast();
  const [page, setPage] = useState(1);
  const [search, setSearch] = useState('');
  const [showCreate, setShowCreate] = useState(false);
  const { data, error, loading, reload } = useAsync(
    () => listModelGroups({ page, pageSize: DEFAULT_PAGE_SIZE, search: search.trim() }),
    [page, search.trim()],
  );

  const groups = data?.groups || [];
  const total = data?.total ?? 0;
  const totalPages = data?.total_pages ?? 0;

  function handlePageChange(newPage) {
    if (newPage < 1 || newPage > totalPages) return;
    setPage(newPage);
  }

  return (
    <>
      <div className="main__header">
        <div>
          <h1 className="main__title">Model Groups</h1>
          <div className="main__subtitle">
            Reusable templates of allowed-models grant lists plus optional
            per-model upstream routing. Attach a group to an API-key policy or
            an internal user to make the group the source of truth for that
            entity's model access.
          </div>
        </div>
        <div className="row gap-sm">
          <button onClick={() => { reload(); toast.info('Groups refreshed'); }}>Refresh</button>
          <button className="primary" onClick={() => setShowCreate(true)}>+ New Group</button>
        </div>
      </div>

      <div className="card">
        <div className="catalog-toolbar">
          <input
            className="search-input"
            type="text"
            value={search}
            onChange={(e) => { setSearch(e.target.value); setPage(1); }}
            placeholder="Search name, description…"
            aria-label="Search model groups"
          />
          <div className="catalog-toolbar__spacer" />
          <span className="catalog-toolbar__count">{total} total</span>
        </div>
      </div>

      <ErrorBanner error={error} onRetry={reload} />

      {loading && (
        <div className="card" style={{ padding: 0 }}>
          <Spinner label="Loading groups…" />
        </div>
      )}

      {!loading && !error && groups.length === 0 && (
        <EmptyState
          title={search ? 'No matching model groups' : 'No model groups yet'}
          hint={search
            ? 'Try a different search term.'
            : 'Create your first group to bundle an allowed-models grant list (and optional per-model upstream routing) that you can attach to internal users and API keys.'}
        />
      )}

      {!loading && !error && groups.length > 0 && (
        <div className="card" style={{ padding: 0 }}>
          <table className="table">
            <thead>
              <tr>
                <th>Name</th>
                <th>Description</th>
                <th>Allowed</th>
                <th>Blocked</th>
                <th>Routed</th>
                <th>Updated</th>
                <th aria-label="Actions" />
              </tr>
            </thead>
            <tbody>
              {groups.map((g) => (
                <GroupRow key={g.id} group={g} onChanged={() => reload()} />
              ))}
            </tbody>
          </table>
          <div style={{ padding: '0 16px 16px' }}>
            <Pager
              page={page}
              totalPages={totalPages}
              total={total}
              pageSize={DEFAULT_PAGE_SIZE}
              onPageChange={handlePageChange}
            />
          </div>
        </div>
      )}

      {showCreate && (
        <CreateGroupModal
          onClose={() => setShowCreate(false)}
          onCreated={() => { setShowCreate(false); setPage(1); reload(); }}
        />
      )}
    </>
  );
}

function GroupRow({ group, onChanged }) {
  const toast = useToast();
  const navigate = useNavigate();
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const allowedCount = Array.isArray(group.allowed_models) ? group.allowed_models.length : 0;
  const blockedCount = Array.isArray(group.blocked_models) ? group.blocked_models.length : 0;
  const routedCount = Array.isArray(group.model_routes) ? group.model_routes.length : 0;

  async function handleDelete() {
    setBusy(true);
    try {
      await deleteModelGroup(group.id);
      toast.success(`Group "${group.name}" deleted`);
      onChanged();
    } catch (err) {
      toast.error(err?.payload?.message || err?.message || 'Failed to delete group');
    } finally {
      setBusy(false);
      setConfirming(false);
    }
  }

  return (
    <tr className="row-link" onClick={() => navigate(`/model-groups/${encodeURIComponent(group.id)}`)}>
      <td>
        <Link to={`/model-groups/${encodeURIComponent(group.id)}`} onClick={(e) => e.stopPropagation()}>{group.name}</Link>
        <div className="dim mono" style={{ marginTop: 2 }}>{group.id}</div>
      </td>
      <td className="dim">{group.description || '—'}</td>
      <td><span className="chip-count">{allowedCount === 0 ? 'all' : allowedCount}</span></td>
      <td>{blockedCount === 0 ? '—' : <span className="chip-count">{blockedCount}</span>}</td>
      <td>{routedCount === 0 ? '—' : <span className="chip-count">{routedCount}</span>}</td>
      <td className="dim">{new Date(group.updated_at).toLocaleString()}</td>
      <td onClick={(e) => e.stopPropagation()}>
        <div className="row-actions">
          {!confirming ? (
            <button
              type="button"
              className="row-actions__btn row-actions__btn--danger"
              onClick={() => setConfirming(true)}
              disabled={busy}
            >Delete</button>
          ) : (
            <>
              <button
                type="button"
                className="row-actions__btn row-actions__btn--danger"
                onClick={handleDelete}
                disabled={busy}
              >Confirm</button>
              <button
                type="button"
                className="row-actions__btn"
                onClick={() => setConfirming(false)}
                disabled={busy}
              >Cancel</button>
            </>
          )}
        </div>
      </td>
    </tr>
  );
}

function CreateGroupModal({ onClose, onCreated }) {
  const toast = useToast();
  const [form, setForm] = useState(null);
  const [busy, setBusy] = useState(false);

  async function handleSubmit() {
    const payload = formToGroup(form);
    if (!payload.name) {
      toast.error('Name is required');
      return;
    }
    setBusy(true);
    try {
      await createModelGroup(payload);
      toast.success('Model group created');
      onCreated();
    } catch (err) {
      toast.error(err?.payload?.message || err?.message || 'Failed to create group');
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      title="New Model Group"
      onClose={onClose}
      size="lg"
      footer={
        <>
          <button onClick={onClose} disabled={busy}>Cancel</button>
          <button className="primary" onClick={handleSubmit} disabled={busy}>
            {busy ? 'Creating…' : 'Create group'}
          </button>
        </>
      }
    >
      <ModelGroupForm initial={null} onChange={setForm} />
    </Modal>
  );
}
