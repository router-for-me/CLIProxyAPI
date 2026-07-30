import { Line } from "react-chartjs-2";
import { StatePanel } from "@/components/StatePanel";
import { useTimeseries } from "@/api/hooks/useTimeseries";
import { useFilterKey } from "@/stores/filtersStore";
import { darkThemeOptions } from "@/lib/chartConfig";

export default function TokenTrendChart() {
  const filters = useFilterKey();
  const { data, isLoading, error, refetch } = useTimeseries(filters) as {
    data?: { hours: Array<{ hour: string; total_tokens: number }> };
    isLoading: boolean;
    error: Error | null;
    refetch: () => void;
  };

  const hours = data?.hours ?? [];
  const labels = hours.map((h) => {
    const d = new Date(h.hour);
    const pad = (n: number) => String(n).padStart(2, "0");
    return `${pad(d.getHours())}:00`;
  });
  const values = hours.map((h) => h.total_tokens);

  return (
    <StatePanel loading={isLoading} error={error} isEmpty={hours.length === 0} onRetry={() => refetch()}>
      <div className="relative h-full">
        <Line
          data={{
            labels,
            datasets: [
              {
                label: "Tokens",
                data: values,
                borderColor: "hsl(187 92% 58%)",
                backgroundColor: "hsla(187 92% 58% / 0.1)",
                fill: true,
                tension: 0.3,
                pointRadius: 2,
              },
            ],
          }}
          options={darkThemeOptions}
        />
      </div>
    </StatePanel>
  );
}