import { create } from "zustand";

interface ChartToggleState {
  modes: Record<string, "tokens" | "cost">;
  getMode: (chartId: string) => "tokens" | "cost";
  setMode: (chartId: string, m: "tokens" | "cost") => void;
}

export const useChartToggleStore = create<ChartToggleState>((set, get) => ({
  modes: {},
  getMode: (chartId) => get().modes[chartId] ?? "tokens",
  setMode: (chartId, m) =>
    set((s) => ({ modes: { ...s.modes, [chartId]: m } })),
}));