import { createContext, useContext } from 'react';

export type LayerStatus = 'current' | 'exiting' | 'stacked';

type PageTransitionLayerContextValue = {
  status: LayerStatus;
};

export const PageTransitionLayerContext =
  createContext<PageTransitionLayerContextValue | null>(null);

export function usePageTransitionLayer() {
  return useContext(PageTransitionLayerContext);
}

