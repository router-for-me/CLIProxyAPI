import { Bar } from "react-chartjs-2";
import { StatePanel } from "@/components/StatePanel";
import { useModels } from "@/api/hooks/useModels";
import { useFilterKey } from "@/stores/filtersStore";
import { useChartToggleStore } from "@/stores/chartToggleStore";
import { darkThemeOptions } from "@/lib/chartConfig";
import { Button } from "@/components/ui/button";
import { useT } from "@/stores/languageStore";

export default function ModelDistributionChart() {
  const filters = useFilterKey();
  const t = useT();
  const { data, isLoading, error, refetch } = useModels(filters) as {
    data?: { models: Array<{ model: string; total_tokens: number; estimated_cost: number }> };
    isLoading: boolean;
    error: Error | null;
    refetch: () => void;
  };
  const CHART_ID = "model";
  const mode = useChartToggleStore((s) => s.modes[CHART_ID] ?? "tokens");
  const setMode = useChartToggleStore((s) => s.setMode);

  const models = data?.models ?? [];
  const labels = models.map((m) => m.model);
  const values = models.map((m) =>
    mode === "tokens" ? m.total_tokens : m.estimated_cost,
  );

  return (
    <StatePanel loading={isLoading} error={error} isEmpty={models.length === 0} onRetry={() => refetch()}>
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
              { label: mode, data: values, backgroundColor: "hsl(187 92% 58%)" },
            ],
          }}
          options={darkThemeOptions}
        />
      </div>
    </StatePanel>
  );
}