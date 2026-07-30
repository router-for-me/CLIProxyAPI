import { Bar } from "react-chartjs-2";
import { StatePanel } from "@/components/StatePanel";
import { useEndpoints } from "@/api/hooks/useEndpoints";
import { useFilterKey } from "@/stores/filtersStore";
import { darkThemeOptions } from "@/lib/chartConfig";

export default function EndpointDistributionChart() {
  const filters = useFilterKey();
  const { data, isLoading, error, refetch } = useEndpoints(filters) as {
    data?: { endpoints: Array<{ endpoint: string; total_tokens: number; estimated_cost: number }> };
    isLoading: boolean;
    error: Error | null;
    refetch: () => void;
  };

  const items = data?.endpoints ?? [];
  const labels = items.map((e) => e.endpoint);
  const values = items.map((e) => e.total_tokens);

  return (
    <StatePanel loading={isLoading} error={error} isEmpty={items.length === 0} onRetry={() => refetch()}>
      <Bar
        data={{
          labels,
          datasets: [
            { label: "Tokens", data: values, backgroundColor: "hsl(142 71% 45%)" },
          ],
        }}
        options={darkThemeOptions}
      />
    </StatePanel>
  );
}