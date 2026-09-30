import React, { useState } from 'react';
import { getRoutingStrategy, putRoutingStrategy } from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import { Spinner, ErrorBanner } from './Primitives.jsx';
import { useToast } from './Toast.jsx';
import StrategyPicker from './StrategyPicker.jsx';
import { GLOBAL_STRATEGY_OPTIONS } from './routingStrategies.js';

// RoutingStrategyCard edits the global `routing.strategy`: the top-level
// credential selector applied when no per-upstream pool strategy or per-model
// ModelRoute overrides it. It appears under Settings as a first-class control
// so operators no longer have to hand-edit the `routing` object as raw JSON in
// the runtime-config snapshot.
//
// Saves immediately on selection (mirrors DynamicSettingsCard's checkbox
// behavior) and reverts the control until the server confirms the new value.
function RoutingStrategyCard() {
  const toast = useToast();
  const strategyReq = useAsync(() => getRoutingStrategy(), []);
  const [strategy, setStrategy] = useState('');
  const [saving, setSaving] = useState(false);

  React.useEffect(() => {
    if (strategyReq.data && typeof strategyReq.data.strategy === 'string') {
      setStrategy(strategyReq.data.strategy);
    }
  }, [strategyReq.data]);

  async function handleSelect(value) {
    const previous = strategy;
    setStrategy(value);
    setSaving(true);
    try {
      await putRoutingStrategy(value);
      toast.success('Global routing strategy saved');
      strategyReq.reload();
    } catch (err) {
      setStrategy(previous);
      toast.error(err?.message || 'Failed to save routing strategy');
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="card">
      <h3 className="card__title" style={{ margin: 0 }}>Global routing strategy</h3>
      <p className="muted" style={{ marginTop: 4, marginBottom: 16 }}>
        How the server picks a credential when no per-upstream pool strategy or
        per-model route applies. Per-model routes on API keys, Model Groups, and
        Global Models override this; an upstream provider&apos;s own Routing
        strategy overrides it within that provider&apos;s pool.
      </p>

      {strategyReq.loading && !strategyReq.data && <Spinner label="Loading routing strategy…" />}
      {strategyReq.error && !strategyReq.data && (
        <ErrorBanner error={strategyReq.error} onRetry={strategyReq.reload} />
      )}
      {strategyReq.data && (
        <>
          <StrategyPicker
            options={GLOBAL_STRATEGY_OPTIONS}
            value={strategy}
            onChange={handleSelect}
            ariaLabel="Global routing strategy"
            showBlurb
          />
          {saving && <div className="muted" style={{ marginTop: 8 }}>Saving…</div>}
        </>
      )}
    </div>
  );
}

export default RoutingStrategyCard;
