import {
  Chart,
  BarElement, CategoryScale, LinearScale, Tooltip, Legend, Title,
  PointElement, LineElement, Filler,
} from "chart.js";

Chart.register(
  BarElement, CategoryScale, LinearScale, Tooltip, Legend, Title,
  PointElement, LineElement, Filler,
);

export const DARK_AXIS_COLOR = "hsl(215 14% 60%)";
export const DARK_GRID_COLOR = "hsl(215 14% 22%)";

export const darkThemeOptions = {
  responsive: true,
  maintainAspectRatio: false,
  plugins: {
    legend: { labels: { color: DARK_AXIS_COLOR } },
    tooltip: { mode: "index" as const, intersect: false },
  },
  scales: {
    x: { ticks: { color: DARK_AXIS_COLOR }, grid: { color: DARK_GRID_COLOR } },
    y: { ticks: { color: DARK_AXIS_COLOR }, grid: { color: DARK_GRID_COLOR } },
  },
};