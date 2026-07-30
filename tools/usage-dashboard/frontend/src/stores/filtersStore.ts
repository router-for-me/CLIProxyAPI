import { create } from "zustand";
import { useShallow } from "zustand/react/shallow";

export type RangePreset = "today" | "1h" | "5h" | "24h" | "7d" | "30d" | "explicit";
export type Granularity = "hour" | "day";

export interface FiltersState {
  range: RangePreset;
  from?: string;
  to?: string;
  granularity: Granularity;
  selectedModels: string[];
  selectedAccounts: string[];
  selectedProviders: string[];
  selectedEndpoints: string[];

  setRange: (r: RangePreset) => void;
  setExplicitRange: (from: string, to: string) => void;
  setGranularity: (g: Granularity) => void;
  toggleModel: (m: string) => void;
  toggleAccount: (a: string) => void;
  toggleProvider: (p: string) => void;
  toggleEndpoint: (e: string) => void;
  clearModels: () => void;
  clearAccounts: () => void;
  clearProviders: () => void;
  clearEndpoints: () => void;
  clearAllFilters: () => void;
}

export const useFiltersStore = create<FiltersState>((set) => ({
  range: "24h",
  from: undefined,
  to: undefined,
  granularity: "hour",
  selectedModels: [],
  selectedAccounts: [],
  selectedProviders: [],
  selectedEndpoints: [],

  setRange: (r) => set({ range: r, from: undefined, to: undefined }),
  setExplicitRange: (from, to) => set({ range: "explicit", from, to }),
  setGranularity: (g) => set({ granularity: g }),
  toggleModel: (m) => set((s) => ({
    selectedModels: s.selectedModels.includes(m)
      ? s.selectedModels.filter((x) => x !== m)
      : [...s.selectedModels, m],
  })),
  toggleAccount: (a) => set((s) => ({
    selectedAccounts: s.selectedAccounts.includes(a)
      ? s.selectedAccounts.filter((x) => x !== a)
      : [...s.selectedAccounts, a],
  })),
  toggleProvider: (p) => set((s) => ({
    selectedProviders: s.selectedProviders.includes(p)
      ? s.selectedProviders.filter((x) => x !== p)
      : [...s.selectedProviders, p],
  })),
  toggleEndpoint: (e) => set((s) => ({
    selectedEndpoints: s.selectedEndpoints.includes(e)
      ? s.selectedEndpoints.filter((x) => x !== e)
      : [...s.selectedEndpoints, e],
  })),
  clearModels: () => set({ selectedModels: [] }),
  clearAccounts: () => set({ selectedAccounts: [] }),
  clearProviders: () => set({ selectedProviders: [] }),
  clearEndpoints: () => set({ selectedEndpoints: [] }),
  clearAllFilters: () => set({
    selectedModels: [],
    selectedAccounts: [],
    selectedProviders: [],
    selectedEndpoints: [],
  }),
}));

// Selector hook: returns the slice of filters relevant to TanStack Query keys.
export function useFilterKey() {
  return useFiltersStore(
    useShallow((s) => ({
      range: s.range, from: s.from, to: s.to,
      models: s.selectedModels, accounts: s.selectedAccounts,
      providers: s.selectedProviders, endpoints: s.selectedEndpoints,
    })),
  );
}