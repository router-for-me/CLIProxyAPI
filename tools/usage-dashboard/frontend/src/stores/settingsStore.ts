import { create } from "zustand";

export type UsageColumn =
  | "time" | "model" | "provider"
  | "input_tokens" | "output_tokens"
  | "cost" | "status" | "duration";

interface SettingsState {
  visibleColumns: Record<UsageColumn, boolean>;
  toggleColumn: (c: UsageColumn) => void;
}

export const useSettingsStore = create<SettingsState>((set) => ({
  visibleColumns: {
    time: true,
    model: true,
    provider: true,
    input_tokens: true,
    output_tokens: true,
    cost: true,
    status: true,
    duration: true,
  },
  toggleColumn: (c) =>
    set((s) => ({
      visibleColumns: { ...s.visibleColumns, [c]: !s.visibleColumns[c] },
    })),
}));