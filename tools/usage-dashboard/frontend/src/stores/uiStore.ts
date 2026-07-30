import { create } from "zustand";

export interface UIState {
  aliasDrawerOpen: boolean;
  pricingDrawerOpen: boolean;
  detailDrawerRequestId: string | null;
  toggleAliasDrawer: () => void;
  togglePricingDrawer: () => void;
  openDetailDrawer: (id: string | null) => void;
}

export const useUIStore = create<UIState>((set) => ({
  aliasDrawerOpen: false,
  pricingDrawerOpen: false,
  detailDrawerRequestId: null,
  toggleAliasDrawer: () => set((s) => ({ aliasDrawerOpen: !s.aliasDrawerOpen })),
  togglePricingDrawer: () => set((s) => ({ pricingDrawerOpen: !s.pricingDrawerOpen })),
  openDetailDrawer: (id) => set({ detailDrawerRequestId: id }),
}));