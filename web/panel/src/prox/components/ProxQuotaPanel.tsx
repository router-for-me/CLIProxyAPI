/** prox block under the quota page tabs: live strip plus pool summary or pool chart. */

import type { PoolChart } from '../types';
import { LiveStrip, PoolPanel, PoolSummary } from './PoolOverview';

export function ProxQuotaPanel({
  tab,
  charts,
  onSelect,
}: {
  tab: string;
  charts: PoolChart[];
  onSelect: (provider: string) => void;
}) {
  const selected = charts.find((c) => c.provider === tab);
  return (
    <>
      <LiveStrip charts={charts} />
      {tab === 'all' ? (
        <PoolSummary charts={charts} onSelect={onSelect} />
      ) : (
        selected && <PoolPanel key={tab} provider={tab} weekly={selected} />
      )}
    </>
  );
}
