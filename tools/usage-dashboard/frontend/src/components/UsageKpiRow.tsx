import { KpiCard } from "./KpiCard";
import { formatTokens, formatMs } from "@/lib/format";
import { useT } from "@/stores/languageStore";

interface UsageKpiRowProps {
  summary?: {
    requests?: number;
    total_tokens?: number;
    estimated_cost?: number;
    estimated_cost_currency?: string;
    success_latency_ms?: number;
    success_requests?: number;
  };
  loading: boolean;
  priceCoverage?: string;
}

export function UsageKpiRow({ summary, loading, priceCoverage }: UsageKpiRowProps) {
  const s = summary;
  const t = useT();
  return (
    <>
      <KpiCard label={t("kpi.total_requests")} loading={loading} value={s?.requests} />
      <KpiCard label={t("kpi.total_tokens")} loading={loading} value={formatTokens(s?.total_tokens)} />
      <KpiCard label={t("kpi.total_cost")} loading={loading}
               value={s ? `${s.estimated_cost?.toFixed(4)} ${s.estimated_cost_currency}` : undefined}
               sub={priceCoverage === "partial" ? t("kpi.partial_pricing") : undefined} />
      <KpiCard label={t("kpi.avg_duration")} loading={loading}
               value={s ? formatMs((s.success_latency_ms ?? 0) / Math.max(s.success_requests ?? 0, 1)) : undefined} />
    </>
  );
}