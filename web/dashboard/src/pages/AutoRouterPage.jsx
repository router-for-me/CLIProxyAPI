import React, { useState } from 'react';
import { useParams, useNavigate, Link } from 'react-router-dom';
import AutoRouterForm, { formToRouter } from '../components/AutoRouterForm.jsx';
import { Spinner, ErrorBanner } from '../components/Primitives.jsx';
import { useAsync } from '../hooks/useAsync.js';
import { useToast } from '../components/Toast.jsx';
import {
  getAutoRouter,
  createAutoRouter,
  updateAutoRouter,
} from '../api/client.js';

// AutoRouterPage renders the Auto Router editor as a full page. It serves both
// creating a new router (/auto-routers/new — no :id) and editing an existing
// one (/auto-routers/:id). When editing, the form is seeded from the persisted
// router; on save the updated payload is PUT back. On create the router is POSTed
// and the operator is navigated to the new router's edit page.
export default function AutoRouterPage() {
  const { id } = useParams();
  const navigate = useNavigate();
  const toast = useToast();
  const isNew = !id;
  const [form, setForm] = useState(null);
  const [saving, setSaving] = useState(false);

  const { data, error, loading, reload } = useAsync(
    () => (isNew ? Promise.resolve(null) : getAutoRouter(id)),
    [id],
  );

  const router = isNew ? null : data?.auto_router;

  async function handleSave() {
    if (!form) return;
    const payload = formToRouter(form);
    const name = (payload.name || '').trim();
    if (!name) {
      toast.error('Router name is required');
      return;
    }
    let modelID = (payload.model_id || '').trim();
    if (!modelID) {
      toast.error('Model id is required');
      return;
    }
    payload.model_id = modelID;

    setSaving(true);
    try {
      if (isNew) {
        const created = await createAutoRouter(payload);
        toast.success(`Auto router "${created?.auto_router?.name || payload.name}" created`);
        const newID = created?.auto_router?.id;
        navigate(newID ? `/auto-routers/${encodeURIComponent(newID)}` : '/auto-routers', { replace: true });
      } else {
        await updateAutoRouter(id, payload);
        toast.success(`Auto router "${payload.name}" saved`);
        reload();
      }
    } catch (err) {
      toast.error(err?.payload?.message || err?.message || (isNew ? 'Failed to create router' : 'Failed to update router'));
      setSaving(false);
      return;
    }
    setSaving(false);
  }

  if (!isNew && loading) return <Spinner label="Loading router…" />;
  if (!isNew && error) return <ErrorBanner error={error} onRetry={reload} />;
  if (!isNew && !router) return <div className="card">Router not found.</div>;

  const title = router?.display_name || router?.name || (isNew ? 'New Auto Router' : 'Edit Auto Router');

  return (
    <>
      <div className="main__header">
        <div>
          <div className="dim"><Link to="/auto-routers">← Auto Routers</Link></div>
          <h1 className="main__title">{title}</h1>
          <div className="main__subtitle">
            {isNew
              ? 'Score each request across 7 complexity dimensions and forward it to the tier-appropriate upstream model. Create the router, then map each tier to a model.'
              : 'Edit this auto router. Each complexity tier forwards the scored request to the model you choose; a tier left empty falls back to the next-lower mapped tier.'}
          </div>
        </div>
        <div className="row gap-sm">
          {!isNew && (
            <button onClick={() => { reload(); toast.info('Reloaded'); }}>Refresh</button>
          )}
          <button
            className="primary"
            onClick={handleSave}
            disabled={saving || (!isNew && !router)}
          >
            {saving ? 'Saving…' : (isNew ? 'Create router' : 'Save changes')}
          </button>
        </div>
      </div>

      <div className="card">
        <h2 className="card__title">{isNew ? 'New Router' : 'Router'}</h2>
        <AutoRouterForm
          initial={isNew ? null : router}
          onChange={setForm}
        />
      </div>
    </>
  );
}
