import { useState } from "react";
import { useExport } from "@/api/hooks/useExport";
import { useSummary } from "@/api/hooks/useSummary";
import { useFilterKey, useFiltersStore } from "@/stores/filtersStore";
import { useT } from "@/stores/languageStore";
import { KpiRow } from "@/components/KpiRow";
import { UsageKpiRow } from "@/components/UsageKpiRow";
import UsageFilterBar from "@/components/UsageFilterBar";
import UsageRequestsTable from "@/components/UsageRequestsTable";
import ErrorsTable from "@/components/ErrorsTable";
import RankingTable from "@/components/RankingTable";
import ChartPanel from "@/components/charts/ChartPanel";
import ModelDistributionChart from "@/components/charts/ModelDistributionChart";
import ProviderDistributionChart from "@/components/charts/ProviderDistributionChart";
import EndpointDistributionChart from "@/components/charts/EndpointDistributionChart";
import UsageTrendChart from "@/components/charts/UsageTrendChart";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { Checkbox } from "@/components/ui/checkbox";

const ALL_COLUMNS = [
  { key: "timestamp", label: "时间" },
  { key: "model", label: "模型" },
  { key: "provider", label: "供应商" },
  { key: "account", label: "用户" },
  { key: "input_tokens", label: "输入" },
  { key: "output_tokens", label: "输出" },
  { key: "estimated_cost", label: "费用" },
  { key: "status", label: "状态" },
  { key: "latency_ms", label: "耗时" },
];

export default function Usage() {
  const filters = useFilterKey();
  const { data, isLoading } = useSummary(filters);
  const t = useT();
  const s = data?.summary;
  const [activeTab, setActiveTab] = useState("usage");
  const [columnSettingsOpen, setColumnSettingsOpen] = useState(false);
  const { export: exportCsv, downloading } = useExport();
  const handleExport = () => {
    const state = useFiltersStore.getState();
    const params: Record<string, string | string[] | undefined> = {
      range: state.range,
      from: state.from,
      to: state.to,
      models: state.selectedModels.length > 0 ? state.selectedModels : undefined,
      accounts: state.selectedAccounts.length > 0 ? state.selectedAccounts : undefined,
      providers: state.selectedProviders.length > 0 ? state.selectedProviders : undefined,
      endpoints: state.selectedEndpoints.length > 0 ? state.selectedEndpoints : undefined,
    };
    exportCsv(params);
  };
  const [visibleColumns, setVisibleColumns] = useState<string[]>(
    ALL_COLUMNS.map((c) => c.key),
  );

  const handleAccountSelect = (accountHash: string) => {
    useFiltersStore.getState().toggleAccount(accountHash);
    setActiveTab("usage");
  };

  const handleDrillDown = (filter: { model?: string }) => {
    if (filter.model) {
      useFiltersStore.getState().toggleModel(filter.model);
    }
    setActiveTab("usage");
  };

  const toggleColumn = (key: string) => {
    setVisibleColumns((prev) =>
      prev.includes(key) ? prev.filter((k) => k !== key) : [...prev, key],
    );
  };

  return (
    <div className="space-y-4">
      <KpiRow>
        <UsageKpiRow
          summary={s}
          loading={isLoading}
          priceCoverage={data?.price_coverage}
        />
      </KpiRow>
      <UsageFilterBar
        onExport={handleExport}
        downloading={downloading}
        onColumnSettings={() => setColumnSettingsOpen(true)} />
      <Tabs value={activeTab} onValueChange={setActiveTab}>
        <TabsList>
          <TabsTrigger value="usage">{t("tab.usage")}</TabsTrigger>
          <TabsTrigger value="errors">{t("tab.errors")}</TabsTrigger>
          <TabsTrigger value="ranking">{t("tab.ranking")}</TabsTrigger>
        </TabsList>
        <TabsContent value="usage">
          <div className="space-y-4">
            <div className="grid grid-cols-2 gap-4">
              <ChartPanel title={t("chart.model_distribution")}>
                <ModelDistributionChart />
              </ChartPanel>
              <ChartPanel title={t("chart.provider_distribution")}>
                <ProviderDistributionChart />
              </ChartPanel>
            </div>
            <div className="grid grid-cols-2 gap-4">
              <ChartPanel title={t("chart.endpoint_distribution")}>
                <EndpointDistributionChart />
              </ChartPanel>
              <ChartPanel title={t("chart.token_usage_trend")}>
                <UsageTrendChart />
              </ChartPanel>
            </div>
            <UsageRequestsTable />
          </div>
        </TabsContent>
        <TabsContent value="errors">
          <ErrorsTable onDrillDown={handleDrillDown} />
        </TabsContent>
        <TabsContent value="ranking">
          <div>
            <h3 className="text-lg font-medium mb-2">{t("chart.account_ranking")}</h3>
            <RankingTable onSelect={handleAccountSelect} />
          </div>
        </TabsContent>
      </Tabs>

      <Sheet open={columnSettingsOpen} onOpenChange={setColumnSettingsOpen}>
        <SheetContent>
          <SheetHeader>
            <SheetTitle>{t("action.column_settings")}</SheetTitle>
          </SheetHeader>
          <div className="mt-4 space-y-2">
            {ALL_COLUMNS.map((col) => (
              <label
                key={col.key}
                className="flex items-center gap-2 px-2 py-1 text-sm rounded cursor-pointer hover:bg-muted"
              >
                <Checkbox
                  checked={visibleColumns.includes(col.key)}
                  onCheckedChange={() => toggleColumn(col.key)}
                />
                {col.label}
              </label>
            ))}
          </div>
        </SheetContent>
      </Sheet>
    </div>
  );
}

export { Usage };