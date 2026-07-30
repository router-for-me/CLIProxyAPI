import { Bar } from "react-chartjs-2";
import { StatePanel } from "@/components/StatePanel";
import { useProviders } from "@/api/hooks/useProviders";
import { useFilterKey } from "@/stores/filtersStore";
import { useChartToggleStore } from "@/stores/chartToggleStore";
import { darkThemeOptions } from "@/lib/chartConfig";
import { Button } from "@/components/ui/button";
import { useT } from "@/stores/languageStore";

const CHART_ID = "provider";

export default function ProviderDistributionChart() {
  const filters = useFilterKey();
  const t = useT();
  const { data, isLoading, error, refetch } = useProviders(filters) as {
    data?: { providers: Array<{ provider: string; total_tokens: number; estimated_cost: number }> };
    isLoading: boolean;
    error: Error | null;
    refetch: () => void;
  };
  const mode = useChartToggleStore((s) => s.modes[CHART_ID] ?? "tokens");
  const setMode = useChartToggleStore((s) => s.setMode);

  const items = data?.providers ?? [];
  const labels = items.map((p) => p.provider);
  const values = items.map((p) =>
    mode === "tokens" ? p.total_tokens : p.estimated_cost,
  );

  return (
    <StatePanel loading={isLoading} error={error} isEmpty={items.length === 0} onRetry={() => refetch()}>
      <div className="relative h-full">
        <div className="absolute right-0 top-0 z-10 flex gap-1">
          <Button
            size="sm"
            variant={mode === "tokens" ? "default" : "ghost"}
            onClick={() => setMode(CHART_ID, "tokens")}
          >
            {t("chart.token_mode")}
          </Button>
          <Button
            size="sm"
            variant={mode === "cost" ? "default" : "ghost"}
            onClick={() => setMode(CHART_ID, "cost")}
          >
            {t("chart.cost_mode")}
          </Button>
        </div>
        <Bar
          data={{
            labels,
            datasets: [
              { label: mode, data: values, backgroundColor: "hsl(280 80% 60%)" },
            ],
          }}
          options={darkThemeOptions}
        />
      </div>
    </StatePanel>
  );
}