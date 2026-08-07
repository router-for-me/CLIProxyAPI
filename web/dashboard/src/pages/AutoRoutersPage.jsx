import React, { useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { Spinner, ErrorBanner, EmptyState } from '../components/Primitives.jsx';
import Pager from '../components/Pager.jsx';
import { useAsync } from '../hooks/useAsync.js';
import { useToast } from '../components/Toast.jsx';
import { listAutoRouters, deleteAutoRouter } from '../api/client.js';

const DEFAULT_PAGE_SIZE = 25;

export default function AutoRoutersPage() {
  const toast = useToast();
  const navigate = useNavigate();
  const [page, setPage] = useState(1);
  const [search, setSearch] = useState('');
  const { data, error, loading, reload } = useAsync(
    () => listAutoRouters({ page, pageSize: DEFAULT_PAGE_SIZE, search: search.trim() }),
    [page, search.trim()],
  );

  const routers = data?.auto_routers || [];
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
          <h1 className="main__title">Auto Routers</h1>
          <div className="main__subtitle">
            Score each request across 7 complexity dimensions and forward it to
            the tier-appropriate upstream model (Simple → Medium → Complex →
            Reasoning). Each router is a kind of global model with its own
            model id, name and pricing — create as many as you like.
          </div>
        </div>
        <div className="row gap-sm">
          <button onClick={() => { reload(); toast.info('Auto routers refreshed'); }}>Refresh</button>
          <button className="primary" onClick={() => navigate('/auto-routers/new')}>+ New Router</button>
        </div>
      </div>

      <div className="card">
        <div className="catalog-toolbar">
          <input
            className="search-input"
            type="text"
            value={search}
            onChange={(e) => { setSearch(e.target.value); setPage(1); }}
            placeholder="Search name, model id…"
            aria-label="Search auto routers"
          />
          <div className="catalog-toolbar__spacer" />
          <span className="catalog-toolbar__count">{total} total</span>
        </div>
      </div>

      <ErrorBanner error={error} onRetry={reload} />

      {loading && (
        <div className="card" style={{ padding: 0 }}>
          <Spinner label="Loading auto routers…" />
        </div>
      )}

      {!loading && !error && routers.length === 0 && (
        <EmptyState
          title={search ? 'No matching auto routers' : 'No auto routers yet'}
          hint={search
            ? 'Try a different search term.'
            : 'Create your first auto router to score requests and forward them to the model that best fits each task complexity tier.'}
        />
      )}

      {!loading && !error && routers.length > 0 && (
        <div className="card" style={{ padding: 0 }}>
          <table className="table">
            <thead>
              <tr>
                <th>Name</th>
                <th>Model ID</th>
                <th>Mapped tiers</th>
                <th>Status</th>
                <th>Updated</th>
                <th aria-label="Actions" />
              </tr>
            </thead>
            <tbody>
              {routers.map((r) => (
                <RouterRow key={r.id} router={r} onChanged={() => reload()} />
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
    </>
  );
}

function RouterRow({ router, onChanged }) {
  const toast = useToast();
  const navigate = useNavigate();
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const mappedCount = (router.mappings || []).filter((m) => (m.model || '').trim() !== '').length;

  async function handleDelete() {
    setBusy(true);
    try {
      await deleteAutoRouter(router.id);
      toast.success(`Router "${router.name}" deleted`);
      onChanged();
    } catch (err) {
      toast.error(err?.payload?.message || err?.message || 'Failed to delete router');
    } finally {
      setBusy(false);
      setConfirming(false);
    }
  }

  return (
    <tr className="row-link" onClick={() => navigate(`/auto-routers/${encodeURIComponent(router.id)}`)}>
      <td>
        <Link to={`/auto-routers/${encodeURIComponent(router.id)}`} onClick={(e) => e.stopPropagation()}>
          {router.display_name || router.name}
        </Link>
        <div className="dim mono" style={{ marginTop: 2 }}>{router.name}</div>
      </td>
      <td>
        <span className="mono chip">{router.model_id}</span>
      </td>
      <td>
        <span className="chip-count">{mappedCount === 0 ? '—' : mappedCount}/4</span>
      </td>
      <td>
        {router.enabled === false
          ? <span className="badge badge--muted">Disabled</span>
          : <span className="badge badge--ok">Enabled</span>}
      </td>
      <td className="dim">{new Date(router.updated_at).toLocaleString()}</td>
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
