import { useSummary } from "@/api/hooks/useSummary";
import { useFilterKey } from "@/stores/filtersStore";
import { useT } from "@/stores/languageStore";
import { KpiRow } from "@/components/KpiRow";
import { KpiCard } from "@/components/KpiCard";
import ChartPanel from "@/components/charts/ChartPanel";
import ModelDistributionChart from "@/components/charts/ModelDistributionChart";
import TokenTrendChart from "@/components/charts/TokenTrendChart";
import RecentUsageTable from "@/components/RecentUsageTable";
import { formatTokens, formatMs } from "@/lib/format";
export default function Dashboard() {
  const filters = useFilterKey();
  const t = useT();
  const { data, isLoading } = useSummary(filters);

  const s = data?.summary;
  return (
    <div className="space-y-4">
      <KpiRow>
        <KpiCard label={t("kpi.api_keys")} loading={isLoading} value={s ? "—" : undefined} sub="—" />
        <KpiCard label={t("kpi.accounts")} loading={isLoading} value={s ? "—" : undefined} sub="—" />
        <KpiCard label={t("kpi.today_requests")} loading={isLoading} value={s?.requests} sub={`${s?.failed ?? 0} ${t("kpi.failed_suffix")}`} />
        <KpiCard label={t("kpi.active_keys")} loading={isLoading} value="—" sub="—" />
      </KpiRow>
      <KpiRow>
        <KpiCard label={t("kpi.today_tokens")} loading={isLoading} value={s ? formatTokens(s.total_tokens) : undefined} sub="—" />
        <KpiCard label={t("kpi.total_tokens")} loading={isLoading} value="—" sub="—" />
        <KpiCard label={t("kpi.performance")} loading={isLoading} value={s ? `${((s.success_requests / Math.max(s.requests, 1)) * 100).toFixed(1)}%` : undefined} sub="—" />
        <KpiCard label={t("kpi.avg_response")} loading={isLoading} value={s ? formatMs(Math.round(s.success_latency_ms / Math.max(s.success_requests, 1))) : undefined} sub="—" />
      </KpiRow>
      <div className="grid grid-cols-2 gap-4">
        <ChartPanel title={t("chart.model_distribution")}>
          <ModelDistributionChart />
        </ChartPanel>
        <ChartPanel title={t("chart.token_trend")}>
          <TokenTrendChart />
        </ChartPanel>
      </div>
      <div>
        <h3 className="mb-2 text-sm font-medium">{t("nav.overview")}</h3>
        <RecentUsageTable />
      </div>
    </div>
  )
}

export { Dashboard };