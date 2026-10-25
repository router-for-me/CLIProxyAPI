import type { KimiQuotaState } from '@/types';
import { CountQuotaBody } from '../../components/CountQuotaBody';
import type { QuotaBodyProps } from '../../types';

export function KimiQuotaBody({ quota, classes }: QuotaBodyProps<KimiQuotaState>) {
  return <CountQuotaBody rows={quota.rows ?? []} provider="kimi" classes={classes} />;
}
